package memory

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/archivist"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
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
			translator := testutil.TestTranslator(t)

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
	translator := testutil.TestTranslator(t)

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
	err := svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

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
	translator := testutil.TestTranslator(t)

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
	err := svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

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
	translator := testutil.TestTranslator(t)

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
	err := svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
	mockArchivist.AssertExpectations(t)
}

func TestSetVectorSearcher(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator := testutil.TestTranslator(t)
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
	translator := testutil.TestTranslator(t)

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
	translator := testutil.TestTranslator(t)

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
	translator := testutil.TestTranslator(t)

	svc := NewService(logger, cfg, nil, nil, nil, mockOR, translator)

	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(nil, assert.AnError)

	_, _, err := svc.getEmbedding(context.Background(), "test text")

	assert.Error(t, err)
}

// TestConvertArchivistResult tests the convertArchivistResult function.
func TestConvertArchivistResult(t *testing.T) {
	tests := []struct {
		name    string
		input   *archivist.Result
		wantAdd int
		wantUpd int
		wantRem int
	}{
		{
			name: "standard conversion with all fields",
			input: &archivist.Result{
				Facts: archivist.FactsResult{
					Added: []archivist.AddedFact{
						{Relation: "name", Content: "Name is John", Category: "bio", Type: "identity", Importance: 100, Reason: "stated"},
					},
					Updated: []archivist.UpdatedFact{
						{FactID: "1", Content: "Updated content", Importance: 50, Reason: "correction"},
					},
					Removed: []archivist.RemovedFact{
						{FactID: "2", Reason: "outdated"},
					},
				},
			},
			wantAdd: 1,
			wantUpd: 1,
			wantRem: 1,
		},
		{
			name: "empty result",
			input: &archivist.Result{
				Facts: archivist.FactsResult{},
			},
			wantAdd: 0,
			wantUpd: 0,
			wantRem: 0,
		},
		{
			name:    "nil slices",
			input:   &archivist.Result{},
			wantAdd: 0,
			wantUpd: 0,
			wantRem: 0,
		},
		{
			name: "invalid fact IDs are skipped",
			input: &archivist.Result{
				Facts: archivist.FactsResult{
					Updated: []archivist.UpdatedFact{
						{FactID: "", Content: "Invalid", Importance: 50, Reason: "no ID"},
						{FactID: "abc", Content: "Invalid ID", Importance: 50, Reason: "bad ID"},
						{FactID: "5", Content: "Valid", Importance: 50, Reason: "good ID"},
					},
					Removed: []archivist.RemovedFact{
						{FactID: "", Reason: "no ID"},
						{FactID: "10", Reason: "valid"},
					},
				},
			},
			wantAdd: 0,
			wantUpd: 1,
			wantRem: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertArchivistResult(tt.input)

			assert.Equal(t, tt.wantAdd, len(got.Added))
			assert.Equal(t, tt.wantUpd, len(got.Updated))
			assert.Equal(t, tt.wantRem, len(got.Removed))

			// Verify field mappings for added facts
			for i, add := range got.Added {
				if i < len(tt.input.Facts.Added) {
					src := tt.input.Facts.Added[i]
					assert.Equal(t, src.Relation, add.Relation)
					assert.Equal(t, src.Content, add.Content)
					assert.Equal(t, src.Category, add.Category)
					assert.Equal(t, src.Type, add.Type)
					assert.Equal(t, src.Importance, add.Importance)
					assert.Equal(t, src.Reason, add.Reason)
				}
			}
		})
	}
}

// TestApplyUpdateWithStats tests the applyUpdateWithStats method.
func TestApplyUpdateWithStats(t *testing.T) {
	tests := []struct {
		name          string
		update        *MemoryUpdate
		existingFacts []storage.Fact
		mockSetup     func(*testutil.MockStorage, *testutil.MockOpenRouterClient)
		wantStats     FactStats
		wantErr       bool
	}{
		{
			name: "add facts successfully",
			update: &MemoryUpdate{
				Added: []struct {
					Relation   string `json:"relation"`
					Content    string `json:"content"`
					Category   string `json:"category"`
					Type       string `json:"type"`
					Importance int    `json:"importance"`
					Reason     string `json:"reason"`
				}{
					{Relation: "name", Content: "Name is Alice", Category: "bio", Type: "identity", Importance: 100, Reason: "stated"},
					{Relation: "hobby", Content: "Likes programming", Category: "interests", Type: "preference", Importance: 50, Reason: "mentioned"},
				},
			},
			existingFacts: []storage.Fact{},
			mockSetup: func(store *testutil.MockStorage, orClient *testutil.MockOpenRouterClient) {
				orClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Times(2)
				store.On("AddFact", mock.Anything).Return(int64(1), nil).Times(2)
			},
			wantStats: FactStats{Created: 2},
		},
		{
			name: "update facts successfully",
			update: &MemoryUpdate{
				Updated: []struct {
					ID         int64  `json:"id"`
					Content    string `json:"content"`
					Type       string `json:"type,omitempty"`
					Importance int    `json:"importance"`
					Reason     string `json:"reason"`
				}{
					{ID: 1, Content: "Updated content", Importance: 75, Reason: "correction"},
				},
			},
			existingFacts: []storage.Fact{
				{ID: 1, UserID: 123, Content: "Old content", Type: "identity", Importance: 50, Embedding: []float32{0.1, 0.2}},
			},
			mockSetup: func(store *testutil.MockStorage, orClient *testutil.MockOpenRouterClient) {
				orClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()
				store.On("UpdateFact", mock.Anything).Return(nil).Once()
			},
			wantStats: FactStats{Updated: 1},
		},
		{
			name: "remove facts successfully",
			update: &MemoryUpdate{
				Removed: []struct {
					ID     int64  `json:"id"`
					Reason string `json:"reason"`
				}{
					{ID: 1, Reason: "outdated"},
				},
			},
			existingFacts: []storage.Fact{
				{ID: 1, UserID: 123, Content: "Old fact", Relation: "name", Category: "bio", Importance: 50},
			},
			mockSetup: func(store *testutil.MockStorage, orClient *testutil.MockOpenRouterClient) {
				store.On("DeleteFact", int64(123), int64(1)).Return(nil).Once()
			},
			wantStats: FactStats{Deleted: 1},
		},
		{
			name: "mixed operations",
			update: &MemoryUpdate{
				Added: []struct {
					Relation   string `json:"relation"`
					Content    string `json:"content"`
					Category   string `json:"category"`
					Type       string `json:"type"`
					Importance int    `json:"importance"`
					Reason     string `json:"reason"`
				}{
					{Relation: "hobby", Content: "New hobby", Category: "interests", Type: "preference", Importance: 50, Reason: "mentioned"},
				},
				Updated: []struct {
					ID         int64  `json:"id"`
					Content    string `json:"content"`
					Type       string `json:"type,omitempty"`
					Importance int    `json:"importance"`
					Reason     string `json:"reason"`
				}{
					{ID: 1, Content: "Same content", Importance: 60, Reason: "priority change"},
				},
				Removed: []struct {
					ID     int64  `json:"id"`
					Reason string `json:"reason"`
				}{
					{ID: 2, Reason: "incorrect"},
				},
			},
			existingFacts: []storage.Fact{
				{ID: 1, UserID: 123, Content: "Same content", Type: "identity", Importance: 50, Embedding: []float32{0.1}},
				{ID: 2, UserID: 123, Content: "To remove", Relation: "name", Category: "bio", Importance: 50},
			},
			mockSetup: func(store *testutil.MockStorage, orClient *testutil.MockOpenRouterClient) {
				orClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()
				store.On("AddFact", mock.Anything).Return(int64(3), nil).Once()
				store.On("UpdateFact", mock.Anything).Return(nil).Once()
				store.On("DeleteFact", int64(123), int64(2)).Return(nil).Once()
			},
			wantStats: FactStats{Created: 1, Updated: 1, Deleted: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := new(testutil.MockStorage)
			mockOR := new(testutil.MockOpenRouterClient)
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			cfg := &config.Config{}
			cfg.Server.DebugMode = false // Disable history for simpler tests
			translator := testutil.TestTranslator(t)

			svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

			if tt.mockSetup != nil {
				tt.mockSetup(mockStore, mockOR)
			}

			stats, err := svc.applyUpdateWithStats(context.Background(), 123, tt.update, tt.existingFacts, time.Now(), 0, "test input")

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantStats.Created, stats.Created)
				assert.Equal(t, tt.wantStats.Updated, stats.Updated)
				assert.Equal(t, tt.wantStats.Deleted, stats.Deleted)
			}
			mockStore.AssertExpectations(t)
			mockOR.AssertExpectations(t)
		})
	}
}

// TestBuildPersonEmbeddingText tests the buildPersonEmbeddingText function.
func TestBuildPersonEmbeddingText(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
		username    *string
		aliases     []string
		bio         string
		want        string
	}{
		{
			name:        "all fields present",
			displayName: "John Doe",
			username:    ptrStr("johndoe"),
			aliases:     []string{"Johnny", "JD"},
			bio:         "Software engineer",
			want:        "John Doe johndoe Johnny JD Software engineer",
		},
		{
			name:        "only display name",
			displayName: "Alice",
			username:    nil,
			aliases:     nil,
			bio:         "",
			want:        "Alice",
		},
		{
			name:        "empty fields handled",
			displayName: "Bob",
			username:    ptrStr(""),
			aliases:     []string{},
			bio:         "",
			want:        "Bob",
		},
		{
			name:        "unicode name (Russian)",
			displayName: "Мария Ивановна",
			username:    nil,
			aliases:     []string{"Маша"},
			bio:         "Программист",
			want:        "Мария Ивановна Маша Программист",
		},
		{
			name:        "display name with bio only",
			displayName: "Jane Smith",
			username:    nil,
			aliases:     nil,
			bio:         "Artist and designer",
			want:        "Jane Smith Artist and designer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPersonEmbeddingText(tt.displayName, tt.username, tt.aliases, tt.bio)
			assert.Equal(t, tt.want, got)
		})
	}
}
