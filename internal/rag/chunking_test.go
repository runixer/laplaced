package rag

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestProcessChunk_HallucinatedIDs(t *testing.T) {
	// Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-model"
	cfg.Agents.Archivist.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	// Create test translator (use %s placeholders for profile/topics)
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.topic_extraction_prompt: '{{.Profile}}\n{{.RecentTopics}}\n{{.Goal}}\nExtract topics'"), 0644)
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

	userID := int64(123)
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Msg 1", CreatedAt: time.Now()},
		{ID: 102, Role: "user", Content: "Msg 2", CreatedAt: time.Now().Add(time.Minute)},
	}

	// Expectations
	mockStore.On("GetUnprocessedMessages", userID).Return(messages, nil)
	// Mock GetFacts for user profile (now called in extractTopics)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Maybe()
	// Mock GetTopicsExtended for recent topics (now called in extractTopics)
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(storage.TopicResult{}, nil).Maybe()

	// Mock LLM response for topic extraction
	// LLM returns a topic with ID 101 (which doesn't exist in messages)
	extractedTopics := struct {
		Topics []ExtractedTopic `json:"topics"`
	}{
		Topics: []ExtractedTopic{
			{Summary: "Hallucinated Topic", StartMsgID: 101, EndMsgID: 101},
		},
	}
	topicsJSON, _ := json.Marshal(extractedTopics)

	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// Check if it's the topic extraction request
		return req.ResponseFormat != nil
	})).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason,omitempty"`
			Index        int    `json:"index"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: string(topicsJSON)}, FinishReason: "stop"},
		},
	}, nil)

	// AddTopic should NOT be called because no messages match 101
	// We don't mock AddTopic, so if it's called, the test will fail (unexpected call)

	// Expect NO CreateTopic for Noise (stragglers 100 and 102) because we now return error
	// mockStore.On("CreateTopic", ...).Return(...)

	// Expect NO UpdateMessageTopic for stragglers
	// mockStore.On("UpdateMessageTopic", ...).Return(...)

	// Mock GetAllTopics/Facts for ReloadVectors (initial load)
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	// Mock incremental loading methods (called after processChunk)
	mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	// Run
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, nil, translator)
	_, err := svc.ForceProcessUser(context.Background(), userID)

	// Now we expect an error because of incomplete coverage
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic extraction incomplete")

	// Wait a bit for goroutines to avoid race conditions with mock assertions
	time.Sleep(50 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
}

func TestProcessChunk_ValidIDs(t *testing.T) {
	// Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-model"
	cfg.Agents.Archivist.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	// Create test translator (use %s placeholders for profile/topics)
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.topic_extraction_prompt: '{{.Profile}}\n{{.RecentTopics}}\n{{.Goal}}\nExtract topics'"), 0644)
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

	userID := int64(123)
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Msg 1", CreatedAt: time.Now()},
		{ID: 102, Role: "user", Content: "Msg 2", CreatedAt: time.Now().Add(time.Minute)},
	}

	// Expectations
	mockStore.On("GetUnprocessedMessages", userID).Return(messages, nil)
	// Mock GetFacts for user profile (now called in extractTopics)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Maybe()
	// Mock GetTopicsExtended for recent topics (now called in extractTopics)
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(storage.TopicResult{}, nil).Maybe()

	// Mock LLM response for topic extraction
	// LLM returns a topic covering 100-102
	extractedTopics := struct {
		Topics []ExtractedTopic `json:"topics"`
	}{
		Topics: []ExtractedTopic{
			{Summary: "Valid Topic", StartMsgID: 100, EndMsgID: 102},
		},
	}
	topicsJSON, _ := json.Marshal(extractedTopics)

	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		return req.ResponseFormat != nil
	})).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason,omitempty"`
			Index        int    `json:"index"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: string(topicsJSON)}, FinishReason: "stop"},
		},
	}, nil)

	// Mock Embedding
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
		},
	}, nil)

	// AddTopic SHOULD be called with correct IDs
	mockStore.On("AddTopic", mock.MatchedBy(func(topic storage.Topic) bool {
		return topic.StartMsgID == 100 && topic.EndMsgID == 102 && topic.Summary == "Valid Topic"
	})).Return(int64(1), nil)

	// Mock GetAllTopics/Facts for ReloadVectors (initial load - not called since we don't Start)
	// But ForceProcessUser calls processChunk which calls LoadNewVectors in goroutine.
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	// Mock incremental loading methods (called after processChunk)
	mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	// Run
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, nil, translator)
	_, err := svc.ForceProcessUser(context.Background(), userID)
	assert.NoError(t, err)

	// Wait a bit for goroutines
	time.Sleep(100 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
}
