package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// FileStorage handles saving files to disk with SHA256 calculation.
type FileStorage struct {
	basePath string
	logger   *slog.Logger
}

// SavedFile contains metadata about a saved file.
type SavedFile struct {
	Path        string // Relative path from base
	ContentHash string // SHA256 hex
	Size        int64  // Bytes
}

// NewFileStorage creates a new file storage service.
func NewFileStorage(basePath string, logger *slog.Logger) *FileStorage {
	return &FileStorage{
		basePath: basePath,
		logger:   logger.With("component", "file_storage"),
	}
}

// SaveFile saves a file to disk with streaming SHA256 calculation.
// Returns relative path, hash, and size.
func (fs *FileStorage) SaveFile(
	ctx context.Context,
	userID int64,
	reader io.Reader,
	filename string,
) (*SavedFile, error) {
	// Create user directory: data/artifacts/user_123/YYYY-MM/
	now := time.Now()
	yearMonth := now.Format("2006-01")
	userDir := filepath.Join(fs.basePath, fmt.Sprintf("user_%d", userID), yearMonth)

	if err := os.MkdirAll(userDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Generate UUID filename (preserve extension)
	ext := filepath.Ext(filename)
	uuidFilename := uuid.New().String() + ext
	filePath := filepath.Join(userDir, uuidFilename)

	// Create file
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Calculate SHA256 while writing
	hash := sha256.New()
	multiWriter := io.MultiWriter(file, hash)

	// Copy with context cancellation
	size, err := io.Copy(multiWriter, reader)
	if err != nil {
		os.Remove(filePath) // Clean up on error
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		os.Remove(filePath) // Clean up on cancellation
		return nil, ctx.Err()
	default:
	}

	// Get hash as hex string
	contentHash := hex.EncodeToString(hash.Sum(nil))

	// Get relative path from base
	relPath, err := filepath.Rel(fs.basePath, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative path: %w", err)
	}

	fs.logger.Info("file saved",
		"user_id", userID,
		"path", relPath,
		"size", size,
		"hash", contentHash[:16]+"...", // Log first 16 chars of hash
	)

	return &SavedFile{
		Path:        relPath,
		ContentHash: contentHash,
		Size:        size,
	}, nil
}

// GetFullPath returns the absolute path for a relative artifact path.
func (fs *FileStorage) GetFullPath(relativePath string) string {
	return filepath.Join(fs.basePath, relativePath)
}

// DeleteFile deletes a file from disk.
func (fs *FileStorage) DeleteFile(relativePath string) error {
	fullPath := fs.GetFullPath(relativePath)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}
