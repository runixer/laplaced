package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/obs"
)

// ChatCompletionChunk is one SSE delta from /chat/completions in stream mode.
// Mirrors the OpenAI-compatible shape OpenRouter emits between `data:` and `\n\n`.
// Most chunks carry a single Choices[0].Delta with partial content/reasoning/tool_calls;
// the final chunk before `data: [DONE]` additionally carries Usage.
type ChatCompletionChunk struct {
	ID          string        `json:"id"`
	Object      string        `json:"object"` // "chat.completion.chunk"
	Created     int64         `json:"created"`
	Model       string        `json:"model"`
	Provider    string        `json:"provider,omitempty"`
	ServiceTier string        `json:"service_tier,omitempty"`
	Choices     []ChunkChoice `json:"choices"`
	Usage       *ChunkUsage   `json:"usage,omitempty"`
}

type ChunkChoice struct {
	Index              int        `json:"index"`
	Delta              ChunkDelta `json:"delta"`
	FinishReason       string     `json:"finish_reason,omitempty"`
	NativeFinishReason string     `json:"native_finish_reason,omitempty"`
}

type ChunkDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ReasoningDetails interface{}     `json:"reasoning_details,omitempty"`
	ToolCalls        []ChunkToolCall `json:"tool_calls,omitempty"`
}

type ChunkToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ChunkToolCall struct {
	Index    int                   `json:"index"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function ChunkToolCallFunction `json:"function,omitempty"`
}

// ChunkUsage is the usage block on a streaming chunk. It aliases Usage so the
// streaming and non-streaming paths share the same polymorphic-cost decoding.
type ChunkUsage = Usage

// StreamEvent carries either a decoded chunk or a terminal error.
// When Err != nil the channel is about to close; callers should treat this as
// the stream's final event and not look for further chunks. Successful streams
// terminate by closing the channel without an Err event.
type StreamEvent struct {
	Chunk *ChatCompletionChunk
	Err   error
}

// ChatCompletionStream is the result of opening a streaming /chat/completions
// request. Events delivers the SSE deltas; DebugRequestBody mirrors the
// buffered ChatCompletionResponse field so callers can record the raw JSON
// request body alongside the synthesized response.
type ChatCompletionStream struct {
	Events           <-chan StreamEvent
	DebugRequestBody string
}

// CreateChatCompletionStream issues a streaming /chat/completions request and
// returns a channel of decoded events. The channel is closed when the upstream
// emits `data: [DONE]` or when context is cancelled. Any I/O or decode error
// is delivered as a single trailing StreamEvent{Err: ...} before close.
//
// Retries: only pre-stream errors (connect, non-2xx, body-error JSON) are
// retried. Once the SSE body starts flowing we never reconnect — partial
// streams produce an Err event and the consumer decides what to do.
//
// Tracing: emits an llm.CreateChatCompletionStream span with the same
// gen_ai.* / llm.* attribute surface as CreateChatCompletion. The span's
// lifetime spans the entire operation — when openStream returns a usable HTTP
// response, span ownership is handed to pumpStream, which sets the response
// model / usage / cost as chunks arrive and ends the span when the SSE body
// terminates (success, decode error, or context cancellation).
func (c *clientImpl) CreateChatCompletionStream(ctx context.Context, req ChatCompletionRequest) (stream *ChatCompletionStream, err error) {
	startTime := time.Now()
	jt := jobtype.FromContext(ctx).String()

	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/llm").Start(
		ctx, "llm.CreateChatCompletionStream",
		trace.WithAttributes(append(c.spanBaseAttributes(),
			attribute.String("gen_ai.request.model", req.Model),
			attribute.String("user.id", req.UserID),
			attribute.String("job.type", jt),
			attribute.Int("prompt.media.filename_collisions", countFilenameCollisions(req.Messages)),
		)...),
	)
	var (
		attempts        int
		retryDelays     []int64
		transferredPump bool
	)
	defer func() {
		if transferredPump {
			return
		}
		span.SetAttributes(attribute.Int("llm.attempts", attempts))
		if len(retryDelays) > 0 {
			span.SetAttributes(attribute.Int64Slice("llm.retry_delays_ms", retryDelays))
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	if req.Provider == nil && c.defaultProvider != nil {
		req.Provider = c.defaultProvider
	}
	req.Stream = true
	// Link OR-emitted Broadcast spans to our local trace. Same call as in
	// CreateChatCompletion; previously missing on the streaming path so
	// OR-side spans for stream requests floated unattached.
	req.Trace, req.User = withBroadcastFields(ctx, req.Trace, req.User, req.UserID)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal stream request: %w", err)
	}

	// Redact base64 multimodal payloads before they hit the span. Same trace-size
	// concern as CreateChatCompletion (see obs.RedactBase64Payloads).
	obs.RecordContent(span, "llm.request", obs.RedactBase64Payloads(string(body)))

	endpoint, err := url.JoinPath(c.apiEndpoint, "chat/completions")
	if err != nil {
		return nil, err
	}

	c.logger.Info("Sending streaming LLM request",
		"model", req.Model,
		"message_count", len(req.Messages),
		"tools_count", len(req.Tools),
	)

	//nolint:bodyclose // body is closed by pumpStream's defer once the stream finishes; bodyclose can't follow `go pumpStream(...)`.
	httpResp, openAttempts, openRetryDelays, err := c.openStream(ctx, span, endpoint, body, req.Model, req.UserID, jt, startTime)
	attempts = openAttempts
	retryDelays = openRetryDelays
	if err != nil {
		return nil, err
	}

	// Hand the span over to pumpStream — it owns lifecycle from here, including
	// final attribute writes and span.End().
	span.SetAttributes(attribute.Int("llm.attempts", attempts))
	if len(retryDelays) > 0 {
		span.SetAttributes(attribute.Int64Slice("llm.retry_delays_ms", retryDelays))
	}
	transferredPump = true

	events := make(chan StreamEvent, 16)
	go c.pumpStream(ctx, span, req, httpResp.Body, events, startTime, jt)
	return &ChatCompletionStream{
		Events:           events,
		DebugRequestBody: string(body),
	}, nil
}

// openStream performs the POST with retry on connect/early errors. Returns the
// open HTTP response (caller takes ownership of Body) on success.
//
// Records one "attempt" span event per HTTP attempt and, on terminal failure,
// lifts the OR error envelope onto the span via recordProviderError — the
// same instrumentation surface CreateChatCompletion uses for its retry loop.
func (c *clientImpl) openStream(
	ctx context.Context,
	span trace.Span,
	endpoint string,
	body []byte,
	model string,
	userID string,
	jt string,
	startTime time.Time,
) (*http.Response, int, []int64, error) {
	var (
		lastErr     error
		attempts    int
		retryDelays []int64
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts = attempt + 1
		if attempt > 0 {
			RecordLLMRetry(model)
			delay := calculateBackoff(attempt - 1)
			retryDelays = append(retryDelays, delay.Milliseconds())
			c.logger.Warn("Retrying LLM stream open",
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)
			select {
			case <-ctx.Done():
				return nil, attempts, retryDelays, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := c.newAPIRequest(ctx, endpoint, body)
		if err != nil {
			return nil, attempts, retryDelays, err
		}
		httpReq.Header.Set("Accept", "text/event-stream")

		attemptStart := time.Now()
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			RecordLLMRequest(userID, model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
			return nil, attempts, retryDelays, err
		}

		if resp.StatusCode == http.StatusOK {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode})
			return resp, attempts, retryDelays, nil
		}

		// Non-200: read body for error detection (it's small for errors).
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: errBody})

		// 200-with-error pattern doesn't apply here (we got non-200), but the
		// upstream sometimes returns retryable codes — match the non-stream
		// retry policy.
		if isRetryableStatusCode(resp.StatusCode) && attempt < maxRetries {
			lastErr = fmt.Errorf("llm API error: %s: %s", resp.Status, truncateForLog(string(errBody), 500))
			continue
		}

		c.logger.Error("LLM stream returned non-OK status",
			"status", resp.Status, "body", truncateForLog(string(errBody), 500))
		RecordLLMRequest(userID, model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		recordProviderError(span, errBody, resp.StatusCode)
		return nil, attempts, retryDelays, fmt.Errorf("llm API error: %s: %s", resp.Status, truncateForLog(string(errBody), 500))
	}

	if lastErr != nil {
		return nil, attempts, retryDelays, lastErr
	}
	return nil, attempts, retryDelays, errors.New("llm stream open: exhausted retries")
}

// pumpStream reads SSE frames from body and emits StreamEvents. Closes events
// channel on termination. Always closes body.
//
// Owns span lifecycle from the point CreateChatCompletionStream handed off: on
// termination it sets gen_ai.response.model, gen_ai.usage.*, and llm.cost_usd
// from the final usage chunk, observes any terminal error, records the
// Prometheus llm_request metric, and ends the span.
func (c *clientImpl) pumpStream(
	ctx context.Context,
	span trace.Span,
	req ChatCompletionRequest,
	body io.ReadCloser,
	events chan<- StreamEvent,
	startTime time.Time,
	jt string,
) {
	defer body.Close()
	defer close(events)

	var (
		responseModel string
		finalUsage    *ChunkUsage
		streamErr     error
	)
	defer func() {
		if responseModel != "" {
			span.SetAttributes(attribute.String("gen_ai.response.model", responseModel))
		}
		if finalUsage != nil {
			if finalUsage.PromptTokens > 0 || finalUsage.CompletionTokens > 0 {
				span.SetAttributes(
					attribute.Int("gen_ai.usage.input_tokens", finalUsage.PromptTokens),
					attribute.Int("gen_ai.usage.output_tokens", finalUsage.CompletionTokens),
				)
			}
			if finalUsage.Cost != nil {
				span.SetAttributes(attribute.Float64("llm.cost_usd", *finalUsage.Cost))
			}
		}
		success := streamErr == nil
		var promptTokens, completionTokens int
		var cost *float64
		if finalUsage != nil {
			promptTokens = finalUsage.PromptTokens
			completionTokens = finalUsage.CompletionTokens
			cost = finalUsage.Cost
		}
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), success, promptTokens, completionTokens, cost, jt)
		_ = obs.ObserveErr(span, streamErr)
		span.End()
	}()

	scanner := bufio.NewScanner(body)
	// SSE frames can be larger than the default 64KB scanner buffer when
	// reasoning_details carries an encrypted blob. Allow up to 1 MiB per frame.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Empty line = frame separator. Comment line (`:` prefix) = keep-alive.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// We only handle `data:` frames. OpenRouter doesn't emit `event:` or
		// `id:` for /chat/completions stream.
		const dataPrefix = "data: "
		if !strings.HasPrefix(line, dataPrefix) {
			// Some servers omit the space after the colon. Be lenient.
			if strings.HasPrefix(line, "data:") {
				line = "data:" + strings.TrimPrefix(line[len("data:"):], " ")
			} else {
				// Unknown frame line — log and skip.
				c.logger.Debug("LLM stream: skipping unknown frame line",
					"line", truncateForLog(line, 200))
				continue
			}
		}

		payload := strings.TrimPrefix(line, "data:")
		payload = strings.TrimPrefix(payload, " ")

		if payload == "[DONE]" {
			return
		}

		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			streamErr = fmt.Errorf("decode SSE chunk: %w (payload=%s)", err, truncateForLog(payload, 200))
			select {
			case events <- StreamEvent{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}

		// Accumulate identity / usage as chunks arrive. Most chunks carry
		// chunk.Model; only the final pre-[DONE] chunk carries chunk.Usage.
		if chunk.Model != "" {
			responseModel = chunk.Model
		}
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}

		select {
		case events <- StreamEvent{Chunk: &chunk}:
		case <-ctx.Done():
			streamErr = ctx.Err()
			return
		}
	}

	if err := scanner.Err(); err != nil {
		streamErr = fmt.Errorf("read SSE stream: %w", err)
		select {
		case events <- StreamEvent{Err: streamErr}:
		case <-ctx.Done():
		}
		return
	}
	// Stream closed without [DONE] — surface as error so caller doesn't treat
	// this as a clean finish and emit a half-baked response.
	streamErr = errors.New("llm stream ended without [DONE] sentinel")
	select {
	case events <- StreamEvent{Err: streamErr}:
	case <-ctx.Done():
	}
}
