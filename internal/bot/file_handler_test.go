package bot

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// TestFileHandler_SaveFile_Success tests successful file save flow.
func TestFileHandler_SaveFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	mockRepo := &testutil.MockStorage{}
	mockRepo.On("GetByHash", int64(123), mock.AnythingOfType("string")).Return(nil, nil) // No existing artifact
	mockRepo.On("AddArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.UserID == 123 &&
			a.MessageID == 456 &&
			a.FileType == "image" &&
			a.MimeType == "image/jpeg" &&
			a.OriginalName == "test.jpg" &&
			a.State == "pending"
	})).Return(int64(1), nil)

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("fake image data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,             // userID
		456,             // messageID
		"image",         // fileType
		"test.jpg",      // originalName
		"image/jpeg",    // mimeType
		data,            // reader
		"photo caption", // messageText
	)

	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, int64(1), *id)
}

// TestFileHandler_SaveFile_Deduplication tests deduplication by hash.
func TestFileHandler_SaveFile_Deduplication(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	existingArtifact := &storage.Artifact{
		ID:          42,
		UserID:      123,
		ContentHash: "abc123",
	}

	mockRepo := &testutil.MockStorage{}
	// GetByHash returns existing artifact
	mockRepo.On("GetByHash", int64(123), mock.AnythingOfType("string")).Return(existingArtifact, nil)

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("duplicate data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,
		456,
		"document",
		"doc.pdf",
		"application/pdf",
		data,
		"",
	)

	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, int64(42), *id) // Should return existing ID

	// AddArtifact should NOT be called
	mockRepo.AssertNotCalled(t, "AddArtifact", mock.Anything)
}

// TestFileHandler_SaveFile_StorageError tests file storage failure.
func TestFileHandler_SaveFile_StorageError(t *testing.T) {
	// Create a storage with invalid path to force error
	fileStorage := files.NewFileStorage("/nonexistent/path/that/does/not/exist", testutil.TestLogger())

	mockRepo := &testutil.MockStorage{}

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("test data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,
		456,
		"image",
		"test.jpg",
		"image/jpeg",
		data,
		"",
	)

	assert.Nil(t, id)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save file")
}

// TestFileHandler_SaveFile_GetByHashError tests hash lookup failure.
func TestFileHandler_SaveFile_GetByHashError(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	mockRepo := &testutil.MockStorage{}
	mockRepo.On("GetByHash", int64(123), mock.AnythingOfType("string")).Return(nil, errors.New("db connection error"))

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("test data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,
		456,
		"image",
		"test.jpg",
		"image/jpeg",
		data,
		"",
	)

	assert.Nil(t, id)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to check for existing artifact")
}

// TestFileHandler_SaveFile_AddArtifactError tests artifact creation failure.
func TestFileHandler_SaveFile_AddArtifactError(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	mockRepo := &testutil.MockStorage{}
	mockRepo.On("GetByHash", int64(123), mock.AnythingOfType("string")).Return(nil, nil) // No existing
	mockRepo.On("AddArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.UserID == 123
	})).Return(int64(0), errors.New("unique constraint violation"))

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("test data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,
		456,
		"image",
		"test.jpg",
		"image/jpeg",
		data,
		"",
	)

	assert.Nil(t, id)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create artifact")
}

// TestFileHandler_SaveFile_WithUserContext tests user context is passed.
func TestFileHandler_SaveFile_WithUserContext(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	mockRepo := &testutil.MockStorage{}
	mockRepo.On("GetByHash", int64(123), mock.AnythingOfType("string")).Return(nil, nil)
	mockRepo.On("AddArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.UserID == 123 &&
			a.UserContext != nil &&
			*a.UserContext == "This is my vacation photo"
	})).Return(int64(1), nil)

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("test data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,
		456,
		"image",
		"vacation.jpg",
		"image/jpeg",
		data,
		"This is my vacation photo",
	)

	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, int64(1), *id)
}

// TestFileHandler_SaveFile_EmptyMessageText tests empty message text results in nil UserContext.
func TestFileHandler_SaveFile_EmptyMessageText(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	mockRepo := &testutil.MockStorage{}
	mockRepo.On("GetByHash", int64(123), mock.AnythingOfType("string")).Return(nil, nil)
	mockRepo.On("AddArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.UserID == 123 && a.UserContext == nil
	})).Return(int64(1), nil)

	handler := NewFileHandler(fileStorage, mockRepo, testutil.TestLogger())

	data := bytes.NewReader([]byte("test data"))
	id, err := handler.SaveFile(
		context.Background(),
		123,
		456,
		"document",
		"doc.pdf",
		"application/pdf",
		data,
		"", // Empty message text
	)

	require.NoError(t, err)
	require.NotNil(t, id)
}

// TestNewFileHandler tests constructor.
func TestNewFileHandler(t *testing.T) {
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())
	mockRepo := &testutil.MockStorage{}
	logger := testutil.TestLogger()

	handler := NewFileHandler(fileStorage, mockRepo, logger)

	assert.NotNil(t, handler)
	assert.Equal(t, fileStorage, handler.storage)
	assert.Equal(t, mockRepo, handler.artifactRepo)
}
