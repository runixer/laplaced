package storage

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const metricsNamespace = "laplaced"

var (
	// storageSizeBytes shows the total size of the database file in bytes.
	storageSizeBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "storage",
			Name:      "size_bytes",
			Help:      "Total size of the database file in bytes",
		},
	)

	// storageTableBytes shows the size of each table in bytes.
	storageTableBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "storage",
			Name:      "table_bytes",
			Help:      "Size of each database table in bytes",
		},
		[]string{"table"},
	)

	// storageCleanupDeletedTotal counts the number of deleted rows during cleanup.
	storageCleanupDeletedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "storage",
			Name:      "cleanup_deleted_total",
			Help:      "Total number of rows deleted during cleanup",
		},
		[]string{"table"},
	)

	// storageCleanupDuration measures the duration of cleanup operations.
	storageCleanupDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "storage",
			Name:      "cleanup_duration_seconds",
			Help:      "Duration of cleanup operations in seconds",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"table"},
	)
)

// SetStorageSize updates the storage size metric.
func SetStorageSize(bytes int64) {
	storageSizeBytes.Set(float64(bytes))
}

// SetTableSize updates the table size metric.
func SetTableSize(table string, bytes int64) {
	storageTableBytes.WithLabelValues(table).Set(float64(bytes))
}

// RecordCleanupDeleted records the number of deleted rows during cleanup.
func RecordCleanupDeleted(table string, count int64) {
	storageCleanupDeletedTotal.WithLabelValues(table).Add(float64(count))
}

// RecordCleanupDuration records the duration of a cleanup operation.
func RecordCleanupDuration(table string, seconds float64) {
	storageCleanupDuration.WithLabelValues(table).Observe(seconds)
}
