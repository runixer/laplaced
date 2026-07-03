package laplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupArtifactTest creates a test Laplace agent with mock artifact repository.
// Returns config, translator, agent, mockStorage (for artifact repo assertions), and temp dir.
func setupArtifactTest(t *testing.T) (*config.Config, *i18n.Translator, *Laplace, *testutil.MockStorage, string) {
	t.Helper()

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG for simpler testing
	// Set smaller limits for testing
	cfg.Agents.Reranker.Artifacts.Max = 3
	cfg.Agents.Reranker.Artifacts.MaxContextBytes = 100 * 1024 // 100KB

	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockLLMClient)

	// Create a temporary directory for artifact files
	tempDir := t.TempDir()

	// Use mockStore for all storage interfaces including artifact repository
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, mockStore, translator, testutil.TestLogger())
	agent.SetFileStorage(files.NewFileStorage(tempDir, testutil.TestLogger()))

	return cfg, translator, agent, mockStore, tempDir
}

// TestArtifactLoader_EarlyReturn tests early return conditions.
func TestArtifactLoader_EarlyReturn(t *testing.T) {
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
			name: "no file storage",
			setupFunc: func(l *Laplace) {
				l.SetFileStorage(nil)
			},
			artifactIDs: []int64{1},
			expectNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc(agent)
			parts, err := agent.artifactLoader().Load(context.Background(), "123", tt.artifactIDs)
			require.NoError(t, err)
			if tt.expectNil {
				assert.Nil(t, parts)
			}
		})
	}
}

// TestArtifactLoader_FailedArtifactSkipped keeps failed artifacts out of context.
func TestArtifactLoader_FailedArtifactSkipped(t *testing.T) {
	_, _, agent, mockStore, tempDir := setupArtifactTest(t)

	userID := storage.ScopeID("123")
	artifactID := int64(1)

	// Create a test file
	testFile := filepath.Join(tempDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test content"), 0600)
	require.NoError(t, err)

	// A failed artifact (e.g. safety-blocked extraction) must never be loaded.
	mockStore.On("GetArtifact", userID, artifactID).Return(&storage.Artifact{
		ID:        artifactID,
		State:     "failed",
		FilePath:  "test.txt",
		FileSize:  12,
		CreatedAt: time.Now(),
	}, nil)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.Nil(t, parts, "failed artifacts must not be loaded into context")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_PendingArtifactLoads loads session-fresh artifacts
// whose extraction hasn't finished yet — the file exists, only metadata is missing.
func TestArtifactLoader_PendingArtifactLoads(t *testing.T) {
	_, _, agent, mockStore, tempDir := setupArtifactTest(t)

	userID := storage.ScopeID("123")
	artifactID := int64(1)

	testFile := filepath.Join(tempDir, "test.png")
	err := os.WriteFile(testFile, []byte("test content"), 0600)
	require.NoError(t, err)

	mockStore.On("GetArtifact", userID, artifactID).Return(&storage.Artifact{
		ID:           artifactID,
		State:        "pending",
		FileType:     "image",
		FilePath:     "test.png",
		FileSize:     12,
		MimeType:     "image/png",
		OriginalName: "test.png",
		CreatedAt:    time.Now(),
	}, nil)
	mockStore.On("IncrementContextLoadCount", userID, []int64{artifactID}).Return(nil)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.NotEmpty(t, parts, "pending artifacts must load into context")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_ArtifactNotFound handles GetArtifact errors gracefully.
func TestArtifactLoader_ArtifactNotFound(t *testing.T) {
	_, _, agent, mockStore, _ := setupArtifactTest(t)

	userID := storage.ScopeID("123")
	artifactID := int64(999)

	// Mock artifact not found
	mockStore.On("GetArtifact", userID, artifactID).Return(nil, assert.AnError)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.Nil(t, parts, "should return nil when artifact not found")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_FileReadError handles file read errors gracefully.
func TestArtifactLoader_FileReadError(t *testing.T) {
	_, _, agent, mockStore, _ := setupArtifactTest(t)

	userID := storage.ScopeID("123")
	artifactID := int64(1)

	// Mock artifact with file that doesn't exist
	mockStore.On("GetArtifact", userID, artifactID).Return(&storage.Artifact{
		ID:        artifactID,
		State:     "ready",
		FilePath:  "nonexistent.txt",
		FileSize:  100,
		CreatedAt: time.Now(),
	}, nil)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{artifactID})
	require.NoError(t, err)
	assert.Nil(t, parts, "should return nil when file cannot be read")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_CountLimit respects the max artifacts count limit.
func TestArtifactLoader_CountLimit(t *testing.T) {
	cfg, _, agent, mockStore, tempDir := setupArtifactTest(t)

	cfg.Agents.Reranker.Artifacts.Max = 2 // Set max to 2

	userID := storage.ScopeID("123")

	// Create test files
	testFile := filepath.Join(tempDir, filepath.Join("test", "file.pdf"))
	err := os.MkdirAll(filepath.Dir(testFile), 0700)
	require.NoError(t, err)
	err = os.WriteFile(testFile, []byte("small content"), 0600)
	require.NoError(t, err)

	// Mock 2 artifacts (3rd one should NOT be fetched due to count limit)
	mockStore.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  filepath.Join("test", "file.pdf"),
		FileSize:  13,
		CreatedAt: time.Now(),
	}, nil).Once()

	mockStore.On("GetArtifact", userID, int64(2)).Return(&storage.Artifact{
		ID:        2,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  filepath.Join("test", "file.pdf"),
		FileSize:  13,
		CreatedAt: time.Now(),
	}, nil).Once()

	// Expect IncrementContextLoadCount to be called with 2 artifact IDs
	mockStore.On("IncrementContextLoadCount", userID, []int64{int64(1), int64(2)}).Return(nil)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{1, 2, 3})
	require.NoError(t, err)
	assert.NotNil(t, parts)
	// One TaggedPart per loaded artifact (markers are rendered later)
	assert.Len(t, parts, 2, "should load max 2 artifacts")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_SizeLimit respects the max bytes limit.
func TestArtifactLoader_SizeLimit(t *testing.T) {
	cfg, _, agent, mockStore, tempDir := setupArtifactTest(t)

	cfg.Agents.Reranker.Artifacts.MaxContextBytes = 30 // Set very low limit

	userID := storage.ScopeID("123")

	// Create a small test file (10 bytes)
	smallFile := filepath.Join(tempDir, "small.pdf")
	err := os.WriteFile(smallFile, []byte("0123456789"), 0600)
	require.NoError(t, err)

	// Create a larger test file (30 bytes) - would exceed limit when added to first
	largeFile := filepath.Join(tempDir, "large.pdf")
	err = os.WriteFile(largeFile, []byte("012345678901234567890123456789"), 0600)
	require.NoError(t, err)

	// First artifact is small
	mockStore.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  "small.pdf",
		FileSize:  10,
		CreatedAt: time.Now(),
	}, nil).Once()

	// Second artifact would exceed size limit (10 + 30 = 40 > 30)
	mockStore.On("GetArtifact", userID, int64(2)).Return(&storage.Artifact{
		ID:        2,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  "large.pdf",
		FileSize:  30,
		CreatedAt: time.Now(),
	}, nil).Once()

	mockStore.On("IncrementContextLoadCount", userID, []int64{int64(1)}).Return(nil)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{1, 2})
	require.NoError(t, err)
	assert.NotNil(t, parts)
	// Only the first artifact fits (one TaggedPart per artifact)
	assert.Len(t, parts, 1, "should stop loading when size limit would be exceeded")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_UsageTracking calls IncrementContextLoadCount.
func TestArtifactLoader_UsageTracking(t *testing.T) {
	_, _, agent, mockStore, tempDir := setupArtifactTest(t)

	userID := storage.ScopeID("123")

	// Create test file
	testFile := filepath.Join(tempDir, "test.pdf")
	err := os.WriteFile(testFile, []byte("content"), 0600)
	require.NoError(t, err)

	mockStore.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "pdf",
		FilePath:  "test.pdf",
		FileSize:  7,
		CreatedAt: time.Now(),
	}, nil).Once()

	mockStore.On("IncrementContextLoadCount", userID, []int64{int64(1)}).Return(nil)

	_, err = agent.artifactLoader().Load(context.Background(), userID, []int64{1})
	require.NoError(t, err)

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_UnknownFileTypeSkipped pins the skip semantics for an
// artifact with an unrecognized file_type: nothing is loaded, no usage is
// counted, and no dangling 📄 marker is produced. (Before the ArtifactLoader
// extraction, such an artifact left a marker TextPart with no media part and
// still consumed budget + usage count.)
func TestArtifactLoader_UnknownFileTypeSkipped(t *testing.T) {
	_, _, agent, mockStore, tempDir := setupArtifactTest(t)

	userID := storage.ScopeID("123")

	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "blob.bin"), []byte("content"), 0600))

	mockStore.On("GetArtifact", userID, int64(1)).Return(&storage.Artifact{
		ID:        1,
		State:     "ready",
		FileType:  "mystery",
		FilePath:  "blob.bin",
		FileSize:  7,
		CreatedAt: time.Now(),
	}, nil).Once()
	// No IncrementContextLoadCount expectation: a skipped artifact must not
	// count as used.

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{1})
	require.NoError(t, err)
	assert.Empty(t, parts, "unknown file type must not produce any parts")

	mockStore.AssertExpectations(t)
}

// TestArtifactLoader_ProvenanceFields verifies the loader tags each part with
// the provenance the renderer and downstream rules rely on: recalled source,
// media kind from file type, artifact ID, display name, and the
// memory_<id>_ filename anchor on the media part itself.
func TestArtifactLoader_ProvenanceFields(t *testing.T) {
	_, _, agent, mockStore, tempDir := setupArtifactTest(t)

	userID := storage.ScopeID("123")
	created := time.Date(2025, 1, 31, 12, 0, 0, 0, time.UTC)

	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "old.ogg"), []byte("AUD"), 0600))

	mockStore.On("GetArtifact", userID, int64(9)).Return(&storage.Artifact{
		ID:        9,
		State:     "ready",
		FileType:  "voice",
		FilePath:  "old.ogg",
		MimeType:  "audio/ogg",
		FileSize:  3,
		CreatedAt: created,
	}, nil).Once()
	mockStore.On("IncrementContextLoadCount", userID, []int64{int64(9)}).Return(nil)

	parts, err := agent.artifactLoader().Load(context.Background(), userID, []int64{9})
	require.NoError(t, err)
	require.Len(t, parts, 1)

	tp := parts[0]
	assert.Equal(t, SourceRecalled, tp.Source)
	assert.Equal(t, KindAudio, tp.Kind)
	assert.Equal(t, int64(9), tp.ArtifactID)
	assert.Equal(t, "artifact_9", tp.Name, "empty OriginalName falls back to artifact_<id>")
	assert.Equal(t, created, tp.CreatedAt)

	fp, ok := tp.Part.(llm.FilePart)
	require.True(t, ok)
	assert.Equal(t, "memory_9_audio.ogg", fp.File.FileName)

	mockStore.AssertExpectations(t)
}
