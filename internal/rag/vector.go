package rag

import (
	"sort"
	"time"
)

// VectorSearchResult holds ID and similarity score from vector search.
type VectorSearchResult struct {
	ID    int64
	Score float32
}

// VectorSearchConfig controls search behavior for generic vectorSearch.
type VectorSearchConfig struct {
	Threshold  float32 // Minimum similarity (default: from config)
	Limit      int     // Max results (0 = no limit)
	MetricType string  // "topics", "facts", "people", "artifacts"
}

// embeddingItem is a generic interface for items with embeddings.
// This allows generic vectorSearch to work with any vector type.
type embeddingItem interface {
	GetID() int64
	GetEmbedding() []float32
}

// === embeddingItem implementations ===

func (t TopicVectorItem) GetID() int64            { return t.TopicID }
func (t TopicVectorItem) GetEmbedding() []float32 { return t.Embedding }

func (f FactVectorItem) GetID() int64            { return f.FactID }
func (f FactVectorItem) GetEmbedding() []float32 { return f.Embedding }

func (p PersonVectorItem) GetID() int64            { return p.PersonID }
func (p PersonVectorItem) GetEmbedding() []float32 { return p.Embedding }

func (a ArtifactVectorItem) GetID() int64            { return a.ArtifactID }
func (a ArtifactVectorItem) GetEmbedding() []float32 { return a.Embedding }

// vectorSearch performs generic vector similarity search.
// Thread-safe (caller must hold RLock), records metrics.
//
// Parameters:
//   - userID: for metrics
//   - queryEmbedding: the query vector to compare against
//   - vectors: slice of items to search (must implement embeddingItem)
//   - cfg: search configuration (threshold, limit, metric type)
//
// Returns results sorted by score (descending).
func (s *Service) vectorSearch(
	userID int64,
	queryEmbedding []float32,
	vectors []embeddingItem,
	cfg VectorSearchConfig,
) []VectorSearchResult {
	searchStart := time.Now()

	threshold := cfg.Threshold
	if threshold == 0 {
		threshold = float32(s.cfg.RAG.GetMinSafetyThreshold())
	}

	var results []VectorSearchResult
	for _, item := range vectors {
		score := cosineSimilarity(queryEmbedding, item.GetEmbedding())
		if score >= threshold {
			results = append(results, VectorSearchResult{
				ID:    item.GetID(),
				Score: score,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if cfg.Limit > 0 && len(results) > cfg.Limit {
		results = results[:cfg.Limit]
	}

	if cfg.MetricType != "" {
		RecordVectorSearch(userID, cfg.MetricType, time.Since(searchStart).Seconds(), len(vectors))
		RecordRAGCandidates(userID, cfg.MetricType, len(results))
	}

	return results
}

// cosineSimilarity is defined in retrieval.go to avoid duplicates.
// It's exported for use in vector.go through the vectorSearch method.
