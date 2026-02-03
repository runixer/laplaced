package extractor

import (
	"time"

	"github.com/runixer/laplaced/internal/agent"
)

// ProcessResultBuilder builds ProcessResult objects for testing.
type ProcessResultBuilder struct {
	result *ProcessResult
}

// NewProcessResultBuilder creates a new ProcessResultBuilder.
func NewProcessResultBuilder() *ProcessResultBuilder {
	return &ProcessResultBuilder{
		result: &ProcessResult{
			Keywords: make([]string, 0),
			Entities: make([]string, 0),
			RAGHints: make([]string, 0),
		},
	}
}

// WithArtifactID sets the artifact ID.
func (b *ProcessResultBuilder) WithArtifactID(id int64) *ProcessResultBuilder {
	b.result.ArtifactID = id
	return b
}

// WithSummary sets the summary.
func (b *ProcessResultBuilder) WithSummary(summary string) *ProcessResultBuilder {
	b.result.Summary = summary
	return b
}

// WithKeywords sets the keywords (variadic for convenience).
func (b *ProcessResultBuilder) WithKeywords(keywords ...string) *ProcessResultBuilder {
	b.result.Keywords = keywords
	return b
}

// WithEntities sets the entities (variadic for convenience).
func (b *ProcessResultBuilder) WithEntities(entities ...string) *ProcessResultBuilder {
	b.result.Entities = entities
	return b
}

// WithRAGHints sets the RAG hints (variadic for convenience).
func (b *ProcessResultBuilder) WithRAGHints(hints ...string) *ProcessResultBuilder {
	b.result.RAGHints = hints
	return b
}

// WithDuration sets the duration.
func (b *ProcessResultBuilder) WithDuration(d time.Duration) *ProcessResultBuilder {
	b.result.Duration = d
	return b
}

// WithTokens sets token usage.
func (b *ProcessResultBuilder) WithTokens(prompt, completion, total int) *ProcessResultBuilder {
	b.result.Tokens = agent.TokenUsage{
		Prompt:     prompt,
		Completion: completion,
		Total:      total,
	}
	return b
}

// Build returns the constructed ProcessResult.
func (b *ProcessResultBuilder) Build() *ProcessResult {
	return b.result
}
