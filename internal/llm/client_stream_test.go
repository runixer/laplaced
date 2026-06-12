package llm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamFromBody serves a fixed SSE body with proper headers and a flusher.
// Each frame in `frames` is sent verbatim followed by `\n\n`. Caller must
// include the `data:` prefix where appropriate.
func sseHandler(t *testing.T, frames []string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request shape: streaming method must request SSE.
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			assert.Equal(t, true, body["stream"], "stream:true must be set on streaming requests")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
}

func newStreamTestClient(t *testing.T, h http.Handler) (*httptest.Server, Client) {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)
	return server, client
}

func collectChunks(t *testing.T, ch <-chan StreamEvent, deadline time.Duration) ([]ChatCompletionChunk, error) {
	t.Helper()
	var chunks []ChatCompletionChunk
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return chunks, nil
			}
			if ev.Err != nil {
				return chunks, ev.Err
			}
			chunks = append(chunks, *ev.Chunk)
		case <-timer.C:
			t.Fatal("timeout waiting for stream events")
		}
	}
}

func TestStream_BasicContentDeltas(t *testing.T) {
	frames := []string{
		`: OPENROUTER PROCESSING`, // keep-alive
		`data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`: keep-alive`,
		`data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":", world"}}]}`,
		`data: {"id":"g1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
		`data: [DONE]`,
	}
	_, client := newStreamTestClient(t, sseHandler(t, frames))

	stream, err := client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	chunks, err := collectChunks(t, stream.Events, 2*time.Second)
	require.NoError(t, err)
	require.Len(t, chunks, 3)

	var contentBuilder strings.Builder
	for _, c := range chunks {
		contentBuilder.WriteString(c.Choices[0].Delta.Content)
	}
	assert.Equal(t, "Hello, world!", contentBuilder.String())

	last := chunks[len(chunks)-1]
	assert.Equal(t, "stop", last.Choices[0].FinishReason)
	require.NotNil(t, last.Usage)
	assert.Equal(t, 5, last.Usage.PromptTokens)
	assert.Equal(t, 3, last.Usage.CompletionTokens)
}

func TestStream_ReasoningDeltasFirstThenContent(t *testing.T) {
	// Mirrors observed Gemini 3 Pro behavior: reasoning chunks arrive first
	// with empty delta.content, then user-visible content chunks.
	frames := []string{
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning":"thinking step 1","reasoning_details":[{"type":"reasoning.text","text":"thinking step 1"}]}}]}`,
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"","reasoning":"thinking step 2","reasoning_details":[{"type":"reasoning.text","text":"thinking step 2"}]}}]}`,
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Final answer."}}]}`,
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`data: [DONE]`,
	}
	_, client := newStreamTestClient(t, sseHandler(t, frames))

	stream, err := client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.NoError(t, err)

	chunks, err := collectChunks(t, stream.Events, 2*time.Second)
	require.NoError(t, err)
	require.Len(t, chunks, 4)

	// First two chunks: reasoning populated, content empty.
	assert.Equal(t, "", chunks[0].Choices[0].Delta.Content)
	assert.Equal(t, "thinking step 1", chunks[0].Choices[0].Delta.Reasoning)
	assert.NotNil(t, chunks[0].Choices[0].Delta.ReasoningDetails)

	// Third chunk: user-visible content.
	assert.Equal(t, "Final answer.", chunks[2].Choices[0].Delta.Content)
}

func TestStream_ToolCallDeltasByIndex(t *testing.T) {
	// Tool calls stream as delta fragments addressed by `index`. Consumers
	// must concatenate `function.arguments` per index.
	frames := []string{
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_history","arguments":"{\"query\":\""}}]}}]}`,
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"hello"}}]}}]}`,
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}`,
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
	}
	_, client := newStreamTestClient(t, sseHandler(t, frames))

	stream, err := client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.NoError(t, err)

	chunks, err := collectChunks(t, stream.Events, 2*time.Second)
	require.NoError(t, err)
	require.Len(t, chunks, 4)

	// First chunk has full id/name; subsequent chunks contribute argument fragments.
	assert.Equal(t, "call_1", chunks[0].Choices[0].Delta.ToolCalls[0].ID)
	assert.Equal(t, "search_history", chunks[0].Choices[0].Delta.ToolCalls[0].Function.Name)
	assert.Equal(t, "{\"query\":\"", chunks[0].Choices[0].Delta.ToolCalls[0].Function.Arguments)
	assert.Equal(t, "hello", chunks[1].Choices[0].Delta.ToolCalls[0].Function.Arguments)
	assert.Equal(t, "\"}", chunks[2].Choices[0].Delta.ToolCalls[0].Function.Arguments)

	// Final chunk: finish_reason and usage, no tool deltas.
	assert.Equal(t, "tool_calls", chunks[3].Choices[0].FinishReason)
	require.NotNil(t, chunks[3].Usage)
}

func TestStream_StreamEndedWithoutDONE(t *testing.T) {
	// Server closes the body cleanly but never emits [DONE]. We must surface
	// this as an error event, not a clean finish.
	frames := []string{
		`data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
	}
	_, client := newStreamTestClient(t, sseHandler(t, frames))

	stream, err := client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.NoError(t, err)

	chunks, err := collectChunks(t, stream.Events, 2*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[DONE]")
	require.Len(t, chunks, 1)
	assert.Equal(t, "partial", chunks[0].Choices[0].Delta.Content)
}

func TestStream_DecodeError(t *testing.T) {
	frames := []string{
		`data: {not valid json`,
		`data: [DONE]`,
	}
	_, client := newStreamTestClient(t, sseHandler(t, frames))

	stream, err := client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.NoError(t, err)

	_, err = collectChunks(t, stream.Events, 2*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode SSE chunk")
}

func TestStream_ContextCancellation(t *testing.T) {
	// Server hangs writing the second frame; we cancel context.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"a"}}]}`+"\n\n")
		flusher.Flush()
		// Hold connection open to simulate a long-running stream.
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "k", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.CreateChatCompletionStream(ctx, ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.NoError(t, err)

	// Read first event then cancel.
	select {
	case ev := <-stream.Events:
		require.NotNil(t, ev.Chunk)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first chunk")
	}
	cancel()

	// Channel should close (possibly with a trailing error event) within deadline.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-stream.Events:
			if !ok {
				return // closed; good.
			}
		case <-deadline:
			t.Fatal("stream did not close after context cancellation")
		}
	}
}

func TestStream_NonOKStatusReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "k", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestStream_RetriesOn5xx(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"id":"g","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"ok"}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: [DONE]`+"\n\n")
		flusher.Flush()
	}))
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "k", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	stream, err := client.CreateChatCompletionStream(context.Background(), ChatCompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	require.NoError(t, err)
	chunks, err := collectChunks(t, stream.Events, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "ok", chunks[0].Choices[0].Delta.Content)
	assert.GreaterOrEqual(t, attempts, 2, "retried at least once")
}
