package rag

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/splitter"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func overlapChunk(ids ...int64) []storage.Message {
	msgs := make([]storage.Message, len(ids))
	for i, id := range ids {
		msgs[i] = storage.Message{ID: id, Role: "user", Content: "m"}
	}
	return msgs
}

type span struct{ start, end int64 }

func TestResolveOverlaps(t *testing.T) {
	// IDs are global, so a user's chunk has gaps where other users' messages sit.
	chunk := overlapChunk(10, 11, 12, 15, 16, 20, 21, 22, 30)

	tests := []struct {
		name        string
		topics      []span
		want        []span
		wantDropped int
		wantTrimmed int
	}{
		{
			name:   "disjoint topics stay as they are",
			topics: []span{{10, 12}, {15, 16}, {20, 30}},
			want:   []span{{10, 12}, {15, 16}, {20, 30}},
		},
		{
			name:        "whole-chunk topic swallows the finer ones",
			topics:      []span{{10, 12}, {15, 16}, {20, 22}, {10, 30}},
			want:        []span{{10, 30}},
			wantDropped: 3,
		},
		{
			name:        "partial overlap is trimmed off the later topic",
			topics:      []span{{10, 16}, {15, 22}},
			want:        []span{{10, 16}, {20, 22}},
			wantTrimmed: 1,
		},
		{
			name:        "trim re-fits the start to a real message id",
			topics:      []span{{10, 12}, {11, 30}},
			want:        []span{{10, 12}, {15, 30}},
			wantTrimmed: 1,
		},
		{
			name:        "identical ranges keep one topic",
			topics:      []span{{15, 16}, {15, 16}},
			want:        []span{{15, 16}},
			wantDropped: 1,
		},
		{
			name:        "out-of-order input is sorted by start",
			topics:      []span{{20, 30}, {10, 12}, {11, 16}},
			want:        []span{{10, 12}, {15, 16}, {20, 30}},
			wantTrimmed: 1,
		},
		{
			name:   "empty input",
			topics: nil,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make([]ExtractedTopic, len(tt.topics))
			for i, s := range tt.topics {
				in[i] = ExtractedTopic{Summary: "t", StartMsgID: s.start, EndMsgID: s.end}
			}
			got, dropped, trimmed := resolveOverlaps(chunk, in)

			var gotSpans []span
			for _, tp := range got {
				gotSpans = append(gotSpans, span{tp.StartMsgID, tp.EndMsgID})
			}
			assert.Equal(t, tt.want, gotSpans)
			assert.Equal(t, tt.wantDropped, dropped, "dropped")
			assert.Equal(t, tt.wantTrimmed, trimmed, "trimmed")
		})
	}
}

// TestProcessChunk_OverlappingTopicsSavedOnce reproduces the production shape
// (a whole-chunk topic plus finer topics inside it) and checks that only
// disjoint topics reach storage, so none ends up without messages.
func TestProcessChunk_OverlappingTopicsSavedOnce(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-model"
	cfg.Agents.Archivist.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockLLMClient)
	translator := testutil.TestTranslator(t)
	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)

	userID := storage.ScopeID("123")
	now := time.Now()
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Msg 1", CreatedAt: now},
		{ID: 101, Role: "assistant", Content: "Msg 2", CreatedAt: now.Add(time.Minute)},
		{ID: 102, Role: "user", Content: "Msg 3", CreatedAt: now.Add(2 * time.Minute)},
		{ID: 103, Role: "assistant", Content: "Msg 4", CreatedAt: now.Add(3 * time.Minute)},
	}
	mockStore.On("GetUnprocessedMessages", userID).Return(messages, nil)

	mockSplitter := new(agenttesting.MockAgent)
	mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
	mockSplitter.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &splitter.Result{
			Topics: []splitter.ExtractedTopic{
				{Summary: "First part", StartMsgID: 100, EndMsgID: 101},
				{Summary: "Second part", StartMsgID: 102, EndMsgID: 103},
				{Summary: "Whole conversation", StartMsgID: 100, EndMsgID: 103},
			},
		},
	}, nil)

	mockClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req llm.EmbeddingRequest) bool {
		return len(req.Input) == 1
	})).Return(llm.EmbeddingResponse{
		Data: []llm.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0}},
	}, nil).Once()
	mockStore.On("AddTopic", mock.MatchedBy(func(topic storage.Topic) bool {
		return topic.StartMsgID == 100 && topic.EndMsgID == 103 && topic.Summary == "Whole conversation"
	})).Return(int64(1), nil).Once()

	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithLLMClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	require.NoError(t, err)
	svc.SetSplitterAgent(mockSplitter)

	_, err = svc.ForceProcessUser(context.Background(), userID)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
}
