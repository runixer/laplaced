package storage

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestSetStorageSize(t *testing.T) {
	// Create a custom registry to collect metrics
	registry := prometheus.NewRegistry()
	registry.MustRegister(storageSizeBytes)

	t.Run("set storage size", func(t *testing.T) {
		// This should not panic
		SetStorageSize(1024 * 1024) // 1MB

		// Verify the metric was set (we can't easily read it without the default registry,
		// but we can at least verify no panic occurred)
	})

	t.Run("set zero storage size", func(t *testing.T) {
		SetStorageSize(0)
	})

	t.Run("set negative storage size", func(t *testing.T) {
		// Prometheus handles negative values for gauges
		SetStorageSize(-1)
	})
}

func TestSetTableSize(t *testing.T) {
	t.Run("set table size for various tables", func(t *testing.T) {
		tables := []string{"history", "topics", "facts", "people", "artifacts"}

		for _, table := range tables {
			// This should not panic
			SetTableSize(table, 1024)
		}
	})

	t.Run("update same table multiple times", func(t *testing.T) {
		table := "history"
		SetTableSize(table, 1000)
		SetTableSize(table, 2000)
		SetTableSize(table, 1500)
	})

	t.Run("set empty table name", func(t *testing.T) {
		// Prometheus allows empty label values
		SetTableSize("", 100)
	})
}

func TestRecordCleanupDeleted(t *testing.T) {
	t.Run("record deleted rows for various tables", func(t *testing.T) {
		tables := []string{"fact_history", "agent_logs"}

		for _, table := range tables {
			// This should not panic
			RecordCleanupDeleted(table, 10)
		}
	})

	t.Run("record zero deleted", func(t *testing.T) {
		RecordCleanupDeleted("history", 0)
	})

	t.Run("record negative deleted panics (counters only increase)", func(t *testing.T) {
		// Prometheus counters cannot decrease - this will panic
		assert.Panics(t, func() {
			RecordCleanupDeleted("history", -5)
		})
	})

	t.Run("accumulate deletions for same table", func(t *testing.T) {
		table := "fact_history"
		RecordCleanupDeleted(table, 10)
		RecordCleanupDeleted(table, 20)
		RecordCleanupDeleted(table, 5)
		// Total should be 35 (but we can't easily verify without custom registry setup)
	})
}

func TestRecordCleanupDuration(t *testing.T) {
	t.Run("record duration for various tables", func(t *testing.T) {
		tables := []string{"fact_history", "agent_logs"}

		for _, table := range tables {
			// This should not panic
			RecordCleanupDuration(table, 0.5)
		}
	})

	t.Run("record various durations", func(t *testing.T) {
		durations := []float64{0.001, 0.01, 0.1, 1.0, 10.0}

		for _, duration := range durations {
			RecordCleanupDuration("history", duration)
		}
	})

	t.Run("record zero duration", func(t *testing.T) {
		RecordCleanupDuration("history", 0)
	})

	t.Run("record negative duration (histogram allows this)", func(t *testing.T) {
		// Histograms observe negative values
		RecordCleanupDuration("history", -0.1)
	})
}

func TestMetricsConstants(t *testing.T) {
	assert.Equal(t, "laplaced", metricsNamespace)
}
