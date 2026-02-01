package rag

import (
	"testing"

	"github.com/runixer/laplaced/internal/storage"
)

// TestArtifactResult_BasicStructure tests the new ArtifactResult struct (v0.6.0).
func TestArtifactResult_BasicStructure(t *testing.T) {
	result := ArtifactResult{
		ArtifactID:   10,
		FileType:     "pdf",
		OriginalName: "meeting.pdf",
		Summary:      "Project timeline discussion",
		Keywords:     []string{"project", "timeline", "meeting"},
		Score:        0.85,
	}

	if result.ArtifactID != 10 {
		t.Errorf("expected artifact ID 10, got %d", result.ArtifactID)
	}

	if result.FileType != "pdf" {
		t.Errorf("expected type pdf, got %s", result.FileType)
	}

	if result.Score != 0.85 {
		t.Errorf("expected score 0.85, got %f", result.Score)
	}
}

// TestRetrievalResult_WithArtifacts tests that RetrievalResult can hold artifacts (v0.6.0).
func TestRetrievalResult_WithArtifacts(t *testing.T) {
	artifactResult := ArtifactResult{
		ArtifactID:   10,
		FileType:     "pdf",
		OriginalName: "meeting.pdf",
		Summary:      "Project timeline discussion",
		Keywords:     []string{"project", "timeline"},
		Score:        0.85,
	}

	result := RetrievalResult{
		Topics:    []TopicSearchResult{},
		People:    []storage.Person{},
		Artifacts: []ArtifactResult{artifactResult},
	}

	if len(result.Artifacts) != 1 {
		t.Errorf("expected 1 artifact, got %d", len(result.Artifacts))
	}

	if result.Artifacts[0].ArtifactID != 10 {
		t.Errorf("expected artifact ID 10, got %d", result.Artifacts[0].ArtifactID)
	}
}

// TestCosineSimilarity_BasicTests tests the cosineSimilarity function
func TestCosineSimilarity_BasicTests(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{0.0, 1.0},
			expected: 0.0,
		},
		{
			name:     "similar vectors",
			a:        []float32{0.9, 0.1},
			b:        []float32{0.8, 0.2},
			expected: 0.99, // Approximately 0.99
		},
		{
			name:     "different length vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 0.0,
		},
		{
			name:     "zero vectors",
			a:        []float32{0.0, 0.0},
			b:        []float32{1.0, 0.0},
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)
			if result < tt.expected-0.01 || result > tt.expected+0.01 {
				t.Errorf("cosineSimilarity() = %f, want %f", result, tt.expected)
			}
		})
	}
}
