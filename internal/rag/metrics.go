package rag

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus метрики для RAG системы
//
// Все метрики используют namespace "laplaced" для консистентности.
// Метрики позволяют отслеживать:
// - Производительность и стоимость embedding API
// - Latency и эффективность vector search
// - Размер и память vector index

const (
	namespace = "laplaced"
)

var (
	// === Embedding API Metrics ===

	// embeddingRequestDuration измеряет время генерации embeddings через OpenRouter API.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели (google/gemini-embedding-001)
	//   - type: тип embedding (topics, facts)
	//   - status: результат запроса (success, error)
	embeddingRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "request_duration_seconds",
			Help:      "Duration of embedding API requests in seconds",
			// Buckets для типичных времён embedding API: 100ms - 5s
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5},
		},
		[]string{"user_id", "model", "type", "status"},
	)

	// embeddingRequestsTotal считает общее количество embedding запросов.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
	//   - type: тип embedding (topics, facts)
	//   - status: результат (success, error)
	embeddingRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "requests_total",
			Help:      "Total number of embedding API requests",
		},
		[]string{"user_id", "model", "type", "status"},
	)

	// embeddingTokensTotal считает использованные токены.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
	embeddingTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "embedding",
			Name:      "tokens_total",
			Help:      "Total number of tokens used for embeddings",
		},
		[]string{"user_id", "model"},
	)

	// embeddingCostTotal отслеживает кумулятивную стоимость embedding API (USD).
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
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

	// vectorSearchDuration измеряет время vector search (cosine similarity).
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - type: тип поиска (topics, facts)
	vectorSearchDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "search_duration_seconds",
			Help:      "Duration of vector search operations in seconds",
			// Buckets для in-memory cosine: 1ms - 500ms
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
		},
		[]string{"user_id", "type"},
	)

	// vectorSearchVectorsScanned отслеживает количество просканированных векторов.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - type: тип поиска (topics, facts)
	vectorSearchVectorsScanned = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "search_vectors_scanned",
			Help:      "Number of vectors scanned per search operation",
			// Buckets для количества векторов: 10 - 100K
			Buckets: []float64{10, 50, 100, 500, 1000, 5000, 10000, 50000, 100000},
		},
		[]string{"user_id", "type"},
	)

	// === Vector Index State Metrics ===

	// vectorIndexSize показывает текущий размер vector index.
	// Labels:
	//   - type: тип индекса (topics, facts)
	vectorIndexSize = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "index_size",
			Help:      "Current number of vectors in the index",
		},
		[]string{"type"},
	)

	// vectorIndexMemoryBytes показывает приблизительный размер индекса в памяти.
	// Labels:
	//   - type: тип индекса (topics, facts)
	vectorIndexMemoryBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "vector",
			Name:      "index_memory_bytes",
			Help:      "Approximate memory usage of the vector index in bytes",
		},
		[]string{"type"},
	)

	// ragRetrievalTotal считает результаты RAG retrieval.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - result: результат (hit, miss)
	// hit = нашли релевантный контекст, miss = контекст пустой
	ragRetrievalTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "rag",
			Name:      "retrieval_total",
			Help:      "Total number of RAG retrieval operations",
		},
		[]string{"user_id", "result"},
	)

	// ragCandidatesTotal считает количество кандидатов до фильтрации.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - type: тип кандидатов (topics, facts)
	// Используется для сравнения "до/после" reranker
	ragCandidatesTotal = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "rag",
			Name:      "candidates",
			Help:      "Number of candidates from vector search before filtering",
			// Buckets: 0 - 100 кандидатов
			Buckets: []float64{0, 1, 5, 10, 20, 30, 50, 75, 100},
		},
		[]string{"user_id", "type"},
	)

	// ragLatency измеряет общее время RAG retrieval (enrichment + embedding + vector search).
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - source: "auto" (buildContext) или "tool" (search_history)
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

	// ragEnrichmentDuration измеряет время LLM вызова для обогащения запроса.
	// Labels:
	//   - user_id: идентификатор пользователя
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

// Константы для статусов
const (
	statusSuccess = "success"
	statusError   = "error"
)

// Константы для типов поиска
const (
	searchTypeTopics = "topics"
	searchTypeFacts  = "facts"
	searchTypePeople = "people" // v0.5.1
)

// Константы для RAG результатов
const (
	resultHit  = "hit"
	resultMiss = "miss"
)

// Размер embedding в байтах (3072 dimensions × 4 bytes per float32)
const embeddingMemoryBytes = 3072 * 4

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID int64) string {
	return strconv.FormatInt(userID, 10)
}

// RecordEmbeddingRequest записывает метрики embedding запроса.
// embeddingType: searchTypeTopics или searchTypeFacts
func RecordEmbeddingRequest(userID int64, model string, embeddingType string, durationSeconds float64, success bool, tokens int, cost *float64) {
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

// RecordVectorSearch записывает метрики vector search.
func RecordVectorSearch(userID int64, searchType string, durationSeconds float64, vectorsScanned int) {
	uid := formatUserID(userID)
	vectorSearchDuration.WithLabelValues(uid, searchType).Observe(durationSeconds)
	vectorSearchVectorsScanned.WithLabelValues(uid, searchType).Observe(float64(vectorsScanned))
}

// UpdateVectorIndexMetrics обновляет метрики размера индекса.
func UpdateVectorIndexMetrics(topicsCount, factsCount, peopleCount int) {
	vectorIndexSize.WithLabelValues(searchTypeTopics).Set(float64(topicsCount))
	vectorIndexSize.WithLabelValues(searchTypeFacts).Set(float64(factsCount))
	vectorIndexSize.WithLabelValues(searchTypePeople).Set(float64(peopleCount))

	// Приблизительный размер в памяти
	vectorIndexMemoryBytes.WithLabelValues(searchTypeTopics).Set(float64(topicsCount * embeddingMemoryBytes))
	vectorIndexMemoryBytes.WithLabelValues(searchTypeFacts).Set(float64(factsCount * embeddingMemoryBytes))
	vectorIndexMemoryBytes.WithLabelValues(searchTypePeople).Set(float64(peopleCount * embeddingMemoryBytes))
}

// RecordRAGRetrieval записывает результат RAG retrieval.
func RecordRAGRetrieval(userID int64, hasContext bool) {
	uid := formatUserID(userID)
	if hasContext {
		ragRetrievalTotal.WithLabelValues(uid, resultHit).Inc()
	} else {
		ragRetrievalTotal.WithLabelValues(uid, resultMiss).Inc()
	}
}

// RecordRAGCandidates записывает количество кандидатов до фильтрации.
func RecordRAGCandidates(userID int64, searchType string, count int) {
	uid := formatUserID(userID)
	ragCandidatesTotal.WithLabelValues(uid, searchType).Observe(float64(count))
}

// RecordRAGLatency записывает общее время RAG retrieval.
// source: "auto" для buildContext, "tool" для search_history tool.
func RecordRAGLatency(userID int64, source string, durationSeconds float64) {
	uid := formatUserID(userID)
	if source == "" {
		source = "auto"
	}
	ragLatency.WithLabelValues(uid, source).Observe(durationSeconds)
}

// RecordRAGEnrichment записывает время LLM вызова для обогащения запроса.
func RecordRAGEnrichment(userID int64, durationSeconds float64) {
	uid := formatUserID(userID)
	ragEnrichmentDuration.WithLabelValues(uid).Observe(durationSeconds)
}

// === Reranker Metrics (v0.4) ===

var (
	// rerankerDuration измеряет общее время reranker (все LLM turns).
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

	// rerankerToolCallsTotal считает количество tool calls в reranker.
	rerankerToolCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "tool_calls_total",
			Help:      "Total number of tool calls in reranker operations",
		},
		[]string{"user_id"},
	)

	// rerankerCandidatesInput - кандидатов на входе (из vector search).
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

	// rerankerCandidatesOutput - кандидатов на выходе (финальный выбор).
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

	// rerankerCostTotal - стоимость reranker (USD).
	rerankerCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "cost_usd_total",
			Help:      "Total cost of reranker LLM calls in USD",
		},
		[]string{"user_id"},
	)

	// rerankerFallbackTotal - срабатывания fallback.
	rerankerFallbackTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "reranker",
			Name:      "fallback_total",
			Help:      "Total number of reranker fallbacks",
		},
		[]string{"user_id", "reason"},
	)

	// rerankerHallucinationTotal - галлюцинированные topic IDs.
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

// RecordRerankerDuration записывает время reranker операции.
func RecordRerankerDuration(userID int64, durationSeconds float64) {
	uid := formatUserID(userID)
	rerankerDuration.WithLabelValues(uid).Observe(durationSeconds)
}

// RecordRerankerToolCalls записывает количество tool calls.
func RecordRerankerToolCalls(userID int64, count int) {
	uid := formatUserID(userID)
	rerankerToolCallsTotal.WithLabelValues(uid).Add(float64(count))
}

// RecordRerankerCandidatesInput записывает кандидатов на входе.
func RecordRerankerCandidatesInput(userID int64, count int) {
	uid := formatUserID(userID)
	rerankerCandidatesInput.WithLabelValues(uid).Observe(float64(count))
}

// RecordRerankerCandidatesOutput записывает кандидатов на выходе.
func RecordRerankerCandidatesOutput(userID int64, count int) {
	uid := formatUserID(userID)
	rerankerCandidatesOutput.WithLabelValues(uid).Observe(float64(count))
}

// RecordRerankerCost записывает стоимость reranker.
func RecordRerankerCost(userID int64, cost float64) {
	uid := formatUserID(userID)
	rerankerCostTotal.WithLabelValues(uid).Add(cost)
}

// RecordRerankerFallback записывает срабатывание fallback.
// reason: "timeout", "error", "max_tool_calls", "invalid_json", "requested_ids", "vector_top", "all_hallucinated"
func RecordRerankerFallback(userID int64, reason string) {
	uid := formatUserID(userID)
	rerankerFallbackTotal.WithLabelValues(uid, reason).Inc()
}

// RecordRerankerHallucination записывает количество галлюцинированных topic IDs.
func RecordRerankerHallucination(userID int64, count int) {
	uid := formatUserID(userID)
	rerankerHallucinationTotal.WithLabelValues(uid).Add(float64(count))
}
