package laplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockArtifactRepo is a simple mock for ArtifactRepository.
type mockArtifactRepo struct {
	mock.Mock
}

func (m *mockArtifactRepo) AddArtifact(artifact storage.Artifact) (int64, error) {
	args := m.Called(artifact)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockArtifactRepo) GetArtifact(userID, artifactID int64) (*storage.Artifact, error) {
	args := m.Called(userID, artifactID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Artifact), args.Error(1)
}

func (m *mockArtifactRepo) GetByHash(userID int64, contentHash string) (*storage.Artifact, error) {
	args := m.Called(userID, contentHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Artifact), args.Error(1)
}

func (m *mockArtifactRepo) GetPendingArtifacts(userID int64, maxRetries int) ([]storage.Artifact, error) {
	args := m.Called(userID, maxRetries)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Artifact), args.Error(1)
}

func (m *mockArtifactRepo) GetArtifacts(filter storage.ArtifactFilter, limit, offset int) ([]storage.Artifact, int64, error) {
	args := m.Called(filter, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]storage.Artifact), args.Get(1).(int64), args.Error(2)
}

func (m *mockArtifactRepo) UpdateArtifact(artifact storage.Artifact) error {
	args := m.Called(artifact)
	return args.Error(0)
}

func (m *mockArtifactRepo) RecoverArtifactStates(threshold time.Duration) error {
	args := m.Called(threshold)
	return args.Error(0)
}

func (m *mockArtifactRepo) GetArtifactsByIDs(userID int64, artifactIDs []int64) ([]storage.Artifact, error) {
	args := m.Called(userID, artifactIDs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Artifact), args.Error(1)
}

func (m *mockArtifactRepo) IncrementContextLoadCount(userID int64, artifactIDs []int64) error {
	args := m.Called(userID, artifactIDs)
	return args.Error(0)
}

func (m *mockArtifactRepo) UpdateMessageID(userID, artifactID, messageID int64) error {
	args := m.Called(userID, artifactID, messageID)
	return args.Error(0)
}

// setupArtifactTest creates a test Laplace agent with mock artifact repository.
func setupArtifactTest(t *testing.T) (*config.Config, *i18n.Translator, *Laplace, *mockArtifactRepo, string) {
	t.Helper()

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG for simpler testing
	// Set smaller limits for testing
	cfg.Agents.Reranker.Artifacts.Max = 3
	cfg.Agents.Reranker.Artifacts.MaxContextBytes = 100 * 1024 // 100KB

	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(mockArtifactRepo)

	// Create a temporary directory for artifact files
	tempDir := t.TempDir()

	agent := New(cfg, mockORClient, nil, mockStore, mockStore, mockArtifactRepo, translator, testutil.TestLogger())

	return cfg, translator, agent, mockArtifactRepo, tempDir
}

// TestLoadArtifactFullContent_EarlyReturn tests early return conditions.
func TestLoadArtifactFullContent_EarlyReturn(t *testing.T) {
	_, _, agent, _, _ := setupArtifactTest(t)

	tests := []struct {
		name        string
		setupFunc   func(*Laplace)
		artifactIDs []int64
		expectNil   bool
	}{
		{
			name: "nil artifact repo",
			setupFunc: func(l *Laplace) {
				l.artifactRepo = nil
			},
			artifactIDs: []int64{1, 2},
			expectNil:   true,
		},
		{
			name: "empty artifact IDs",
			setupFunc: func(l *Laplace) {
				// No setup needed
			},
			artifactIDs: []int64{},
			expectNil:   true,
		},
		{
			name: "empty storage path",
			setupFunc: func(l *Laplace) {
				l.storagePath = ""
			},
			artifactIDs: []int64{1},
			expectNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc(agent)
			parts, err := agent.loadArtifactFullContent(context.Background(), 123, tt.artifactIDs)
			require.NoError(t, err)
			if tt.expectNil {
				assert.Nil(t, parts)
			}
		})
	}
}

// TestLoadArtifactFullContent_ArtifactNotReady skips artifacts that aren't ready.
func TestLoadArtifactFullContent_ArtifactNotReady(t *testing.T) {
	_, _, agent, mockArtifactRepo, tempDir := setupArtifactTest(t)

	agent.storagePath = tempDir

	userID := int64(123)
	artifactID := int64(1)

	// Create a test file
	testFile := filepath.Join(tempDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test content"), 0600)
	require.NoError(t, err)

	// Mock artifact that's not ready
	mockArtifactRepo.On("GetArtifact", userID, artifactID).Return(&storage.Artifact{
		ID:        artifactID,
		State:     "pending", // Not ready
		FilePath:  "test.txt",
		FileSize:  12,
		CreatedAt: time.Now(),
	}, nil)

	parts, err := agent.loadArtifactFullContent(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.Nil(t, parts, "should return nil when no artifacts are ready")

	mockArtifactRepo.AssertExpectations(t)
}

// TestLoadArtifactFullContent_ArtifactNotFound handles GetArtifact errors gracefully.
func TestLoadArtifactFullContent_ArtifactNotFound(t *testing.T) {
	_, _, agent, mockArtifactRepo, tempDir := setupArtifactTest(t)

	agent.storagePath = tempDir

	userID := int64(123)
	artifactID := int64(999)

	// Mock artifact not found
	mockArtifactRepo.On("GetArtifact", userID, artifactID).Return(nil, assert.AnError)

	parts, err := agent.loadArtifactFullContent(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.Nil(t, parts, "should return nil when artifact not found")

	mockArtifactRepo.AssertExpectations(t)
}

// TestLoadArtifactFullContent_FileReadError handles file read errors gracefully.
func TestLoadArtifactFullContent_FileReadError(t *testing.T) {
	_, _, agent, mockArtifactRepo, tempDir := setupArtifactTest(t)

	agent.storagePath = tempDir

	userID := int64(123)
	artifactID := int64(1)

	// Mock artifact with file that doesn't exist
	mockArtifactRepo.On("GetArtifact", userID, artifactID).Return(&storage.Artifact{
		ID:        artifactID,
		State:     "ready",
		FilePath:  "nonexistent.txt",
		FileSize:  100,
		CreatedAt: time.Now(),
	}, nil)

	parts, err := agent.loadArtifactFullContent(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.Nil(t, parts, "should return nil when file cannot be read")

	mockArtifactRepo.AssertExpectations(t)
}

// TestLoadArtifactFullContent_CountLimit respects the max artifacts count limit.
func TestLoadArtifactFullContent_CountLimit(t *testing.T) {
	cfg, _, agent, mockArtifactRepo, tempDir := setupArtifactTest(t)

	agent.storagePath = tempDir
	cfg.Agents.Reranker.Artifacts.Max = 2 // Set max to 2

	userID := int64(123)

	// Create test files
	testFile := filepath.Join(tempDir, filepath.Join("test", "file.pdf"))
	err := os.MkdirAll(filepath.Dir(testFile), 0700)
	require.NoError(t, err)
	err = os.WriteFile(testFile, []byte("small content"), 0600)
	require.NoError(t, err)

	// Mock 2 artifacts (3rd one should NOT be fetched due to count limit)
	mockArtifactRepo.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  filepath.Join("test", "file.pdf"),
		FileSize:  13,
		CreatedAt: time.Now(),
	}, nil).Once()

	mockArtifactRepo.On("GetArtifact", userID, int64(2)).Return(&storage.Artifact{
		ID:        2,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  filepath.Join("test", "file.pdf"),
		FileSize:  13,
		CreatedAt: time.Now(),
	}, nil).Once()

	// Expect IncrementContextLoadCount to be called with 2 artifact IDs
	mockArtifactRepo.On("IncrementContextLoadCount", userID, []int64{int64(1), int64(2)}).Return(nil)

	parts, err := agent.loadArtifactFullContent(context.Background(), userID, []int64{1, 2, 3})
	require.NoError(t, err)
	assert.NotNil(t, parts)
	// Should have 2 artifacts (TextPart + FilePart for each = 4 parts)
	assert.Len(t, parts, 4, "should load max 2 artifacts")

	mockArtifactRepo.AssertExpectations(t)
}

// TestLoadArtifactFullContent_SizeLimit respects the max bytes limit.
func TestLoadArtifactFullContent_SizeLimit(t *testing.T) {
	cfg, _, agent, mockArtifactRepo, tempDir := setupArtifactTest(t)

	agent.storagePath = tempDir
	cfg.Agents.Reranker.Artifacts.MaxContextBytes = 30 // Set very low limit

	userID := int64(123)

	// Create a small test file (10 bytes)
	smallFile := filepath.Join(tempDir, "small.pdf")
	err := os.WriteFile(smallFile, []byte("0123456789"), 0600)
	require.NoError(t, err)

	// Create a larger test file (30 bytes) - would exceed limit when added to first
	largeFile := filepath.Join(tempDir, "large.pdf")
	err = os.WriteFile(largeFile, []byte("012345678901234567890123456789"), 0600)
	require.NoError(t, err)

	// First artifact is small
	mockArtifactRepo.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  "small.pdf",
		FileSize:  10,
		CreatedAt: time.Now(),
	}, nil).Once()

	// Second artifact would exceed size limit (10 + 30 = 40 > 30)
	mockArtifactRepo.On("GetArtifact", userID, int64(2)).Return(&storage.Artifact{
		ID:        2,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  "large.pdf",
		FileSize:  30,
		CreatedAt: time.Now(),
	}, nil).Once()

	mockArtifactRepo.On("IncrementContextLoadCount", userID, []int64{int64(1)}).Return(nil)

	parts, err := agent.loadArtifactFullContent(context.Background(), userID, []int64{1, 2})
	require.NoError(t, err)
	assert.NotNil(t, parts)
	// Should have only the first artifact (TextPart + FilePart = 2 parts)
	assert.Len(t, parts, 2, "should stop loading when size limit would be exceeded")

	mockArtifactRepo.AssertExpectations(t)
}

// TestLoadArtifactFullContent_UsageTracking calls IncrementContextLoadCount.
func TestLoadArtifactFullContent_UsageTracking(t *testing.T) {
	_, _, agent, mockArtifactRepo, tempDir := setupArtifactTest(t)

	agent.storagePath = tempDir

	userID := int64(123)

	// Create test file
	testFile := filepath.Join(tempDir, "test.pdf")
	err := os.WriteFile(testFile, []byte("content"), 0600)
	require.NoError(t, err)

	mockArtifactRepo.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  "test.pdf",
		FileSize:  7,
		CreatedAt: time.Now(),
	}, nil).Once()

	mockArtifactRepo.On("IncrementContextLoadCount", userID, []int64{int64(1)}).Return(nil)

	_, err = agent.loadArtifactFullContent(context.Background(), userID, []int64{1})
	require.NoError(t, err)

	mockArtifactRepo.AssertExpectations(t)
}
