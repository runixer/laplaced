package bot

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestSetActiveSessions(t *testing.T) {
	SetActiveSessions(5)
	assert.Equal(t, float64(5), testutil.ToFloat64(activeSessions))

	SetActiveSessions(0)
	assert.Equal(t, float64(0), testutil.ToFloat64(activeSessions))
}

func TestIncDecActiveSessions(t *testing.T) {
	SetActiveSessions(0)
	IncActiveSessions()
	assert.Equal(t, float64(1), testutil.ToFloat64(activeSessions))

	DecActiveSessions()
	assert.Equal(t, float64(0), testutil.ToFloat64(activeSessions))
}

func TestRecordMessageProcessing(t *testing.T) {
	tests := []struct {
		name     string
		userID   int64
		duration float64
		success  bool
		status   string
	}{
		{"success", 123, 1.5, true, statusSuccess},
		{"error", 456, 0.5, false, statusError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordMessageProcessing(tt.userID, tt.duration, tt.success)
			})
		})
	}
}

func TestRecordContextTokens(t *testing.T) {
	assert.NotPanics(t, func() {
		RecordContextTokens(1000)
		RecordContextTokens(5000)
		RecordContextTokens(10000)
	})
}

func TestRecordContextTokensBySource(t *testing.T) {
	tests := []struct {
		source string
		tokens int
	}{
		{ContextSourceProfile, 500},
		{ContextSourceTopics, 2000},
		{ContextSourceSession, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			assert.NotPanics(t, func() {
				RecordContextTokensBySource(tt.source, tt.tokens)
			})
		})
	}
}

func TestMetricsRegistration(t *testing.T) {
	// Verify all metrics are registered with correct names
	metrics := []string{
		"laplaced_bot_active_sessions",
		"laplaced_bot_message_processing_duration_seconds",
		"laplaced_bot_messages_processed_total",
		"laplaced_bot_context_tokens",
		"laplaced_bot_context_tokens_by_source",
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
		if strings.HasPrefix(name, "laplaced_bot_") {
			assert.NotEmpty(t, mf.GetHelp(), "metric %s should have help text", name)
		}
	}
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "laplaced", metricsNamespace)
	assert.Equal(t, "success", statusSuccess)
	assert.Equal(t, "error", statusError)
	assert.Equal(t, "profile", ContextSourceProfile)
	assert.Equal(t, "topics", ContextSourceTopics)
	assert.Equal(t, "session", ContextSourceSession)
}
