package rag

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/splitter"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// TestProcessChunk_DoNotStoreRedaction proves that do-not-store content never
// reaches the splitter or the topic embeddings, while chunk structure (IDs,
// coverage) is preserved, and that archiving such a chunk auto-disables the
// scope's privacy mode.
func TestProcessChunk_DoNotStoreRedaction(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
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
	const secret = "my deepest secret confession"
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Msg 1", CreatedAt: time.Now()},
		{ID: 101, Role: "user", Content: secret, DoNotStore: true, CreatedAt: time.Now().Add(time.Minute)},
		{ID: 102, Role: "assistant", Content: "Msg 3", CreatedAt: time.Now().Add(2 * time.Minute)},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return(messages, nil)

	var splitterSaw []storage.Message
	mockSplitter := new(agenttesting.MockAgent)
	mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
	mockSplitter.On("Execute", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		req := args.Get(1).(*agent.Request)
		splitterSaw = req.Params[splitter.ParamMessages].([]storage.Message)
	}).Return(&agent.Response{
		Structured: &splitter.Result{
			Topics: []splitter.ExtractedTopic{
				{Summary: "Topic", StartMsgID: 100, EndMsgID: 102},
			},
		},
	}, nil)

	var embeddingInputs []string
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		req := args.Get(1).(llm.EmbeddingRequest)
		embeddingInputs = append(embeddingInputs, req.Input...)
	}).Return(llm.EmbeddingResponse{
		Data: []llm.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0}},
	}, nil)

	mockStore.On("AddTopic", mock.Anything).Return(int64(1), nil)
	mockStore.On("SetPrivacyMode", userID, false).Return(nil).Once()
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
		WithUserRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetSplitterAgent(mockSplitter)

	_, err = svc.ForceProcessUser(context.Background(), userID)
	assert.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// Splitter got the full chunk structurally, with the secret redacted.
	if assert.Len(t, splitterSaw, 3) {
		assert.Equal(t, int64(101), splitterSaw[1].ID)
		assert.Equal(t, doNotStoreRedaction, splitterSaw[1].Content)
	}
	// No embedding input carries the secret.
	joined := strings.Join(embeddingInputs, "\n")
	assert.NotEmpty(t, embeddingInputs)
	assert.NotContains(t, joined, secret)
	assert.Contains(t, joined, doNotStoreRedaction)

	mockStore.AssertExpectations(t)
	mockSplitter.AssertExpectations(t)
}
