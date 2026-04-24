package reranker

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/testutil"
)

// TestReranker_RecordsSpan_EarlyFallback covers the cheapest happy path:
// reranker is disabled (or given zero candidates). The span must still be
// emitted with the agreed name and must NOT carry Error status — reranker's
// contract is "always returns a result, never errors".
func TestReranker_RecordsSpan_EarlyFallback(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Reranker: config.RerankerAgentConfig{
				Enabled: false,
				Topics:  config.RerankerTypeConfig{Max: 5, CandidatesLimit: 50},
				People:  config.RerankerTypeConfig{Max: 5, CandidatesLimit: 20},
				Artifacts: config.RerankerTypeConfig{
					Max:             5,
					CandidatesLimit: 20,
					MaxContextBytes: 1024,
				},
				MaxToolCalls: 3,
			},
			Default: config.AgentConfig{Model: "test-model"},
		},
	}
	mockClient := new(testutil.MockOpenRouterClient)
	mockStore := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	r := New(mockClient, cfg, logger, translator, mockStore, nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          []Candidate{},
			ParamPersonCandidates:    []PersonCandidate{},
			ParamArtifactCandidates:  []ArtifactCandidate{},
			ParamContextualizedQuery: "ctx",
			ParamOriginalQuery:       "orig",
			ParamCurrentMessages:     "msgs",
			"user_id":                int64(7777),
		},
	}

	_, err := r.Execute(context.Background(), req)
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1, "exactly one reranker span")
	span := spans[0]
	assert.Equal(t, "reranker.Execute", span.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, int64(7777), attrs["user.id"].AsInt64())
	assert.Equal(t, int64(0), attrs["reranker.iterations"].AsInt64(),
		"no iterations in early-fallback path")
	// fallback_reason is empty here — the early-fallback path bails BEFORE
	// the trace state is built, so we capture "" and keep status Unset.
	assert.Equal(t, "", attrs["reranker.fallback_reason"].AsString())
	assert.Equal(t, sdkcodes.Unset, span.Status.Code,
		"reranker must never set Error status, even on fallback")
}
