package rag

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func buildRetrieveServiceForTracing(t *testing.T, mockStore *testutil.MockStorage, mockClient *testutil.MockOpenRouterClient) *Service {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &config.Config{
		RAG: config.RAGConfig{
			Enabled:              true,
			RetrievedTopicsCount: 10,
		},
		Embedding: config.EmbeddingConfig{Model: "test-model"},
	}
	translator := testutil.TestTranslator(t)

	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetTopicsByIDs", mock.Anything, mock.Anything).Return([]storage.Topic{}, nil).Maybe()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	require.NoError(t, err)
	_ = svc.Start(context.Background())
	return svc
}

// TestRetrieve_RecordsSpan covers the structural side of the span: name +
// the always-on attributes that every TraceQL query relies on. Matches the
// pattern from process_group_tracing_test.go so future span sites can
// mirror it mechanically.
func TestRetrieve_RecordsSpan(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
		openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
		}, nil,
	).Maybe()

	svc := buildRetrieveServiceForTracing(t, mockStore, mockClient)
	_, _, err := svc.Retrieve(context.Background(), 123, "hello", &RetrievalOptions{Source: "auto"})
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "rag.Retrieve", span.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, int64(123), attrs["user.id"].AsInt64())
	assert.Equal(t, "auto", attrs["rag.source"].AsString())
	assert.False(t, attrs["rag.used_reranker"].AsBool(), "empty store means no reranker path taken")
	assert.Equal(t, sdkcodes.Unset, span.Status.Code)
}

func TestRetrieve_ContentEventsGatedByToggle(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
		openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
		}, nil,
	).Maybe()
	svc := buildRetrieveServiceForTracing(t, mockStore, mockClient)

	t.Run("disabled: no events", func(t *testing.T) {
		getSpans := testutil.WithTracingCapture(t)
		prev := obs.ContentEnabled()
		t.Cleanup(func() { obs.SetContentEnabled(prev) })
		obs.SetContentEnabled(false)

		_, _, err := svc.Retrieve(context.Background(), 123, "secret question", &RetrievalOptions{Source: "auto"})
		require.NoError(t, err)
		spans := getSpans()
		require.Len(t, spans, 1)
		assert.Empty(t, spans[0].Events, "no content events when toggle off")
	})

	t.Run("enabled: raw_query attached", func(t *testing.T) {
		getSpans := testutil.WithTracingCapture(t)
		prev := obs.ContentEnabled()
		t.Cleanup(func() { obs.SetContentEnabled(prev) })
		obs.SetContentEnabled(true)

		_, _, err := svc.Retrieve(context.Background(), 123, "secret question", &RetrievalOptions{Source: "tool", SkipEnrichment: true})
		require.NoError(t, err)
		spans := getSpans()
		require.Len(t, spans, 1)

		var sawRaw bool
		for _, ev := range spans[0].Events {
			if ev.Name == "rag.raw_query" {
				sawRaw = true
				for _, kv := range ev.Attributes {
					if kv.Key == "body" {
						assert.Equal(t, "secret question", kv.Value.AsString())
					}
				}
			}
		}
		assert.True(t, sawRaw, "raw_query event must be present when content toggle is on")
	})
}

func TestRetrieve_EmbeddingError_SetsErrorStatus(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
		openrouter.EmbeddingResponse{}, errors.New("embeddings boom"),
	)

	svc := buildRetrieveServiceForTracing(t, mockStore, mockClient)
	_, _, err := svc.Retrieve(context.Background(), 123, "anything", &RetrievalOptions{Source: "auto"})
	require.Error(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, sdkcodes.Error, spans[0].Status.Code, "embedding failure must surface as span Error")
	assert.Contains(t, spans[0].Status.Description, "embeddings boom")
}
