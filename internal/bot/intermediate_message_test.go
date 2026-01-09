package bot

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestProcessMessageGroup_IntermediateMessageSending(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := createTestConfig()
	cfg.RAG.Enabled = false
	cfg.Tools = []config.ToolConfig{
		{
			Name:  "test_tool",
			Model: "test-tool-model",
		},
	}

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)

	// Test Data
	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Test message",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      1234567890,
		},
	}

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)

	expectedHistoryContent := messages[0].BuildContent(translator, "en")
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == expectedHistoryContent
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock API calls
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Track all messages sent to Telegram
	var sentMessages []string
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		req := args.Get(1).(telegram.SendMessageRequest)
		sentMessages = append(sentMessages, req.Text)
	}).Return(&telegram.Message{}, nil)

	// Mock OpenRouter calls
	// First call: Model returns intermediate text + tool call
	intermediateText := "Let me search for that information..."
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// First call has only system + user message
		return len(req.Messages) == 2
	})).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason,omitempty"`
			Index        int    `json:"index"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{
				Role:    "assistant",
				Content: intermediateText,
				ToolCalls: []openrouter.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "test_tool",
							Arguments: `{"query":"test query"}`,
						},
					},
				},
			}, FinishReason: "tool_calls"},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil).Once()

	// Mock the tool execution
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// Tool execution call
		return req.Model == "test-tool-model"
	})).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason,omitempty"`
			Index        int    `json:"index"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{
				Role:    "assistant",
				Content: "Tool result data",
			}, FinishReason: "stop"},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
	}, nil).Once()

	// Second call: Model returns final response after tool execution
	finalText := "Based on the search results, here is the answer..."
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// Second call has system + user + assistant (with tool call) + tool result
		return len(req.Messages) == 4
	})).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason,omitempty"`
			Index        int    `json:"index"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{
				Role:    "assistant",
				Content: finalText,
			}, FinishReason: "stop"},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
	}, nil).Once()

	// Execute
	group := &MessageGroup{
		Messages: messages,
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	mockAPI.AssertExpectations(t)

	// Verify that two messages were sent to Telegram
	assert.Len(t, sentMessages, 2, "Should send intermediate message and final message")

	// Verify intermediate message was sent first
	assert.Contains(t, sentMessages[0], intermediateText, "First message should be the intermediate text")

	// Verify final message was sent second
	assert.Contains(t, sentMessages[1], finalText, "Second message should be the final response")
}
