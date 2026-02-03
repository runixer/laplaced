package rag

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestSplitStats_AddCost tests adding cost to stats.
func TestSplitStats_AddCost(t *testing.T) {
	t.Run("nil TotalCost initializes", func(t *testing.T) {
		stats := &SplitStats{}
		cost := 0.5
		stats.AddCost(&cost)

		assert.NotNil(t, stats.TotalCost)
		assert.InDelta(t, 0.5, *stats.TotalCost, 0.001)
	})

	t.Run("nil cost does nothing", func(t *testing.T) {
		stats := &SplitStats{}
		stats.AddCost(nil)

		assert.Nil(t, stats.TotalCost)
	})

	t.Run("accumulates cost", func(t *testing.T) {
		stats := &SplitStats{}
		cost1 := 0.3
		cost2 := 0.2
		stats.AddCost(&cost1)
		stats.AddCost(&cost2)

		assert.InDelta(t, 0.5, *stats.TotalCost, 0.001)
	})
}

// TestServiceSplitLargeTopics_Success tests successful topic splitting.
func TestServiceSplitLargeTopics_Success(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Large topics
	topics := []storage.Topic{
		{ID: 1, UserID: userID, SizeChars: 30000},
		{ID: 2, UserID: userID, SizeChars: 40000},
		{ID: 3, UserID: userID, SizeChars: 1000}, // Below threshold
	}
	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "Message 1"},
		{ID: 2, Role: "assistant", Content: "Response 1"},
	}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()
	// splitTopic calls GetMessagesByTopicID for each large topic
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(messages, nil).Once()
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return(messages, nil).Once()
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()
	mockStore.On("GetAllTopics").Return(topics, nil).Maybe()          // For ReloadVectors
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe() // For ReloadVectors

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	// Mock splitter agent (set but not used in extractTopicsForSplit)
	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	// Mock OpenRouter client for extractTopicsWithPrompt (actual code path)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		testutil.MockChatResponse(`{"topics":[{"summary":"Single topic","start_msg_id":1,"end_msg_id":2}]}`),
		nil,
	).Times(2)

	stats, err := svc.SplitLargeTopics(context.Background(), userID, 25000)
	assert.NoError(t, err)

	// Both large topics were "processed" but couldn't be split
	assert.Equal(t, 2, stats.TopicsProcessed)
	assert.Equal(t, 0, stats.TopicsCreated)

	mockStore.AssertExpectations(t)
	mockAgent.AssertExpectations(t)
}

// TestServiceSplitLargeTopics_AllUsers tests splitting for all users.
func TestServiceSplitLargeTopics_AllUsers(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	topics := []storage.Topic{
		{ID: 1, UserID: 123, SizeChars: 30000},
		{ID: 2, UserID: 456, SizeChars: 35000},
	}
	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "Message"},
	}

	mockStore.On("GetAllTopics").Return(topics, nil).Maybe() // Called multiple times
	// splitTopic calls GetMessagesByTopicID for each large topic
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(messages, nil).Once()
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return(messages, nil).Once()
	mockStore.On("GetFacts", int64(123)).Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetFacts", int64(456)).Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe() // For ReloadVectors

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	// Mock OpenRouter client for extractTopicsWithPrompt (actual code path)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		testutil.MockChatResponse(`{"topics":[{"summary":"Single","start_msg_id":1,"end_msg_id":5}]}`),
		nil,
	).Times(2)

	stats, err := svc.SplitLargeTopics(context.Background(), 0, 25000)
	assert.NoError(t, err)

	assert.Equal(t, 2, stats.TopicsProcessed)

	mockStore.AssertExpectations(t)
}

// TestServiceSplitLargeTopics_NoLargeTopics tests when no topics exceed threshold.
func TestServiceSplitLargeTopics_NoLargeTopics(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topics := []storage.Topic{
		{ID: 1, UserID: userID, SizeChars: 10000},
		{ID: 2, UserID: userID, SizeChars: 15000},
	}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.SplitLargeTopics(context.Background(), userID, 25000)
	assert.NoError(t, err)

	assert.Equal(t, 0, stats.TopicsProcessed)
	assert.Equal(t, 0, stats.TopicsCreated)

	mockStore.AssertExpectations(t)
}

// TestServiceSplitLargeTopics_ContextCanceled tests context cancellation.
func TestServiceSplitLargeTopics_ContextCanceled(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topics := []storage.Topic{
		{ID: 1, UserID: userID, SizeChars: 30000},
		{ID: 2, UserID: userID, SizeChars: 35000},
	}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	stats, err := svc.SplitLargeTopics(ctx, userID, 25000)
	assert.Error(t, err)
	assert.Equal(t, 0, stats.TopicsProcessed)

	mockStore.AssertExpectations(t)
}

// TestServiceSplitLargeTopics_GetTopicsError tests error handling.
func TestServiceSplitLargeTopics_GetTopicsError(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	mockStore.On("GetTopics", userID).Return(nil, errors.New("db error"))

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.SplitLargeTopics(context.Background(), userID, 25000)
	assert.Error(t, err)
	assert.Equal(t, 0, stats.TopicsProcessed)

	mockStore.AssertExpectations(t)
}

// TestServiceSplitTopic_EmptyTopic tests splitting topic with no messages.
func TestServiceSplitTopic_EmptyTopic(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	topic := storage.Topic{ID: 1, UserID: 123, SizeChars: 1000}

	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{}, nil).Once()
	mockStore.On("DeleteTopicCascade", int64(123), int64(1)).Return(nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	ids, stats, err := svc.splitTopic(context.Background(), topic)
	assert.NoError(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, 0, stats.TopicsCreated)

	mockStore.AssertExpectations(t)
}

// TestServiceSplitTopic_CannotSplit tests when topic cannot be split.
func TestServiceSplitTopic_CannotSplit(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	topic := storage.Topic{ID: 1, UserID: 123, SizeChars: 30000}
	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "Hello"},
		{ID: 2, Role: "assistant", Content: "Hi"},
	}

	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(messages, nil).Once()
	mockStore.On("GetFacts", int64(123)).Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	// Mock OpenRouter client for extractTopicsWithPrompt (actual code path)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		testutil.MockChatResponse(`{"topics":[{"summary":"Single topic","start_msg_id":1,"end_msg_id":2}]}`),
		nil,
	).Once()

	ids, stats, err := svc.splitTopic(context.Background(), topic)
	assert.NoError(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, 0, stats.TopicsCreated)

	mockStore.AssertExpectations(t)
}

// TestServiceExtractTopicsForSplit_Success tests topic extraction for splitting.
func TestServiceExtractTopicsForSplit_Success(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "First message", CreatedAt: time.Now()},
		{ID: 2, Role: "assistant", Content: "Response", CreatedAt: time.Now()},
	}

	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Once()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	// Mock OpenRouter client for extractTopicsWithPrompt (actual code path)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		testutil.MockChatResponseWithTokens(`{"topics":[`+
			`{"summary":"Part 1","start_msg_id":1,"end_msg_id":1},`+
			`{"summary":"Part 2","start_msg_id":2,"end_msg_id":2}`+
			`]}`, 100, 50),
		nil,
	).Once()

	topics, usage, err := svc.extractTopicsForSplit(context.Background(), userID, messages)
	assert.NoError(t, err)
	assert.Len(t, topics, 2)
	assert.Equal(t, "Part 1", topics[0].Summary)
	assert.Equal(t, int64(1), topics[0].StartMsgID)
	assert.Equal(t, int64(1), topics[0].EndMsgID)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 50, usage.CompletionTokens)

	mockStore.AssertExpectations(t)
}

// TestServiceExtractTopicsForSplit_AgentError tests error handling.
func TestServiceExtractTopicsForSplit_AgentError(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	messages := []storage.Message{{ID: 1, Role: "user", Content: "Hello"}}

	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Once()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	// Mock OpenRouter client to return error
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("LLM error")).Once()

	topics, usage, err := svc.extractTopicsForSplit(context.Background(), userID, messages)
	assert.Error(t, err)
	assert.Nil(t, topics)
	assert.Equal(t, UsageInfo{}, usage)

	mockStore.AssertExpectations(t)
}

// TestServiceExtractTopicsForSplit_NoFacts tests handling when facts fetch fails.
func TestServiceExtractTopicsForSplit_NoFacts(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	messages := []storage.Message{{ID: 1, Role: "user", Content: "Hello"}}

	// GetFacts returns error - should continue with empty profile
	mockStore.On("GetFacts", userID).Return(nil, errors.New("db error")).Once()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	// Mock OpenRouter client for extractTopicsWithPrompt (actual code path)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		testutil.MockChatResponseWithTokens(`{"topics":[{"summary":"Single","start_msg_id":1,"end_msg_id":1}]}`, 50, 20),
		nil,
	).Once()

	topics, usage, err := svc.extractTopicsForSplit(context.Background(), userID, messages)
	assert.NoError(t, err)
	assert.Len(t, topics, 1)
	assert.Equal(t, 50, usage.PromptTokens)
	assert.Equal(t, 20, usage.CompletionTokens)

	mockStore.AssertExpectations(t)
}

// TestExtractTopics_NoSplitterAgent tests error when splitter agent is not configured.
func TestExtractTopics_NoSplitterAgent(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	// Don't set splitter agent
	messages := []storage.Message{{ID: 1, Role: "user", Content: "Hello"}}

	topics, usage, err := svc.extractTopics(context.Background(), 123, messages)
	assert.Error(t, err)
	assert.Nil(t, topics)
	assert.Equal(t, UsageInfo{}, usage)
	assert.Contains(t, err.Error(), "splitter agent not configured")
}

// TestExtractTopicsViaAgent_UnexpectedResult tests error when agent returns wrong type.
func TestExtractTopicsViaAgent_UnexpectedResult(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	// Mock splitter agent that returns wrong type
	mockAgent := new(agenttesting.MockAgent)
	mockAgent.On("Type").Return(string(agent.TypeSplitter)).Maybe()
	mockAgent.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: "wrong type", // Not *splitter.Result
	}, nil)
	svc.SetSplitterAgent(mockAgent)

	messages := []storage.Message{{ID: 1, Role: "user", Content: "Hello"}}

	topics, usage, err := svc.extractTopics(context.Background(), 123, messages)
	assert.Error(t, err)
	assert.Nil(t, topics)
	assert.Equal(t, UsageInfo{}, usage)
	assert.Contains(t, err.Error(), "unexpected result type")
}

// TestServiceSplitTopic_SuccessfulSplit tests successful split of a topic into multiple topics.
func TestServiceSplitTopic_SuccessfulSplit(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Large topic with multiple messages
	topic := storage.Topic{ID: 1, UserID: userID, SizeChars: 30000, FactsExtracted: false}
	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "First part", CreatedAt: time.Now()},
		{ID: 2, Role: "assistant", Content: "Response 1", CreatedAt: time.Now()},
		{ID: 3, Role: "user", Content: "Second part", CreatedAt: time.Now()},
		{ID: 4, Role: "assistant", Content: "Response 2", CreatedAt: time.Now()},
	}

	// splitTopic calls GetMessagesByTopicID
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(messages, nil).Once()

	// extractTopicsForSplit calls GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Once()

	// extractTopicsForSplit calls GetTopicsExtended for recent topics (via GetRecentTopics)
	// GetRecentTopics uses default limit from config, filter with userID
	mockStore.On("GetTopicsExtended", mock.MatchedBy(func(f storage.TopicFilter) bool {
		return f.UserID == userID
	}), mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Once()

	// extractTopicsWithPrompt calls CreateChatCompletion
	// Return 2 topics split across the message range
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		testutil.MockChatResponseWithTokens(`{"topics":[`+
			`{"summary":"First topic","start_msg_id":1,"end_msg_id":2},`+
			`{"summary":"Second topic","start_msg_id":3,"end_msg_id":4}`+
			`]}`, 100, 50),
		nil,
	).Once()

	// splitTopic calls CreateEmbeddings for the new topics
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
		openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				{Embedding: []float32{0.4, 0.5, 0.6}, Index: 1},
			},
		},
		nil,
	).Once()

	// AddTopicWithoutMessageUpdate for each new topic
	mockStore.On("AddTopicWithoutMessageUpdate", mock.MatchedBy(func(t storage.Topic) bool {
		return t.Summary == "First topic" && t.StartMsgID == 1 && t.EndMsgID == 2
	})).Return(int64(10), nil).Once()

	mockStore.On("AddTopicWithoutMessageUpdate", mock.MatchedBy(func(t storage.Topic) bool {
		return t.Summary == "Second topic" && t.StartMsgID == 3 && t.EndMsgID == 4
	})).Return(int64(11), nil).Once()

	// GetFactsByTopicID for relinking facts
	mockStore.On("GetFactsByTopicID", userID, int64(1)).Return([]storage.Fact{}, nil).Once()

	// UpdateMessagesTopicInRange for each new topic
	mockStore.On("UpdateMessagesTopicInRange", mock.Anything, userID, int64(1), int64(2), int64(10)).Return(nil).Once()
	mockStore.On("UpdateMessagesTopicInRange", mock.Anything, userID, int64(3), int64(4), int64(11)).Return(nil).Once()

	// UpdateFactHistoryTopic for the split topic
	mockStore.On("UpdateFactHistoryTopic", int64(1), int64(10)).Return(nil).Maybe()

	// DeleteTopic for the old topic
	mockStore.On("DeleteTopic", userID, int64(1)).Return(nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	mockAgent := new(agenttesting.MockAgent)
	svc.SetSplitterAgent(mockAgent)

	ids, stats, err := svc.splitTopic(context.Background(), topic)

	assert.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, int64(10))
	assert.Contains(t, ids, int64(11))
	// Note: stats.TopicsCreated is incremented by SplitLargeTopics, not splitTopic
	// stats.TopicsCreated should be 0 here, but len(ids) tells us the actual count
	assert.Equal(t, 0, stats.TopicsCreated) // splitTopic doesn't increment this
	assert.Equal(t, 100, stats.PromptTokens)
	assert.Equal(t, 50, stats.CompletionTokens)
	// Note: stats.EmbeddingTokens is not set in splitTopic - it's set in SplitLargeTopics
	assert.Equal(t, 0, stats.EmbeddingTokens)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
}
