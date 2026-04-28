package reranker

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
	assert.Equal(t, int64(0), attrs["reranker.tool_calls"].AsInt64(),
		"no tool calls in early-fallback path")
	assert.Equal(t, int64(0), attrs["reranker.llm_calls"].AsInt64())
	assert.Equal(t, float64(0), attrs["reranker.cost_usd"].AsFloat64())
	assert.Equal(t, int64(0), attrs["reranker.candidates_in.topics"].AsInt64())
	assert.Equal(t, int64(0), attrs["reranker.model_raw_count.topics"].AsInt64())
	assert.Equal(t, int64(0), attrs["reranker.model_kept.topics"].AsInt64())
	// fallback_reason is empty here — the early-fallback path bails BEFORE
	// the trace state is built, so we capture "" and keep status Unset.
	assert.Equal(t, "", attrs["reranker.fallback_reason"].AsString())
	assert.Equal(t, sdkcodes.Unset, span.Status.Code,
		"reranker must never set Error status, even on fallback")
}

// TestReranker_RecordsModelRawCounts_EmptyResponse verifies that when the
// model finalises with empty arrays after inspecting candidate content,
// the span captures model_raw_count.* = 0 and model_kept.* = 0 alongside
// the tool_call event for the inspection round-trip. This is the signal
// downstream consumers need to distinguish "model explicitly refused"
// from "model returned IDs but all were hallucinated" — the current code
// labels both as all_hallucinated, which is the bug to fix on main.
func TestReranker_RecordsModelRawCounts_EmptyResponse(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConfig()
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	r := New(mockClient, cfg, logger, translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	// Round 1: model asks for topic content via tool call.
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	// Round 2: model inspects content and explicitly returns empty arrays.
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [], "people_ids": [], "artifact_ids": []}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamPersonCandidates:    []PersonCandidate{},
			ParamArtifactCandidates:  []ArtifactCandidate{},
			ParamContextualizedQuery: "ctx",
			ParamOriginalQuery:       "orig",
			ParamCurrentMessages:     "msgs",
			"user_id":                int64(8888),
		},
	}

	_, err := r.Execute(context.Background(), req)
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "reranker.Execute", span.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}

	assert.Equal(t, int64(1), attrs["reranker.tool_calls"].AsInt64(),
		"one tool round-trip happened")
	assert.Equal(t, int64(2), attrs["reranker.llm_calls"].AsInt64(),
		"two LLM calls: tool round-trip + final response")
	assert.Equal(t, int64(0), attrs["reranker.model_raw_count.topics"].AsInt64(),
		"model returned empty topic_ids — this is the signal to distinguish refusal from hallucination")
	assert.Equal(t, int64(0), attrs["reranker.model_raw_count.people"].AsInt64())
	assert.Equal(t, int64(0), attrs["reranker.model_raw_count.artifacts"].AsInt64())
	assert.Equal(t, int64(0), attrs["reranker.model_kept.topics"].AsInt64())
	assert.Equal(t, int64(1), attrs["reranker.candidates_in.topics"].AsInt64())
	// Current behaviour: empty response triggers all_hallucinated fallback. The
	// behavioural fix (treating raw == 0 as no_relevant_results) lands on main.
	assert.Equal(t, "all_hallucinated", attrs["reranker.fallback_reason"].AsString())

	var foundToolCallEvent bool
	for _, ev := range span.Events {
		if ev.Name == "reranker.tool_call" {
			foundToolCallEvent = true
			eventAttrs := map[attribute.Key]attribute.Value{}
			for _, kv := range ev.Attributes {
				eventAttrs[kv.Key] = kv.Value
			}
			assert.Equal(t, int64(1), eventAttrs["iteration"].AsInt64())
			assert.Equal(t, int64(1), eventAttrs["requested_count"].AsInt64())
			assert.Equal(t, int64(1), eventAttrs["valid_count"].AsInt64())
			assert.Equal(t, []int64{1}, eventAttrs["requested_ids"].AsInt64Slice())
		}
	}
	assert.True(t, foundToolCallEvent, "reranker.tool_call event must be emitted per iteration")

	mockClient.AssertExpectations(t)
}
