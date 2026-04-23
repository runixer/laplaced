package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/telegram"
)

// Processor handles file downloads and processing from Telegram messages.
type Processor struct {
	downloader          telegram.FileDownloader
	translator          *i18n.Translator
	language            string
	logger              *slog.Logger
	fileHandler         FileSaver // Optional: for saving artifacts
	maxRetries          int
	retryDelay          time.Duration
	minVoiceDurationSec int // Minimum voice duration (seconds) to save as artifact. 0 = save all, -1 = disable
}

// FileSaver is the interface for saving artifacts (to avoid circular dependency).
type FileSaver interface {
	// SaveFile saves a file and returns the artifact ID (existing on dedup, new on creation).
	// messageText is the text content of the message (msg.Text or msg.Caption) for context (v0.6.0).
	SaveFile(ctx context.Context, userID int64, messageID int64, fileType string, originalName string, mimeType string, reader io.Reader, messageText string) (*int64, error)
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

// SetFileHandler sets the optional file handler for saving artifacts.
func (p *Processor) SetFileHandler(handler FileSaver) {
	p.fileHandler = handler
}

// SetMinVoiceDurationSec sets the minimum voice duration for saving as artifact.
// 0 = save all voices, -1 = disable voice artifacts, N = only save voices >= N seconds.
func (p *Processor) SetMinVoiceDurationSec(seconds int) {
	p.minVoiceDurationSec = seconds
}

// ProcessMessage extracts and processes the file from a message.
// A Telegram message contains at most one file (photo OR document OR voice OR audio).
// Individual file errors are logged as warnings and nil is returned for that file.
// Returns a slice with 0 or 1 successfully processed files.
// groupText is the full text of all messages in the current MessageGroup (v0.6.0).
func (p *Processor) ProcessMessage(ctx context.Context, msg *telegram.Message, userID int64, groupText string) ([]*ProcessedFile, error) {
	// Debug: Log what we received
	hasPhoto := len(msg.Photo) > 0
	hasDocument := msg.Document != nil
	hasVoice := msg.Voice != nil
	hasAudio := msg.Audio != nil
	hasVideoNote := msg.VideoNote != nil

	if hasPhoto || hasDocument || hasVoice || hasAudio || hasVideoNote {
		p.logger.Debug("processing file from message",
			"user_id", userID,
			"has_photo", hasPhoto,
			"has_document", hasDocument,
			"has_voice", hasVoice,
			"has_audio", hasAudio,
			"has_video_note", hasVideoNote,
		)
	}

	// Process photo (array of sizes, take best quality)
	if len(msg.Photo) > 0 {
		bestPhoto := msg.Photo[len(msg.Photo)-1]
		f, err := p.processPhotoWithRetry(ctx, &bestPhoto, userID, groupText)
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
		f, err := p.processDocumentWithRetry(ctx, msg.Document, userID, groupText)
		if err != nil {
			// Validation errors (unsupported format, too large) should be returned to caller
			// Other errors (download failures) are logged and nil returned
			var unsupportedErr *UnsupportedFormatError
			var tooLargeErr *FileTooLargeError
			switch {
			case errors.As(err, &unsupportedErr), errors.As(err, &tooLargeErr):
				return nil, err
			default:
				p.logger.Warn("failed to process document after retries",
					"error", err,
					"file_id", msg.Document.FileID,
					"file_name", msg.Document.FileName,
					"user_id", userID,
				)
				return nil, nil
			}
		}
		return []*ProcessedFile{f}, nil
	}

	// Process voice message
	if msg.Voice != nil {
		f, err := p.processVoiceWithRetry(ctx, msg.Voice, userID, groupText)
		if err != nil {
			// Validation errors (too large) should be returned to caller
			var tooLargeErr *FileTooLargeError
			if errors.As(err, &tooLargeErr) {
				return nil, err
			}
			p.logger.Warn("failed to process voice after retries",
				"error", err,
				"file_id", msg.Voice.FileID,
				"user_id", userID,
			)
			return nil, nil
		}
		return []*ProcessedFile{f}, nil
	}

	// Process audio file (MP3, etc.)
	if msg.Audio != nil {
		f, err := p.processAudioWithRetry(ctx, msg.Audio, userID, groupText)
		if err != nil {
			// Validation errors (too large) should be returned to caller
			var tooLargeErr *FileTooLargeError
			if errors.As(err, &tooLargeErr) {
				return nil, err
			}
			p.logger.Warn("failed to process audio after retries",
				"error", err,
				"file_id", msg.Audio.FileID,
				"file_name", msg.Audio.FileName,
				"user_id", userID,
			)
			return nil, nil
		}
		return []*ProcessedFile{f}, nil
	}

	// Process video note (video circle)
	if msg.VideoNote != nil {
		f, err := p.processVideoNoteWithRetry(ctx, msg.VideoNote, userID, groupText)
		if err != nil {
			// Validation errors (too large) should be returned to caller
			var tooLargeErr *FileTooLargeError
			if errors.As(err, &tooLargeErr) {
				return nil, err
			}
			p.logger.Warn("failed to process video note after retries",
				"error", err,
				"file_id", msg.VideoNote.FileID,
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
func (p *Processor) processPhotoWithRetry(ctx context.Context, photo *telegram.PhotoSize, userID int64, groupText string) (*ProcessedFile, error) {
	data, duration, err := p.downloadWithRetry(ctx, photo.FileID, userID, FileTypePhoto)
	if err != nil {
		return nil, err
	}

	p.logger.Debug("downloaded photo file",
		"user_id", userID,
		"file_id", photo.FileID,
		"size", len(data),
		"duration_ms", duration.Milliseconds(),
	)

	base64Data := base64.StdEncoding.EncodeToString(data)

	// Save artifact if file handler is set
	var artifactID *int64
	if p.fileHandler != nil {
		reader := bytes.NewReader(data)
		artifactID, err = p.fileHandler.SaveFile(ctx, userID, 0, "image", "photo.jpg", "image/jpeg", reader, groupText)
		if err != nil {
			p.logger.Warn("failed to save artifact", "error", err, "file_id", photo.FileID, "user_id", userID)
			// Don't fail the request, just log
		}
	}

	return &ProcessedFile{
		LLMParts: []interface{}{
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: "photo.jpg",
					FileData: fmt.Sprintf("data:image/jpeg;base64,%s", base64Data),
				},
			},
		},
		FileType:   FileTypePhoto,
		FileID:     photo.FileID,
		MimeType:   "image/jpeg",
		Size:       int64(len(data)),
		Duration:   duration,
		ArtifactID: artifactID,
	}, nil
}

// processDocumentWithRetry processes a document with retry logic.
// Handles images, PDFs, videos, and text files differently.
func (p *Processor) processDocumentWithRetry(ctx context.Context, doc *telegram.Document, userID int64, groupText string) (*ProcessedFile, error) {
	// Validate MIME type BEFORE download (Gemini support)
	if !IsGeminiSupported(doc.MimeType) {
		p.logger.Info("unsupported file format",
			"user_id", userID,
			"file_id", doc.FileID,
			"file_name", doc.FileName,
			"mime_type", doc.MimeType,
		)
		return nil, &UnsupportedFormatError{
			MimeType: doc.MimeType,
			FileName: doc.FileName,
		}
	}

	// Validate file size BEFORE download (Telegram Bot API limit: 20MB)
	if doc.FileSize > 0 && !IsFileSizeAllowed(int64(doc.FileSize)) {
		p.logger.Info("file too large",
			"user_id", userID,
			"file_id", doc.FileID,
			"file_name", doc.FileName,
			"size", doc.FileSize,
			"max_size", MaxFileSize(),
		)
		return nil, &FileTooLargeError{
			Size:     int64(doc.FileSize),
			FileName: doc.FileName,
		}
	}

	// Determine file type based on MIME type
	var fileType FileType
	var artifactFileType string
	switch {
	case strings.HasPrefix(doc.MimeType, "image/"):
		fileType = FileTypeImage
		artifactFileType = "image"
	case doc.MimeType == "application/pdf":
		fileType = FileTypePDF
		artifactFileType = "pdf"
	case strings.HasPrefix(doc.MimeType, "video/"):
		fileType = FileTypeVideo
		artifactFileType = "video"
	default:
		fileType = FileTypeDocument
		artifactFileType = "document"
	}

	p.logger.Debug("detected document file type",
		"user_id", userID,
		"file_id", doc.FileID,
		"file_name", doc.FileName,
		"mime_type", doc.MimeType,
		"file_type", fileType,
		"artifact_type", artifactFileType,
	)

	data, duration, err := p.downloadWithRetry(ctx, doc.FileID, userID, fileType)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	// Save artifact if file handler is set
	var artifactID *int64
	if p.fileHandler != nil {
		reader := bytes.NewReader(data)
		artifactID, err = p.fileHandler.SaveFile(ctx, userID, 0, artifactFileType, doc.FileName, doc.MimeType, reader, groupText)
		if err != nil {
			p.logger.Warn("failed to save artifact", "error", err, "file_id", doc.FileID, "file_name", doc.FileName, "user_id", userID)
			// Don't fail the request, just log
		}
	}

	switch fileType {
	case FileTypeVideo:
		p.logger.Debug("detected video file",
			"user_id", userID,
			"file_id", doc.FileID,
			"file_name", doc.FileName,
			"mime_type", doc.MimeType,
			"size", len(data),
		)

		mimeType := doc.MimeType
		if mimeType == "" {
			mimeType = "video/mp4"
		}

		return &ProcessedFile{
			LLMParts: []interface{}{
				openrouter.FilePart{
					Type: "file",
					File: openrouter.File{
						FileName: doc.FileName,
						FileData: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data),
					},
				},
			},
			FileType:   FileTypeVideo,
			FileID:     doc.FileID,
			FileName:   doc.FileName,
			MimeType:   doc.MimeType,
			Size:       int64(len(data)),
			Duration:   duration,
			ArtifactID: artifactID,
		}, nil

	case FileTypeImage:
		return &ProcessedFile{
			LLMParts: []interface{}{
				openrouter.FilePart{
					Type: "file",
					File: openrouter.File{
						FileName: doc.FileName,
						FileData: fmt.Sprintf("data:%s;base64,%s", doc.MimeType, base64Data),
					},
				},
			},
			FileType:   FileTypeImage,
			FileID:     doc.FileID,
			FileName:   doc.FileName,
			MimeType:   doc.MimeType,
			Size:       int64(len(data)),
			Duration:   duration,
			ArtifactID: artifactID,
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
			FileType:   FileTypePDF,
			FileID:     doc.FileID,
			FileName:   doc.FileName,
			MimeType:   "application/pdf",
			Size:       int64(len(data)),
			Duration:   duration,
			ArtifactID: artifactID,
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
			FileType:   FileTypeDocument,
			FileID:     doc.FileID,
			FileName:   doc.FileName,
			MimeType:   doc.MimeType,
			Size:       int64(len(data)),
			Duration:   duration,
			ArtifactID: artifactID,
		}, nil
	}
}

// processVoiceWithRetry processes a voice message with retry logic.
func (p *Processor) processVoiceWithRetry(ctx context.Context, voice *telegram.Voice, userID int64, groupText string) (*ProcessedFile, error) {
	// Validate file size BEFORE download (Telegram Bot API limit: 20MB)
	if voice.FileSize > 0 && !IsFileSizeAllowed(int64(voice.FileSize)) {
		p.logger.Info("voice file too large",
			"user_id", userID,
			"file_id", voice.FileID,
			"size", voice.FileSize,
			"max_size", MaxFileSize(),
		)
		return nil, &FileTooLargeError{
			Size:     int64(voice.FileSize),
			FileName: "voice.ogg",
		}
	}

	data, duration, err := p.downloadWithRetry(ctx, voice.FileID, userID, FileTypeVoice)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	mimeType := voice.MimeType
	if mimeType == "" {
		mimeType = "audio/ogg"
	}

	p.logger.Debug("detected voice file",
		"user_id", userID,
		"file_id", voice.FileID,
		"mime_type", mimeType,
		"size", len(data),
		"duration_ms", duration.Milliseconds(),
	)

	// Save artifact if file handler is set and duration meets threshold
	// -1 = disabled, 0 = save all, N = only save voices >= N seconds
	var artifactID *int64
	if p.fileHandler != nil {
		shouldSave := false
		switch {
		case p.minVoiceDurationSec == -1:
			// Voice artifacts disabled
			p.logger.Debug("voice artifacts disabled, skipping save",
				"user_id", userID,
				"duration", voice.Duration,
			)
		case p.minVoiceDurationSec == 0 || voice.Duration >= p.minVoiceDurationSec:
			shouldSave = true
		default:
			p.logger.Debug("voice too short, skipping artifact save",
				"user_id", userID,
				"duration", voice.Duration,
				"min_duration", p.minVoiceDurationSec,
			)
		}

		if shouldSave {
			reader := bytes.NewReader(data)
			artifactID, err = p.fileHandler.SaveFile(ctx, userID, 0, "voice", "voice.ogg", mimeType, reader, groupText)
			if err != nil {
				p.logger.Warn("failed to save artifact", "error", err, "file_id", voice.FileID, "user_id", userID)
				// Don't fail the request, just log
			}
		}
	}

	// Get localized instruction for voice handling
	instruction := p.translator.Get(p.language, "bot.voice_instruction")

	p.logger.Debug("sending voice with file format",
		"user_id", userID,
		"mime_type", mimeType,
		"size", len(data),
	)

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
		ArtifactID:  artifactID,
	}, nil
}

// processAudioWithRetry processes an audio file (MP3, etc.) with retry logic.
func (p *Processor) processAudioWithRetry(ctx context.Context, audio *telegram.Audio, userID int64, groupText string) (*ProcessedFile, error) {
	// Validate file size BEFORE download (Telegram Bot API limit: 20MB)
	if audio.FileSize > 0 && !IsFileSizeAllowed(int64(audio.FileSize)) {
		p.logger.Info("audio file too large",
			"user_id", userID,
			"file_id", audio.FileID,
			"file_name", audio.FileName,
			"size", audio.FileSize,
			"max_size", MaxFileSize(),
		)
		return nil, &FileTooLargeError{
			Size:     int64(audio.FileSize),
			FileName: audio.FileName,
		}
	}

	data, duration, err := p.downloadWithRetry(ctx, audio.FileID, userID, FileTypeAudio)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	mimeType := audio.MimeType
	if mimeType == "" {
		mimeType = "audio/mpeg"
	}

	// Determine filename from metadata or default
	filename := audio.FileName
	if filename == "" {
		// Use title + performer if available, else generic name
		switch {
		case audio.Title != "" && audio.Performers != "":
			filename = fmt.Sprintf("%s - %s.mp3", audio.Performers, audio.Title)
		case audio.Title != "":
			filename = audio.Title + ".mp3"
		default:
			filename = "audio.mp3"
		}
	}

	// Extract audio format from filename or mime type
	// OpenRouter expects: "mp3", "wav", "ogg", etc. (NOT "audio/mpeg")
	audioFormat := "mp3" // default
	if filename != "" {
		ext := filepath.Ext(filename)
		if ext != "" {
			audioFormat = strings.TrimPrefix(ext, ".")
		}
	}

	p.logger.Debug("detected audio file",
		"user_id", userID,
		"file_id", audio.FileID,
		"file_name", filename,
		"mime_type", mimeType,
		"audio_format", audioFormat,
		"performer", audio.Performers,
		"title", audio.Title,
		"size", len(data),
		"duration_ms", duration.Milliseconds(),
	)

	// Save artifact if file handler is set
	var artifactID *int64
	if p.fileHandler != nil {
		reader := bytes.NewReader(data)
		artifactID, err = p.fileHandler.SaveFile(ctx, userID, 0, "audio", filename, mimeType, reader, groupText)
		if err != nil {
			p.logger.Warn("failed to save artifact", "error", err, "file_id", audio.FileID, "file_name", filename, "user_id", userID)
			// Don't fail the request, just log
		}
	}

	p.logger.Debug("sending audio with file format",
		"user_id", userID,
		"mime_type", mimeType,
		"size", len(data),
	)

	return &ProcessedFile{
		LLMParts: []interface{}{
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: filename,
					FileData: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data),
				},
			},
		},
		FileType:   FileTypeAudio,
		FileID:     audio.FileID,
		FileName:   filename,
		MimeType:   mimeType,
		Size:       int64(len(data)),
		Duration:   duration,
		ArtifactID: artifactID,
	}, nil
}

// processVideoNoteWithRetry processes a video note (video circle) with retry logic.
func (p *Processor) processVideoNoteWithRetry(ctx context.Context, videoNote *telegram.VideoNote, userID int64, groupText string) (*ProcessedFile, error) {
	// Validate file size BEFORE download (Telegram Bot API limit: 20MB)
	if videoNote.FileSize > 0 && !IsFileSizeAllowed(int64(videoNote.FileSize)) {
		p.logger.Info("video note file too large",
			"user_id", userID,
			"file_id", videoNote.FileID,
			"size", videoNote.FileSize,
			"max_size", MaxFileSize(),
		)
		return nil, &FileTooLargeError{
			Size:     int64(videoNote.FileSize),
			FileName: "video_note.mp4",
		}
	}

	data, duration, err := p.downloadWithRetry(ctx, videoNote.FileID, userID, FileTypeVideoNote)
	if err != nil {
		return nil, err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	// Video notes are always MP4
	mimeType := "video/mp4"
	filename := fmt.Sprintf("video_note_%s.mp4", videoNote.FileUniqueID)

	p.logger.Debug("detected video note file",
		"user_id", userID,
		"file_id", videoNote.FileID,
		"length", videoNote.Length,
		"duration_sec", videoNote.Duration,
		"size", len(data),
	)

	// Save artifact if file handler is set
	var artifactID *int64
	if p.fileHandler != nil {
		reader := bytes.NewReader(data)
		artifactID, err = p.fileHandler.SaveFile(ctx, userID, 0, "video_note", filename, mimeType, reader, groupText)
		if err != nil {
			p.logger.Warn("failed to save artifact", "error", err, "file_id", videoNote.FileID, "user_id", userID)
			// Don't fail the request, just log
		}
	}

	return &ProcessedFile{
		LLMParts: []interface{}{
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: filename,
					FileData: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data),
				},
			},
		},
		FileType:   FileTypeVideoNote,
		FileID:     videoNote.FileID,
		FileName:   filename,
		MimeType:   mimeType,
		Size:       int64(len(data)),
		Duration:   duration,
		ArtifactID: artifactID,
	}, nil
}
