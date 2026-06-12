package rag

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/runixer/laplaced/internal/storage"
)

// Prometheus metrics for the RAG system
//
// All metrics use the "laplaced" namespace for consistency.
// These metrics track:
// - Embedding API performance and cost
// - Vector search latency and effectiveness
// - Vector index size and memory

const (
	namespace = "laplaced"
)

var (
	// === Embedding API Metrics ===

	// embeddingRequestDuration measures embedding generation time via the LLM API.
	// Labels:
	//   - user_id: user identifier
	//   - model: model name (google/gemini-embedding-001)
	//   - type: embedding type (topics, facts)
	//   - status: request outcome (success, error)
	embeddingRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "request_duration_seconds",
			Help:      "Duration of embedding API requests in seconds",
			// Buckets for typical embedding API times: 100ms - 5s
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5},
		},
		[]string{"user_id", "model", "type", "status"},
	)

	// embeddingRequestsTotal counts the total number of embedding requests.
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	//   - type: embedding type (topics, facts)
	//   - status: outcome (success, error)
	embeddingRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "requests_total",
			Help:      "Total number of embedding API requests",
		},
		[]string{"user_id", "model", "type", "status"},
	)

	// embeddingTokensTotal counts tokens used.
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	embeddingTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "tokens_total",
			Help:      "Total number of tokens used for embeddings",
		},
		[]string{"user_id", "model"},
	)

	// embeddingCostTotal tracks cumulative embedding API cost (USD).
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	embeddingCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "cost_usd_total",
			Help:      "Total cost of embedding API requests in USD",
		},
		[]string{"user_id", "model"},
	)

	// === Vector Search Metrics ===

	// vectorSearchDuration measures vector search time (cosine similarity).
	// Labels:
	//   - user_id: user identifier
	//   - type: search type (topics, facts)
	vectorSearchDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "search_duration_seconds",
			Help:      "Duration of vector search operations in seconds",
			// Buckets for in-memory cosine: 1ms - 500ms
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
		},
		[]string{"user_id", "type"},
	)

	// vectorSearchVectorsScanned tracks the number of vectors scanned.
	// Labels:
	//   - user_id: user identifier
	//   - type: search type (topics, facts)
	vectorSearchVectorsScanned = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "search_vectors_scanned",
			Help:      "Number of vectors scanned per search operation",
			// Buckets for vector counts: 10 - 100K
			Buckets: []float64{10, 50, 100, 500, 1000, 5000, 10000, 50000, 100000},
		},
		[]string{"user_id", "type"},
	)

	// === Vector Index State Metrics ===

	// vectorIndexSize shows the current vector index size.
	// Labels:
	//   - type: index type (topics, facts)
	vectorIndexSize = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "index_size",
			Help:      "Current number of vectors in the index",
		},
		[]string{"type"},
	)

	// vectorIndexMemoryBytes shows the approximate in-memory size of the index.
	// Labels:
	//   - type: index type (topics, facts)
	vectorIndexMemoryBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "index_memory_bytes",
			Help:      "Approximate memory usage of the vector index in bytes",
		},
		[]string{"type"},
	)

	// ragRetrievalTotal counts RAG retrieval results.
	// Labels:
	//   - user_id: user identifier
	//   - result: outcome (hit, miss)
	// hit = relevant context found, miss = context is empty
	ragRetrievalTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "rag",
			Name:      "retrieval_total",
			Help:      "Total number of RAG retrieval operations",
		},
		[]string{"user_id", "result"},
	)

	// ragCandidatesTotal counts the number of candidates before filtering.
	// Labels:
	//   - user_id: user identifier
	//   - type: candidate type (topics, facts)
	// Used for before/after reranker comparison
	ragCandidatesTotal = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "rag",
			Name:      "candidates",
			Help:      "Number of candidates from vector search before filtering",
			// Buckets: 0 - 100 candidates
			Buckets: []float64{0, 1, 5, 10, 20, 30, 50, 75, 100},
		},
		[]string{"user_id", "type"},
	)

	// ragLatency measures total RAG retrieval time (enrichment + embedding + vector search).
	// Labels:
	//   - user_id: user identifier
	//   - source: "auto" (buildContext) or "tool" (search_history)
	ragLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "rag",
			Name:      "latency_seconds",
			Help:      "Total latency of RAG retrieval operations in seconds",
			// Buckets: 5ms - 10s (includes LLM enrichment call)
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"user_id", "source"},
	)

	// ragEnrichmentDuration measures the LLM call time for query enrichment.
	// Labels:
	//   - user_id: user identifier
	ragEnrichmentDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "rag",
			Name:      "enrichment_duration_seconds",
			Help:      "Duration of query enrichment LLM calls in seconds",
			// Buckets: 500ms - 30s (LLM Flash call)
			Buckets: []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30},
		},
		[]string{"user_id"},
	)
)

// Status constants
const (
	statusSuccess = "success"
	statusError   = "error"
)

// Search type constants
const (
	searchTypeTopics    = "topics"
	searchTypeFacts     = "facts"
	searchTypePeople    = "people"    // v0.5.1
	searchTypeArtifacts = "artifacts" // v0.5.2
)

// RAG result constants
const (
	resultHit  = "hit"
	resultMiss = "miss"
)

// defaultEmbeddingDim is the fallback dim when EmbeddingConfig.Dimensions
// isn't set — matches the provider default for Gemini embedding models.
const defaultEmbeddingDim = 3072

// embeddingMemoryBytes returns the approximate in-memory size per embedding
// vector, derived from the configured dimension. Used only for the
// vector_index_memory_bytes metric.
func embeddingMemoryBytes(dim int) int {
	if dim <= 0 {
		dim = defaultEmbeddingDim
	}
	return dim * 4 // float32 = 4 bytes
}

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID storage.ScopeID) string {
	return string(userID)
}

// RecordEmbeddingRequest records embedding request metrics.
// embeddingType: searchTypeTopics or searchTypeFacts
func RecordEmbeddingRequest(userID storage.ScopeID, model string, embeddingType string, durationSeconds float64, success bool, tokens int, cost *float64) {
	status := statusSuccess
	if !success {
		status = statusError
	}

	uid := formatUserID(userID)
	embeddingRequestDuration.WithLabelValues(uid, model, embeddingType, status).Observe(durationSeconds)
	embeddingRequestsTotal.WithLabelValues(uid, model, embeddingType, status).Inc()

	if success && tokens > 0 {
		embeddingTokensTotal.WithLabelValues(uid, model).Add(float64(tokens))
	}

	if success && cost != nil && *cost > 0 {
		embeddingCostTotal.WithLabelValues(uid, model).Add(*cost)
	}
}

// RecordVectorSearch records vector search metrics.
func RecordVectorSearch(userID storage.ScopeID, searchType string, durationSeconds float64, vectorsScanned int) {
	uid := formatUserID(userID)
	vectorSearchDuration.WithLabelValues(uid, searchType).Observe(durationSeconds)
	vectorSearchVectorsScanned.WithLabelValues(uid, searchType).Observe(float64(vectorsScanned))
}

// UpdateVectorIndexMetrics updates index size metrics.
// dim should be the configured embedding dimension; used to derive a
// ballpark RAM estimate for the vector_index_memory_bytes metric.
func UpdateVectorIndexMetrics(topicsCount, factsCount, peopleCount, dim int) {
	vectorIndexSize.WithLabelValues(searchTypeTopics).Set(float64(topicsCount))
	vectorIndexSize.WithLabelValues(searchTypeFacts).Set(float64(factsCount))
	vectorIndexSize.WithLabelValues(searchTypePeople).Set(float64(peopleCount))

	perVec := embeddingMemoryBytes(dim)
	vectorIndexMemoryBytes.WithLabelValues(searchTypeTopics).Set(float64(topicsCount * perVec))
	vectorIndexMemoryBytes.WithLabelValues(searchTypeFacts).Set(float64(factsCount * perVec))
	vectorIndexMemoryBytes.WithLabelValues(searchTypePeople).Set(float64(peopleCount * perVec))
}

// RecordRAGRetrieval records the result of a RAG retrieval.
func RecordRAGRetrieval(userID storage.ScopeID, hasContext bool) {
	uid := formatUserID(userID)
	if hasContext {
		ragRetrievalTotal.WithLabelValues(uid, resultHit).Inc()
	} else {
		ragRetrievalTotal.WithLabelValues(uid, resultMiss).Inc()
	}
}

// RecordRAGCandidates records the number of candidates before filtering.
func RecordRAGCandidates(userID storage.ScopeID, searchType string, count int) {
	uid := formatUserID(userID)
	ragCandidatesTotal.WithLabelValues(uid, searchType).Observe(float64(count))
}

// RecordRAGLatency records total RAG retrieval time.
// source: "auto" for buildContext, "tool" for the search_history tool.
func RecordRAGLatency(userID storage.ScopeID, source string, durationSeconds float64) {
	uid := formatUserID(userID)
	if source == "" {
		source = "auto"
	}
	ragLatency.WithLabelValues(uid, source).Observe(durationSeconds)
}

// RecordRAGEnrichment records the LLM call time for query enrichment.
func RecordRAGEnrichment(userID storage.ScopeID, durationSeconds float64) {
	uid := formatUserID(userID)
	ragEnrichmentDuration.WithLabelValues(uid).Observe(durationSeconds)
}

// === Reranker Metrics (v0.4) ===

var (
	// rerankerDuration measures total reranker time (all LLM turns).
	rerankerDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "duration_seconds",
			Help:      "Total duration of reranker operation in seconds",
			Buckets:   []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20},
		},
		[]string{"user_id"},
	)

	// rerankerToolCallsTotal counts tool calls in the reranker.
	rerankerToolCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "tool_calls_total",
			Help:      "Total number of tool calls in reranker operations",
		},
		[]string{"user_id"},
	)

	// rerankerCandidatesInput - candidates on input (from vector search).
	rerankerCandidatesInput = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "candidates_input",
			Help:      "Number of candidates passed to reranker",
			Buckets:   []float64{0, 10, 20, 30, 40, 50, 75, 100},
		},
		[]string{"user_id"},
	)

	// rerankerCandidatesOutput - candidates on output (final selection).
	rerankerCandidatesOutput = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "candidates_output",
			Help:      "Number of candidates selected by reranker",
			Buckets:   []float64{0, 1, 2, 3, 4, 5, 7, 10},
		},
		[]string{"user_id"},
	)

	// rerankerCostTotal - reranker cost (USD).
	rerankerCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "cost_usd_total",
			Help:      "Total cost of reranker LLM calls in USD",
		},
		[]string{"user_id"},
	)

	// rerankerFallbackTotal - fallback activations.
	rerankerFallbackTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "fallback_total",
			Help:      "Total number of reranker fallbacks",
		},
		[]string{"user_id", "reason"},
	)

	// rerankerHallucinationTotal - hallucinated topic IDs.
	rerankerHallucinationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "hallucination_total",
			Help:      "Total number of hallucinated topic IDs returned by reranker",
		},
		[]string{"user_id"},
	)
)

// RecordRerankerDuration records reranker operation duration.
func RecordRerankerDuration(userID storage.ScopeID, durationSeconds float64) {
	uid := formatUserID(userID)
	rerankerDuration.WithLabelValues(uid).Observe(durationSeconds)
}

// RecordRerankerToolCalls records the number of tool calls.
func RecordRerankerToolCalls(userID storage.ScopeID, count int) {
	uid := formatUserID(userID)
	rerankerToolCallsTotal.WithLabelValues(uid).Add(float64(count))
}

// RecordRerankerCandidatesInput records the number of input candidates.
func RecordRerankerCandidatesInput(userID storage.ScopeID, count int) {
	uid := formatUserID(userID)
	rerankerCandidatesInput.WithLabelValues(uid).Observe(float64(count))
}

// RecordRerankerCandidatesOutput records the number of selected candidates.
func RecordRerankerCandidatesOutput(userID storage.ScopeID, count int) {
	uid := formatUserID(userID)
	rerankerCandidatesOutput.WithLabelValues(uid).Observe(float64(count))
}

// RecordRerankerCost records reranker cost.
func RecordRerankerCost(userID storage.ScopeID, cost float64) {
	uid := formatUserID(userID)
	rerankerCostTotal.WithLabelValues(uid).Add(cost)
}

// RecordRerankerFallback records a fallback activation.
// reason: "timeout", "error", "max_tool_calls", "invalid_json", "requested_ids", "vector_top", "all_hallucinated"
func RecordRerankerFallback(userID storage.ScopeID, reason string) {
	uid := formatUserID(userID)
	rerankerFallbackTotal.WithLabelValues(uid, reason).Inc()
}

// RecordRerankerHallucination records the number of hallucinated topic IDs.
func RecordRerankerHallucination(userID storage.ScopeID, count int) {
	uid := formatUserID(userID)
	rerankerHallucinationTotal.WithLabelValues(uid).Add(float64(count))
}

// === Artifact Extraction Metrics (Phase 3, v0.6.0) ===

var (
	// artifactExtractionJobsTotal counts artifact extraction jobs.
	// Labels:
	//   - user_id: user identifier
	//   - status: outcome (success, error)
	artifactExtractionJobsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "artifact",
			Name:      "extraction_jobs_total",
			Help:      "Total number of artifact extraction jobs",
		},
		[]string{"user_id", "status"},
	)

	// artifactExtractionDuration measures time for artifact extraction.
	// Labels:
	//   - user_id: user identifier
	//   - status: outcome (success, error)
	artifactExtractionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "artifact",
			Name:      "extraction_duration_seconds",
			Help:      "Duration of artifact extraction in seconds",
			// Buckets: 1s - 5min (LLM calls + chunking + embeddings)
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300},
		},
		[]string{"user_id", "status"},
	)

	// artifactSummaryIndexSize shows current number of artifact summaries in vector index (v0.6.0).
	artifactSummaryIndexSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "artifact",
			Name:      "summary_index_size",
			Help:      "Current number of artifact summaries in vector index",
		},
	)
)

// RecordArtifactExtraction records metrics for artifact extraction job (v0.6.0).
func RecordArtifactExtraction(userID storage.ScopeID, durationSeconds float64, success bool) {
	status := statusSuccess
	if !success {
		status = statusError
	}

	uid := formatUserID(userID)
	artifactExtractionJobsTotal.WithLabelValues(uid, status).Inc()
	artifactExtractionDuration.WithLabelValues(uid, status).Observe(durationSeconds)
}

// UpdateArtifactSummaryMetrics updates the artifact summary index size metric (v0.6.0).
func UpdateArtifactSummaryMetrics(perUserCounts map[storage.ScopeID]int) {
	total := 0
	for _, count := range perUserCounts {
		total += count
	}
	artifactSummaryIndexSize.Set(float64(total))
}
