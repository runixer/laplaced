package reranker

import (
	"fmt"
)

// RerankerResultBuilder builds Result objects for testing.
type RerankerResultBuilder struct {
	result *Result
}

// NewRerankerResultBuilder creates a new RerankerResultBuilder.
func NewRerankerResultBuilder() *RerankerResultBuilder {
	return &RerankerResultBuilder{
		result: &Result{
			Topics:    make([]TopicSelection, 0),
			People:    make([]PersonSelection, 0),
			Artifacts: make([]ArtifactSelection, 0),
		},
	}
}

// WithTopics adds topic IDs (backward compatible format).
func (b *RerankerResultBuilder) WithTopics(ids ...int64) *RerankerResultBuilder {
	for _, id := range ids {
		b.result.Topics = append(b.result.Topics, TopicSelection{
			ID:     fmt.Sprintf("Topic:%d", id),
			Reason: "",
		})
	}
	return b
}

// WithTopicWithReason adds a topic with explanation.
func (b *RerankerResultBuilder) WithTopicWithReason(id int64, reason string) *RerankerResultBuilder {
	b.result.Topics = append(b.result.Topics, TopicSelection{
		ID:     fmt.Sprintf("Topic:%d", id),
		Reason: reason,
	})
	return b
}

// WithPersonWithReason adds a person with explanation.
func (b *RerankerResultBuilder) WithPersonWithReason(id int64, reason string) *RerankerResultBuilder {
	b.result.People = append(b.result.People, PersonSelection{
		ID:     fmt.Sprintf("Person:%d", id),
		Reason: reason,
	})
	return b
}

// WithArtifactWithReason adds an artifact with explanation.
func (b *RerankerResultBuilder) WithArtifactWithReason(id int64, reason string) *RerankerResultBuilder {
	b.result.Artifacts = append(b.result.Artifacts, ArtifactSelection{
		ID:     fmt.Sprintf("Artifact:%d", id),
		Reason: reason,
	})
	return b
}

// Build returns the constructed Result.
func (b *RerankerResultBuilder) Build() *Result {
	return b.result
}
