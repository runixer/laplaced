package rag

import (
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

// VectorSearchConfig controls search behavior for generic vectorSearch.
type VectorSearchConfig struct {
	Threshold float32 // Minimum similarity (0 = default from config)
	Limit     int     // Max results (0 = no limit)
}

// vectorSearch performs vector similarity search over the given index kind
// and records search metrics (the kind doubles as the metric label).
//
// Returns results sorted by score (descending) and the number of vectors scanned.
func (s *Service) vectorSearch(
	userID storage.ScopeID,
	kind VectorKind,
	queryEmbedding []float32,
	cfg VectorSearchConfig,
) ([]VectorSearchResult, int) {
	searchStart := time.Now()

	threshold := cfg.Threshold
	if threshold == 0 {
		threshold = float32(s.cfg.RAG.GetMinSafetyThreshold())
	}

	results, scanned := s.vectors.Search(kind, userID, queryEmbedding, threshold, cfg.Limit)

	RecordVectorSearch(userID, string(kind), time.Since(searchStart).Seconds(), scanned)
	RecordRAGCandidates(userID, string(kind), len(results))

	return results, scanned
}
