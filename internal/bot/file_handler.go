package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
)

// FileHandler coordinates file saving with deduplication.
type FileHandler struct {
	storage      *files.FileStorage
	artifactRepo storage.ArtifactRepository
	logger       *slog.Logger
}

// NewFileHandler creates a new file handler.
func NewFileHandler(
	storage *files.FileStorage,
	artifactRepo storage.ArtifactRepository,
	logger *slog.Logger,
) *FileHandler {
	return &FileHandler{
		storage:      storage,
		artifactRepo: artifactRepo,
		logger:       logger.With("component", "file_handler"),
	}
}

// SaveFile saves a Telegram file as an artifact.
// Returns artifact ID (existing on deduplication, new on creation) and any error.
// messageText is the text content of the message (msg.Text or msg.Caption) for context (v0.6.0).
func (fh *FileHandler) SaveFile(
	ctx context.Context,
	userID int64,
	messageID int64,
	fileType string,
	originalName string,
	mimeType string,
	reader io.Reader,
	messageText string,
) (*int64, error) {
	// Log file type detection for debugging
	fh.logger.Debug("saving file as artifact",
		"user_id", userID,
		"file_type", fileType,
		"original_name", originalName,
		"mime_type", mimeType,
		"message_id", messageID,
	)

	// Save file to disk with hash calculation
	savedFile, err := fh.storage.SaveFile(ctx, userID, reader, originalName)
	if err != nil {
		return nil, fmt.Errorf("failed to save file: %w", err)
	}

	// Check for existing artifact by hash
	existing, err := fh.artifactRepo.GetByHash(userID, savedFile.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing artifact: %w", err)
	}

	// If exists, return existing ID (deduplication)
	if existing != nil {
		fh.logger.Info("artifact already exists, reusing",
			"user_id", userID,
			"existing_id", existing.ID,
			"hash", savedFile.ContentHash[:16]+"...",
		)
		return &existing.ID, nil
	}

	// Create new artifact record
	artifact := storage.Artifact{
		UserID:       userID,
		MessageID:    messageID,
		FileType:     fileType,
		FilePath:     savedFile.Path,
		FileSize:     savedFile.Size,
		MimeType:     mimeType,
		OriginalName: originalName,
		ContentHash:  savedFile.ContentHash,
		State:        "pending",
	}

	// Add user context if provided (v0.6.0)
	if messageText != "" {
		artifact.UserContext = &messageText
	}

	id, err := fh.artifactRepo.AddArtifact(artifact)
	if err != nil {
		return nil, fmt.Errorf("failed to create artifact: %w", err)
	}

	fh.logger.Info("artifact created",
		"id", id,
		"user_id", userID,
		"file_type", fileType,
		"size", savedFile.Size,
	)

	return &id, nil
}
