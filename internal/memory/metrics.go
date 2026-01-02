package memory

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus метрики для Memory System
//
// Метрики позволяют отслеживать:
// - Операции с фактами (add/update/delete)
// - Решения дедупликации
// - Время извлечения фактов и обработки топиков
// - Количество фактов и топиков

const metricsNamespace = "laplaced"

var (
	// factOperationsTotal считает операции с фактами.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - operation: тип операции (add, update, delete)
	factOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "fact_operations_total",
			Help:      "Total number of fact operations",
		},
		[]string{"user_id", "operation"},
	)

	// dedupDecisionsTotal считает решения дедупликации.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - decision: решение (ignore, merge, replace, add)
	dedupDecisionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "dedup_decisions_total",
			Help:      "Total number of deduplication decisions",
		},
		[]string{"user_id", "decision"},
	)

	// memoryExtractionDuration измеряет время извлечения фактов из сообщений.
	memoryExtractionDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "extraction_duration_seconds",
			Help:      "Duration of fact extraction from messages in seconds",
			// Buckets для типичных времён LLM extraction: 1s - 30s
			Buckets: []float64{1, 2, 3, 5, 7, 10, 15, 20, 30},
		},
	)

	// topicProcessingDuration измеряет время обработки топика (создание + извлечение фактов).
	topicProcessingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "topic_processing_duration_seconds",
			Help:      "Duration of topic processing in seconds",
			// Buckets для полной обработки топика: 2s - 60s
			Buckets: []float64{2, 5, 10, 15, 20, 30, 45, 60},
		},
	)

	// factsTotal показывает текущее количество фактов.
	// Labels:
	//   - type: тип факта (identity, context, status)
	factsTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "facts_total",
			Help:      "Current number of facts by type",
		},
		[]string{"type"},
	)

	// topicsTotal показывает текущее количество топиков.
	topicsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "topics_total",
			Help:      "Current total number of topics",
		},
	)
)

// Константы для операций
const (
	OperationAdd    = "add"
	OperationUpdate = "update"
	OperationDelete = "delete"
)

// Константы для решений дедупликации
const (
	DecisionIgnore  = "ignore"
	DecisionMerge   = "merge"
	DecisionReplace = "replace"
	DecisionAdd     = "add"
)

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID int64) string {
	return strconv.FormatInt(userID, 10)
}

// RecordFactOperation записывает операцию с фактом.
func RecordFactOperation(userID int64, operation string) {
	factOperationsTotal.WithLabelValues(formatUserID(userID), operation).Inc()
}

// RecordDedupDecision записывает решение дедупликации.
func RecordDedupDecision(userID int64, decision string) {
	dedupDecisionsTotal.WithLabelValues(formatUserID(userID), decision).Inc()
}

// RecordMemoryExtraction записывает время извлечения фактов.
func RecordMemoryExtraction(durationSeconds float64) {
	memoryExtractionDuration.Observe(durationSeconds)
}

// RecordTopicProcessing записывает время обработки топика.
func RecordTopicProcessing(durationSeconds float64) {
	topicProcessingDuration.Observe(durationSeconds)
}

// UpdateFactsTotal обновляет gauge количества фактов по типам.
func UpdateFactsTotal(identity, context, status int) {
	factsTotal.WithLabelValues("identity").Set(float64(identity))
	factsTotal.WithLabelValues("context").Set(float64(context))
	factsTotal.WithLabelValues("status").Set(float64(status))
}

// SetTopicsTotal устанавливает общее количество топиков.
func SetTopicsTotal(count int) {
	topicsTotal.Set(float64(count))
}
