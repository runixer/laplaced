package rag

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
)

func TestCosineSimilarity(t *testing.T) {
	t.Run("returns 0 for different length vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{1, 2}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})

	t.Run("returns 0 when first vector is zero", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 2, 3}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})

	t.Run("returns 0 when second vector is zero", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{0, 0, 0}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})

	t.Run("returns 1 for identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{1, 2, 3}

		result := cosineSimilarity(a, b)

		assert.InDelta(t, float32(1), result, 0.0001)
	})

	t.Run("returns -1 for opposite vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{-1, -2, -3}

		result := cosineSimilarity(a, b)

		assert.InDelta(t, float32(-1), result, 0.0001)
	})

	t.Run("returns correct similarity for orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{0, 1}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})
}

func TestFindSimilarFacts(t *testing.T) {
	userID := int64(123)

	t.Run("no similar facts", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Facts with low similarity
		facts := []storage.Fact{
			{ID: 1, UserID: userID, Content: "unrelated", Embedding: []float32{0.0, 0.0, 1.0}},
		}

		mockStore.On("GetAllFacts").Return(facts, nil)

		svc := TestRAGService(t, mockStore, mockClient)

		// Query embedding that doesn't match
		result, err := svc.FindSimilarFacts(context.Background(), userID, []float32{1.0, 0.0, 0.0}, 0.85)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("finds similar facts", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		facts := []storage.Fact{
			{ID: 1, UserID: userID, Content: "similar fact", Embedding: []float32{0.95, 0.05, 0.0}},
			{ID: 2, UserID: userID, Content: "unrelated", Embedding: []float32{0.0, 0.0, 1.0}},
		}

		mockStore.On("GetAllFacts").Return(facts, nil)
		mockStore.On("GetFactsByIDs", userID, []int64{1}).Return([]storage.Fact{facts[0]}, nil)

		svc := TestRAGService(t, mockStore, mockClient)

		result, err := svc.FindSimilarFacts(context.Background(), userID, []float32{1.0, 0.0, 0.0}, 0.85)
		assert.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "similar fact", result[0].Content)
	})
}

func TestLoadNewVectors(t *testing.T) {
	t.Run("loads new topics and facts", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		embedding := []float32{0.1, 0.2, 0.3}
		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{
			{ID: 1, UserID: 123, Summary: "Topic 1", Embedding: embedding},
			{ID: 2, UserID: 123, Summary: "Topic 2", Embedding: embedding},
		}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{
			{ID: 1, UserID: 123, Content: "Fact 1", Embedding: embedding},
		}, nil)

		svc := TestRAGServiceNoStart(t, mockStore, mockClient)

		err := svc.LoadNewVectors()

		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})

	t.Run("returns early when no new data", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{}, nil)

		svc := TestRAGServiceNoStart(t, mockStore, mockClient)

		err := svc.LoadNewVectors()

		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})

	t.Run("handles topic load error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{}, assert.AnError)

		svc := TestRAGServiceNoStart(t, mockStore, mockClient)

		err := svc.LoadNewVectors()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "load new topics")
	})

	t.Run("handles fact load error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{}, assert.AnError)

		svc := TestRAGServiceNoStart(t, mockStore, mockClient)

		err := svc.LoadNewVectors()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "load new facts")
	})

	t.Run("skips topics without embeddings", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		embedding := []float32{0.1, 0.2, 0.3}
		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{
			{ID: 1, UserID: 123, Summary: "Topic with embedding", Embedding: embedding},
			{ID: 2, UserID: 123, Summary: "Topic without embedding", Embedding: nil},
		}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{}, nil)

		svc := TestRAGServiceNoStart(t, mockStore, mockClient)

		err := svc.LoadNewVectors()

		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})
}
