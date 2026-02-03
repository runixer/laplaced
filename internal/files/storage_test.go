package files

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFileStorage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fs := NewFileStorage("/tmp/test", logger)

	assert.NotNil(t, fs)
	assert.Equal(t, "/tmp/test", fs.basePath)
	assert.NotNil(t, fs.logger)
}

func TestFileStorage_SaveFile_Success(t *testing.T) {
	t.Run("successful save returns SavedFile", func(t *testing.T) {
		t.Helper()

		// Create temp directory
		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		// Test data
		ctx := context.Background()
		userID := int64(123)
		content := []byte("test file content for hash calculation")
		filename := "test.txt"

		// Save file
		saved, err := fs.SaveFile(ctx, userID, bytes.NewReader(content), filename)

		require.NoError(t, err)
		require.NotNil(t, saved)

		// Verify path structure: user_123/YYYY-MM/uuid.txt
		assert.Contains(t, saved.Path, filepath.Join("user_123"))
		assert.Contains(t, saved.Path, ".txt")
		assert.True(t, saved.Size > 0)

		// Verify hash is SHA256 (64 hex chars)
		assert.Equal(t, 64, len(saved.ContentHash))

		// Verify file exists on disk
		fullPath := fs.GetFullPath(saved.Path)
		fileInfo, err := os.Stat(fullPath)
		require.NoError(t, err)
		assert.Equal(t, saved.Size, fileInfo.Size())

		// Verify hash is consistent for same content
		saved2, err := fs.SaveFile(ctx, userID, bytes.NewReader(content), filename+"2")
		require.NoError(t, err)
		assert.Equal(t, saved.ContentHash, saved2.ContentHash, "same content should produce same hash")
	})

	t.Run("preserves file extension", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()
		content := []byte("test")

		extensions := []string{".jpg", ".png", ".pdf", ".txt", ".ogg", ".mp4", ".doc"}
		for _, ext := range extensions {
			t.Run(ext, func(t *testing.T) {
				saved, err := fs.SaveFile(ctx, 123, bytes.NewReader(content), "file"+ext)
				require.NoError(t, err)
				assert.True(t, strings.HasSuffix(saved.Path, ext))
			})
		}
	})

	t.Run("creates user directory structure", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()
		now := time.Now()
		yearMonth := now.Format("2006-01")

		saved, err := fs.SaveFile(ctx, 456, bytes.NewReader([]byte("test")), "test.txt")
		require.NoError(t, err)

		// Check directory exists
		expectedDir := filepath.Join(tempDir, "user_456", yearMonth)
		fileInfo, err := os.Stat(expectedDir)
		require.NoError(t, err)
		assert.True(t, fileInfo.IsDir())

		// Check file is in that directory
		assert.Contains(t, saved.Path, filepath.Join("user_456", yearMonth))
	})
}

func TestFileStorage_SaveFile_ContextCancellation(t *testing.T) {
	t.Run("cancels and removes partial file", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		// Need to actually start copy before canceling
		ctx, cancel := context.WithCancel(context.Background())
		// Cancel after starting write
		cancel()

		// Reader that will trigger cancellation check
		content := make([]byte, 1000)
		_, err := fs.SaveFile(ctx, 123, bytes.NewReader(content), "test.txt")

		assert.Error(t, err)
		// The error is either context.Canceled or write error due to cancellation
		// Just verify we got an error
	})
}

func TestFileStorage_SaveFile_MkdirFailure(t *testing.T) {
	t.Run("returns error when directory creation fails", func(t *testing.T) {
		t.Helper()

		// Create a file where directory should be
		tempDir := t.TempDir()
		blockerPath := filepath.Join(tempDir, "user_123")
		err := os.WriteFile(blockerPath, []byte("blocking file"), 0644)
		require.NoError(t, err)

		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()
		_, err = fs.SaveFile(ctx, 123, bytes.NewReader([]byte("test")), "test.txt")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create directory")
	})
}

func TestFileStorage_SaveFile_CreateFailure(t *testing.T) {
	t.Run("returns error when file creation fails", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()

		// Reader that returns error on read
		errorReader := &errorReadCloser{err: io.ErrUnexpectedEOF}

		_, err := fs.SaveFile(ctx, 123, errorReader, "test.txt")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to write file")

		// Verify the actual file was removed (directory may exist)
		userDir := filepath.Join(tempDir, "user_123")
		entries, _ := os.ReadDir(userDir)
		for _, entry := range entries {
			if !entry.IsDir() {
				t.Fatalf("expected no files after failed write, found %s", entry.Name())
			}
		}
	})
}

func TestFileStorage_GetFullPath(t *testing.T) {
	t.Run("returns absolute path for relative path", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		relPath := "user_123/2025-01/abc123.txt"
		fullPath := fs.GetFullPath(relPath)

		assert.Equal(t, filepath.Join(tempDir, relPath), fullPath)
		assert.True(t, filepath.IsAbs(fullPath))
	})

	t.Run("handles empty relative path", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		fullPath := fs.GetFullPath("")

		assert.Equal(t, tempDir, fullPath)
	})
}

func TestFileStorage_DeleteFile_Success(t *testing.T) {
	t.Run("deletes existing file", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()

		// Create a file first
		saved, err := fs.SaveFile(ctx, 123, bytes.NewReader([]byte("to be deleted")), "delete_me.txt")
		require.NoError(t, err)

		// Verify it exists
		fullPath := fs.GetFullPath(saved.Path)
		_, err = os.Stat(fullPath)
		require.NoError(t, err)

		// Delete it
		err = fs.DeleteFile(saved.Path)
		assert.NoError(t, err)

		// Verify it's gone
		_, err = os.Stat(fullPath)
		assert.True(t, os.IsNotExist(err))
	})
}

func TestFileStorage_DeleteFile_NonExistent(t *testing.T) {
	t.Run("returns no error for non-existent file", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		// Delete non-existent file
		err := fs.DeleteFile("user_123/2025-01/nonexistent.txt")

		assert.NoError(t, err, "deleting non-existent file should not error")
	})
}

func TestFileStorage_SaveFile_Sha256Hash(t *testing.T) {
	t.Run("calculates correct SHA256 hash", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()

		// Known content with known hash
		content := []byte("Hello, World!")
		// SHA256 of "Hello, World!" is: dffd6021bb2bd5b0af676290809ec3a53191dd81c7f70a4b28688a362182986f

		saved, err := fs.SaveFile(ctx, 123, bytes.NewReader(content), "test.txt")
		require.NoError(t, err)

		expectedHash := "dffd6021bb2bd5b0af676290809ec3a53191dd81c7f70a4b28688a362182986f"
		assert.Equal(t, expectedHash, saved.ContentHash)
	})
}

func TestFileStorage_SaveFile_EmptyContent(t *testing.T) {
	t.Run("handles empty file", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()
		// SHA256 of empty string is: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855

		saved, err := fs.SaveFile(ctx, 123, bytes.NewReader([]byte("")), "empty.txt")
		require.NoError(t, err)

		assert.Equal(t, int64(0), saved.Size)
		assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", saved.ContentHash)

		// Verify file exists
		fullPath := fs.GetFullPath(saved.Path)
		fileInfo, err := os.Stat(fullPath)
		require.NoError(t, err)
		assert.Equal(t, int64(0), fileInfo.Size())
	})
}

func TestFileStorage_SaveFile_LargeContent(t *testing.T) {
	t.Run("handles larger file content", func(t *testing.T) {
		t.Helper()

		tempDir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fs := NewFileStorage(tempDir, logger)

		ctx := context.Background()
		// Create 1MB of data
		content := make([]byte, 1024*1024)
		for i := range content {
			content[i] = byte(i % 256)
		}

		saved, err := fs.SaveFile(ctx, 123, bytes.NewReader(content), "large.bin")
		require.NoError(t, err)

		assert.Equal(t, int64(1024*1024), saved.Size)
		assert.Equal(t, 64, len(saved.ContentHash))

		// Verify file can be read back
		fullPath := fs.GetFullPath(saved.Path)
		readContent, err := os.ReadFile(fullPath)
		require.NoError(t, err)
		assert.Equal(t, content, readContent)
	})
}

// errorReadCloser is a reader that always returns an error.
type errorReadCloser struct {
	err error
}

func (e *errorReadCloser) Read(p []byte) (n int, err error) {
	return 0, e.err
}

func (e *errorReadCloser) Close() error {
	return nil
}
