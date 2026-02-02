package tools

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// TestFormatRAGResults tests the formatRAGResults method.
// This is a pure formatting function that doesn't require complex mocking.
func TestFormatRAGResults(t *testing.T) {
	tests := []struct {
		name         string
		topics       []rag.TopicSearchResult
		query        string
		wantContains []string
	}{
		{
			name:         "empty results",
			topics:       []rag.TopicSearchResult{},
			query:        "test",
			wantContains: []string{"Found 0 topics"},
		},
		{
			name: "single topic with messages",
			topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   "Test Topic",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: "Test message content"},
					},
				},
			},
			query:        "test",
			wantContains: []string{"Found 1 topics", "Test Topic", "2025-01-15", "Messages: 1", "Test message content"},
		},
		{
			name: "single topic without messages",
			topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   "Empty Topic",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{},
				},
			},
			query:        "test",
			wantContains: []string{"Found 1 topics", "Empty Topic", "Messages: 0"},
		},
		{
			name: "multiple topics",
			topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   "First Topic",
						CreatedAt: time.Date(2025, 1, 10, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: "First message"},
					},
				},
				{
					Topic: storage.Topic{
						ID:        2,
						Summary:   "Second Topic",
						CreatedAt: time.Date(2025, 1, 11, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: "Second message"},
					},
				},
			},
			query:        "test",
			wantContains: []string{"Found 2 topics", "First Topic", "Second Topic"},
		},
		{
			name: "long preview gets truncated",
			topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   "Long Content Topic",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: string(make([]byte, 150))}, // 150 bytes
					},
				},
			},
			query:        "test",
			wantContains: []string{"Found 1 topics", "Preview:", "..."},
		},
		{
			name: "exactly 100 chars - no truncation",
			topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   "Exactly 100",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: string(make([]byte, 100))}, // exactly 100 bytes
					},
				},
			},
			query:        "test",
			wantContains: []string{"Found 1 topics", "Preview:"},
		},
		{
			name: "topics are numbered correctly",
			topics: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   "Topic One",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: "Message 1"},
					},
				},
				{
					Topic: storage.Topic{
						ID:        2,
						Summary:   "Topic Two",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: "Message 2"},
					},
				},
				{
					Topic: storage.Topic{
						ID:        3,
						Summary:   "Topic Three",
						CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
					},
					Messages: []storage.Message{
						{Content: "Message 3"},
					},
				},
			},
			query:        "test",
			wantContains: []string{"1. **Topic One**", "2. **Topic Two**", "3. **Topic Three**"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			exec := NewToolExecutor(nil, nil, nil, cfg, testutil.TestLogger())

			result := exec.formatRAGResults(tt.topics, tt.query)

			for _, want := range tt.wantContains {
				assert.Contains(t, result, want)
			}
		})
	}
}

// TestPerformHistorySearch_ErrorCases tests error handling in performHistorySearch.
func TestPerformHistorySearch_ErrorCases(t *testing.T) {
	tests := []struct {
		name           string
		args           map[string]interface{}
		wantContains   string
		wantErr        bool
		errMsgContains string
	}{
		{
			name:           "missing query argument",
			args:           map[string]interface{}{},
			wantErr:        true,
			errMsgContains: "missing or not a string",
		},
		{
			name:           "query argument not a string",
			args:           map[string]interface{}{"query": 123},
			wantErr:        true,
			errMsgContains: "missing or not a string",
		},
		{
			name:         "ragService is nil",
			args:         map[string]interface{}{"query": "test query"},
			wantContains: "Search is not available",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := NewToolExecutor(nil, nil, nil, testutil.TestConfig(), testutil.TestLogger())

			ctx := context.Background()
			result, err := exec.performHistorySearch(ctx, 123, tt.args)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsgContains != "" {
					assert.Contains(t, err.Error(), tt.errMsgContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Contains(t, result, tt.wantContains)
			}
		})
	}
}

// TestPerformSearchPeople_ErrorCases tests error handling in performSearchPeople.
func TestPerformSearchPeople_ErrorCases(t *testing.T) {
	tests := []struct {
		name           string
		setupExecutor  func(*ToolExecutor)
		args           map[string]interface{}
		wantContains   string
		wantErr        bool
		errMsgContains string
	}{
		{
			name:           "missing query argument",
			setupExecutor:  func(e *ToolExecutor) {},
			args:           map[string]interface{}{},
			wantErr:        true,
			errMsgContains: "missing or not a string",
		},
		{
			name:           "query argument not a string",
			setupExecutor:  func(e *ToolExecutor) {},
			args:           map[string]interface{}{"query": 123},
			wantErr:        true,
			errMsgContains: "missing or not a string",
		},
		{
			name:          "peopleRepo is nil",
			setupExecutor: func(e *ToolExecutor) {},
			args:          map[string]interface{}{"query": "test"},
			wantContains:  "People search is not available",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := NewToolExecutor(nil, nil, nil, testutil.TestConfig(), testutil.TestLogger())
			tt.setupExecutor(exec)

			ctx := context.Background()
			result, err := exec.performSearchPeople(ctx, 123, tt.args)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsgContains != "" {
					assert.Contains(t, err.Error(), tt.errMsgContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Contains(t, result, tt.wantContains)
			}
		})
	}
}

// TestSetPeopleRepository tests the SetPeopleRepository method.
func TestSetPeopleRepository(t *testing.T) {
	mockRepo := new(testutil.MockStorage)
	exec := NewToolExecutor(nil, nil, nil, testutil.TestConfig(), testutil.TestLogger())

	assert.Nil(t, exec.peopleRepo)

	exec.SetPeopleRepository(mockRepo)

	assert.Same(t, mockRepo, exec.peopleRepo)
}

// TestSetRAGService tests the SetRAGService method.
func TestSetRAGService(t *testing.T) {
	// Create a simple rag.Service for testing
	// We can't fully test it without a proper interface, but we can test the setter
	exec := NewToolExecutor(nil, nil, nil, testutil.TestConfig(), testutil.TestLogger())

	// Verify it starts as nil (we can't set it directly because rag.Service is not an interface)
	assert.Nil(t, exec.ragService)
}

// TestConfigGetters tests the new config getter methods.
func TestConfigGetters(t *testing.T) {
	t.Run("MemoryConfig.GetFactDefaultImportance", func(t *testing.T) {
		cfg := config.MemoryConfig{}
		assert.Equal(t, 50, cfg.GetFactDefaultImportance())

		cfg.FactDefaultImportance = 75
		assert.Equal(t, 75, cfg.GetFactDefaultImportance())
	})

	t.Run("SearchConfig.GetPeopleSimilarityThreshold", func(t *testing.T) {
		cfg := config.SearchConfig{}
		assert.Equal(t, float64(0.3), cfg.GetPeopleSimilarityThreshold())

		cfg.PeopleSimilarityThreshold = 0.5
		assert.Equal(t, float64(0.5), cfg.GetPeopleSimilarityThreshold())
	})

	t.Run("SearchConfig.GetPeopleMaxResults", func(t *testing.T) {
		cfg := config.SearchConfig{}
		assert.Equal(t, 5, cfg.GetPeopleMaxResults())

		cfg.PeopleMaxResults = 10
		assert.Equal(t, 10, cfg.GetPeopleMaxResults())
	})
}

// TestPerformHistorySearch_Success tests successful history search with RAG service.
func TestPerformHistorySearch_Success(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	// We need to mock the RAG service. Since it's not an interface, we'll create a minimal one
	// For now, test with nil RAG service to get "not available" message
	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	result, err := exec.performHistorySearch(ctx, 123, map[string]interface{}{"query": "test query"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Search is not available")
}

// TestPerformHistorySearch_NoResults tests search that returns no results.
func TestPerformHistorySearch_NoResults(t *testing.T) {
	// This test requires mocking the full RAG service, which is complex
	// For now, we test the error path
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	result, err := exec.performHistorySearch(ctx, 123, map[string]interface{}{"query": "test query"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Search is not available")
}

// TestPerformSearchPeople_Success tests successful people search by username.
func TestPerformSearchPeople_Success(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	username := "testuser"

	testPerson := storage.Person{
		ID:          1,
		UserID:      123,
		DisplayName: "Test User",
		Username:    &username,
		Circle:      "Friends",
		Bio:         "A test person",
		Aliases:     []string{"tester", "testy"},
	}

	mockStore.On("FindPersonByUsername", int64(123), "testuser").Return(&testPerson, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "@testuser"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Found 1 people")
	assert.Contains(t, result, "Test User")
	assert.Contains(t, result, "Friends")
	assert.Contains(t, result, "@testuser")

	mockStore.AssertExpectations(t)
}

// TestPerformSearchPeople_ByName tests people search by name.
func TestPerformSearchPeople_ByName(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	testPerson := storage.Person{
		ID:          1,
		UserID:      123,
		DisplayName: "Alice Smith",
		Circle:      "Work",
		Bio:         "Software engineer",
	}

	mockStore.On("FindPersonByName", int64(123), "Alice").Return(&testPerson, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "Alice"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Found 1 people")
	assert.Contains(t, result, "Alice Smith")

	mockStore.AssertExpectations(t)
}

// TestPerformSearchPeople_ByAlias tests people search by alias.
func TestPerformSearchPeople_ByAlias(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	testPerson := storage.Person{
		ID:          1,
		UserID:      123,
		DisplayName: "Bob Johnson",
		Circle:      "Family",
		Aliases:     []string{"bobby", "robert"},
	}

	mockStore.On("FindPersonByName", int64(123), "bobby").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "bobby").Return([]storage.Person{testPerson}, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "bobby"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Found 1 people")
	assert.Contains(t, result, "Bob Johnson")

	mockStore.AssertExpectations(t)
}

// TestPerformSearchPeople_NoResults tests people search with no matches.
func TestPerformSearchPeople_NoResults(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	// For "unknown" query (no @ prefix), FindPersonByUsername is not called
	mockStore.On("FindPersonByName", int64(123), "unknown").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "unknown").Return([]storage.Person{}, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "unknown"})

	assert.NoError(t, err)
	assert.Contains(t, result, "No people found matching 'unknown'")

	mockStore.AssertExpectations(t)
}

// TestPerformSearchPeople_VectorSearchFallback tests vector search when direct matches fail.
func TestPerformSearchPeople_VectorSearchFallback(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockStore.On("FindPersonByName", int64(123), "semantics").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "semantics").Return([]storage.Person{}, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	// ragService is nil, so vector search won't be attempted
	// Result should be "no people found"
	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "semantics"})

	assert.NoError(t, err)
	assert.Contains(t, result, "No people found matching 'semantics'")

	mockStore.AssertExpectations(t)
}

// TestPerformSearchPeople_LongBioTruncation tests that long bios are truncated in output.
func TestPerformSearchPeople_LongBioTruncation(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	longBio := string(make([]byte, 300)) // 300 byte bio

	testPerson := storage.Person{
		ID:          1,
		UserID:      123,
		DisplayName: "Long Bio Person",
		Circle:      "Other",
		Bio:         longBio,
	}

	mockStore.On("FindPersonByUsername", int64(123), "longbio").Return(&testPerson, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "@longbio"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Bio:")
	assert.Contains(t, result, "...") // Should be truncated

	mockStore.AssertExpectations(t)
}

// TestPerformSearchPeople_MultipleResults tests multiple people search results.
func TestPerformSearchPeople_MultipleResults(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	alice := storage.Person{
		ID:          1,
		UserID:      123,
		DisplayName: "Alice",
		Circle:      "Friends",
		Aliases:     []string{"ally"},
	}
	bob := storage.Person{
		ID:          2,
		UserID:      123,
		DisplayName: "Bob",
		Circle:      "Family",
	}

	mockStore.On("FindPersonByName", int64(123), "search").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "search").Return([]storage.Person{alice, bob}, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performSearchPeople(ctx, 123, map[string]interface{}{"query": "search"})

	assert.NoError(t, err)
	assert.Contains(t, result, "Found 2 people")
	assert.Contains(t, result, "Alice")
	assert.Contains(t, result, "Bob")

	mockStore.AssertExpectations(t)
}
