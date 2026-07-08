package files

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// Processor handles file downloads and processing from messages.
type Processor struct {
	downloader          telegram.FileDownloader
	translator          *i18n.Translator
	language            string
	logger              *slog.Logger
	fileHandler         FileSaver // Optional: for saving artifacts
	maxRetries          int
	retryDelay          time.Duration
	minVoiceDurationSec int    // Min voice duration (sec) to RAG-index. 0 = index all, -1 = disable voice artifacts, N = index >= N (shorter still retained raw for replay)
	imageInputFormat    string // How image/video parts are encoded for the LLM (llm.ImageInputFormat*). "" = file.
}

// FileSaver is the interface for saving artifacts (to avoid circular dependency).
type FileSaver interface {
	// SaveFile saves a file and returns the artifact ID (existing on dedup, new on creation).
	// messageText is the text content of the message (msg.Text or msg.Caption) for context (v0.6.0).
	SaveFile(ctx context.Context, userID storage.ScopeID, messageID int64, fileType string, originalName string, mimeType string, reader io.Reader, messageText string, skipExtraction bool) (*int64, error)
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

// SetImageInputFormat selects how image/video attachments are encoded as LLM
// content parts (llm.ImageInputFormatFile | ImageInputFormatOpenAI).
func (p *Processor) SetImageInputFormat(format string) {
	p.imageInputFormat = format
}

// ProcessMessage extracts and processes the file from a Telegram message.
// It is the Telegram adapter over the transport-neutral pipeline: it maps the
// message to IncomingFiles (ExtractFiles) and runs them through ProcessFiles.
// A Telegram message contains at most one file, so the result has 0 or 1 entry.
// groupText is the full text of all messages in the current MessageGroup (v0.6.0).
func (p *Processor) ProcessMessage(ctx context.Context, msg *telegram.Message, userID storage.ScopeID, groupText string) ([]*ProcessedFile, error) {
	return p.ProcessFiles(ctx, p.ExtractFiles(msg, userID), userID, groupText)
}

// ExtractFiles maps a Telegram message's single attachment (if any) to a
// transport-neutral IncomingFile, with a Fetch closure that downloads on demand
// via downloadWithRetry. Returns an empty slice when the message has no file.
// Priority matches the legacy dispatch: photo, document, voice, audio, video note.
func (p *Processor) ExtractFiles(msg *telegram.Message, userID storage.ScopeID) []IncomingFile {
	fetch := func(fileID string, kind FileType) func(context.Context) ([]byte, error) {
		return func(ctx context.Context) ([]byte, error) {
			data, _, err := p.downloadWithRetry(ctx, fileID, userID, kind)
			return data, err
		}
	}

	switch {
	case len(msg.Photo) > 0:
		best := msg.Photo[len(msg.Photo)-1]
		return []IncomingFile{{
			Kind: FileTypePhoto, SourceID: best.FileID,
			Fetch: fetch(best.FileID, FileTypePhoto),
		}}

	case msg.Document != nil:
		doc := msg.Document
		var kind FileType
		switch {
		case strings.HasPrefix(doc.MimeType, "image/"):
			kind = FileTypeImage
		case doc.MimeType == "application/pdf":
			kind = FileTypePDF
		case strings.HasPrefix(doc.MimeType, "video/"):
			kind = FileTypeVideo
		default:
			kind = FileTypeDocument
		}
		return []IncomingFile{{
			Kind: kind, SourceID: doc.FileID, FileName: doc.FileName, MIME: doc.MimeType,
			Size: int64(doc.FileSize), Fetch: fetch(doc.FileID, kind),
		}}

	case msg.Voice != nil:
		v := msg.Voice
		return []IncomingFile{{
			Kind: FileTypeVoice, SourceID: v.FileID, FileName: "voice.ogg", MIME: v.MimeType,
			Size: int64(v.FileSize), Duration: v.Duration, Fetch: fetch(v.FileID, FileTypeVoice),
		}}

	case msg.Audio != nil:
		a := msg.Audio
		return []IncomingFile{{
			Kind: FileTypeAudio, SourceID: a.FileID,
			FileName: audioFilename(a.FileName, a.Title, a.Performers), MIME: a.MimeType,
			Size: int64(a.FileSize), Fetch: fetch(a.FileID, FileTypeAudio),
		}}

	case msg.VideoNote != nil:
		vn := msg.VideoNote
		return []IncomingFile{{
			Kind: FileTypeVideoNote, SourceID: vn.FileID,
			FileName: fmt.Sprintf("video_note_%s.mp4", vn.FileUniqueID),
			Size:     int64(vn.FileSize), Fetch: fetch(vn.FileID, FileTypeVideoNote),
		}}
	}

	return nil
}

// downloadWithRetry attempts to download a file with retries and exponential backoff.
func (p *Processor) downloadWithRetry(ctx context.Context, fileID string, userID storage.ScopeID, fileType FileType) ([]byte, time.Duration, error) {
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
