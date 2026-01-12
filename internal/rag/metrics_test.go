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
		{"people search", 789, searchTypePeople, 0.008, 25}, // v0.5.1
		{"empty search", 101112, searchTypeTopics, 0.001, 0},
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
	UpdateVectorIndexMetrics(1000, 500, 0)

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

func TestUpdateVectorIndexMetricsWithPeople(t *testing.T) {
	UpdateVectorIndexMetrics(1000, 500, 150)

	// Verify topics gauge
	topicsSize := testutil.ToFloat64(vectorIndexSize.WithLabelValues(searchTypeTopics))
	assert.Equal(t, float64(1000), topicsSize)

	// Verify facts gauge
	factsSize := testutil.ToFloat64(vectorIndexSize.WithLabelValues(searchTypeFacts))
	assert.Equal(t, float64(500), factsSize)

	// Verify people gauge (v0.5.1)
	peopleSize := testutil.ToFloat64(vectorIndexSize.WithLabelValues(searchTypePeople))
	assert.Equal(t, float64(150), peopleSize)

	// Verify memory calculation for people
	peopleMemory := testutil.ToFloat64(vectorIndexMemoryBytes.WithLabelValues(searchTypePeople))
	assert.Equal(t, float64(150*embeddingMemoryBytes), peopleMemory)
}

func TestUpdateVectorIndexMetrics_Zero(t *testing.T) {
	UpdateVectorIndexMetrics(0, 0, 0)

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

// === Reranker Metrics Tests ===

func TestRecordRerankerDuration(t *testing.T) {
	tests := []struct {
		name     string
		userID   int64
		duration float64
	}{
		{"fast rerank", 111, 0.5},
		{"slow rerank", 222, 5.0},
		{"zero duration", 333, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRerankerDuration(tt.userID, tt.duration)
			})
		})
	}
}

func TestRecordRerankerToolCalls(t *testing.T) {
	userID := int64(55555)
	uid := formatUserID(userID)

	// Record some tool calls
	RecordRerankerToolCalls(userID, 3)

	// Verify counter incremented
	count := testutil.ToFloat64(rerankerToolCallsTotal.WithLabelValues(uid))
	assert.GreaterOrEqual(t, count, float64(3))

	// Record more
	RecordRerankerToolCalls(userID, 2)
	count = testutil.ToFloat64(rerankerToolCallsTotal.WithLabelValues(uid))
	assert.GreaterOrEqual(t, count, float64(5))
}

func TestRecordRerankerCandidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		userID int64
		count  int
	}{
		{"50 candidates", 666, 50},
		{"zero candidates", 777, 0},
		{"100 candidates", 888, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRerankerCandidatesInput(tt.userID, tt.count)
			})
		})
	}
}

func TestRecordRerankerCandidatesOutput(t *testing.T) {
	tests := []struct {
		name   string
		userID int64
		count  int
	}{
		{"5 selected", 999, 5},
		{"zero selected", 1000, 0},
		{"max selected", 1001, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRerankerCandidatesOutput(tt.userID, tt.count)
			})
		})
	}
}

func TestRecordRerankerCost(t *testing.T) {
	userID := int64(77777)
	uid := formatUserID(userID)

	// Record cost
	RecordRerankerCost(userID, 0.05)

	cost := testutil.ToFloat64(rerankerCostTotal.WithLabelValues(uid))
	assert.GreaterOrEqual(t, cost, 0.05)

	// Record more cost
	RecordRerankerCost(userID, 0.03)
	cost = testutil.ToFloat64(rerankerCostTotal.WithLabelValues(uid))
	assert.GreaterOrEqual(t, cost, 0.08)
}

func TestRecordRerankerFallback(t *testing.T) {
	tests := []struct {
		name   string
		userID int64
		reason string
	}{
		{"timeout", 1111, "timeout"},
		{"error", 2222, "error"},
		{"max tool calls", 3333, "max_tool_calls"},
		{"invalid json", 4444, "invalid_json"},
		{"requested ids fallback", 5555, "requested_ids"},
		{"vector top fallback", 6666, "vector_top"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordRerankerFallback(tt.userID, tt.reason)
			})

			uid := formatUserID(tt.userID)
			count := testutil.ToFloat64(rerankerFallbackTotal.WithLabelValues(uid, tt.reason))
			assert.GreaterOrEqual(t, count, float64(1))
		})
	}
}

func TestRerankerMetricsRegistration(t *testing.T) {
	// Verify all reranker metrics are registered
	metrics := []string{
		"laplaced_reranker_duration_seconds",
		"laplaced_reranker_tool_calls_total",
		"laplaced_reranker_candidates_input",
		"laplaced_reranker_candidates_output",
		"laplaced_reranker_cost_usd_total",
		"laplaced_reranker_fallback_total",
	}

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
