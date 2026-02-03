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

// TestPerformAddFact_Success tests successful fact addition.
func TestPerformAddFact_Success(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return len(req.Input) > 0 && req.Input[0] == "Test fact content"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.Content == "Test fact content" &&
			f.Category == "other" &&
			f.Type == "context" &&
			f.Importance == 50 // default
	})).Return(int64(1), nil)

	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "add",
		Content: "Test fact content",
	}

	result, err := exec.performAddFact(ctx, 123, params)

	assert.NoError(t, err)
	assert.Contains(t, result, "Fact added successfully")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformAddFact_WithCustomFields tests fact addition with custom category, type, and importance.
func TestPerformAddFact_WithCustomFields(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.Content == "Important fact" &&
			f.Category == "work" &&
			f.Type == "identity" &&
			f.Importance == 90
	})).Return(int64(1), nil)

	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:     "add",
		Content:    "Important fact",
		Category:   "work",
		FactType:   "identity",
		Importance: 90,
		Reason:     "User explicitly stated this",
	}

	result, err := exec.performAddFact(ctx, 123, params)

	assert.NoError(t, err)
	assert.Contains(t, result, "Fact added successfully")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformAddFact_EmptyContent tests error when content is empty.
func TestPerformAddFact_EmptyContent(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "add",
		Content: "",
	}

	result, err := exec.performAddFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "content is required")
}

// TestPerformAddFact_EmbeddingAPIError tests error when embedding API call fails.
func TestPerformAddFact_EmbeddingAPIError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(openrouter.EmbeddingResponse{}, errors.New("API error"))

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "add",
		Content: "Test fact",
	}

	result, err := exec.performAddFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "generating embedding")
}

// TestPerformAddFact_EmbeddingError tests error when embedding generation returns empty data.
func TestPerformAddFact_EmbeddingError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(openrouter.EmbeddingResponse{Data: []openrouter.EmbeddingObject{}}, errors.New("empty embedding"))

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "add",
		Content: "Test fact",
	}

	result, err := exec.performAddFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "empty embedding")
}

// TestPerformAddFact_StorageError tests error when fact storage fails.
func TestPerformAddFact_StorageError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("AddFact", mock.Anything).Return(int64(0), errors.New("database error"))

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "add",
		Content: "Test fact",
	}

	result, err := exec.performAddFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "adding fact")
}

// TestPerformDeleteFact_Success tests successful fact deletion.
func TestPerformDeleteFact_Success(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	oldFact := storage.Fact{
		ID:         42,
		UserID:     123,
		Content:    "Old fact to delete",
		Category:   "test",
		Relation:   "related_to",
		Importance: 70,
	}

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{oldFact}, nil)
	mockStore.On("DeleteFact", int64(123), int64(42)).Return(nil)
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 42 && h.Action == "delete"
	})).Return(nil)

	exec := NewToolExecutor(nil, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action: "delete",
		FactID: 42,
		Reason: "No longer relevant",
	}

	result, err := exec.performDeleteFact(ctx, 123, params)

	assert.NoError(t, err)
	assert.Contains(t, result, "Fact deleted successfully")

	mockStore.AssertExpectations(t)
}

// TestPerformDeleteFact_MissingFactID tests error when fact ID is missing.
func TestPerformDeleteFact_MissingFactID(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	exec := NewToolExecutor(nil, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action: "delete",
		// FactID is 0
	}

	result, err := exec.performDeleteFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "fact ID is required")
}

// TestPerformDeleteFact_StorageError tests error when deletion fails.
func TestPerformDeleteFact_StorageError(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{{ID: 42}}, nil)
	mockStore.On("DeleteFact", int64(123), int64(42)).Return(errors.New("database error"))

	exec := NewToolExecutor(nil, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action: "delete",
		FactID: 42,
	}

	result, err := exec.performDeleteFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "deleting fact")
}

// TestPerformUpdateFact_Success tests successful fact update.
func TestPerformUpdateFact_Success(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	oldFact := storage.Fact{
		ID:         42,
		Content:    "Old content",
		Category:   "old",
		Relation:   "old_relation",
		Importance: 50,
	}

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{oldFact}, nil)
	mockStore.On("UpdateFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.ID == 42 && f.Content == "Updated content"
	})).Return(nil)
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 42 && h.Action == "update" && h.NewContent == "Updated content"
	})).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "update",
		FactID:  42,
		Content: "Updated content",
		Reason:  "Correction needed",
	}

	result, err := exec.performUpdateFact(ctx, 123, params)

	assert.NoError(t, err)
	assert.Contains(t, result, "Fact updated successfully")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformUpdateFact_WithCustomTypeAndImportance tests update with custom fields.
func TestPerformUpdateFact_WithCustomTypeAndImportance(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{{ID: 42}}, nil)
	mockStore.On("UpdateFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.Type == "identity" && f.Importance == 80
	})).Return(nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:     "update",
		FactID:     42,
		Content:    "Updated",
		FactType:   "identity",
		Importance: 80,
	}

	result, err := exec.performUpdateFact(ctx, 123, params)

	assert.NoError(t, err)
	assert.Contains(t, result, "Fact updated successfully")
}

// TestPerformUpdateFact_MissingFactID tests error when fact ID is missing.
func TestPerformUpdateFact_MissingFactID(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "update",
		Content: "Some content",
		// FactID is 0
	}

	result, err := exec.performUpdateFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "fact ID is required")
}

// TestPerformUpdateFact_EmbeddingError tests error when embedding generation fails.
func TestPerformUpdateFact_EmbeddingError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(openrouter.EmbeddingResponse{}, errors.New("API error"))

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	params := MemoryOpParams{
		Action:  "update",
		FactID:  42,
		Content: "Updated",
	}

	result, err := exec.performUpdateFact(ctx, 123, params)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "generating embedding")
}

// TestPerformManageMemory_SingleOperation tests single memory operation.
func TestPerformManageMemory_SingleOperation(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"action":"add","content":"Test fact"}`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully processed 1 operations")
	assert.Contains(t, result, "Success")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManageMemory_BatchOperations tests batch memory operations.
func TestPerformManageMemory_BatchOperations(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil).Times(2)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil).Times(2)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"operations":[` +
			`{"action":"add","content":"First fact"},` +
			`{"action":"add","content":"Second fact"}` +
			`]}`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully processed 2 operations")
	assert.Contains(t, result, "Op 1 (add): Success")
	assert.Contains(t, result, "Op 2 (add): Success")
}

// TestPerformManageMemory_InvalidJSON tests error when JSON is invalid.
func TestPerformManageMemory_InvalidJSON(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{invalid json`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "parsing query JSON")
}

// TestPerformManageMemory_MissingQuery tests error when query is missing.
func TestPerformManageMemory_MissingQuery(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{} // no query

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "query argument is missing")
}

// TestPerformManageMemory_DeleteOperation tests delete operation in batch.
func TestPerformManageMemory_DeleteOperation(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{{ID: 42}}, nil)
	mockStore.On("DeleteFact", int64(123), int64(42)).Return(nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"action":"delete","fact_id":"Fact:42"}`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.NoError(t, err)
	assert.Contains(t, result, "Success")

	mockStore.AssertExpectations(t)
}

// TestPerformManageMemory_UpdateOperation tests update operation in batch.
func TestPerformManageMemory_UpdateOperation(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{{ID: 42}}, nil)
	mockStore.On("UpdateFact", mock.Anything).Return(nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"action":"update","fact_id":"Fact:42","content":"Updated content"}`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.NoError(t, err)
	assert.Contains(t, result, "Success")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestPerformManageMemory_UnknownAction tests error on unknown action.
func TestPerformManageMemory_UnknownAction(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"action":"unknown","content":"test"}`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "completed with 1 errors")
	assert.Contains(t, err.Error(), "Unknown action")
}

// TestPerformManageMemory_MixedSuccessFailure tests batch with some failures.
func TestPerformManageMemory_MixedSuccessFailure(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	// First call succeeds (add)
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil).Once()

	// Second operation fails (delete with missing fact_id)
	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil).Once()
	mockStore.On("AddFactHistory", mock.Anything).Return(nil).Once()

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"operations":[` +
			`{"action":"add","content":"Valid fact"},` +
			`{"action":"delete"}` + // missing fact_id
			`]}`,
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "completed with 1 errors")
}

// TestPerformManageMemory_NumericFactID tests backward compatibility with numeric fact_id.
func TestPerformManageMemory_NumericFactID(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockStore.On("GetFactsByIDs", int64(123), []int64{42}).Return([]storage.Fact{{ID: 42}}, nil)
	mockStore.On("DeleteFact", int64(123), int64(42)).Return(nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())

	ctx := context.Background()
	args := map[string]interface{}{
		"query": `{"action":"delete","fact_id":42}`, // numeric instead of "Fact:42"
	}

	result, err := exec.performManageMemory(ctx, 123, args)

	assert.NoError(t, err)
	assert.Contains(t, result, "Success")

	mockStore.AssertExpectations(t)
}
