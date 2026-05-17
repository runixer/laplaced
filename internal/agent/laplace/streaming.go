package laplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
)

// streamTurnResult is the synthesized output of a single streaming LLM turn.
// It mimics the relevant subset of openrouter.ChatCompletionResponse so the
// surrounding tool loop in Execute can be agnostic about whether the call was
// streamed or buffered.
type streamTurnResult struct {
	Content          string
	ToolCalls        []openrouter.ToolCall
	ReasoningDetails interface{}
	FinishReason     string
	Model            string

	PromptTokens     int
	CompletionTokens int
	Cost             *float64

	// FirstContentDelay is the elapsed time from the LLM call start to the
	// first user-visible content delta. Zero when no content was emitted
	// (e.g. the iteration was a pure tool-call iteration or empty response).
	FirstContentDelay time.Duration

	// DebugResponseBody is a synthetic JSON serialization of the synthesized
	// turn, kept for the agentlog turn tracker. Tagged "(stream-reconstructed)"
	// so debug viewers can tell it apart from a real provider response body.
	DebugResponseBody string
}

// runStreamingTurn drives one streaming /chat/completions request and
// aggregates the SSE deltas into a streamTurnResult.
//
// Content-delta forwarding policy: once any tool-call delta appears in the
// stream we stop forwarding `delta.content` to the user (the iteration is
// non-final, the bubble will be repurposed by the next OnToolStart status
// edit). Up to that point we forward content deltas eagerly so the user sees
// the response form in real time.
//
// reasoning_details: accumulated across ALL deltas. The encrypted blob is
// required for follow-up tool turns and arrives in fragments.
func (l *Laplace) runStreamingTurn(
	ctx context.Context,
	orReq openrouter.ChatCompletionRequest,
	onContentDelta func(string),
	logger *slog.Logger,
) (*streamTurnResult, error) {
	llmStart := time.Now()

	events, err := l.orClient.CreateChatCompletionStream(ctx, orReq)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}

	out := &streamTurnResult{}
	// Reasoning details merge: providers emit reasoning_details as a JSON
	// array per delta. We collect the entries and combine into a single slice
	// (kept as []interface{} to mirror the buffered response shape).
	var mergedReasoning []interface{}

	// Tool-call accumulator keyed by index. OpenAI/Gemini SSE addresses
	// fragments by `tool_calls[i].index`, with id/type/name typically present
	// in the first fragment and `arguments` accumulating across subsequent
	// fragments.
	type toolCallAcc struct {
		ID       string
		Type     string
		Name     string
		ArgsBuf  []byte // builder for arguments fragments
		HasIndex bool
	}
	tcByIndex := map[int]*toolCallAcc{}
	var maxToolIndex = -1
	contentForwardingActive := true

	var contentBuf []byte

	for ev := range events {
		if ev.Err != nil {
			return nil, ev.Err
		}
		chunk := ev.Chunk
		if chunk == nil || len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		delta := choice.Delta

		// Capture model from the first chunk that names it (not all chunks
		// repeat it, but most do).
		if out.Model == "" && chunk.Model != "" {
			out.Model = chunk.Model
		}

		// Reasoning details: append entries from this chunk's array.
		if delta.ReasoningDetails != nil {
			if arr, ok := delta.ReasoningDetails.([]interface{}); ok {
				mergedReasoning = append(mergedReasoning, arr...)
			} else {
				// Single-object form — wrap.
				mergedReasoning = append(mergedReasoning, delta.ReasoningDetails)
			}
		}

		// Tool calls: any tool delta switches us to non-final-iteration mode.
		if len(delta.ToolCalls) > 0 {
			if contentForwardingActive {
				logger.Debug("stream: tool_call delta seen, halting content forwarding")
				contentForwardingActive = false
			}
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				acc := tcByIndex[idx]
				if acc == nil {
					acc = &toolCallAcc{HasIndex: true}
					tcByIndex[idx] = acc
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Type != "" {
					acc.Type = tc.Type
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					acc.ArgsBuf = append(acc.ArgsBuf, tc.Function.Arguments...)
				}
				if idx > maxToolIndex {
					maxToolIndex = idx
				}
			}
		}

		// Content: forward live unless tool calls have been seen.
		if delta.Content != "" {
			contentBuf = append(contentBuf, delta.Content...)
			if contentForwardingActive {
				if out.FirstContentDelay == 0 {
					out.FirstContentDelay = time.Since(llmStart)
				}
				if onContentDelta != nil {
					onContentDelta(delta.Content)
				}
			}
		}

		// Finish reason / usage typically arrive on the last meaningful chunk.
		if choice.FinishReason != "" {
			out.FinishReason = choice.FinishReason
		}
		if chunk.Usage != nil {
			out.PromptTokens = chunk.Usage.PromptTokens
			out.CompletionTokens = chunk.Usage.CompletionTokens
			out.Cost = chunk.Usage.Cost
		}
	}

	out.Content = string(contentBuf)

	// Materialize tool calls in index order.
	if maxToolIndex >= 0 {
		for i := 0; i <= maxToolIndex; i++ {
			acc, ok := tcByIndex[i]
			if !ok {
				// A gap in indices is unusual but not fatal; skip silently.
				continue
			}
			tc := openrouter.ToolCall{
				ID:   acc.ID,
				Type: acc.Type,
			}
			tc.Function.Name = acc.Name
			tc.Function.Arguments = string(acc.ArgsBuf)
			out.ToolCalls = append(out.ToolCalls, tc)
		}
	}

	if len(mergedReasoning) > 0 {
		out.ReasoningDetails = mergedReasoning
	}

	// Build a synthetic response body for agentlog. Mark it explicitly so
	// debug consumers know it isn't the raw provider payload.
	debugBody, _ := json.Marshal(map[string]interface{}{
		"_synthetic":        "stream-reconstructed",
		"model":             out.Model,
		"finish_reason":     out.FinishReason,
		"content":           out.Content,
		"tool_calls":        out.ToolCalls,
		"reasoning_details": out.ReasoningDetails,
		"usage": map[string]interface{}{
			"prompt_tokens":     out.PromptTokens,
			"completion_tokens": out.CompletionTokens,
			"cost":              out.Cost,
		},
	})
	out.DebugResponseBody = string(debugBody)

	if out.Model == "" && orReq.Model != "" {
		out.Model = orReq.Model
	}
	if out.Content == "" && len(out.ToolCalls) == 0 && out.FinishReason == "" {
		// Empty stream with no signals — almost certainly a provider error
		// that wasn't surfaced as an Err event. Fall back to error so the
		// caller doesn't treat this as a content-empty success.
		return out, errors.New("stream completed with no content, tool calls, or finish reason")
	}
	return out, nil
}
