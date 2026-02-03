package laplace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockRetriever implements rag.Retriever for testing.
// Note: This inline mock is intentional. Using testutil.MockRetriever would require
// testutil to import rag, but rag tests import testutil, creating an import cycle.
// Since laplace doesn't have this cycle issue, we define the mock here.
type mockRetriever struct {
	mock.Mock
}

func (m *mockRetriever) GetRecentTopics(userID int64, limit int) ([]storage.TopicExtended, error) {
	args := m.Called(userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.TopicExtended), args.Error(1)
}

func (m *mockRetriever) Retrieve(ctx context.Context, userID int64, query string, opts *rag.RetrievalOptions) (*rag.RetrievalResult, *rag.RetrievalDebugInfo, error) {
	args := m.Called(ctx, userID, query, opts)
	if args.Get(0) == nil {
		return nil, nil, args.Error(2)
	}
	return args.Get(0).(*rag.RetrievalResult), args.Get(1).(*rag.RetrievalDebugInfo), args.Error(2)
}

// setupContextTest creates a test Laplace agent with all mocks.
func setupContextTest(t *testing.T) (*config.Config, *i18n.Translator, *Laplace, *testutil.MockStorage, *testutil.MockOpenRouterClient, *mockRetriever) {
	t.Helper()

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG for tests that don't need it
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockRAG := new(mockRetriever)

	// Setup default RAG mock expectations (return empty results)
	mockRAG.On("Retrieve", mock.Anything, mock.Anything, mock.Anything, mock.AnythingOfType("*rag.RetrievalOptions")).
		Return(&rag.RetrievalResult{}, &rag.RetrievalDebugInfo{}, nil)
	mockRAG.On("GetRecentTopics", mock.Anything, mock.Anything).
		Return([]storage.TopicExtended{}, nil)

	lap := New(cfg, mockORClient, mockRAG, mockStore, mockStore, nil, translator, testutil.TestLogger())

	return cfg, translator, lap, mockStore, mockORClient, mockRAG
}

// TestLoadContextData_WithSharedContext tests LoadContextData when SharedContext is provided.
func TestLoadContextData_WithSharedContext(t *testing.T) {
	_, translator, lap, mockStore, _, _ := setupContextTest(t)

	userID := int64(123)
	rawQuery := "test query"

	// Create SharedContext
	sharedCtx := &agent.SharedContext{
		UserID:       userID,
		Profile:      "<user_profile>\n  Name: Test User\n</user_profile>",
		RecentTopics: "<recent_topics>\n  <topic>Test</topic>\n</recent_topics>",
		InnerCircle:  "<inner_circle>\n  <person>John</person>\n</inner_circle>",
		Language:     "en",
		LoadedAt:     time.Now(),
	}

	ctx := agent.WithContext(context.Background(), sharedCtx)

	// Mock unprocessed messages
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{ID: 1, Role: "user", Content: "Hello", CreatedAt: time.Now()},
		{ID: 2, Role: "assistant", Content: "Hi", CreatedAt: time.Now()},
	}, nil)

	// GetFacts should not be called when using SharedContext
	// GetUnprocessedMessages is still called to get recent history

	contextData, err := lap.LoadContextData(ctx, userID, rawQuery, nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.Equal(t, userID, contextData.UserID)
	assert.Equal(t, sharedCtx.Profile, contextData.ProfileFacts)
	assert.Equal(t, sharedCtx.RecentTopics, contextData.RecentTopics)
	assert.Equal(t, sharedCtx.InnerCircle, contextData.InnerCircle)
	assert.Len(t, contextData.RecentHistory, 2)
	assert.NotEmpty(t, contextData.BaseSystemPrompt)
	// RAG is skipped when ragService is nil
	assert.Nil(t, contextData.RAGResults)

	mockStore.AssertExpectations(t)

	_ = translator
}

// TestLoadContextData_WithoutSharedContext tests LoadContextData without SharedContext (fallback to DB).
func TestLoadContextData_WithoutSharedContext(t *testing.T) {
	cfg, translator, lap, mockStore, _, _ := setupContextTest(t)
	cfg.RAG.Enabled = false // Disable RAG to avoid ragService calls

	userID := int64(123)
	rawQuery := "test query"

	// No SharedContext in context
	ctx := context.Background()

	// Mock unprocessed messages
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{ID: 1, Role: "user", Content: "Hello", CreatedAt: time.Now()},
	}, nil)

	// Mock facts (profile fallback)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{
		{ID: 1, Category: "identity", Content: "Name is Alice"},
	}, nil)

	contextData, err := lap.LoadContextData(ctx, userID, rawQuery, nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.Equal(t, userID, contextData.UserID)
	assert.NotEmpty(t, contextData.ProfileFacts)
	// RecentTopics should be empty when RAG is disabled
	assert.Empty(t, contextData.RecentTopics)
	assert.Len(t, contextData.RecentHistory, 1)
	assert.NotEmpty(t, contextData.BaseSystemPrompt)

	mockStore.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLoadContextData_RAGDisabled tests LoadContextData with RAG disabled.
func TestLoadContextData_RAGDisabled(t *testing.T) {
	cfg, translator, lap, mockStore, _, _ := setupContextTest(t)
	cfg.RAG.Enabled = false

	userID := int64(123)
	rawQuery := "test query"

	ctx := context.Background()

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	contextData, err := lap.LoadContextData(ctx, userID, rawQuery, nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.Nil(t, contextData.RAGResults)
	assert.Nil(t, contextData.RAGInfo)

	mockStore.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLoadContextData_MessageLimiting tests MaxContextMessages limiting.
func TestLoadContextData_MessageLimiting(t *testing.T) {
	cfg, translator, lap, mockStore, _, _ := setupContextTest(t)
	cfg.RAG.Enabled = false
	cfg.RAG.MaxContextMessages = 3

	userID := int64(123)

	ctx := context.Background()

	// Return 5 messages, but only 3 should be kept
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{ID: 1, Role: "user", Content: "msg1"},
		{ID: 2, Role: "user", Content: "msg2"},
		{ID: 3, Role: "user", Content: "msg3"},
		{ID: 4, Role: "user", Content: "msg4"},
		{ID: 5, Role: "user", Content: "msg5"},
	}, nil)

	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	contextData, err := lap.LoadContextData(ctx, userID, "test", nil)
	require.NoError(t, err)
	assert.Len(t, contextData.RecentHistory, 3, "should limit to MaxContextMessages")
	// Should keep the LAST 3 messages
	assert.Equal(t, int64(3), contextData.RecentHistory[0].ID)
	assert.Equal(t, int64(5), contextData.RecentHistory[2].ID)

	mockStore.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLoadContextData_GetFactsError tests error handling when GetFacts fails.
func TestLoadContextData_GetFactsError(t *testing.T) {
	cfg, translator, lap, mockStore, _, _ := setupContextTest(t)
	cfg.RAG.Enabled = false

	userID := int64(123)

	ctx := context.Background()

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return(nil, errors.New("database error"))

	// Should not return error, just log and continue
	contextData, err := lap.LoadContextData(ctx, userID, "test", nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.Empty(t, contextData.ProfileFacts) // Should be empty on error

	mockStore.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLoadContextData_GetUnprocessedMessagesError tests error handling when GetUnprocessedMessages fails.
func TestLoadContextData_GetUnprocessedMessagesError(t *testing.T) {
	cfg, translator, lap, mockStore, _, _ := setupContextTest(t)
	cfg.RAG.Enabled = false

	userID := int64(123)

	ctx := context.Background()

	mockStore.On("GetUnprocessedMessages", userID).Return(nil, errors.New("db error"))
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Should not return error, just log and continue with empty history
	contextData, err := lap.LoadContextData(ctx, userID, "test", nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.Empty(t, contextData.RecentHistory)

	mockStore.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLoadContextData_RAGRetrievalSuccessful tests LoadContextData with successful RAG retrieval.
func TestLoadContextData_RAGRetrievalSuccessful(t *testing.T) {
	cfg, translator, lap, mockStore, _, mockRAG := setupContextTest(t)
	cfg.RAG.Enabled = true

	userID := int64(123)
	rawQuery := "test query"

	ctx := context.Background()

	// Clear default expectations and set up specific ones
	mockRAG.ExpectedCalls = nil

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{ID: 1, Role: "user", Content: "Hello", CreatedAt: time.Now()},
		{ID: 2, Role: "assistant", Content: "Hi", CreatedAt: time.Now()},
		{ID: 3, Role: "user", Content: "How are you?", CreatedAt: time.Now()},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{
		{ID: 1, Category: "identity", Content: "Name is Alice"},
	}, nil)

	// Mock GetRecentTopics for recent topics in context
	mockRAG.On("GetRecentTopics", userID, 3).Return([]storage.TopicExtended{
		{
			Topic: storage.Topic{
				ID:        1,
				Summary:   "We talked about X",
				CreatedAt: time.Now(),
				SizeChars: 5000,
			},
			MessageCount: 5,
		},
	}, nil)

	// Mock successful RAG retrieval
	mockRAG.On("Retrieve", mock.Anything, userID, rawQuery, mock.AnythingOfType("*rag.RetrievalOptions")).
		Return(&rag.RetrievalResult{
			Topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{ID: 10, Summary: "Similar discussion"},
					Messages: []storage.Message{
						{ID: 100, Role: "user", Content: "Old message", CreatedAt: time.Now()},
					},
				},
			},
			People: []storage.Person{
				{ID: 1, DisplayName: "Bob", Circle: "Friends"},
			},
			Artifacts: []rag.ArtifactResult{
				{ArtifactID: 5, Summary: "Important note"},
			},
			SelectedArtifactIDs: []int64{5},
		}, &rag.RetrievalDebugInfo{
			OriginalQuery: rawQuery,
			EnrichedQuery: "enriched " + rawQuery,
			Results:       []rag.TopicSearchResult{},
		}, nil)

	contextData, err := lap.LoadContextData(ctx, userID, rawQuery, nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.NotEmpty(t, contextData.ProfileFacts)
	assert.NotEmpty(t, contextData.RecentTopics)
	assert.NotEmpty(t, contextData.RAGResults)
	assert.NotEmpty(t, contextData.RelevantPeople)
	assert.NotEmpty(t, contextData.ArtifactResults)
	assert.NotNil(t, contextData.RAGInfo)

	mockStore.AssertExpectations(t)
	mockRAG.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLoadContextData_RAGRetrievalError tests LoadContextData when RAG retrieval fails.
func TestLoadContextData_RAGRetrievalError(t *testing.T) {
	cfg, translator, lap, mockStore, _, mockRAG := setupContextTest(t)
	cfg.RAG.Enabled = true

	userID := int64(123)
	rawQuery := "test query"

	ctx := context.Background()

	// Clear default expectations and set up specific ones
	mockRAG.ExpectedCalls = nil

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Mock GetRecentTopics for recent topics in context
	mockRAG.On("GetRecentTopics", userID, 3).Return([]storage.TopicExtended{}, nil)

	// Mock RAG retrieval error
	mockRAG.On("Retrieve", mock.Anything, userID, rawQuery, mock.AnythingOfType("*rag.RetrievalOptions")).
		Return(nil, nil, errors.New("RAG service unavailable"))

	// Should not return error, just log and continue without RAG results
	contextData, err := lap.LoadContextData(ctx, userID, rawQuery, nil)
	require.NoError(t, err)
	assert.NotNil(t, contextData)
	assert.Nil(t, contextData.RAGResults)
	assert.Nil(t, contextData.ArtifactResults)

	mockStore.AssertExpectations(t)
	mockRAG.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestBuildMessages_Minimal tests BuildMessages with minimal context.
func TestBuildMessages_Minimal(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are a helpful assistant.",
		RecentHistory:    []storage.Message{},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "", nil, "")

	require.Len(t, messages, 1)
	assert.Equal(t, "system", messages[0].Role)
	content, ok := messages[0].Content.([]interface{})
	require.True(t, ok)
	textPart, ok := content[0].(openrouter.TextPart)
	require.True(t, ok)
	assert.Contains(t, textPart.Text, "You are a helpful assistant.")
}

// TestBuildMessages_FullContext tests BuildMessages with full context including profile.
func TestBuildMessages_FullContext(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are a helpful assistant.",
		ProfileFacts:     "<user_profile>\nName: Test User\n</user_profile>",
		RecentTopics:     "<recent_topics>\n<topic>Previous chat</topic>\n</recent_topics>",
		InnerCircle:      "<inner_circle>\n<person>John</person>\n</inner_circle>",
		RecentHistory: []storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "Hello",
		[]interface{}{openrouter.TextPart{Type: "text", Text: "Hello"}}, "Hello")

	// Should have: system message, user message with history
	require.GreaterOrEqual(t, len(messages), 2)

	// First message should be system
	assert.Equal(t, "system", messages[0].Role)

	// System prompt should include all components
	content, ok := messages[0].Content.([]interface{})
	require.True(t, ok)
	textPart, ok := content[0].(openrouter.TextPart)
	require.True(t, ok)
	assert.Contains(t, textPart.Text, "You are a helpful assistant.")
	assert.Contains(t, textPart.Text, "Test User")
}

// TestBuildMessages_WithRAGResults tests BuildMessages with RAG results.
func TestBuildMessages_WithRAGResults(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		RAGResults: []rag.TopicSearchResult{
			{
				Topic: storage.Topic{ID: 1, Summary: "AI discussion"},
				Messages: []storage.Message{
					{ID: 10, Role: "user", Content: "What is AI?"},
				},
			},
		},
		RecentHistory: []storage.Message{},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "", nil, "")

	// Should have system + RAG context message
	require.GreaterOrEqual(t, len(messages), 1)

	// Find RAG context message
	foundRAGContext := false
	for _, msg := range messages {
		if msg.Role == "user" {
			if content, ok := msg.Content.([]interface{}); ok {
				if len(content) > 0 {
					if textPart, ok := content[0].(openrouter.TextPart); ok {
						if strings.HasPrefix(textPart.Text, "<retrieved_context query=") {
							foundRAGContext = true
							break
						}
					}
				}
			}
		}
	}
	assert.True(t, foundRAGContext, "should contain RAG context")
}

// TestBuildMessages_WithRelevantPeople tests that the relevant people field exists in ContextData.
// Note: FormatPeople with TagRelevantPeople returns empty by design - this is intentional
// as relevant people are handled differently than inner circle people.
func TestBuildMessages_WithRelevantPeople(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	// Test with empty relevant people - should not add extra user message
	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		// Empty RelevantPeople - no user message should be added
		RelevantPeople: []storage.Person{},
		RecentHistory:  []storage.Message{},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "", nil, "")

	// Should only have system message when relevant people is empty
	require.Equal(t, 1, len(messages), "should only have system message when no context parts")
	assert.Equal(t, "system", messages[0].Role)
}

// TestBuildMessages_WithArtifacts tests BuildMessages with artifact results.
func TestBuildMessages_WithArtifacts(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		ArtifactResults: []rag.ArtifactResult{
			{
				ArtifactID:   1,
				FileType:     "pdf",
				OriginalName: "document.pdf",
				Summary:      "A test document",
				Score:        0.85,
			},
		},
		RecentHistory: []storage.Message{},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "", nil, "")

	// Find artifact context
	foundArtifacts := false
	for _, msg := range messages {
		if msg.Role == "user" {
			if content, ok := msg.Content.([]interface{}); ok {
				if len(content) > 0 {
					if textPart, ok := content[0].(openrouter.TextPart); ok {
						if strings.HasPrefix(textPart.Text, "<artifact_context query=") {
							foundArtifacts = true
							assert.Contains(t, textPart.Text, "document.pdf")
							break
						}
					}
				}
			}
		}
	}
	assert.True(t, foundArtifacts, "should contain artifact context")
}

// TestBuildMessages_WithRecentHistory tests BuildMessages includes recent history messages.
func TestBuildMessages_WithRecentHistory(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		RecentHistory: []storage.Message{
			{ID: 1, Role: "user", Content: "First message"},
			{ID: 2, Role: "assistant", Content: "First response"},
			{ID: 3, Role: "user", Content: "Second message"},
		},
	}

	currentMessageParts := []interface{}{
		openrouter.TextPart{Type: "text", Text: "Second message"},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "Second message",
		currentMessageParts, "")

	// Should have system + history messages
	require.GreaterOrEqual(t, len(messages), 3)

	// Check that history messages are present
	foundFirst := false
	foundResponse := false
	foundCurrent := false

	for _, msg := range messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			content := ""
			if cs, ok := msg.Content.(string); ok {
				content = cs
			} else if parts, ok := msg.Content.([]interface{}); ok && len(parts) > 0 {
				if tp, ok := parts[0].(openrouter.TextPart); ok {
					content = tp.Text
				}
			}

			switch content {
			case "First message":
				foundFirst = true
			case "First response":
				foundResponse = true
			case "Second message":
				foundCurrent = true
			}
		}
	}

	assert.True(t, foundFirst, "should contain first user message")
	assert.True(t, foundResponse, "should contain assistant response")
	assert.True(t, foundCurrent, "should contain current user message")
}

// TestBuildMessages_HistoryUsesMultimodalContentForLastMessage tests that the last user message
// uses multimodal content when available.
func TestBuildMessages_HistoryUsesMultimodalContentForLastMessage(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	// Last message in history is a user message, and we have multimodal content
	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		RecentHistory: []storage.Message{
			{ID: 1, Role: "user", Content: "Simple text"},
			{ID: 2, Role: "assistant", Content: "Response"},
			{ID: 3, Role: "user", Content: "This should be replaced"},
		},
	}

	currentMessageParts := []interface{}{
		openrouter.TextPart{Type: "text", Text: "Multimodal message"},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "Multimodal message",
		currentMessageParts, "")

	// Find the last user message - it should use multimodal content
	foundMultimodal := false
	for _, msg := range messages {
		if msg.Role == "user" {
			if content, ok := msg.Content.([]interface{}); ok {
				if len(content) > 0 {
					if tp, ok := content[0].(openrouter.TextPart); ok && tp.Text == "Multimodal message" {
						foundMultimodal = true
						break
					}
				}
			}
		}
	}
	assert.True(t, foundMultimodal, "last user message should use multimodal content")
}

// TestBuildMessages_FallbackCurrentMessage tests BuildMessages adds current message separately
// when it wasn't added through history.
func TestBuildMessages_FallbackCurrentMessage(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	// Empty history - current message should be added separately
	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		RecentHistory:    []storage.Message{},
	}

	currentMessageParts := []interface{}{
		openrouter.TextPart{Type: "text", Text: "Current message"},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "Current message",
		currentMessageParts, "")

	// Should have system + current user message
	require.GreaterOrEqual(t, len(messages), 1)

	// Find current user message
	foundCurrent := false
	for _, msg := range messages {
		if msg.Role == "user" {
			if content, ok := msg.Content.([]interface{}); ok {
				if len(content) > 0 {
					if tp, ok := content[0].(openrouter.TextPart); ok && tp.Text == "Current message" {
						foundCurrent = true
						break
					}
				}
			}
		}
	}
	assert.True(t, foundCurrent, "should contain current user message")
}

// TestBuildMessages_WithNonEmptyRelevantPeople tests BuildMessages with non-empty relevant people.
func TestBuildMessages_WithNonEmptyRelevantPeople(t *testing.T) {
	_, _, lap, _, _, _ := setupContextTest(t)

	alice := "Alice"
	username := "alice123"
	contextData := &ContextData{
		UserID:           123,
		BaseSystemPrompt: "You are helpful.",
		RelevantPeople: []storage.Person{
			{ID: 1, DisplayName: "Alice", Circle: "colleague", Username: &alice},
			{ID: 2, DisplayName: "Bob", Circle: "friend", Username: &username},
		},
		RecentHistory: []storage.Message{},
	}

	messages := lap.BuildMessages(context.Background(), contextData, "", nil, "")

	// Should have system + user message with relevant people
	require.GreaterOrEqual(t, len(messages), 2)

	// Find relevant people in messages
	foundRelevantPeople := false
	for _, msg := range messages {
		if msg.Role == "user" {
			if content, ok := msg.Content.([]interface{}); ok {
				if len(content) > 0 {
					if tp, ok := content[0].(openrouter.TextPart); ok {
						if strings.Contains(tp.Text, "<relevant_people>") {
							foundRelevantPeople = true
							assert.Contains(t, tp.Text, "Alice")
							assert.Contains(t, tp.Text, "Bob")
							break
						}
					}
				}
			}
		}
	}
	assert.True(t, foundRelevantPeople, "should contain relevant people in context")
}
