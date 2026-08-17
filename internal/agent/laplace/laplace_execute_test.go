package laplace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestExecuteToolCalls tests the executeToolCalls method.
func TestExecuteToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls []llm.ToolCall
		mockSetup func(*mockToolHandler)
		verify    func(*testing.T, []llm.Message)
	}{
		{
			name: "single successful tool call",
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_web",
						Arguments: `{"query": "test"}`,
					},
				},
			},
			mockSetup: func(h *mockToolHandler) {
				h.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", `{"query": "test"}`).
					Return(&ToolResult{Content: "Search results: found 5 items"}, nil)
			},
			verify: func(t *testing.T, msgs []llm.Message) {
				require.Len(t, msgs, 1)
				assert.Equal(t, "tool", msgs[0].Role)
				assert.Equal(t, "call_1", msgs[0].ToolCallID)
				assert.Equal(t, "Search results: found 5 items", msgs[0].Content)
			},
		},
		{
			name: "tool call with error",
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_2",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_history",
						Arguments: `{"query": "test"}`,
					},
				},
			},
			mockSetup: func(h *mockToolHandler) {
				h.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", mock.Anything).
					Return(nil, errors.New("database error"))
			},
			verify: func(t *testing.T, msgs []llm.Message) {
				require.Len(t, msgs, 1)
				assert.Equal(t, "tool", msgs[0].Role)
				assert.Contains(t, msgs[0].Content, "Tool execution failed")
				assert.Contains(t, msgs[0].Content, "database error")
			},
		},
		{
			name: "multiple tool calls",
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_web",
						Arguments: `{"query": "golang"}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_history",
						Arguments: `{"query": "memory"}`,
					},
				},
			},
			mockSetup: func(h *mockToolHandler) {
				h.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", `{"query": "golang"}`).
					Return(&ToolResult{Content: "Golang results"}, nil)
				h.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{"query": "memory"}`).
					Return(&ToolResult{Content: "History results"}, nil)
			},
			verify: func(t *testing.T, msgs []llm.Message) {
				require.Len(t, msgs, 2)
				assert.Equal(t, "call_1", msgs[0].ToolCallID)
				assert.Equal(t, "Golang results", msgs[0].Content)
				assert.Equal(t, "call_2", msgs[1].ToolCallID)
				assert.Equal(t, "History results", msgs[1].Content)
			},
		},
		{
			name:      "empty tool calls list",
			toolCalls: []llm.ToolCall{},
			mockSetup: func(h *mockToolHandler) {},
			verify: func(t *testing.T, msgs []llm.Message) {
				assert.Len(t, msgs, 0)
			},
		},
		{
			name: "mixed success and failure",
			toolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_web",
						Arguments: `{"query": "good"}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_history",
						Arguments: `{"query": "bad"}`,
					},
				},
			},
			mockSetup: func(h *mockToolHandler) {
				h.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", `{"query": "good"}`).
					Return(&ToolResult{Content: "Success"}, nil)
				h.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{"query": "bad"}`).
					Return(nil, errors.New("failed"))
			},
			verify: func(t *testing.T, msgs []llm.Message) {
				require.Len(t, msgs, 2)
				assert.Equal(t, "Success", msgs[0].Content)
				assert.Contains(t, msgs[1].Content, "Tool execution failed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := new(mockToolHandler)
			tt.mockSetup(handler)

			agent := &Laplace{
				logger: testutil.TestLogger(),
			}

			messages, _, _, _, _ := agent.executeToolCalls(
				context.Background(),
				handler,
				ToolCallContext{},
				tt.toolCalls,
				0,
				false,
				nil,
				agent.logger,
			)

			tt.verify(t, messages)
			handler.AssertExpectations(t)
		})
	}
}

// imgToolCall builds a generate_image tool call with the given id and prompt args.
func imgToolCall(id, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:   id,
		Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "generate_image", Arguments: args},
	}
}

func namedToolCall(id, name, args string) llm.ToolCall {
	call := imgToolCall(id, args)
	call.Function.Name = name
	return call
}

// TestExecuteToolCalls_ParallelImageGen verifies that multiple generate_image
// calls in one turn run concurrently yet the assembled messages and artifact
// IDs keep the declared order. The first call sleeps longest so it finishes
// last — if ordering followed completion (not index), the assertions would fail.
func TestExecuteToolCalls_ParallelImageGen(t *testing.T) {
	handler := new(mockToolHandler)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"cat"}`).
		Run(func(mock.Arguments) { time.Sleep(60 * time.Millisecond) }).
		Return(&ToolResult{Content: "img cat", GeneratedArtifactIDs: []int64{11}}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"dog"}`).
		Run(func(mock.Arguments) { time.Sleep(20 * time.Millisecond) }).
		Return(&ToolResult{Content: "img dog", GeneratedArtifactIDs: []int64{22}}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"bird"}`).
		Return(&ToolResult{Content: "img bird", GeneratedArtifactIDs: []int64{33}}, nil)

	agent := &Laplace{logger: testutil.TestLogger()}
	toolCalls := []llm.ToolCall{
		imgToolCall("c1", `{"prompt":"cat"}`),
		imgToolCall("c2", `{"prompt":"dog"}`),
		imgToolCall("c3", `{"prompt":"bird"}`),
	}

	start := time.Now()
	msgs, artifactIDs, generated, _, _ := agent.executeToolCalls(
		context.Background(), handler, ToolCallContext{}, toolCalls, 0, true, nil, agent.logger,
	)
	elapsed := time.Since(start)

	require.Len(t, msgs, 3)
	assert.Equal(t, "c1", msgs[0].ToolCallID)
	assert.Contains(t, msgs[0].Content, "img cat")
	assert.Contains(t, msgs[0].Content, "MEDIA:1")
	assert.NotContains(t, msgs[0].Content, "MEDIA:2")
	assert.Equal(t, "c2", msgs[1].ToolCallID)
	assert.Contains(t, msgs[1].Content, "img dog")
	assert.Contains(t, msgs[1].Content, "MEDIA:2")
	assert.NotContains(t, msgs[1].Content, "MEDIA:1,")
	assert.Equal(t, "c3", msgs[2].ToolCallID)
	assert.Contains(t, msgs[2].Content, "img bird")
	assert.Contains(t, msgs[2].Content, "MEDIA:3")
	assert.Equal(t, []int64{11, 22, 33}, artifactIDs)
	assert.Equal(t, []artifactdelivery.Generated{
		{ArtifactID: 11, Mode: artifactdelivery.ModePreview},
		{ArtifactID: 22, Mode: artifactdelivery.ModePreview},
		{ArtifactID: 33, Mode: artifactdelivery.ModePreview},
	}, generated, "legacy ID-only handlers default to preview in declared order")
	// Concurrent: wall time tracks the slowest call (~60ms), not the sum (~80ms).
	assert.Less(t, elapsed, 80*time.Millisecond, "generate_image calls should run in parallel")
	handler.AssertExpectations(t)
}

func TestExecuteToolCalls_DeliveryModesDriveMediaOrdinalsAndTypedOrder(t *testing.T) {
	handler := new(mockToolHandler)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"original"}`).
		Return(&ToolResult{
			Content: "original image",
			GeneratedArtifacts: []artifactdelivery.Generated{
				{ArtifactID: 11, Mode: artifactdelivery.ModeOriginal},
			},
		}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "send_artifacts", `{"items":[{"artifact_id":7,"mode":"original"}]}`).
		Return(&ToolResult{
			Content: "staged",
			SelectedArtifacts: []artifactdelivery.Selected{
				{ArtifactID: 7, Mode: artifactdelivery.ModeOriginal},
			},
		}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"both"}`).
		Return(&ToolResult{
			Content: "both image",
			GeneratedArtifacts: []artifactdelivery.Generated{
				{ArtifactID: 22, Mode: artifactdelivery.ModePreviewAndOriginal},
			},
		}, nil)

	agent := &Laplace{logger: testutil.TestLogger()}
	messages, artifactIDs, generated, selected, _ := agent.executeToolCalls(
		context.Background(), handler, ToolCallContext{}, []llm.ToolCall{
			imgToolCall("original", `{"prompt":"original"}`),
			namedToolCall("stored", "send_artifacts", `{"items":[{"artifact_id":7,"mode":"original"}]}`),
			imgToolCall("both", `{"prompt":"both"}`),
		}, 3, true, nil, agent.logger,
	)

	require.Len(t, messages, 3)
	assert.NotContains(t, messages[0].Content, "MEDIA:", "original-only consumes no ordinal")
	assert.NotContains(t, messages[1].Content, "MEDIA:")
	assert.Contains(t, messages[2].Content, "MEDIA:4", "preview_and_original contains one preview")
	assert.Equal(t, []int64{11, 22}, artifactIDs)
	assert.Equal(t, []artifactdelivery.Generated{
		{ArtifactID: 11, Mode: artifactdelivery.ModeOriginal},
		{ArtifactID: 22, Mode: artifactdelivery.ModePreviewAndOriginal},
	}, generated)
	assert.Equal(t, []artifactdelivery.Selected{
		{ArtifactID: 7, Mode: artifactdelivery.ModeOriginal},
	}, selected)
	handler.AssertExpectations(t)
}

func TestExecuteToolCalls_ImagePlacementRefsAreRichOnly(t *testing.T) {
	for _, tt := range []struct {
		name string
		rich bool
	}{
		{name: "rich", rich: true},
		{name: "legacy", rich: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := new(mockToolHandler)
			handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"pair"}`).
				Return(&ToolResult{
					Content:              "Generated 2 images (artifact:9001, artifact:9002).",
					GeneratedArtifactIDs: []int64{9001, 9002},
				}, nil)
			agent := &Laplace{logger: testutil.TestLogger()}

			messages, artifactIDs, _, _, _ := agent.executeToolCalls(
				context.Background(), handler, ToolCallContext{},
				[]llm.ToolCall{imgToolCall("pair", `{"prompt":"pair"}`)},
				3, tt.rich, nil, agent.logger,
			)

			require.Len(t, messages, 1)
			assert.Equal(t, []int64{9001, 9002}, artifactIDs)
			assert.Contains(t, messages[0].Content, "artifact:9001",
				"artifact IDs remain available for a later input_artifact_ids tool call")
			if tt.rich {
				assert.Contains(t, messages[0].Content, "MEDIA:4, MEDIA:5")
				assert.Contains(t, messages[0].Content, "Artifact IDs are only for input_artifact_ids")
			} else {
				assert.NotContains(t, messages[0].Content, "MEDIA:")
			}
			handler.AssertExpectations(t)
		})
	}
}

func TestExecuteToolCalls_ImagePlacementRefsSkipFailuresAndDisableAboveTen(t *testing.T) {
	handler := new(mockToolHandler)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"failed"}`).
		Return(&ToolResult{Content: "IMAGE GENERATION FAILED. Do not retry."}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"ten"}`).
		Return(&ToolResult{Content: "Generated (artifact:9010).", GeneratedArtifactIDs: []int64{9010}}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt":"eleven"}`).
		Return(&ToolResult{Content: "Generated (artifact:9011).", GeneratedArtifactIDs: []int64{9011}}, nil)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{"query":"context"}`).
		Return(&ToolResult{Content: "Later context"}, nil)
	agent := &Laplace{logger: testutil.TestLogger()}

	messages, artifactIDs, _, _, _ := agent.executeToolCalls(
		context.Background(), handler, ToolCallContext{},
		[]llm.ToolCall{
			imgToolCall("failed", `{"prompt":"failed"}`),
			imgToolCall("ten", `{"prompt":"ten"}`),
			imgToolCall("eleven", `{"prompt":"eleven"}`),
			namedToolCall("context", "search_history", `{"query":"context"}`),
		},
		9, true, nil, agent.logger,
	)

	require.Len(t, messages, 4)
	assert.Equal(t, []int64{9010, 9011}, artifactIDs)
	assert.NotContains(t, messages[0].Content, "MEDIA:", "failed calls consume no ordinal")
	assert.Contains(t, messages[1].Content, "MEDIA:10")
	assert.Contains(t, messages[2].Content, "MEDIA:11")
	assert.NotContains(t, messages[2].Content, "omit every",
		"the override must remain the terminal tool result")
	assert.Contains(t, messages[3].Content, "above the 10-image MEDIA layout limit")
	assert.Contains(t, messages[3].Content, "omit every ###MEDIA:...### directive")
	handler.AssertExpectations(t)
}

func TestExecuteToolCalls_ImageLayoutDisableIsStickyAcrossLaterBatches(t *testing.T) {
	handler := new(mockToolHandler)
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_history", `{"query":"later"}`).
		Return(&ToolResult{Content: "A later non-image result"}, nil)
	agent := &Laplace{logger: testutil.TestLogger()}

	messages, artifactIDs, _, _, _ := agent.executeToolCalls(
		context.Background(), handler, ToolCallContext{},
		[]llm.ToolCall{namedToolCall("later", "search_history", `{"query":"later"}`)},
		11, true, nil, agent.logger,
	)

	require.Len(t, messages, 1)
	assert.Empty(t, artifactIDs)
	assert.Contains(t, messages[0].Content, "above the 10-image MEDIA layout limit")
	assert.Contains(t, messages[0].Content, "omit every ###MEDIA:...### directive")
	handler.AssertExpectations(t)
}

func TestExecute_ImagePlacementRefsContinueAcrossToolIterations(t *testing.T) {
	_, _, agent, mockStore, mockORClient, handler := setupExecuteTest(t)
	userID := storage.ScopeID("123")

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("generate_image", `{"prompt":"first"}`, WithTokens(10, 2, 12)), nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.MatchedBy(func(tcc ToolCallContext) bool {
		return len(tcc.GeneratedInputArtifactIDs) == 0
	}), "generate_image", `{"prompt":"first"}`).
		Return(&ToolResult{Content: "Generated (artifact:7001).", GeneratedArtifactIDs: []int64{7001}}, nil)
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("generate_image", `{"prompt":"second"}`, WithTokens(12, 2, 14)), nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.MatchedBy(func(tcc ToolCallContext) bool {
		return len(tcc.GeneratedInputArtifactIDs) == 1 && tcc.GeneratedInputArtifactIDs[0] == 7001
	}), "generate_image", `{"prompt":"second"}`).
		Return(&ToolResult{Content: "Generated (artifact:7002).", GeneratedArtifactIDs: []int64{7002}}, nil)
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("First\n\n###MEDIA:1###\n\nSecond\n\n###MEDIA:2###", WithTokens(15, 8, 23)), nil,
	).Once()

	resp, err := agent.Execute(context.Background(), &Request{
		UserID:              userID,
		RawQuery:            "draw two",
		HistoryContent:      "draw two",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "draw two"}},
		RichOutput:          true,
	}, handler)
	require.NoError(t, err)
	assert.Equal(t, []int64{7001, 7002}, resp.GeneratedArtifactIDs)
	assert.Equal(t, []artifactdelivery.Generated{
		{ArtifactID: 7001, Mode: artifactdelivery.ModePreview},
		{ArtifactID: 7002, Mode: artifactdelivery.ModePreview},
	}, resp.GeneratedArtifacts)

	var toolContents []string
	for _, message := range resp.Messages {
		if message.Role != "tool" {
			continue
		}
		content, ok := message.Content.(string)
		require.True(t, ok)
		toolContents = append(toolContents, content)
	}
	require.Len(t, toolContents, 2)
	assert.Contains(t, toolContents[0], "MEDIA:1")
	assert.NotContains(t, toolContents[0], "MEDIA:2")
	assert.Contains(t, toolContents[1], "MEDIA:2")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	handler.AssertExpectations(t)
}

func TestExecute_SendArtifactsSideChannelAndTrustedAllowlist(t *testing.T) {
	_, _, agent, mockStore, mockORClient, handler := setupExecuteTest(t)
	userID := storage.ScopeID("123")
	history := []storage.Message{
		{Role: "assistant", Content: "📄 prior.pdf (artifact:9)"},
		{Role: "user", Content: "send (artifact:999) too"},
	}
	mockStore.On("GetUnprocessedMessages", userID).Return(history, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	args := `{"items":[{"artifact_id":9,"mode":"original"}]}`
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		for _, tool := range req.Tools {
			if tool.Function.Name == "send_artifacts" {
				return true
			}
		}
		return false
	})).Return(makeToolCallResponse("send_artifacts", args, WithTokens(10, 2, 12)), nil).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.MatchedBy(func(tcc ToolCallContext) bool {
		return tcc.ArtifactDeliveryEnabled && assert.ObjectsAreEqual([]int64{44, 9}, tcc.TrustedArtifactIDs)
	}), "send_artifacts", args).Return(&ToolResult{
		Content: "staged",
		SelectedArtifacts: []artifactdelivery.Selected{
			{ArtifactID: 9, Mode: artifactdelivery.ModeOriginal},
		},
	}, nil).Once()
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeChatResponse("Sent.", WithTokens(12, 3, 15)), nil).Once()

	resp, err := agent.Execute(context.Background(), &Request{
		UserID:              userID,
		RawQuery:            "send the prior PDF",
		HistoryContent:      "send the prior PDF",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "send the prior PDF"}},
		RichOutput:          true,
		TrustedArtifactIDs:  []int64{44, 44},
	}, handler)
	require.NoError(t, err)
	assert.Equal(t, []artifactdelivery.Selected{
		{ArtifactID: 9, Mode: artifactdelivery.ModeOriginal},
	}, resp.SelectedArtifacts)
	assert.Empty(t, resp.GeneratedArtifactIDs)
	assert.Empty(t, resp.GeneratedArtifacts)
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestExecute_HappyPath tests successful execution with a simple text response.
func TestExecute_HappyPath(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "Hello",
		HistoryContent:      "Hello",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "Hello"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Hello! How can I help you today?",
			WithID("resp-1"),
			WithModel("gemini-2.0-flash-exp"),
			WithTokens(10, 9, 19),
		), nil,
	)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Hello! How can I help you today?", resp.Content)
	assert.Equal(t, 10, resp.PromptTokens)
	assert.Equal(t, 9, resp.CompletionTokens)
	assert.Equal(t, 1, resp.TotalTurns)
	assert.Nil(t, resp.Error)

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)

	// Clean up
	_ = cfg
	_ = translator
}

// TestExecute_WithToolCall tests execution with a tool call.
func TestExecute_WithToolCall(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "Search for golang",
		HistoryContent:      "Search for golang",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "Search for golang"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First call: returns tool call
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("search_web", `{"query": "golang"}`,
			WithID("resp-1"),
			WithModel("gemini-2.0-flash-exp"),
			WithTokens(15, 20, 35),
		), nil,
	).Once()

	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", `{"query": "golang"}`).
		Return(&ToolResult{Content: "Golang is a programming language"}, nil)

	// Second call: returns final response
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Based on the search, Golang is a programming language created by Google.",
			WithID("resp-2"),
			WithModel("gemini-2.0-flash-exp"),
			WithTokens(50, 15, 65),
		), nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Based on the search, Golang is a programming language created by Google.", resp.Content)
	// Tokens accumulate across both LLM calls: (15+20) + (50+15) = 100
	assert.Equal(t, 100, resp.PromptTokens+resp.CompletionTokens)
	assert.Equal(t, 2, resp.TotalTurns)
	assert.Nil(t, resp.Error)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_LLMErrorAfterImageGen verifies the imagegen-lost-on-error fix:
// when a tool call has already generated an image and the post-tool LLM call
// fails terminally, the partial Response still carries the artifact ID so the
// bot layer can deliver the paid-for image.
func TestExecute_LLMErrorAfterImageGen(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "draw a cat",
		HistoryContent:      "draw a cat",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "draw a cat"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First call: model requests image generation; the tool succeeds and
	// produces artifact 42.
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("generate_image", `{"prompt": "a cat"}`,
			WithID("resp-1"),
			WithTokens(15, 20, 35),
		), nil,
	).Once()
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "generate_image", `{"prompt": "a cat"}`).
		Return(&ToolResult{Content: "image generated", GeneratedArtifactIDs: []int64{42}}, nil)

	// Second call (post-tool): terminal API failure after retries.
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(llm.ChatCompletionResponse{}, errors.New("429 upstream"))

	resp, err := agent.Execute(context.Background(), req, handler)
	assert.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, []int64{42}, resp.GeneratedArtifactIDs)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_CitationGuard verifies the final reply's source links are
// grounded against the URLs a search tool actually returned: a verified link
// survives, an invented one is unwrapped to plain text, and the stripped URL
// is surfaced on resp.StrippedURLs for the bot.anomaly.fabricated_url signal.
func TestExecute_CitationGuard(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "find a product",
		HistoryContent:      "find a product",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "find a product"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First call: model searches.
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("search_web", `{"query": "product"}`, WithTokens(10, 5, 15)), nil,
	).Once()

	// Search tool returns one real source URL.
	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", `{"query": "product"}`).
		Return(&ToolResult{
			Content:   "Found it [1].",
			Citations: []llm.Citation{{URL: "https://real.example/item/42", Title: "Item 42"}},
		}, nil)

	// Final reply: one verified link + one invented link.
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse(
			"Вот [товар](https://real.example/item/42) и ещё [похожий](https://invented.example/fake).",
			WithTokens(20, 10, 30),
		), nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verified link kept, invented one unwrapped to its anchor text.
	assert.Contains(t, resp.Content, "[товар](https://real.example/item/42)")
	assert.NotContains(t, resp.Content, "invented.example")
	assert.Contains(t, resp.Content, "похожий")
	assert.Equal(t, []string{"https://invented.example/fake"}, resp.StrippedURLs)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)
	_ = cfg
	_ = translator
}

// TestExecute_EmptyResponseWithRetry tests the empty response retry mechanism.
func TestExecute_EmptyResponseWithRetry(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First call empty, second call success - use Once() for each
	emptyResp := makeEmptyResponse(WithTokens(10, 0, 10))
	successResp := makeChatResponse("Now I have a response!", WithTokens(12, 6, 18))

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(emptyResp, nil).Once()
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(successResp, nil).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Now I have a response!", resp.Content)
	// TotalTurns includes both attempts (empty + retry)
	assert.Equal(t, 2, resp.TotalTurns)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_MaxEmptyRetries tests hitting the max empty retries limit.
func TestExecute_MaxEmptyRetries(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Always return empty response
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeEmptyResponse(WithTokens(10, 0, 10)), nil,
	)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Content)
	assert.Error(t, resp.Error)
	assert.Contains(t, resp.Error.Error(), "max empty response retries")

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_LLMError tests handling of LLM errors.
func TestExecute_LLMError(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(llm.ChatCompletionResponse{}, errors.New("API error"))

	// Fatal exits must land in agent_logs as failures — callers' LogExecution
	// never runs on nil resp (see the deferred closure in Execute).
	var logged storage.AgentLog
	mockStore.On("AddAgentLog", mock.Anything).Run(func(args mock.Arguments) {
		logged = args.Get(0).(storage.AgentLog)
	}).Return(nil)
	agent.SetAgentLogger(agentlog.NewLogger(mockStore, testutil.TestLogger(), true))

	resp, err := agent.Execute(context.Background(), req, handler)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "LLM call failed")
	// LLM-fatal returns a partial Response (err stays non-nil) so the bot
	// layer can deliver any generated artifacts produced before the failure.
	require.NotNil(t, resp)
	assert.Empty(t, resp.GeneratedArtifactIDs)

	assert.Equal(t, "laplace", logged.AgentType)
	assert.False(t, logged.Success)
	assert.Contains(t, logged.ErrorMessage, "API error")

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_ResponseSanitization tests hallucination tag sanitization.
func TestExecute_ResponseSanitization(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Response with hallucination tags
	hallucinatedContent := "This is the response.</tool_code>garbage text"
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse(hallucinatedContent, WithTokens(10, 10, 20)), nil,
	)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "This is the response.", resp.Content)
	assert.Equal(t, 1, resp.TotalTurns)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_WithPDFParserPlugin tests PDF parser plugin configuration.
func TestExecute_WithPDFParserPlugin(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.LLM.PDFParserEngine = "legacy"
	cfg.RAG.Enabled = false // Disable RAG since we pass nil for ragService

	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockLLMClient)

	// Pass nil for ragService and artifactRepo
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, testutil.TestLogger())

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Verify plugin is in request
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r llm.ChatCompletionRequest) bool {
		return len(r.Plugins) == 1 && r.Plugins[0].ID == "file-parser" && r.Plugins[0].PDF.Engine == "legacy"
	})).Return(
		makeChatResponse("Response", WithTokens(5, 3, 8)), nil,
	)

	handler := new(mockToolHandler)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Response", resp.Content)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestExecute_IntermediateMessageWithToolCall tests intermediate message callback during tool execution.
func TestExecute_IntermediateMessageWithToolCall(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")

	var intermediateMessages []string
	req := &Request{
		UserID:              userID,
		RawQuery:            "search",
		HistoryContent:      "search",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "search"}},
		OnIntermediateMessage: func(text string) {
			intermediateMessages = append(intermediateMessages, text)
		},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First response has both content and tool call
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallWithContentResponse("Let me search that for you.", "search_web", `{"query":"test"}`, "call_1",
			WithTokens(10, 15, 25),
		), nil,
	).Once()

	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", mock.Anything).Return(&ToolResult{Content: "Results"}, nil)

	// Second response: final
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Done searching.", WithTokens(30, 5, 35)), nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, "Done searching.", resp.Content)
	assert.Len(t, intermediateMessages, 1)
	assert.Equal(t, "Let me search that for you.", intermediateMessages[0])

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_MaxIterations_ForcesSynthesis tests that when the model keeps
// requesting tools until the iteration cap, the loop grants one final turn
// without tools (plus an explicit synthesis instruction) instead of returning
// the empty-response placeholder.
func TestExecute_MaxIterations_ForcesSynthesis(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)
	cfg.Agents.Chat.MaxToolIterations = 3

	userID := storage.ScopeID("123")
	req := &Request{
		UserID:              userID,
		RawQuery:            "loop",
		HistoryContent:      "loop",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "loop"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Every regular turn comes back as yet another tool call (infinite
	// search spiral scenario).
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r llm.ChatCompletionRequest) bool {
		return len(r.Tools) > 0 && r.ToolChoice == nil
	})).Return(
		makeToolCallWithContentResponse("Searching again...", "search_web", `{"query":"more"}`, "call_1",
			WithTokens(10, 0, 10),
		), nil,
	).Times(3)

	// The synthesis turn keeps the tool declarations but forbids calling
	// them (tool_choice=none) and ends with the synthesis instruction as a
	// system message.
	synthesisInstruction := translator.Get("en", "bot.tool_budget_synthesis")
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r llm.ChatCompletionRequest) bool {
		if r.ToolChoice != "none" || len(r.Messages) == 0 {
			return false
		}
		last := r.Messages[len(r.Messages)-1]
		content, _ := last.Content.(string)
		return last.Role == "system" && content == synthesisInstruction
	})).Return(
		makeChatResponse("Here is what I found so far.", WithTokens(30, 5, 35)), nil,
	).Once()

	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", mock.Anything).
		Return(&ToolResult{Content: "results"}, nil).Times(3)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, "Here is what I found so far.", resp.Content)
	assert.False(t, resp.WasEmpty)
	assert.Equal(t, 4, resp.TotalTurns) // 3 tool turns + 1 synthesis turn

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestLogExecution tests the LogExecution method.
func TestLogExecution(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG since we pass nil for ragService
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockLLMClient)

	// Pass nil for ragService and artifactRepo
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, testutil.TestLogger())

	// Create a real agent logger with nil repo (will be a no-op)
	agentLogger := agentlog.NewLogger(nil, testutil.TestLogger(), true)
	agent.agentLogger = agentLogger

	userID := storage.ScopeID("123")
	cost := 0.001

	resp := &Response{
		Content:          "Test response",
		PromptTokens:     10,
		CompletionTokens: 5,
		LLMDuration:      100 * time.Millisecond,
		ToolDuration:     50 * time.Millisecond,
		TotalTurns:       1,
		RAGInfo: &rag.RetrievalDebugInfo{
			OriginalQuery: "test query",
			EnrichedQuery: "enriched query",
		},
		Messages: []llm.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
		},
	}

	// LogExecution should not panic even with nil repo
	agent.LogExecution(context.Background(), userID, resp, cost)

	_ = cfg
	_ = translator
}

// TestExecute_WithToolStart tests the per-tool start callback during tool execution.
func TestExecute_WithToolStart(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := storage.ScopeID("123")
	typingCallCount := 0
	var seenToolNames []string
	req := &Request{
		UserID:              userID,
		RawQuery:            "search",
		HistoryContent:      "search",
		CurrentMessageParts: []interface{}{llm.TextPart{Type: "text", Text: "search"}},
		OnToolStart: func(toolName, arguments string) {
			typingCallCount++
			seenToolNames = append(seenToolNames, toolName)
			_ = arguments
		},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("search_web", `{}`),
		nil,
	).Once()

	handler.On("ExecuteToolCall", mock.Anything, mock.Anything, "search_web", mock.Anything).Return(&ToolResult{Content: "done"}, nil)

	// Second call: no more tools
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Final", WithTokens(20, 5, 25)),
		nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, typingCallCount, "OnToolStart should be called once per tool execution")
	assert.Equal(t, []string{"search_web"}, seenToolNames, "OnToolStart should receive the tool name")
	assert.Equal(t, "Final", resp.Content)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// setupExecuteTest is a helper for Execute tests.
func setupExecuteTest(t *testing.T) (*config.Config, *i18n.Translator, *Laplace, *testutil.MockStorage, *testutil.MockLLMClient, *mockToolHandler) {
	t.Helper()

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG since we pass nil for ragService
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockLLMClient)

	// Pass nil for ragService and artifactRepo (concrete structs, not interfaces)
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, testutil.TestLogger())

	handler := new(mockToolHandler)

	return cfg, translator, agent, mockStore, mockORClient, handler
}
