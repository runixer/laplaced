package memory

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/runixer/laplaced/internal/storage"
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

	// topicsTotal показывает текущее количество топиков.
	// Labels:
	//   - user_id: идентификатор пользователя
	topicsTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "topics_total",
			Help:      "Current total number of topics per user",
		},
		[]string{"user_id"},
	)
)

// Константы для операций
const (
	OperationAdd    = "add"
	OperationUpdate = "update"
	OperationDelete = "delete"
)

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID storage.ScopeID) string {
	return string(userID)
}

// RecordFactOperation записывает операцию с фактом.
func RecordFactOperation(userID storage.ScopeID, operation string) {
	factOperationsTotal.WithLabelValues(formatUserID(userID), operation).Inc()
}

// RecordMemoryExtraction записывает время извлечения фактов.
func RecordMemoryExtraction(durationSeconds float64) {
	memoryExtractionDuration.Observe(durationSeconds)
}

// RecordTopicProcessing записывает время обработки топика.
func RecordTopicProcessing(durationSeconds float64) {
	topicProcessingDuration.Observe(durationSeconds)
}

// SetTopicsTotal устанавливает количество топиков для пользователя.
func SetTopicsTotal(userID storage.ScopeID, count int) {
	topicsTotal.WithLabelValues(formatUserID(userID)).Set(float64(count))
}
