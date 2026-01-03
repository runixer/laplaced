package rag

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestRecordEmbeddingRequest_Success(t *testing.T) {
	userID := int64(12345)
	model := "test-model-success"
	uid := formatUserID(userID)

	cost := 0.001
	RecordEmbeddingRequest(userID, model, searchTypeTopics, 0.5, true, 100, &cost)

	// Verify counter was incremented
	requests := testutil.ToFloat64(embeddingRequestsTotal.WithLabelValues(uid, model, searchTypeTopics, statusSuccess))
	assert.GreaterOrEqual(t, requests, float64(1))

	// Verify tokens were recorded
	tokens := testutil.ToFloat64(embeddingTokensTotal.WithLabelValues(uid, model))
	assert.GreaterOrEqual(t, tokens, float64(100))

	// Verify cost was recorded
	costTotal := testutil.ToFloat64(embeddingCostTotal.WithLabelValues(uid, model))
	assert.GreaterOrEqual(t, costTotal, 0.001)
}

func TestRecordEmbeddingRequest_Error(t *testing.T) {
	userID := int64(22222)
	model := "test-model-error"
	uid := formatUserID(userID)

	RecordEmbeddingRequest(userID, model, searchTypeFacts, 0.1, false, 0, nil)

	// Verify error counter was incremented
	requests := testutil.ToFloat64(embeddingRequestsTotal.WithLabelValues(uid, model, searchTypeFacts, statusError))
	assert.Equal(t, float64(1), requests)

	// Verify tokens were NOT recorded on error
	tokens := testutil.ToFloat64(embeddingTokensTotal.WithLabelValues(uid, model))
	assert.Equal(t, float64(0), tokens)
}

func TestRecordEmbeddingRequest_NilCost(t *testing.T) {
	userID := int64(33333)
	model := "test-model-nil-cost"
	uid := formatUserID(userID)

	RecordEmbeddingRequest(userID, model, searchTypeTopics, 0.2, true, 50, nil)

	// Should not panic with nil cost
	tokens := testutil.ToFloat64(embeddingTokensTotal.WithLabelValues(uid, model))
	assert.Equal(t, float64(50), tokens)

	// Cost should be zero (not incremented)
	costTotal := testutil.ToFloat64(embeddingCostTotal.WithLabelValues(uid, model))
	assert.Equal(t, float64(0), costTotal)
}

func TestRecordEmbeddingRequest_ZeroCost(t *testing.T) {
	userID := int64(44444)
	model := "test-model-zero-cost"
	uid := formatUserID(userID)

	zeroCost := 0.0
	RecordEmbeddingRequest(userID, model, searchTypeFacts, 0.2, true, 50, &zeroCost)

	// Zero cost should not be recorded
	costTotal := testutil.ToFloat64(embeddingCostTotal.WithLabelValues(uid, model))
	assert.Equal(t, float64(0), costTotal)
}

func TestRecordVectorSearch(t *testing.T) {
	tests := []struct {
		name           string
		userID         int64
		searchType     string
		duration       float64
		vectorsScanned int
	}{
		{"topics search", 123, searchTypeTopics, 0.01, 100},
		{"facts search", 456, searchTypeFacts, 0.005, 50},
		{"empty search", 789, searchTypeTopics, 0.001, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			assert.NotPanics(t, func() {
				RecordVectorSearch(tt.userID, tt.searchType, tt.duration, tt.vectorsScanned)
			})
		})
	}
}

func TestRecordRAGRetrieval(t *testing.T) {
	tests := []struct {
		name       string
		userID     int64
		hasContext bool
		expected   string
	}{
		{"hit", 123, true, resultHit},
		{"miss", 456, false, resultMiss},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRAGRetrieval(tt.userID, tt.hasContext)
			})
		})
	}
}

func TestRecordRAGCandidates(t *testing.T) {
	tests := []struct {
		name       string
		userID     int64
		searchType string
		count      int
	}{
		{"topics candidates", 123, searchTypeTopics, 25},
		{"facts candidates", 456, searchTypeFacts, 10},
		{"zero candidates", 789, searchTypeTopics, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRAGCandidates(tt.userID, tt.searchType, tt.count)
			})
		})
	}
}

func TestRecordRAGLatency(t *testing.T) {
	tests := []struct {
		name     string
		userID   int64
		source   string
		duration float64
	}{
		{"fast auto retrieval", 123, "auto", 0.05},
		{"slow tool retrieval", 456, "tool", 2.5},
		{"empty source defaults to auto", 789, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRAGLatency(tt.userID, tt.source, tt.duration)
			})
		})
	}
}

func TestUpdateVectorIndexMetrics(t *testing.T) {
	UpdateVectorIndexMetrics(1000, 500)

	// Verify topics gauge
	topicsSize := testutil.ToFloat64(vectorIndexSize.WithLabelValues(searchTypeTopics))
	assert.Equal(t, float64(1000), topicsSize)

	// Verify facts gauge
	factsSize := testutil.ToFloat64(vectorIndexSize.WithLabelValues(searchTypeFacts))
	assert.Equal(t, float64(500), factsSize)

	// Verify memory calculation (size × 3072 × 4 bytes)
	topicsMemory := testutil.ToFloat64(vectorIndexMemoryBytes.WithLabelValues(searchTypeTopics))
	assert.Equal(t, float64(1000*embeddingMemoryBytes), topicsMemory)

	factsMemory := testutil.ToFloat64(vectorIndexMemoryBytes.WithLabelValues(searchTypeFacts))
	assert.Equal(t, float64(500*embeddingMemoryBytes), factsMemory)
}

func TestUpdateVectorIndexMetrics_Zero(t *testing.T) {
	UpdateVectorIndexMetrics(0, 0)

	topicsSize := testutil.ToFloat64(vectorIndexSize.WithLabelValues(searchTypeTopics))
	assert.Equal(t, float64(0), topicsSize)

	topicsMemory := testutil.ToFloat64(vectorIndexMemoryBytes.WithLabelValues(searchTypeTopics))
	assert.Equal(t, float64(0), topicsMemory)
}

func TestMetricsRegistration(t *testing.T) {
	// Verify all metrics are registered with correct names
	metrics := []string{
		"laplaced_embedding_request_duration_seconds",
		"laplaced_embedding_requests_total",
		"laplaced_embedding_tokens_total",
		"laplaced_embedding_cost_usd_total",
		"laplaced_vector_search_duration_seconds",
		"laplaced_vector_search_vectors_scanned",
		"laplaced_vector_index_size",
		"laplaced_vector_index_memory_bytes",
		"laplaced_rag_retrieval_total",
		"laplaced_rag_candidates",
	}

	// Collect all metric names from default registry
	mfs, err := prometheus.DefaultGatherer.Gather()
	assert.NoError(t, err)

	registeredNames := make(map[string]bool)
	for _, mf := range mfs {
		registeredNames[mf.GetName()] = true
	}

	for _, name := range metrics {
		assert.True(t, registeredNames[name], "metric %s should be registered", name)
	}
}

func TestMetricsHelp(t *testing.T) {
	// Verify metrics have proper help text
	mfs, err := prometheus.DefaultGatherer.Gather()
	assert.NoError(t, err)

	for _, mf := range mfs {
		name := mf.GetName()
		if strings.HasPrefix(name, "laplaced_embedding_") ||
			strings.HasPrefix(name, "laplaced_vector_") ||
			strings.HasPrefix(name, "laplaced_rag_") {
			assert.NotEmpty(t, mf.GetHelp(), "metric %s should have help text", name)
		}
	}
}

func TestConstants(t *testing.T) {
	// Verify constants are set correctly
	assert.Equal(t, "laplaced", namespace)
	assert.Equal(t, "success", statusSuccess)
	assert.Equal(t, "error", statusError)
	assert.Equal(t, "topics", searchTypeTopics)
	assert.Equal(t, "facts", searchTypeFacts)
	assert.Equal(t, "hit", resultHit)
	assert.Equal(t, "miss", resultMiss)
	assert.Equal(t, 3072*4, embeddingMemoryBytes)
}

func TestFormatUserID(t *testing.T) {
	tests := []struct {
		userID   int64
		expected string
	}{
		{0, "0"},
		{123, "123"},
		{-1, "-1"},
		{9223372036854775807, "9223372036854775807"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatUserID(tt.userID)
			assert.Equal(t, tt.expected, result)
		})
	}
}
