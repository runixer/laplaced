package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestCurrentEmbeddingVersion(t *testing.T) {
	tests := []struct {
		name  string
		model string
		dim   int
		want  string
	}{
		{"both set", "google/gemini-embedding-2-preview", 1536, "google/gemini-embedding-2-preview:1536"},
		{"zero dim uses default suffix", "model-x", 0, "model-x"},
		{"negative dim uses default suffix", "model-x", -1, "model-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, currentEmbeddingVersion(tt.model, tt.dim))
		})
	}
}

func TestBatchCandidates(t *testing.T) {
	cands := make([]storage.ReembedCandidate, 5)
	for i := range cands {
		cands[i].ID = int64(i + 1)
	}
	got := batchCandidates(cands, 2)
	assert.Len(t, got, 3)
	assert.Equal(t, 2, len(got[0]))
	assert.Equal(t, 2, len(got[1]))
	assert.Equal(t, 1, len(got[2]))

	got = batchCandidates(nil, 10)
	assert.Empty(t, got)
}

// TestReembedIfNeeded_NoCandidates verifies fast path: no rows to re-embed.
func TestReembedIfNeeded_NoCandidates(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	SetupCommonRAGMocks(mockStore)

	svc := newTestRAGServiceNoStart(t, mockStore, mockClient, func(c *config.Config) {
		c.Embedding.Model = "new-model"
		c.Embedding.Dimensions = 1536
	})

	err := svc.ReembedIfNeeded(context.Background())
	assert.NoError(t, err)

	// CreateEmbeddings should NEVER be called when nothing is pending.
	mockClient.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

// TestReembedIfNeeded_TopicsReembedded verifies that pending topics go through
// embed → persist → next batch.
func TestReembedIfNeeded_TopicsReembedded(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	candidates := []storage.ReembedCandidate{
		{ID: 10, UserID: 1, Content: "topic one"},
		{ID: 11, UserID: 1, Content: "topic two"},
	}
	expectedVersion := "new-model:1536"

	mockStore.On("GetTopicsNeedingReembed", expectedVersion, 0).Return(candidates, nil).Once()
	// After first fetch we expect ZERO further GetTopicsNeedingReembed calls because
	// the current implementation processes all candidates in one pass.

	// The embed call receives both contents in one batch; echo fake vectors back.
	mockClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return req.Model == "new-model" && req.Dimensions == 1536 && len(req.Input) == 2
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.2}, Index: 0},
			{Embedding: []float32{0.3, 0.4}, Index: 1},
		},
	}, nil).Once()

	// Each row is persisted individually.
	mockStore.On("UpdateTopicEmbeddingVersion", int64(10), []float32{0.1, 0.2}, expectedVersion).Return(nil).Once()
	mockStore.On("UpdateTopicEmbeddingVersion", int64(11), []float32{0.3, 0.4}, expectedVersion).Return(nil).Once()

	svc := newTestRAGServiceNoStart(t, mockStore, mockClient, func(c *config.Config) {
		c.Embedding.Model = "new-model"
		c.Embedding.Dimensions = 1536
	})

	err := svc.ReembedIfNeeded(context.Background())
	assert.NoError(t, err)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
}

// TestReembedIfNeeded_EmbedError propagates a hard error and leaves the DB alone.
func TestReembedIfNeeded_EmbedError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	SetupCommonRAGMocks(mockStore)

	candidates := []storage.ReembedCandidate{{ID: 10, UserID: 1, Content: "topic"}}
	mockStore.On("GetTopicsNeedingReembed", mock.Anything, 0).Return(candidates, nil).Once()
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(openrouter.EmbeddingResponse{}, errors.New("provider down")).Once()

	svc := newTestRAGServiceNoStart(t, mockStore, mockClient, func(c *config.Config) {
		c.Embedding.Model = "new-model"
		c.Embedding.Dimensions = 1536
	})

	err := svc.ReembedIfNeeded(context.Background())
	if !assert.Error(t, err) {
		return
	}
	assert.Contains(t, err.Error(), "re-embed topics")

	mockStore.AssertNotCalled(t, "UpdateTopicEmbeddingVersion", mock.Anything, mock.Anything, mock.Anything)
}
