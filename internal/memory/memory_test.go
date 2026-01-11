package memory

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/archivist"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAddFactWithHistory(t *testing.T) {
	tests := []struct {
		name        string
		debugMode   bool
		addFactErr  error
		wantHistory bool
		wantErr     bool
	}{
		{
			name:        "success with debug mode",
			debugMode:   true,
			addFactErr:  nil,
			wantHistory: true,
			wantErr:     false,
		},
		{
			name:        "success without debug mode",
			debugMode:   false,
			addFactErr:  nil,
			wantHistory: false,
			wantErr:     false,
		},
		{
			name:        "AddFact error",
			debugMode:   true,
			addFactErr:  assert.AnError,
			wantHistory: false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := new(testutil.MockStorage)
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			cfg := &config.Config{}
			cfg.Server.DebugMode = tt.debugMode
			translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

			svc := NewService(logger, cfg, mockStore, mockStore, mockStore, nil, translator)

			fact := storage.Fact{
				UserID:     123,
				Content:    "Test fact",
				Category:   "test",
				Relation:   "related_to",
				Importance: 50,
			}
			topicID := int64(10)

			mockStore.On("AddFact", mock.Anything).Return(int64(1), tt.addFactErr)
			if tt.wantHistory {
				mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
					return h.FactID == 1 && h.Action == "add" && h.TopicID != nil && *h.TopicID == topicID
				})).Return(nil)
			}

			id, err := svc.addFactWithHistory(fact, "test reason", &topicID, "input")

			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, int64(0), id)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, int64(1), id)
			}
			mockStore.AssertExpectations(t)
		})
	}
}

func TestProcessSession_AddFact_RecordsHistory(t *testing.T) {
	// Arrange
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Server.DebugMode = true
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	userID := int64(123)
	messages := []storage.Message{{Role: "user", Content: "My name is John"}}
	refDate := time.Now()

	// Mock GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil)

	// Mock archivist agent that returns added fact
	mockArchivist := new(agenttesting.MockAgent)
	mockArchivist.On("Type").Return(string(agent.TypeArchivist)).Maybe()
	mockArchivist.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &archivist.Result{
			Facts: archivist.FactsResult{
				Added: []archivist.AddedFact{
					{
						Relation:   "name",
						Content:    "Name is John",
						Category:   "bio",
						Type:       "identity",
						Importance: 100,
						Reason:     "User stated name",
					},
				},
			},
		},
	}, nil)
	svc.SetArchivistAgent(mockArchivist)

	// Mock Embedding
	embResp := openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}},
	}
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(&embResp, nil)

	// Mock AddFact
	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil)

	// Mock AddFactHistory - THIS IS WHAT WE WANT TO VERIFY
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 1 && h.Action == "add" && h.Reason == "User stated name" && h.Relation == "name"
	})).Return(nil)

	// Act
	err = svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
	mockArchivist.AssertExpectations(t)
}

func TestProcessSession_UpdateFact_RecordsHistory(t *testing.T) {
	// Arrange
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Server.DebugMode = true
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	userID := int64(123)
	messages := []storage.Message{{Role: "user", Content: "My name is actually Bob"}}
	refDate := time.Now()

	existingFact := storage.Fact{
		ID:       1,
		UserID:   userID,
		Content:  "Name is John",
		Type:     "identity",
		Relation: "name",
	}

	// Mock GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{existingFact}, nil)
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil)

	// Mock archivist agent that returns updated fact
	mockArchivist := new(agenttesting.MockAgent)
	mockArchivist.On("Type").Return(string(agent.TypeArchivist)).Maybe()
	mockArchivist.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &archivist.Result{
			Facts: archivist.FactsResult{
				Updated: []archivist.UpdatedFact{
					{
						ID:         1,
						Content:    "Name is Bob",
						Importance: 100,
						Reason:     "Correction",
					},
				},
			},
		},
	}, nil)
	svc.SetArchivistAgent(mockArchivist)

	// Mock Embedding
	embResp := openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}},
	}
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(&embResp, nil)

	// Mock UpdateFact
	mockStore.On("UpdateFact", mock.Anything).Return(nil)

	// Mock AddFactHistory - THIS IS WHAT WE WANT TO VERIFY
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 1 && h.Action == "update" && h.OldContent == "Name is John" && h.NewContent == "Name is Bob" && h.Reason == "Correction"
	})).Return(nil)

	// Act
	err = svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
	mockArchivist.AssertExpectations(t)
}

func TestProcessSession_RemoveFact_RecordsHistory(t *testing.T) {
	// Arrange
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Server.DebugMode = true
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	userID := int64(123)
	messages := []storage.Message{{Role: "user", Content: "Forget my name"}}
	refDate := time.Now()

	existingFact := storage.Fact{
		ID:       1,
		UserID:   userID,
		Content:  "Name is John",
		Relation: "name",
	}

	// Mock GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{existingFact}, nil)
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil)

	// Mock archivist agent that returns removed fact
	mockArchivist := new(agenttesting.MockAgent)
	mockArchivist.On("Type").Return(string(agent.TypeArchivist)).Maybe()
	mockArchivist.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &archivist.Result{
			Facts: archivist.FactsResult{
				Removed: []archivist.RemovedFact{
					{
						ID:     1,
						Reason: "User request",
					},
				},
			},
		},
	}, nil)
	svc.SetArchivistAgent(mockArchivist)

	// Mock DeleteFact
	mockStore.On("DeleteFact", userID, int64(1)).Return(nil)

	// Mock AddFactHistory - THIS IS WHAT WE WANT TO VERIFY
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 1 && h.Action == "delete" && h.OldContent == "Name is John" && h.Reason == "User request"
	})).Return(nil)

	// Act
	err = svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
	mockArchivist.AssertExpectations(t)
}

func TestSetVectorSearcher(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	svc := NewService(logger, cfg, nil, nil, nil, nil, translator)

	assert.Nil(t, svc.vectorSearcher)

	mockVS := new(testutil.MockVectorSearcher)
	svc.SetVectorSearcher(mockVS)

	assert.NotNil(t, svc.vectorSearcher)
	assert.Equal(t, mockVS, svc.vectorSearcher)
}

func TestGetEmbedding(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Embedding.Model = "test-model"
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, nil, nil, nil, mockOR, translator)

	embResp := openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
	}
	mockOR.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return req.Model == "test-model" && len(req.Input) == 1 && req.Input[0] == "test text"
	})).Return(&embResp, nil)

	emb, _, err := svc.getEmbedding(context.Background(), "test text")

	assert.NoError(t, err)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, emb)
}

func TestGetEmbedding_EmptyResponse(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, nil, nil, nil, mockOR, translator)

	embResp := openrouter.EmbeddingResponse{Data: []openrouter.EmbeddingObject{}}
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(&embResp, nil)

	_, _, err := svc.getEmbedding(context.Background(), "test text")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no embedding returned")
}

func TestGetEmbedding_Error(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, nil, nil, nil, mockOR, translator)

	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(nil, assert.AnError)

	_, _, err := svc.getEmbedding(context.Background(), "test text")

	assert.Error(t, err)
}
