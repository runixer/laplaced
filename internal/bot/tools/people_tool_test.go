package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestPerformManagePeople_MissingQuery tests error when query is missing.
func TestPerformManagePeople_MissingQuery(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error: query argument is missing")
}

// TestPerformManagePeople_InvalidJSON tests error when JSON is invalid.
func TestPerformManagePeople_InvalidJSON(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": "{invalid json",
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error parsing query JSON")
}

// TestPerformManagePeople_UnknownOperation tests error on unknown operation.
func TestPerformManagePeople_UnknownOperation(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"unknown"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error: unknown operation 'unknown'")
}

// TestPerformManagePeople_CreateSuccess tests successful person creation.
func TestPerformManagePeople_CreateSuccess(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return len(req.Input) > 0
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("FindPersonByName", int64(123), "Alice").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "Alice").Return([]storage.Person{}, nil)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "Alice" && p.Circle == "Friends"
	})).Return(int64(42), nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"create","name":"Alice","circle":"Friends"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully created person 'Alice'")
	assert.Contains(t, result, "ID: 42")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManagePeople_CreateWithAllFields tests person creation with all optional fields.
func TestPerformManagePeople_CreateWithAllFields(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("FindPersonByName", int64(123), "Bob").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "Bob").Return([]storage.Person{}, nil)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "Bob" &&
			p.Bio == "Software engineer" &&
			p.Username != nil && *p.Username == "alice" &&
			len(p.Aliases) == 2 &&
			p.Circle == "Work"
	})).Return(int64(42), nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"create","name":"Bob","bio":"Software engineer","username":"@alice","circle":"Work","aliases":["bobby","robert"]}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully created person 'Bob'")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManagePeople_CreateAlreadyExists tests error when person already exists.
func TestPerformManagePeople_CreateAlreadyExists(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	existingPerson := storage.Person{ID: 42, DisplayName: "Alice"}

	mockStore.On("FindPersonByName", int64(123), "Alice").Return(&existingPerson, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"create","name":"Alice"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "already exists")
	assert.Contains(t, result, "ID: 42")

	mockStore.AssertExpectations(t)
}

// TestPerformManagePeople_CreateMissingName tests error when name is missing.
func TestPerformManagePeople_CreateMissingName(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"create","circle":"Friends"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error: name is required for create operation")
}

// TestPerformManagePeople_UpdateByID tests successful update by person ID.
func TestPerformManagePeople_UpdateByID(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	existingPerson := storage.Person{
		ID:          42,
		UserID:      123,
		DisplayName: "Alice",
		Bio:         "Old bio",
		Aliases:     []string{"ally"},
	}

	mockStore.On("GetPerson", int64(123), int64(42)).Return(&existingPerson, nil)
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 42 && p.Bio == "Old bio New info"
	})).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"update","person_id":"Person:42","updates":{"bio_append":"New info"}}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully updated person")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManagePeople_UpdateByName tests successful update by name.
func TestPerformManagePeople_UpdateByName(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	existingPerson := storage.Person{
		ID:          42,
		UserID:      123,
		DisplayName: "Alice",
		Circle:      "Other",
	}

	// No person_id provided, so GetPerson is not called
	// FindPersonByName is called first and finds the person
	mockStore.On("FindPersonByName", int64(123), "Alice").Return(&existingPerson, nil)
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 42 && p.Circle == "Friends"
	})).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"update","name":"Alice","updates":{"circle":"Friends"}}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully updated person")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManagePeople_UpdateWithAliases tests update with adding aliases.
func TestPerformManagePeople_UpdateWithAliases(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	existingPerson := storage.Person{
		ID:          42,
		UserID:      123,
		DisplayName: "Alice",
		Aliases:     []string{"ally"},
	}

	mockStore.On("GetPerson", int64(123), int64(42)).Return(&existingPerson, nil)
	mockStore.On("UpdatePerson", mock.AnythingOfType("storage.Person")).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"update","person_id":"Person:42","updates":{"aliases_add":["alice2","ally2"]}}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully updated person")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManagePeople_UpdateMissingUpdates tests error when updates object is missing.
func TestPerformManagePeople_UpdateMissingUpdates(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	existingPerson := storage.Person{ID: 42, DisplayName: "Alice"}

	mockStore.On("GetPerson", int64(123), int64(42)).Return(&existingPerson, nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"update","person_id":"Person:42"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error: updates object is required")

	mockStore.AssertExpectations(t)
}

// TestPerformManagePeople_DeleteByID tests successful deletion by person ID.
func TestPerformManagePeople_DeleteByID(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	existingPerson := storage.Person{ID: 42, DisplayName: "Alice"}

	mockStore.On("GetPerson", int64(123), int64(42)).Return(&existingPerson, nil)
	mockStore.On("DeletePerson", int64(123), int64(42)).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"delete","person_id":"Person:42"}`,
	})

	assert.NoError(t, err)
	// When only person_id is provided (no name), the result shows empty name
	assert.Contains(t, result, "Successfully deleted person")

	mockStore.AssertExpectations(t)
}

// TestPerformManagePeople_DeleteByName tests successful deletion by name.
func TestPerformManagePeople_DeleteByName(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	existingPerson := storage.Person{ID: 42, DisplayName: "Alice"}

	// No person_id provided, so GetPerson is not called
	mockStore.On("FindPersonByName", int64(123), "Alice").Return(&existingPerson, nil)
	mockStore.On("DeletePerson", int64(123), int64(42)).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"delete","name":"Alice"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully deleted person 'Alice'")

	mockStore.AssertExpectations(t)
}

// TestPerformManagePeople_MergeSuccess tests successful people merge.
func TestPerformManagePeople_MergeSuccess(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	targetPerson := storage.Person{ID: 1, DisplayName: "Alice", Bio: "Original bio"}
	sourcePerson := storage.Person{ID: 2, DisplayName: "Alice Smith", Bio: "Additional info"}

	mockStore.On("GetPerson", int64(123), int64(1)).Return(&targetPerson, nil)
	mockStore.On("GetPerson", int64(123), int64(2)).Return(&sourcePerson, nil)
	mockStore.On("MergePeople", int64(123), int64(1), int64(2),
		mock.AnythingOfType("string"),   // newBio
		mock.AnythingOfType("[]string"), // newAliases
		mock.AnythingOfType("*string"),  // newUsername
		mock.AnythingOfType("*int64"),   // newTelegramID
	).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	// When only IDs are provided, targetName and sourceName are empty
	// The code uses names from params (which are empty when only IDs given)
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"merge","target_id":"Person:1","source_id":"Person:2"}`,
	})

	assert.NoError(t, err)
	// With only IDs (no names), the message shows empty names
	assert.Contains(t, result, "Successfully merged")

	mockStore.AssertExpectations(t)
}

// TestPerformManagePeople_MergeSelfMerge tests error when trying to merge person with themselves.
func TestPerformManagePeople_MergeSelfMerge(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	person := storage.Person{ID: 1, DisplayName: "Alice"}

	mockStore.On("GetPerson", int64(123), int64(1)).Return(&person, nil).Twice()

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"merge","target_id":"Person:1","source_id":"Person:1"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Cannot merge person with itself")

	mockStore.AssertExpectations(t)
}

// TestPerformManagePeople_UpdateRequiresPersonIDOrName tests error when neither provided.
func TestPerformManagePeople_UpdateRequiresPersonIDOrName(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"update"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error: person_id or name is required for update operation")
}

// TestPerformManagePeople_DeleteRequiresPersonIDOrName tests error when neither provided.
func TestPerformManagePeople_DeleteRequiresPersonIDOrName(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
		"query": `{"operation":"delete"}`,
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Error: person_id or name is required for delete operation")
}

// TestPerformManagePeople_MergeRequiresTargetAndSource tests error when target or source missing.
func TestPerformManagePeople_MergeRequiresTargetAndSource(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	t.Run("missing target", func(t *testing.T) {
		ctx := context.Background()
		result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
			"query": `{"operation":"merge","source_id":"Person:1"}`,
		})

		assert.NoError(t, err)
		assert.Contains(t, result, "Error: target_id or target is required")
	})

	t.Run("missing source", func(t *testing.T) {
		ctx := context.Background()
		result, err := exec.performManagePeople(ctx, 123, map[string]interface{}{
			"query": `{"operation":"merge","target_id":"Person:1"}`,
		})

		assert.NoError(t, err)
		assert.Contains(t, result, "Error: source_id or source is required")
	})
}

// TestPerformCreatePerson_WithEmbeddingFailure tests that person creation succeeds even if embedding fails.
func TestPerformCreatePerson_WithEmbeddingFailure(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockStore.On("FindPersonByName", int64(123), "Alice").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "Alice").Return([]storage.Person{}, nil)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "Alice"
	})).Return(int64(42), nil)

	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(openrouter.EmbeddingResponse{}, errors.New("API error"))

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	result, err := exec.performCreatePerson(ctx, 123, "Alice", map[string]interface{}{
		"operation": "create",
		"name":      "Alice",
	})

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully created person 'Alice'")

	mockStore.AssertExpectations(t)
}
