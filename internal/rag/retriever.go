package rag

import (
	"context"

	"github.com/runixer/laplaced/internal/storage"
)

// Retriever is the interface used by agents that need RAG functionality.
// This allows agents to be tested with mocks instead of the full Service.
type Retriever interface {
	// GetRecentTopics returns the N most recent topics for a user with message counts.
	GetRecentTopics(userID storage.ScopeID, limit int) ([]storage.TopicExtended, error)

	// Retrieve performs RAG retrieval for a query.
	Retrieve(ctx context.Context, userID storage.ScopeID, query string, opts *RetrievalOptions) (*RetrievalResult, *RetrievalDebugInfo, error)
}
