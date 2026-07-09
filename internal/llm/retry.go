package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// retryRequest describes one POST-with-retry operation for doRequestWithRetry.
type retryRequest struct {
	endpoint string
	body     []byte
	// opName labels log lines ("LLM request", "embeddings request",
	// "LLM stream open") so operators can tell the paths apart in Loki.
	opName string
	// model feeds the RecordLLMRetry metric.
	model string
	// streamMode returns the open *http.Response on a 200 instead of reading
	// the body, and asks for an SSE response via the Accept header. The
	// 200-with-error-body detection is skipped — the body must stay unread
	// for the SSE pump. Non-200 error bodies are still read and classified.
	streamMode bool
}

// retryOutcome is the terminal result of doRequestWithRetry. Exactly one of
// body/resp is set on success, depending on retryRequest.streamMode.
type retryOutcome struct {
	// body is the full response body (buffered mode).
	body []byte
	// resp is the open HTTP response (stream mode); the caller owns Body.
	resp *http.Response
	// attempts is the number of HTTP attempts made (1 = no retries).
	attempts int
	// retryDelays holds the backoff waits in ms, one per retry, for the
	// llm.retry_delays_ms span attribute.
	retryDelays []int64
}

// doRequestWithRetry POSTs body to endpoint with the shared retry policy:
// exponential backoff with jitter, retry on transport errors
// (isRetryableError), retryable HTTP statuses (isRetryableStatusCode), and —
// in buffered mode — provider error envelopes inside HTTP-200 bodies
// (detectProviderBodyError, gated by ErrorKind retryability).
//
// The helper owns per-attempt instrumentation: one "attempt" span event per
// HTTP attempt, RecordLLMRetry before each retry, warn logs on retry, and
// recordProviderError on terminal provider failures. Callers own everything
// about the successful response (decode, response logging, span attributes)
// and the RecordLLMRequest success/failure metric — the helper has no notion
// of user id or job type.
func (c *clientImpl) doRequestWithRetry(ctx context.Context, span trace.Span, r retryRequest) (retryOutcome, error) {
	out := retryOutcome{}
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		out.attempts = attempt + 1
		if attempt > 0 {
			RecordLLMRetry(r.model)
			delay := calculateBackoff(attempt - 1)
			out.retryDelays = append(out.retryDelays, delay.Milliseconds())
			c.logger.Warn("Retrying "+r.opName,
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)

			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := c.newAPIRequest(ctx, r.endpoint, r.body)
		if err != nil {
			return out, err
		}
		if r.streamMode {
			httpReq.Header.Set("Accept", "text/event-stream")
		}

		attemptStart := time.Now()
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return out, err
		}

		if r.streamMode && resp.StatusCode == http.StatusOK {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode})
			out.resp = resp
			return out, nil
		}

		// Buffered mode always reads the body; stream mode reaches here only
		// on non-200, where the body is a small error payload.
		responseBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err, httpStatus: resp.StatusCode})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return out, err
		}

		if resp.StatusCode == http.StatusOK {
			// The provider sometimes returns 200 with an error payload in the
			// body when the upstream fails. Treat as retryable unless the
			// classified ErrorKind is permanent.
			if bodyErr := detectProviderBodyError(responseBody); bodyErr != nil {
				recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, bodyErr: bodyErr, body: responseBody})
				c.logger.Warn(r.opName+" returned HTTP 200 with error body",
					"error", bodyErr, "body", string(responseBody), "attempt", attempt)
				if attempt < maxRetries && !isNonRetryableBodyError(bodyErr) {
					lastErr = bodyErr
					continue
				}
				recordProviderError(span, responseBody, resp.StatusCode)
				return out, bodyErr
			}
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: responseBody})
			out.body = responseBody
			return out, nil
		}

		recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: responseBody})

		// Gemini's "Corrupted thought signature" arrives as HTTP 400 — a
		// status the blanket gate treats as permanent — but it is a transient
		// reasoning-state failure a retry usually clears. Gate on the specific
		// kind rather than widening to all 4xx: most 4xx bodies really are
		// deterministic rejections of this exact request.
		retryableBody := classifyUpstreamError(detectProviderBodyError(responseBody)) == KindThoughtSignature
		if (isRetryableStatusCode(resp.StatusCode) || retryableBody) && attempt < maxRetries {
			lastErr = fmt.Errorf("llm API error: %s: %s", resp.Status, truncateForLog(string(responseBody), 500))
			continue
		}

		// Non-retryable status or max retries reached.
		c.logger.Error(r.opName+" returned non-OK status",
			"status", resp.Status, "body", truncateForLog(string(responseBody), 500))
		recordProviderError(span, responseBody, resp.StatusCode)
		return out, fmt.Errorf("llm API error: %s: %s", resp.Status, truncateForLog(string(responseBody), 500))
	}

	// Retries exhausted with a retryable error carried over from the last
	// attempt. Unreachable in practice (terminal branches return in-loop),
	// kept as a defensive backstop.
	if lastErr != nil {
		return out, lastErr
	}
	return out, errors.New(r.opName + ": exhausted retries")
}
