package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/jobtype"
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

type ChunkUsage struct {
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	TotalTokens      int      `json:"total_tokens"`
	Cost             *float64 `json:"cost,omitempty"`
}

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
func (c *clientImpl) CreateChatCompletionStream(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionStream, error) {
	startTime := time.Now()
	jt := jobtype.FromContext(ctx).String()

	if req.Provider == nil && c.defaultProvider != nil {
		req.Provider = c.defaultProvider
	}
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal stream request: %w", err)
	}

	endpoint, err := url.JoinPath(c.apiEndpoint, "chat/completions")
	if err != nil {
		return nil, err
	}

	c.logger.Info("Sending streaming request to OpenRouter",
		"model", req.Model,
		"message_count", len(req.Messages),
		"tools_count", len(req.Tools),
	)

	//nolint:bodyclose // body is closed by pumpStream's defer once the stream finishes; bodyclose can't follow `go pumpStream(...)`.
	httpResp, err := c.openStream(ctx, endpoint, body, req.Model, req.UserID, jt, startTime)
	if err != nil {
		return nil, err
	}

	events := make(chan StreamEvent, 16)
	go c.pumpStream(ctx, httpResp.Body, events)
	return &ChatCompletionStream{
		Events:           events,
		DebugRequestBody: string(body),
	}, nil
}

// openStream performs the POST with retry on connect/early errors. Returns the
// open HTTP response (caller takes ownership of Body) on success.
func (c *clientImpl) openStream(
	ctx context.Context,
	endpoint string,
	body []byte,
	model string,
	userID int64,
	jt string,
	startTime time.Time,
) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			RecordLLMRetry(model)
			delay := calculateBackoff(attempt - 1)
			c.logger.Warn("Retrying OpenRouter stream open",
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("User-Agent", "laplaced/1.0")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			RecordLLMRequest(userID, model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		// Non-200: read body for error detection (it's small for errors).
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// 200-with-error pattern doesn't apply here (we got non-200), but the
		// upstream sometimes returns retryable codes — match the non-stream
		// retry policy.
		if isRetryableStatusCode(resp.StatusCode) && attempt < maxRetries {
			lastErr = fmt.Errorf("openrouter API error: %s: %s", resp.Status, truncateForLog(string(errBody), 500))
			continue
		}

		c.logger.Error("OpenRouter stream returned non-OK status",
			"status", resp.Status, "body", truncateForLog(string(errBody), 500))
		RecordLLMRequest(userID, model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		return nil, fmt.Errorf("openrouter API error: %s: %s", resp.Status, truncateForLog(string(errBody), 500))
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("openrouter stream open: exhausted retries")
}

// pumpStream reads SSE frames from body and emits StreamEvents. Closes events
// channel on termination. Always closes body.
func (c *clientImpl) pumpStream(ctx context.Context, body io.ReadCloser, events chan<- StreamEvent) {
	defer body.Close()
	defer close(events)

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
				c.logger.Debug("OpenRouter stream: skipping unknown frame line",
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
			select {
			case events <- StreamEvent{Err: fmt.Errorf("decode SSE chunk: %w (payload=%s)", err, truncateForLog(payload, 200))}:
			case <-ctx.Done():
			}
			return
		}

		select {
		case events <- StreamEvent{Chunk: &chunk}:
		case <-ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		select {
		case events <- StreamEvent{Err: fmt.Errorf("read SSE stream: %w", err)}:
		case <-ctx.Done():
		}
		return
	}
	// Stream closed without [DONE] — surface as error so caller doesn't treat
	// this as a clean finish and emit a half-baked response.
	select {
	case events <- StreamEvent{Err: errors.New("openrouter stream ended without [DONE] sentinel")}:
	case <-ctx.Done():
	}
}
