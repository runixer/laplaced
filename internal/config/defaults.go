// Package config provides configuration loading and defaults for the laplaced bot.
package config

import "time"

// RAG defaults (documented for AI/developers).
//
// These defaults are used when config values are zero or unset.
// They represent reasonable baselines for RAG operations.

const (
	// Retrieval thresholds
	DefaultMinSafetyThreshold     = 0.1  // Relaxed for recall - minimum cosine similarity for vector search
	DefaultConsolidationThreshold = 0.75 // Strict for dedup - minimum similarity for topic merge

	// Session formatting limits (for reranker context)
	DefaultMaxSessionMessages = 10  // Recent messages to show reranker
	DefaultMaxCharsPerMessage = 500 // Truncate long messages for reranker

	// Topic retrieval limits
	DefaultRetrievedTopicsCount = 10 // Max topics to retrieve without reranker

	// Consolidation
	DefaultMaxMergedSizeChars = 50000 // 50K chars max for merged topics
	DefaultMergeGapThreshold  = 100   // Max message gap for merge candidate

	// Background loop intervals
	DefaultFactExtractionInterval = 1 * time.Minute  // Check for topics needing fact extraction
	DefaultConsolidationInterval  = 10 * time.Minute // Check for topics needing consolidation
	DefaultChunkProcessingTimeout = 10 * time.Minute // Timeout for processing a single chunk
	DefaultBackfillInterval       = 1 * time.Minute  // Background processing check interval

	// Chunking
	DefaultMaxChunkSize = 400 // Max messages per chunk before forced split

	// Memory
	DefaultFactDefaultImportance = 50 // Default importance for facts without explicit importance

	// Search
	DefaultPeopleSimilarityThreshold = 0.3 // Minimum similarity for people vector search
	DefaultPeopleMaxResults          = 5   // Max results for people vector search
)
