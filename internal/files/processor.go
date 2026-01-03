package files

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/telegram"
)

// Processor handles file downloads and processing from Telegram messages.
type Processor struct {
	downloader telegram.FileDownloader
	translator *i18n.Translator
	language   string
	logger     *slog.Logger
	maxRetries int
	retryDelay time.Duration
}

// NewProcessor creates a new file processor.
func NewProcessor(
	downloader telegram.FileDownloader,
	translator *i18n.Translator,
	language string,
	logger *slog.Logger,
) *Processor {
	return &Processor{
		downloader: downloader,
		translator: translator,
		language:   language,
		logger:     logger.With("component", "file_processor"),
		maxRetries: 3,
		retryDelay: 500 * time.Millisecond,
	}
}

// ProcessMessage extracts and processes the file from a message.
// A Telegram message contains at most one file (photo OR document OR voice).
// Individual file errors are logged as warnings and nil is returned for that file.
// Returns a slice with 0 or 1 successfully processed files.
func (p *Processor) ProcessMessage(ctx context.Context, msg *telegram.Message, userID int64) ([]*ProcessedFile, error) {
	// Process photo (array of sizes, take best quality)
	if len(msg.Photo) > 0 {
		bestPhoto := msg.Photo[len(msg.Photo)-1]
		f, err := p.processPhotoWithRetry(ctx, &bestPhoto, userID)
		if err != nil {
			p.logger.Warn("failed to process photo after retries",
				"error", err,
				"file_id", bestPhoto.FileID,
				"user_id", userID,
			)
			return nil, nil
		}
		return []*ProcessedFile{f}, nil
	}

	// Process document (image, PDF, or text file)
	if msg.Document != nil {
		f, err := p.processDocumentWithRetry(ctx, msg.Document, userID)
		if err != nil {
			p.logger.Warn("failed to process document after retries",
				"error", err,
				"file_id", msg.Document.FileID,
				"file_name", msg.Document.FileName,
				"user_id", userID,
			)
			return nil, nil
		}
		return []*ProcessedFile{f}, nil
	}

	// Process voice message
	if msg.Voice != nil {
		f, err := p.processVoiceWithRetry(ctx, msg.Voice, userID)
		if err != nil {
			p.logger.Warn("failed to process voice after retries",
				"error", err,
				"file_id", msg.Voice.FileID,
				"user_id", userID,
			)
			return nil, nil
		}
		return []*ProcessedFile{f}, nil
	}

	return nil, nil
}

// downloadWithRetry attempts to download a file with retries and exponential backoff.
func (p *Processor) downloadWithRetry(ctx context.Context, fileID string, userID int64, fileType FileType) ([]byte, time.Duration, error) {
	var lastErr error
	totalDuration := time.Duration(0)

	for attempt := 1; attempt <= p.maxRetries; attempt++ {
		start := time.Now()
		data, err := p.downloader.DownloadFile(ctx, fileID)
		duration := time.Since(start)
		totalDuration += duration

		if err == nil {
			RecordFileDownload(userID, fileType, duration.Seconds(), int64(len(data)), true)
			return data, totalDuration, nil
		}

		lastErr = err
		p.logger.Warn("download attempt failed",
			"attempt", attempt,
			"max_retries", p.maxRetries,
			"file_id", fileID,
			"error", err,
		)

		if attempt < p.maxRetries {
			// Exponential backoff: 500ms, 1000ms, 2000ms, ...
			backoff := p.retryDelay * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				return nil, totalDuration, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	RecordFileDownload(userID, fileType, totalDuration.Seconds(), 0, false)
	return nil, totalDuration, fmt.Errorf("download failed after %d retries: %w", p.maxRetries, lastErr)
}

// processPhotoWithRetry processes a photo with retry logic.
func (p *Processor) processPhotoWithRetry(ctx context.Context, photo *telegram.PhotoSize, userID int64) (*ProcessedFile, error) {
	data, duration, err := p.downloadWithRetry(ctx, photo.FileID, userID, FileTypePhoto)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	return &ProcessedFile{
		LLMParts: []interface{}{
			openrouter.ImagePart{
				Type: "image_url",
				ImageURL: openrouter.ImageURL{
					URL: fmt.Sprintf("data:image/jpeg;base64,%s", base64Data),
				},
			},
		},
		FileType: FileTypePhoto,
		FileID:   photo.FileID,
		MimeType: "image/jpeg",
		Size:     int64(len(data)),
		Duration: duration,
	}, nil
}

// processDocumentWithRetry processes a document with retry logic.
// Handles images, PDFs, and text files differently.
func (p *Processor) processDocumentWithRetry(ctx context.Context, doc *telegram.Document, userID int64) (*ProcessedFile, error) {
	// Determine file type based on MIME type
	var fileType FileType
	switch {
	case strings.HasPrefix(doc.MimeType, "image/"):
		fileType = FileTypeImage
	case doc.MimeType == "application/pdf":
		fileType = FileTypePDF
	default:
		fileType = FileTypeDocument
	}

	data, duration, err := p.downloadWithRetry(ctx, doc.FileID, userID, fileType)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	switch fileType {
	case FileTypeImage:
		return &ProcessedFile{
			LLMParts: []interface{}{
				openrouter.ImagePart{
					Type: "image_url",
					ImageURL: openrouter.ImageURL{
						URL: fmt.Sprintf("data:%s;base64,%s", doc.MimeType, base64Data),
					},
				},
			},
			FileType: FileTypeImage,
			FileID:   doc.FileID,
			FileName: doc.FileName,
			MimeType: doc.MimeType,
			Size:     int64(len(data)),
			Duration: duration,
		}, nil

	case FileTypePDF:
		return &ProcessedFile{
			LLMParts: []interface{}{
				openrouter.FilePart{
					Type: "file",
					File: openrouter.File{
						FileName: doc.FileName,
						FileData: fmt.Sprintf("data:application/pdf;base64,%s", base64Data),
					},
				},
			},
			FileType: FileTypePDF,
			FileID:   doc.FileID,
			FileName: doc.FileName,
			MimeType: "application/pdf",
			Size:     int64(len(data)),
			Duration: duration,
		}, nil

	default:
		// Text file - include content as text part
		fileContent := string(data)
		textContent := fmt.Sprintf("%s:\n\n%s", doc.FileName, fileContent)

		return &ProcessedFile{
			LLMParts: []interface{}{
				openrouter.TextPart{
					Type: "text",
					Text: textContent,
				},
			},
			FileType: FileTypeDocument,
			FileID:   doc.FileID,
			FileName: doc.FileName,
			MimeType: doc.MimeType,
			Size:     int64(len(data)),
			Duration: duration,
		}, nil
	}
}

// processVoiceWithRetry processes a voice message with retry logic.
func (p *Processor) processVoiceWithRetry(ctx context.Context, voice *telegram.Voice, userID int64) (*ProcessedFile, error) {
	data, duration, err := p.downloadWithRetry(ctx, voice.FileID, userID, FileTypeVoice)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	mimeType := voice.MimeType
	if mimeType == "" {
		mimeType = "audio/ogg"
	}

	// Get localized instruction for voice handling
	instruction := p.translator.Get(p.language, "bot.voice_instruction")

	return &ProcessedFile{
		LLMParts: []interface{}{
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: "voice.ogg",
					FileData: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data),
				},
			},
		},
		Instruction: instruction,
		FileType:    FileTypeVoice,
		FileID:      voice.FileID,
		MimeType:    mimeType,
		Size:        int64(len(data)),
		Duration:    duration,
	}, nil
}
