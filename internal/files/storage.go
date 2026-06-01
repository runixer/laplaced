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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/runixer/laplaced/internal/storage"
)

// Storage abstracts the artifact blob store so the bot can persist files on a
// local disk (FileStorage) or in an S3-compatible bucket (S3Storage) chosen by
// config. The DB only ever stores the relative key returned in SavedFile.Path;
// that key is backend-agnostic (same object key on disk and in S3), so reads go
// through ReadFile(key) and never need a filesystem path.
//
// GetFullPath is intentionally NOT part of this interface: an absolute path has
// no meaning for an object store. Callers that need the bytes use ReadFile.
type Storage interface {
	// SaveFile persists the reader's contents under a freshly generated key and
	// returns the relative key, content hash, and size.
	SaveFile(ctx context.Context, userID storage.ScopeID, reader io.Reader, filename string) (*SavedFile, error)
	// ReadFile returns the full contents of the object at key (the relative path
	// stored as artifact.FilePath).
	ReadFile(ctx context.Context, key string) ([]byte, error)
	// DeleteFile removes the object at key. Missing objects are not an error.
	DeleteFile(ctx context.Context, key string) error
}

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

// compile-time assertion that the local backend satisfies the interface.
var _ Storage = (*FileStorage)(nil)

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
	userID storage.ScopeID,
	reader io.Reader,
	filename string,
) (*SavedFile, error) {
	// Create user directory: data/artifacts/user_123/YYYY-MM/
	now := time.Now()
	yearMonth := now.Format("2006-01")
	userDir := filepath.Join(fs.basePath, fmt.Sprintf("user_%s", userID), yearMonth)

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

// resolveWithinBase joins key onto the base path and verifies the result stays
// inside the storage directory. This is the path-traversal guard (formerly
// duplicated in the web download handler): artifact keys come from the DB, but
// defending here keeps every read site safe by construction.
func (fs *FileStorage) resolveWithinBase(key string) (string, error) {
	fullPath := filepath.Join(fs.basePath, key)
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	absBase, err := filepath.Abs(fs.basePath)
	if err != nil {
		return "", fmt.Errorf("resolve storage path: %w", err)
	}
	if absFull != absBase && !strings.HasPrefix(absFull, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("artifact key %q escapes storage directory", key)
	}
	return fullPath, nil
}

// ReadFile returns the contents of the artifact stored at key (relative path).
func (fs *FileStorage) ReadFile(_ context.Context, key string) ([]byte, error) {
	fullPath, err := fs.resolveWithinBase(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(fullPath) //nolint:gosec // path validated against base by resolveWithinBase
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", key, err)
	}
	return data, nil
}

// GetFullPath returns the absolute path for a relative artifact path. Local-only
// helper (not part of Storage) — used by DeleteFile and any disk-specific code.
func (fs *FileStorage) GetFullPath(relativePath string) string {
	return filepath.Join(fs.basePath, relativePath)
}

// DeleteFile deletes a file from disk. Routed through the same containment
// guard as ReadFile so a malformed/traversing key can never escape the base.
func (fs *FileStorage) DeleteFile(_ context.Context, relativePath string) error {
	fullPath, err := fs.resolveWithinBase(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}
