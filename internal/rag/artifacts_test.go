package rag

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/extractor"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// TestArtifactLocking tests the artifact locking mechanism.
func TestArtifactLocking(t *testing.T) {
	s := &Service{
		processingArtifacts: sync.Map{},
	}

	artifactID := int64(123)

	// First attempt should succeed
	assert.True(t, s.tryStartProcessingArtifact(artifactID), "first attempt should succeed")

	// Second attempt should fail (already processing)
	assert.False(t, s.tryStartProcessingArtifact(artifactID), "second attempt should fail")

	// Finish processing
	s.finishProcessingArtifact(artifactID)

	// Third attempt should succeed again
	assert.True(t, s.tryStartProcessingArtifact(artifactID), "third attempt should succeed after finish")
}

// TestArtifactVectorItem tests the artifact summary vector item structure (v0.6.0).
func TestArtifactVectorItem(t *testing.T) {
	embedding := make([]float32, 10)
	for i := range embedding {
		embedding[i] = float32(i) * 0.1
	}

	item := ArtifactVectorItem{
		ArtifactID: 123,
		UserID:     456,
		Embedding:  embedding,
	}

	assert.Equal(t, int64(123), item.ArtifactID)
	assert.Equal(t, int64(456), item.UserID)
	assert.Equal(t, 10, len(item.Embedding))
	assert.Equal(t, float32(0.5), item.Embedding[5])
}

// TestLoadNewArtifactSummaries tests incremental artifact summary loading (v0.6.0).
func TestLoadNewArtifactSummaries(t *testing.T) {
	s := &Service{
		artifactRepo:        nil, // Would be mocked in real test
		artifactVectors:     make(map[int64][]ArtifactVectorItem),
		maxLoadedArtifactID: 0,
		logger:              testutil.TestLogger(),
	}

	// When artifactRepo is nil, should return nil
	err := s.LoadNewArtifactSummaries()
	assert.NoError(t, err, "should return nil when artifactRepo is not set")
}

// TestProcessResultFields tests the ProcessResult struct from extractor (v0.6.0).
func TestProcessResultFields(t *testing.T) {
	result := &extractor.ProcessResult{
		ArtifactID: 123,
		Summary:    "Test summary of the document content",
		Keywords:   []string{"test", "artifact", "document"},
		Entities:   []string{"Person", "Company"},
		RAGHints:   []string{"What does this file contain?", "Summary of content"},
		Duration:   10 * time.Second,
	}

	assert.Equal(t, int64(123), result.ArtifactID)
	assert.Equal(t, "Test summary of the document content", result.Summary)
	assert.Equal(t, 3, len(result.Keywords))
	assert.Equal(t, 2, len(result.Entities))
	assert.Equal(t, 2, len(result.RAGHints))
	assert.Equal(t, 10*time.Second, result.Duration)
}

// TestProcessSingleArtifact tests the artifact processing flow.
// This is a smoke test - full testing requires LLM mocking.
func TestProcessSingleArtifact_Smoke(t *testing.T) {
	// Create a mock service
	s := &Service{
		extractorAgent: nil, // Would be mocked in real test
		logger:         nil,
	}

	artifact := storage.Artifact{
		ID:       123,
		UserID:   456,
		FileType: "document",
		State:    "pending",
	}

	ctx := context.Background()

	// When extractorAgent is nil, should handle gracefully
	// In real scenario, this would call the agent
	_ = ctx
	_ = artifact
	_ = s

	// This test just verifies the signature compiles
	// Real testing would require mocking the extractor agent
}
