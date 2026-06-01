package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// IncomingFile is a transport-neutral description of a single attachment plus a
// Fetch closure that lazily pulls its bytes (encapsulating transport-specific
// download + retry). Telegram populates these via ExtractFiles; other
// transports build them directly. v0.10: only Telegram produces files; the
// Mattermost/Time PoC is text-only and passes an empty slice.
//
// Validation metadata (MIME, Size, Duration) is carried so ProcessFiles can
// reject unsupported/oversized files BEFORE invoking Fetch — preserving the
// pre-download validation the Telegram path always did.
type IncomingFile struct {
	Kind     FileType
	SourceID string // transport file id (Telegram file_id); used for ProcessedFile.FileID + logging
	FileName string // declared filename where the kind carries one; "" otherwise
	MIME     string // declared/resolved MIME type
	Size     int64  // declared size in bytes (pre-download), for validation; 0 if unknown
	Duration int    // duration in seconds (voice gating); 0 otherwise
	Fetch    func(ctx context.Context) ([]byte, error)
}

// ProcessFiles turns transport-neutral IncomingFiles into ProcessedFiles
// (LLM parts + artifact rows). It is the neutral core shared by all transports;
// the Telegram entry point ProcessMessage delegates here via ExtractFiles.
//
// Behavior matches the legacy per-type Telegram pipeline: validation errors
// (unsupported format, too large) are returned to the caller; download failures
// are logged and the file is skipped (not fatal). A Telegram message carries at
// most one file, so for that path the slice has 0 or 1 element.
func (p *Processor) ProcessFiles(ctx context.Context, incoming []IncomingFile, userID storage.ScopeID, groupText string) ([]*ProcessedFile, error) {
	var out []*ProcessedFile
	for i := range incoming {
		f := incoming[i]
		processed, err := p.processOne(ctx, f, userID, groupText)
		if err != nil {
			// Validation errors propagate to the caller (user-facing message);
			// download failures are surfaced the same way the legacy per-type
			// methods did (logged + skipped) — see processOne.
			return nil, err
		}
		if processed != nil {
			out = append(out, processed)
		}
	}
	return out, nil
}

// processOne validates, fetches, optionally archives, and assembles a single
// file. Returns (nil, nil) when a download fails after retries (logged, skipped)
// and (nil, err) for validation failures.
func (p *Processor) processOne(ctx context.Context, f IncomingFile, userID storage.ScopeID, groupText string) (*ProcessedFile, error) {
	// Pre-fetch validation, matching the legacy per-type methods.
	switch f.Kind {
	case FileTypeImage, FileTypePDF, FileTypeVideo, FileTypeDocument:
		// Document-derived kinds: Gemini support + size, validated before download.
		if !IsGeminiSupported(f.MIME) {
			p.logger.Info("unsupported file format",
				"user_id", userID, "file_id", f.SourceID, "file_name", f.FileName, "mime_type", f.MIME,
			)
			return nil, &UnsupportedFormatError{MimeType: f.MIME, FileName: f.FileName}
		}
		if f.Size > 0 && !IsFileSizeAllowed(f.Size) {
			p.logger.Info("file too large",
				"user_id", userID, "file_id", f.SourceID, "file_name", f.FileName,
				"size", f.Size, "max_size", MaxFileSize(),
			)
			return nil, &FileTooLargeError{Size: f.Size, FileName: f.FileName}
		}
	case FileTypeVoice, FileTypeAudio, FileTypeVideoNote:
		// Media kinds: size only.
		if f.Size > 0 && !IsFileSizeAllowed(f.Size) {
			p.logger.Info("file too large",
				"user_id", userID, "file_id", f.SourceID, "size", f.Size, "max_size", MaxFileSize(),
			)
			return nil, &FileTooLargeError{Size: f.Size, FileName: f.FileName}
		}
	case FileTypePhoto:
		// Photos are always small JPEGs; no validation (legacy parity).
	}

	start := time.Now()
	data, err := f.Fetch(ctx)
	duration := time.Since(start)
	if err != nil {
		p.logger.Warn("failed to fetch file after retries",
			"error", err, "file_id", f.SourceID, "kind", f.Kind, "user_id", userID,
		)
		return nil, nil // skip — non-fatal, matches legacy download-failure handling
	}

	base64Data := base64.StdEncoding.EncodeToString(data)

	switch f.Kind {
	case FileTypePhoto:
		artifactID := p.saveArtifact(ctx, userID, "image", "photo.jpg", "image/jpeg", data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart("photo.jpg", "image/jpeg", base64Data)},
			FileType: FileTypePhoto, FileID: f.SourceID, MimeType: "image/jpeg",
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	case FileTypeImage:
		artifactID := p.saveArtifact(ctx, userID, "image", f.FileName, f.MIME, data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, f.MIME, base64Data)},
			FileType: FileTypeImage, FileID: f.SourceID, FileName: f.FileName, MimeType: f.MIME,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	case FileTypePDF:
		artifactID := p.saveArtifact(ctx, userID, "pdf", f.FileName, f.MIME, data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, "application/pdf", base64Data)},
			FileType: FileTypePDF, FileID: f.SourceID, FileName: f.FileName, MimeType: "application/pdf",
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	case FileTypeVideo:
		mimeType := f.MIME
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		artifactID := p.saveArtifact(ctx, userID, "video", f.FileName, f.MIME, data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, mimeType, base64Data)},
			FileType: FileTypeVideo, FileID: f.SourceID, FileName: f.FileName, MimeType: f.MIME,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	case FileTypeVideoNote:
		artifactID := p.saveArtifact(ctx, userID, "video_note", f.FileName, "video/mp4", data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, "video/mp4", base64Data)},
			FileType: FileTypeVideoNote, FileID: f.SourceID, FileName: f.FileName, MimeType: "video/mp4",
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	case FileTypeVoice:
		mimeType := f.MIME
		if mimeType == "" {
			mimeType = "audio/ogg"
		}
		artifactID := p.saveVoiceArtifact(ctx, userID, mimeType, data, groupText, f.SourceID, f.Duration)
		return &ProcessedFile{
			LLMParts:    []interface{}{p.mediaPart("voice.ogg", mimeType, base64Data)},
			Instruction: p.translator.Get(p.language, "bot.voice_instruction"),
			FileType:    FileTypeVoice, FileID: f.SourceID, MimeType: mimeType,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	case FileTypeAudio:
		mimeType := f.MIME
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		artifactID := p.saveArtifact(ctx, userID, "audio", f.FileName, mimeType, data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, mimeType, base64Data)},
			FileType: FileTypeAudio, FileID: f.SourceID, FileName: f.FileName, MimeType: mimeType,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil

	default: // FileTypeDocument — text file inlined as a text part
		textContent := fmt.Sprintf("%s:\n\n%s", f.FileName, string(data))
		artifactID := p.saveArtifact(ctx, userID, "document", f.FileName, f.MIME, data, groupText, f.SourceID)
		return &ProcessedFile{
			LLMParts: []interface{}{openrouter.TextPart{Type: "text", Text: textContent}},
			FileType: FileTypeDocument, FileID: f.SourceID, FileName: f.FileName, MimeType: f.MIME,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
		}, nil
	}
}

// mediaPart builds the LLM content part for an inbound file in the configured
// backend format: image_url/video_url for OpenAI-compatible backends, else the
// `file` part (also the shape for pdf/audio regardless of format). The single
// chokepoint for inbound media encoding.
func (p *Processor) mediaPart(fileName, mimeType, base64Data string) interface{} {
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)
	return openrouter.MediaPart(p.imageInputFormat, mimeType, fileName, dataURL)
}

// saveArtifact persists a file as an artifact when a file handler is configured.
// Returns the artifact id (nil on disabled handler or save failure — non-fatal).
func (p *Processor) saveArtifact(ctx context.Context, userID storage.ScopeID, artifactType, fileName, mimeType string, data []byte, groupText, sourceID string) *int64 {
	if p.fileHandler == nil {
		return nil
	}
	artifactID, err := p.fileHandler.SaveFile(ctx, userID, 0, artifactType, fileName, mimeType, bytes.NewReader(data), groupText)
	if err != nil {
		p.logger.Warn("failed to save artifact", "error", err, "file_id", sourceID, "file_name", fileName, "user_id", userID)
		return nil
	}
	return artifactID
}

// saveVoiceArtifact applies the voice-duration gating before saving:
// -1 disables voice artifacts, 0 saves all, N saves voices >= N seconds.
func (p *Processor) saveVoiceArtifact(ctx context.Context, userID storage.ScopeID, mimeType string, data []byte, groupText, sourceID string, durationSec int) *int64 {
	if p.fileHandler == nil {
		return nil
	}
	switch {
	case p.minVoiceDurationSec == -1:
		p.logger.Debug("voice artifacts disabled, skipping save", "user_id", userID, "duration", durationSec)
		return nil
	case p.minVoiceDurationSec == 0 || durationSec >= p.minVoiceDurationSec:
		return p.saveArtifact(ctx, userID, "voice", "voice.ogg", mimeType, data, groupText, sourceID)
	default:
		p.logger.Debug("voice too short, skipping artifact save",
			"user_id", userID, "duration", durationSec, "min_duration", p.minVoiceDurationSec)
		return nil
	}
}

// audioFilename synthesizes a filename for an audio attachment lacking one,
// preferring "performer - title.mp3", then "title.mp3", else "audio.mp3".
func audioFilename(name, title, performers string) string {
	if name != "" {
		return name
	}
	switch {
	case title != "" && performers != "":
		return fmt.Sprintf("%s - %s.mp3", performers, title)
	case title != "":
		return title + ".mp3"
	default:
		return "audio.mp3"
	}
}
