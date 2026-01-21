package archivist

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestArchivist_Execute_AddFacts(t *testing.T) {
	// Response uses new format with facts section
	llmResponse := `{
		"facts": {
			"added": [{
				"relation": "works_as",
				"content": "Software Engineer",
				"category": "work",
				"type": "identity",
				"importance": 90,
				"reason": "User mentioned their profession"
			}],
			"updated": [],
			"removed": []
		},
		"people": {
			"added": [],
			"updated": [],
			"merged": []
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I work as a Software Engineer", CreatedAt: time.Now()},
				{ID: 2, Role: "assistant", Content: "That's great!", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Facts.Added, 1)
	assert.Equal(t, "works_as", result.Facts.Added[0].Relation)
	assert.Equal(t, "Software Engineer", result.Facts.Added[0].Content)
	assert.Equal(t, 1, resp.Metadata["facts_added"])
	assert.Equal(t, 0, resp.Metadata["facts_updated"])
	assert.Equal(t, 0, resp.Metadata["facts_removed"])

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_UpdateFacts(t *testing.T) {
	llmResponse := `{
		"facts": {
			"added": [],
			"updated": [{
				"id": 42,
				"content": "Senior Software Engineer",
				"type": "identity",
				"importance": 95,
				"reason": "User got promoted"
			}],
			"removed": []
		},
		"people": {
			"added": [],
			"updated": [],
			"merged": []
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I just got promoted!", CreatedAt: time.Now()},
			},
			ParamFacts: []storage.Fact{
				{ID: 42, UserID: 123, Relation: "works_as", Content: "Software Engineer", Category: "work", Type: "identity", Importance: 90},
			},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Facts.Updated, 1)
	assert.Equal(t, int64(42), result.Facts.Updated[0].ID)
	assert.Equal(t, "Senior Software Engineer", result.Facts.Updated[0].Content)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_RemoveFacts(t *testing.T) {
	llmResponse := `{
		"facts": {
			"added": [],
			"updated": [],
			"removed": [{
				"id": 99,
				"reason": "User said this is no longer true"
			}]
		},
		"people": {
			"added": [],
			"updated": [],
			"merged": []
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I no longer work there", CreatedAt: time.Now()},
			},
			ParamFacts: []storage.Fact{
				{ID: 99, UserID: 123, Content: "Works at Company X"},
			},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Facts.Removed, 1)
	assert.Equal(t, int64(99), result.Facts.Removed[0].ID)
	assert.Equal(t, 1, resp.Metadata["facts_removed"])

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_EmptyMessages(t *testing.T) {
	executor := agent.NewExecutor(nil, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamMessages: []storage.Message{},
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.Empty(t, result.Facts.Added)
	assert.Empty(t, result.Facts.Updated)
	assert.Empty(t, result.Facts.Removed)
}

func TestArchivist_Execute_LegacyFormat(t *testing.T) {
	// Test backward compatibility with legacy format (no facts wrapper)
	llmResponse := `{
		"added": [{
			"relation": "likes",
			"content": "Coffee",
			"category": "preferences",
			"type": "identity",
			"importance": 70,
			"reason": "User mentioned preference"
		}],
		"updated": [],
		"removed": []
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I love coffee", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Facts.Added, 1)
	assert.Equal(t, "Coffee", result.Facts.Added[0].Content)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_ArrayFieldsFormat(t *testing.T) {
	// Test LLM quirk: "facts": [...] instead of "facts": {...}
	llmResponse := `{
		"facts": [{
			"added": [{
				"relation": "has_pet",
				"content": "Cat named Whiskers",
				"category": "bio",
				"type": "context",
				"importance": 60,
				"reason": "User mentioned their pet"
			}],
			"updated": [],
			"removed": []
		}],
		"people": [{
			"added": [{
				"display_name": "John Doe",
				"circle": "Friends",
				"bio": "College friend",
				"reason": "Mentioned in conversation"
			}],
			"updated": [],
			"merged": []
		}]
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "My cat Whiskers is sleeping", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Facts.Added, 1)
	assert.Equal(t, "Cat named Whiskers", result.Facts.Added[0].Content)
	require.Len(t, result.People.Added, 1)
	assert.Equal(t, "John Doe", result.People.Added[0].DisplayName)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Type(t *testing.T) {
	archivist := &Archivist{}
	assert.Equal(t, agent.TypeArchivist, archivist.Type())
}

func TestArchivist_Capabilities(t *testing.T) {
	archivist := &Archivist{}
	caps := archivist.Capabilities()

	assert.True(t, caps.IsAgentic)
	assert.Equal(t, "json", caps.OutputFormat)
}

func TestArchivist_FormatUserName(t *testing.T) {
	a := &Archivist{}

	tests := []struct {
		name     string
		user     *storage.User
		expected string
	}{
		{
			name:     "nil user",
			user:     nil,
			expected: "User",
		},
		{
			name: "full name with username",
			user: &storage.User{
				ID:        123,
				FirstName: "John",
				LastName:  "Doe",
				Username:  "johndoe",
			},
			expected: "John Doe (@johndoe)",
		},
		{
			name: "only first name",
			user: &storage.User{
				ID:        123,
				FirstName: "John",
			},
			expected: "John",
		},
		{
			name: "only username",
			user: &storage.User{
				ID:       123,
				Username: "johndoe",
			},
			expected: "@johndoe",
		},
		{
			name: "only id",
			user: &storage.User{
				ID: 123,
			},
			expected: "ID:123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.formatUserName(tt.user)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestArchivist_PrepareUserFacts(t *testing.T) {
	a := &Archivist{}

	facts := []storage.Fact{
		{ID: 1, Relation: "works_as", Content: "Engineer", Category: "work", Type: "identity", Importance: 90},
		{ID: 2, Relation: "lives_in", Content: "Moscow", Category: "bio", Type: "context", Importance: 80},
		{ID: 3, Relation: "created_by", Content: "Developer", Category: "other", Type: "identity", Importance: 50},
	}

	// prepareUserFacts converts all facts to FactView (entity field was removed, all facts are about User)
	userFacts := a.prepareUserFacts(facts)

	assert.Len(t, userFacts, 3) // All facts are included
	assert.Equal(t, int64(1), userFacts[0].ID)
	assert.Equal(t, int64(2), userFacts[1].ID)
	assert.Equal(t, int64(3), userFacts[2].ID)
}

func TestArchivist_Execute_PeopleResult(t *testing.T) {
	// Test archivist with people extraction (v0.5.1 feature)
	llmResponse := `{
		"facts": {
			"added": [{
				"relation": "works_as",
				"content": "Software Engineer",
				"category": "work",
				"type": "identity",
				"importance": 90,
				"reason": "User mentioned their profession"
			}],
			"updated": [],
			"removed": []
		},
		"people": {
			"added": [{
				"display_name": "Alice Smith",
				"circle": "Friends",
				"bio": "College friend, works at Google",
				"aliases": ["Ally"],
				"reason": "Mentioned in conversation"
			}],
			"updated": [{
				"display_name": "Bob Jones",
				"circle": "Work_Inner",
				"bio": "Promoted to senior engineer",
				"new_display_name": "Robert Jones",
				"aliases": ["Rob"],
				"reason": "Name change and promotion"
			}],
			"merged": [{
				"target_name": "Charlie Brown",
				"source_name": "Chuck",
				"reason": "Same person, nickname vs full name"
			}]
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "Alice and Bob got promoted", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)

	// Verify facts
	assert.Len(t, result.Facts.Added, 1)
	assert.Equal(t, "Software Engineer", result.Facts.Added[0].Content)

	// Verify people - Added
	assert.Len(t, result.People.Added, 1)
	assert.Equal(t, "Alice Smith", result.People.Added[0].DisplayName)
	assert.Equal(t, "Friends", result.People.Added[0].Circle)
	assert.Equal(t, "College friend, works at Google", result.People.Added[0].Bio)
	assert.Equal(t, []string{"Ally"}, result.People.Added[0].Aliases)

	// Verify people - Updated
	assert.Len(t, result.People.Updated, 1)
	assert.Equal(t, "Bob Jones", result.People.Updated[0].DisplayName)
	assert.Equal(t, "Robert Jones", result.People.Updated[0].NewDisplayName)
	assert.Equal(t, "Work_Inner", result.People.Updated[0].Circle)

	// Verify people - Merged
	assert.Len(t, result.People.Merged, 1)
	assert.Equal(t, "Charlie Brown", result.People.Merged[0].TargetName)
	assert.Equal(t, "Chuck", result.People.Merged[0].SourceName)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_OnlyPeople(t *testing.T) {
	// Test archivist with only people operations, no facts
	llmResponse := `{
		"facts": {
			"added": [],
			"updated": [],
			"removed": []
		},
		"people": {
			"added": [{
				"display_name": "John Doe",
				"circle": "Family",
				"bio": "Cousin from New York",
				"reason": "New family member"
			}],
			"updated": [],
			"merged": []
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "My cousin John visited", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)

	// Verify no facts
	assert.Empty(t, result.Facts.Added)

	// Verify person added
	assert.Len(t, result.People.Added, 1)
	assert.Equal(t, "John Doe", result.People.Added[0].DisplayName)
	assert.Equal(t, "Family", result.People.Added[0].Circle)
	assert.Equal(t, "Cousin from New York", result.People.Added[0].Bio)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_PeopleEmptyFields(t *testing.T) {
	// Test people with minimal fields (no bio, no aliases, default circle)
	llmResponse := `{
		"facts": {
			"added": [],
			"updated": [],
			"removed": []
		},
		"people": {
			"added": [{
				"display_name": "Jane",
				"reason": "Brief mention"
			}],
			"updated": [],
			"merged": []
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "Jane is here", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)

	assert.Len(t, result.People.Added, 1)
	assert.Equal(t, "Jane", result.People.Added[0].DisplayName)
	assert.Equal(t, "", result.People.Added[0].Circle) // Empty circle (will be defaulted to "Other" in storage)
	assert.Equal(t, "", result.People.Added[0].Bio)
	assert.Nil(t, result.People.Added[0].Aliases) // No aliases

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_PeopleAndFacts(t *testing.T) {
	// Test archivist extracting both facts and people in same call
	llmResponse := `{
		"facts": {
			"added": [{
				"relation": "friend_of",
				"content": "Alice Smith",
				"category": "social",
				"type": "identity",
				"importance": 85,
				"reason": "Long-time friend"
			}],
			"updated": [],
			"removed": []
		},
		"people": {
			"added": [{
				"display_name": "Alice Smith",
				"circle": "Inner",
				"bio": "Best friend since childhood",
				"reason": "Key person in user's life"
			}],
			"updated": [],
			"merged": []
		}
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "Alice is my best friend", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)

	// Verify both fact and person were extracted for Alice
	assert.Len(t, result.Facts.Added, 1)
	assert.Equal(t, "Alice Smith", result.Facts.Added[0].Content)
	assert.Equal(t, "friend_of", result.Facts.Added[0].Relation)

	assert.Len(t, result.People.Added, 1)
	assert.Equal(t, "Alice Smith", result.People.Added[0].DisplayName)
	assert.Equal(t, "Inner", result.People.Added[0].Circle)
	assert.Equal(t, "Best friend since childhood", result.People.Added[0].Bio)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_RawArraysFormat(t *testing.T) {
	// Test fallback for LLM error: returns raw fact/person arrays instead of operations
	llmResponse := `{
		"facts": [
			{
				"id": 1522,
				"content": "Updated fact about User",
				"type": "identity",
				"importance": 95,
				"reason": "Clarified information"
			},
			{
				"id": 1624,
				"content": "Another fact",
				"type": "context",
				"importance": 70,
				"reason": "Added context"
			}
		],
		"people": [
			{
				"display_name": "Ivan Petrov",
				"aliases": ["@ivanp"],
				"circle": "Work_Inner",
				"bio": "DevOps engineer at company X",
				"reason": "User mentioned colleague"
			}
		]
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg, testutil.TestLogger(), nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "Update my info", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)

	// Verify raw facts were converted to updated (they have IDs)
	assert.Len(t, result.Facts.Updated, 2)
	assert.Equal(t, int64(1522), result.Facts.Updated[0].ID)
	assert.Equal(t, "Updated fact about User", result.Facts.Updated[0].Content)
	assert.Equal(t, "identity", result.Facts.Updated[0].Type)
	assert.Equal(t, 95, result.Facts.Updated[0].Importance)

	// Verify raw people were converted to added
	assert.Len(t, result.People.Added, 1)
	assert.Equal(t, "Ivan Petrov", result.People.Added[0].DisplayName)
	assert.Equal(t, "Work_Inner", result.People.Added[0].Circle)
	assert.Equal(t, "DevOps engineer at company X", result.People.Added[0].Bio)

	mockClient.AssertExpectations(t)
}
