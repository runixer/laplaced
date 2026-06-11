package memory

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/runixer/laplaced/internal/storage"
)

// Prometheus metrics for Memory System
//
// These metrics track:
// - Fact operations (add/update/delete)
// - Deduplication decisions
// - Fact extraction and topic processing time
// - Number of facts and topics

const metricsNamespace = "laplaced"

var (
	// factOperationsTotal counts fact operations.
	// Labels:
	//   - user_id: user identifier
	//   - operation: operation type (add, update, delete)
	factOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "fact_operations_total",
			Help:      "Total number of fact operations",
		},
		[]string{"user_id", "operation"},
	)

	// memoryExtractionDuration measures fact extraction time from messages.
	memoryExtractionDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "extraction_duration_seconds",
			Help:      "Duration of fact extraction from messages in seconds",
			// Buckets for typical LLM extraction times: 1s - 30s
			Buckets: []float64{1, 2, 3, 5, 7, 10, 15, 20, 30},
		},
	)

	// topicProcessingDuration measures topic processing time (creation + fact extraction).
	topicProcessingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "topic_processing_duration_seconds",
			Help:      "Duration of topic processing in seconds",
			// Buckets for full topic processing: 2s - 60s
			Buckets: []float64{2, 5, 10, 15, 20, 30, 45, 60},
		},
	)

	// topicsTotal shows the current number of topics.
	// Labels:
	//   - user_id: user identifier
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

// Operation constants
const (
	OperationAdd    = "add"
	OperationUpdate = "update"
	OperationDelete = "delete"
)

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID storage.ScopeID) string {
	return string(userID)
}

// RecordFactOperation records a fact operation.
func RecordFactOperation(userID storage.ScopeID, operation string) {
	factOperationsTotal.WithLabelValues(formatUserID(userID), operation).Inc()
}

// RecordMemoryExtraction records fact extraction duration.
func RecordMemoryExtraction(durationSeconds float64) {
	memoryExtractionDuration.Observe(durationSeconds)
}

// RecordTopicProcessing records topic processing duration.
func RecordTopicProcessing(durationSeconds float64) {
	topicProcessingDuration.Observe(durationSeconds)
}

// SetTopicsTotal sets the number of topics for a user.
func SetTopicsTotal(userID storage.ScopeID, count int) {
	topicsTotal.WithLabelValues(formatUserID(userID)).Set(float64(count))
}
