package laplace

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// makeStreamChunks builds a slice of ChatCompletionChunks. helpers below.

func contentChunk(text string, finishReason string) llm.ChatCompletionChunk {
	return llm.ChatCompletionChunk{
		Object: "chat.completion.chunk",
		Choices: []llm.ChunkChoice{{
			Index:        0,
			Delta:        llm.ChunkDelta{Content: text},
			FinishReason: finishReason,
		}},
	}
}

func reasoningChunk(reasoningText string, detail interface{}) llm.ChatCompletionChunk {
	return llm.ChatCompletionChunk{
		Object: "chat.completion.chunk",
		Choices: []llm.ChunkChoice{{
			Index: 0,
			Delta: llm.ChunkDelta{Reasoning: reasoningText, ReasoningDetails: detail},
		}},
	}
}

func toolCallChunk(idx int, id, name, argsFragment string) llm.ChatCompletionChunk {
	tc := llm.ChunkToolCall{Index: idx, ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = argsFragment
	return llm.ChatCompletionChunk{
		Object: "chat.completion.chunk",
		Choices: []llm.ChunkChoice{{
			Index: 0,
			Delta: llm.ChunkDelta{ToolCalls: []llm.ChunkToolCall{tc}},
		}},
	}
}

func usageChunk(prompt, completion int) llm.ChatCompletionChunk {
	return llm.ChatCompletionChunk{
		Object: "chat.completion.chunk",
		Choices: []llm.ChunkChoice{{
			Index:        0,
			Delta:        llm.ChunkDelta{},
			FinishReason: "stop",
		}},
		Usage: &llm.ChunkUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion},
	}
}

// TestStreaming_ContentDeltasForwarded verifies that content fragments arrive
// at OnContentDelta in order during the final iteration of the tool loop.
func TestStreaming_ContentDeltasForwarded(t *testing.T) {
	cfg, _, agent, mockStore, mockOR, handler := setupExecuteTest(t)
	_ = cfg
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			contentChunk("Hello", ""),
			contentChunk(", ", ""),
			contentChunk("world!", "stop"),
			usageChunk(10, 3),
		),
		nil,
	).Once()

	var deltas []string
	req := &Request{
		UserID:              userID,
		RawQuery:            "hi",
		HistoryContent:      "hi",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "hi"}},
		UseStreaming:        true,
		OnContentDelta:      func(s string) { deltas = append(deltas, s) },
	}

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, []string{"Hello", ", ", "world!"}, deltas)
	assert.Equal(t, "Hello, world!", resp.Content)
	assert.Greater(t, resp.FirstContentDelay.Nanoseconds(), int64(0), "FirstContentDelay must be set when content was streamed")

	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

// TestStreaming_ToolCallSuppressesContentForwarding verifies that once a
// tool-call delta appears in the stream, subsequent content fragments do NOT
// reach OnContentDelta. Any content that arrived BEFORE the tool delta is
// surfaced via OnIntermediateMessage to mirror buffered-mode semantics.
func TestStreaming_ToolCallSuppressesContentForwarding(t *testing.T) {
	cfg, _, agent, mockStore, mockOR, handler := setupExecuteTest(t)
	_ = cfg
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Iteration 1: a small content prelude, then a tool call. Content should
	// NOT continue into OnContentDelta after the tool delta arrives.
	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			contentChunk("Let me check…", ""),
			toolCallChunk(0, "call_1", "search_history", `{"query":"x"}`),
			contentChunk(" (suppressed)", ""),
			usageChunk(10, 3),
		),
		nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{"query":"x"}`).
		Return(&ToolResult{Content: "ok"}, nil)

	// Iteration 2: pure content (final answer).
	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			contentChunk("Final answer.", "stop"),
			usageChunk(20, 5),
		),
		nil,
	).Once()

	var deltas []string
	var intermediates []string
	req := &Request{
		UserID:                userID,
		RawQuery:              "hi",
		HistoryContent:        "hi",
		CurrentMessageParts:   []interface{}{llm.TextPart{Type: "text", Text: "hi"}},
		UseStreaming:          true,
		OnContentDelta:        func(s string) { deltas = append(deltas, s) },
		OnIntermediateMessage: func(s string) { intermediates = append(intermediates, s) },
	}

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)

	// Pre-tool prelude reached OnContentDelta (we forward eagerly until a
	// tool delta is observed). The post-tool-delta content was suppressed.
	assert.Equal(t, []string{"Let me check…", "Final answer."}, deltas,
		"forwarding halts on first tool-call delta; resumes in the final iteration")
	assert.Equal(t, []string{"Let me check… (suppressed)"}, intermediates,
		"buffered tool-iteration content surfaces via OnIntermediateMessage")

	assert.Equal(t, "Final answer.", resp.Content)

	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestStreaming_ReasoningDetailsAccumulated verifies that reasoning_details
// from every delta are merged and propagated to the assistant message used
// for follow-up tool turns. The encrypted blob is required by the provider
// to continue the chain.
func TestStreaming_ReasoningDetailsAccumulated(t *testing.T) {
	cfg, _, agent, mockStore, mockOR, handler := setupExecuteTest(t)
	_ = cfg
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	r1 := []interface{}{map[string]interface{}{"type": "reasoning.encrypted", "data": "AAA"}}
	r2 := []interface{}{map[string]interface{}{"type": "reasoning.encrypted", "data": "BBB"}}
	r3 := []interface{}{map[string]interface{}{"type": "reasoning.encrypted", "data": "CCC"}}

	// Iteration 1: 3 reasoning chunks, then tool call. We assert the
	// accumulated reasoning_details ends up on the assistant message that
	// gets appended to orMessages for the next turn.
	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			reasoningChunk("a", r1),
			reasoningChunk("b", r2),
			reasoningChunk("c", r3),
			toolCallChunk(0, "call_1", "search_history", `{}`),
			usageChunk(5, 5),
		),
		nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{}`).
		Return(&ToolResult{Content: "x"}, nil)

	// Iteration 2: simple content.
	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			contentChunk("Done.", "stop"),
			usageChunk(10, 1),
		),
		nil,
	).Once()

	req := &Request{
		UserID:              userID,
		RawQuery:            "hi",
		HistoryContent:      "hi",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "hi"}},
		UseStreaming:        true,
	}

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)

	// Find the assistant message that carries tool_calls. Its
	// ReasoningDetails should contain the merged blobs from all three deltas.
	var found bool
	for _, m := range resp.Messages {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		details, ok := m.ReasoningDetails.([]interface{})
		require.True(t, ok, "ReasoningDetails on tool-call assistant message should be []interface{}")
		assert.Len(t, details, 3, "all reasoning_details from stream chunks merged")
		found = true
		break
	}
	assert.True(t, found, "expected one assistant message with tool calls")

	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestStreaming_ToolCallArgumentsConcatenated verifies that arguments
// fragments addressed by index merge into a single arguments string.
func TestStreaming_ToolCallArgumentsConcatenated(t *testing.T) {
	cfg, _, agent, mockStore, mockOR, handler := setupExecuteTest(t)
	_ = cfg
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			toolCallChunk(0, "call_1", "search_history", `{"query":"`),
			toolCallChunk(0, "", "", `hello`),
			toolCallChunk(0, "", "", `"}`),
			usageChunk(5, 5),
		),
		nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{"query":"hello"}`).
		Return(&ToolResult{Content: "ok"}, nil)

	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			contentChunk("Done.", "stop"),
			usageChunk(10, 1),
		),
		nil,
	).Once()

	req := &Request{
		UserID:              userID,
		RawQuery:            "hi",
		HistoryContent:      "hi",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "hi"}},
		UseStreaming:        true,
	}

	_, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)

	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestStreaming_OnToolStartReceivesToolName confirms the per-tool callback
// is fired with the actual tool name from the stream-aggregated tool call.
func TestStreaming_OnToolStartReceivesToolName(t *testing.T) {
	cfg, _, agent, mockStore, mockOR, handler := setupExecuteTest(t)
	_ = cfg
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			toolCallChunk(0, "call_1", "internet_search", `{"query":"q"}`),
			usageChunk(5, 5),
		),
		nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "internet_search", `{"query":"q"}`).
		Return(&ToolResult{Content: "ok"}, nil)

	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		testutil.StreamEventsFromChunks(
			contentChunk("Final.", "stop"),
			usageChunk(10, 1),
		),
		nil,
	).Once()

	var seen []string
	req := &Request{
		UserID:              userID,
		RawQuery:            "hi",
		HistoryContent:      "hi",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "hi"}},
		UseStreaming:        true,
		OnToolStart:         func(name, _ string) { seen = append(seen, name) },
	}

	_, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, []string{"internet_search"}, seen)

	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestStreaming_DebugBodiesShapeForAgentLog locks down the contract the web
// agent-log UI relies on: each ConversationTurn must carry the raw request
// body and a synthetic response shaped like the buffered chat-completion payload
// (choices[0].message.content). Regression guard for the two display bugs
// where the streaming path left Request="" and Response in a flat shape with
// no "choices" array.
func TestStreaming_DebugBodiesShapeForAgentLog(t *testing.T) {
	cfg, _, agent, mockStore, mockOR, handler := setupExecuteTest(t)
	_ = cfg
	_ = handler
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	const debugReq = `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	events := testutil.StreamEventsFromChunks(
		contentChunk("Hello", ""),
		contentChunk(" world", "stop"),
		usageChunk(7, 2),
	)
	mockOR.On("CreateChatCompletionStream", mock.Anything, mock.Anything).Return(
		&llm.ChatCompletionStream{Events: events, DebugRequestBody: debugReq},
		nil,
	).Once()

	req := &Request{
		UserID:              userID,
		RawQuery:            "hi",
		HistoryContent:      "hi",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "hi"}},
		UseStreaming:        true,
	}

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	require.NotNil(t, resp.ConversationTurns)
	require.Len(t, resp.ConversationTurns.Turns, 1)

	turn := resp.ConversationTurns.Turns[0]

	reqStr, ok := turn.Request.(string)
	require.True(t, ok, "Turn.Request should be a string for the agentlog turn tracker")
	assert.Equal(t, debugReq, reqStr, "DebugRequestBody must be plumbed verbatim from the stream client")

	respStr, ok := turn.Response.(string)
	require.True(t, ok, "Turn.Response should be a string for the agentlog turn tracker")

	var respObj map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(respStr), &respObj), "synthetic response must be valid JSON")
	assert.Equal(t, "stream-reconstructed", respObj["_synthetic"], "synthetic marker preserved")

	choices, ok := respObj["choices"].([]interface{})
	require.True(t, ok, "synthetic response must have a choices array (buffered chat-completion shape)")
	require.Len(t, choices, 1)
	choice, ok := choices[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "stop", choice["finish_reason"])
	message, ok := choice["message"].(map[string]interface{})
	require.True(t, ok, "choices[0].message must be present")
	assert.Equal(t, "assistant", message["role"])
	assert.Equal(t, "Hello world", message["content"])

	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}
