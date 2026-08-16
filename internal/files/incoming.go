package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// IncomingFile is a transport-neutral description of a single attachment plus a
// Fetch closure that lazily pulls its bytes (encapsulating transport-specific
// download + retry). Telegram populates these via ExtractFiles/ExtractRichMedia;
// other transports build them directly.
//
// Validation metadata (MIME, Size, Duration) is carried so ProcessFiles can
// reject unsupported/oversized files BEFORE invoking Fetch — preserving the
// pre-download validation the Telegram path always did.
type IncomingFile struct {
	Kind         FileType
	SourceID     string // transport file id (Telegram file_id); used for ProcessedFile.FileID + logging
	FileUniqueID string // stable transport identity; non-empty values coalesce downloads within one call
	FetchKey     string // transport reference useful for diagnostics/markers; never used as a dedupe key
	Origin       string // low-cardinality source such as "telegram_rich" or "mattermost"
	Ordinal      int    // semantic occurrence ordinal assigned by the transport/projector
	BlockPath    string // semantic source path assigned by a recursive rich projector
	Marker       string // stable textual marker emitted by the rich projector
	FileName     string // declared filename where the kind carries one; "" otherwise
	MIME         string // declared/resolved MIME type
	Size         int64  // declared size in bytes (pre-download), for validation; 0 if unknown
	Duration     int    // duration in seconds (voice gating); 0 otherwise
	// Fetch must honor maxBytes when it is positive and reject an oversized
	// source before returning a buffer larger than that limit. maxBytes==0 asks
	// for the transport's ordinary per-file policy (the legacy single-file path).
	Fetch func(ctx context.Context, maxBytes int64) ([]byte, error)
}

// ProcessFileStatus is the per-occurrence disposition returned by
// ProcessFilesDetailed. It is intentionally low-cardinality so callers can
// safely reuse it as an observability label.
type ProcessFileStatus string

const (
	ProcessFileAvailable    ProcessFileStatus = "available"
	ProcessFileOmittedLimit ProcessFileStatus = "omitted_limit"
	ProcessFileUnsupported  ProcessFileStatus = "unsupported"
	ProcessFileFailed       ProcessFileStatus = "failed"
	ProcessFileDuplicateRef ProcessFileStatus = "duplicate_ref"
)

const maxConcurrentFileFetches = 3

// ProcessFileResult preserves one result for every incoming occurrence and in
// the same order. DuplicateOf is a result-slice index, or -1 when this is not a
// duplicate. A duplicate_ref deliberately has no Processed value: its binary
// payload is represented by the referenced available occurrence while its
// own semantic metadata remains available in Incoming.
type ProcessFileResult struct {
	Incoming       IncomingFile
	Processed      *ProcessedFile
	Status         ProcessFileStatus
	Err            error
	DuplicateOf    int
	DownloadedSize int64
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

type detailedFetchResult struct {
	data     []byte
	duration time.Duration
	err      error
}

type detailedFetchJob struct {
	index       int
	reservation int64
}

// ProcessFilesDetailed processes a multi-occurrence rich attachment set under
// a single 20 MiB aggregate budget. Downloads are coalesced by a non-empty
// FileUniqueID, run with at most three concurrent fetches, and results always
// correspond one-for-one with incoming in input order.
//
// Unlike the legacy ProcessFiles wrapper, validation and download failures are
// per-occurrence outcomes and never abort later siblings.
func (p *Processor) ProcessFilesDetailed(
	ctx context.Context,
	incoming []IncomingFile,
	userID storage.ScopeID,
	groupText string,
) []ProcessFileResult {
	results := make([]ProcessFileResult, len(incoming))
	normalized := make([]IncomingFile, len(incoming))
	duplicates := make([]int, len(incoming))
	for i := range duplicates {
		duplicates[i] = -1
	}

	canonicalByUniqueID := make(map[string]int)
	candidates := make([]int, 0, len(incoming))
	for i := range incoming {
		f := normalizeIncomingFile(incoming[i])
		normalized[i] = f
		results[i] = ProcessFileResult{Incoming: f, DuplicateOf: -1}

		if err := validateIncomingFile(f); err != nil {
			results[i].Status = statusForProcessError(err)
			results[i].Err = err
			p.logRejectedIncomingFile(userID, f, err)
			continue
		}
		if f.Fetch == nil {
			results[i].Status = ProcessFileFailed
			results[i].Err = fmt.Errorf("file %q has no fetch function", f.SourceID)
			continue
		}

		if key := richDedupeKey(f); key != "" {
			if canonical, ok := canonicalByUniqueID[key]; ok {
				duplicates[i] = canonical
				continue
			}
			canonicalByUniqueID[key] = i
		}
		candidates = append(candidates, i)
	}

	// The scheduler reserves declared bytes in occurrence order. Unknown-size
	// files reserve all currently available bytes, so they never run beside a
	// download that could make the aggregate bound unknowable. A completed job
	// releases its reservation and commits its actual byte count, allowing later
	// smaller siblings to proceed after an earlier failure or overestimate.
	fetched := make([]detailedFetchResult, len(incoming))
	done := make(chan detailedFetchJob, maxConcurrentFileFetches)
	committedBytes := int64(0)
	reservedBytes := int64(0)
	active := 0
	next := 0

	for next < len(candidates) || active > 0 {
		for active < maxConcurrentFileFetches && next < len(candidates) {
			index := candidates[next]
			f := normalized[index]
			remaining := MaxFileSize() - committedBytes - reservedBytes
			reservation := f.Size
			if reservation <= 0 {
				// An unknown-size file can consume the entire remaining budget;
				// wait until no other reservation is active before starting it.
				if active > 0 {
					break
				}
				reservation = remaining
			}

			if reservation <= 0 || reservation > remaining {
				if active > 0 {
					// Earlier active jobs may use less than their declarations or
					// fail; decide this occurrence after one of them completes.
					break
				}
				results[index].Status = ProcessFileOmittedLimit
				results[index].Err = &AggregateBudgetExceededError{
					Size: f.Size, Remaining: max64(remaining, 0), FileName: f.FileName,
				}
				next++
				continue
			}

			job := detailedFetchJob{index: index, reservation: reservation}
			reservedBytes += reservation
			active++
			next++
			go func(f IncomingFile, index int, job detailedFetchJob) {
				start := time.Now()
				data, err := f.Fetch(ctx, job.reservation)
				fetched[index] = detailedFetchResult{data: data, duration: time.Since(start), err: err}
				done <- job
			}(f, index, job)
		}

		if active == 0 {
			continue
		}

		job := <-done
		active--
		reservedBytes -= job.reservation
		fetchResult := fetched[job.index]
		f := normalized[job.index]
		if fetchResult.err != nil {
			fetched[job.index].data = nil
			if errors.Is(fetchResult.err, telegram.ErrFileDownloadTooLarge) {
				results[job.index].Status = ProcessFileOmittedLimit
				if f.Size > 0 {
					results[job.index].Err = &DeclaredFileSizeExceededError{
						Declared: f.Size,
						Actual:   job.reservation + 1,
						FileName: f.FileName,
					}
				} else {
					results[job.index].Err = &AggregateBudgetExceededError{
						Size:      job.reservation + 1,
						Remaining: job.reservation,
						FileName:  f.FileName,
					}
				}
			} else {
				results[job.index].Status = ProcessFileFailed
				results[job.index].Err = fetchResult.err
			}
			p.logRichIncomingFileIssue("rich file fetch failed", f, results[job.index].Err)
			continue
		}

		actualSize := int64(len(fetchResult.data))
		results[job.index].DownloadedSize = actualSize
		if actualSize > MaxFileSize() {
			fetched[job.index].data = nil
			results[job.index].Status = ProcessFileOmittedLimit
			results[job.index].Err = &FileTooLargeError{Size: actualSize, FileName: f.FileName}
			continue
		}
		if actualSize > job.reservation {
			fetched[job.index].data = nil
			results[job.index].Status = ProcessFileOmittedLimit
			if f.Size > 0 {
				results[job.index].Err = &DeclaredFileSizeExceededError{
					Declared: f.Size,
					Actual:   actualSize,
					FileName: f.FileName,
				}
			} else {
				results[job.index].Err = &AggregateBudgetExceededError{
					Size: actualSize, Remaining: job.reservation, FileName: f.FileName,
				}
			}
			continue
		}
		if actualSize > MaxFileSize()-committedBytes-reservedBytes {
			fetched[job.index].data = nil
			results[job.index].Status = ProcessFileOmittedLimit
			results[job.index].Err = &AggregateBudgetExceededError{
				Size:      actualSize,
				Remaining: max64(MaxFileSize()-committedBytes-reservedBytes, 0),
				FileName:  f.FileName,
			}
			continue
		}
		committedBytes += actualSize
	}

	// Materialize LLM/artifact values in input order. Downloads are concurrent,
	// but observable processing and returned parts remain deterministic.
	for _, index := range candidates {
		if results[index].Status != "" {
			continue
		}
		fetchResult := fetched[index]
		processed, err := p.processData(ctx, normalized[index], fetchResult.data, fetchResult.duration, userID, groupText)
		fetched[index].data = nil
		if err != nil {
			results[index].Status = statusForProcessError(err)
			results[index].Err = err
			p.logRejectedIncomingFile(userID, normalized[index], err)
			continue
		}
		results[index].Status = ProcessFileAvailable
		results[index].Processed = processed
	}

	for i, canonical := range duplicates {
		if canonical < 0 {
			continue
		}
		results[i].DuplicateOf = canonical
		if results[canonical].Status == ProcessFileAvailable {
			results[i].Status = ProcessFileDuplicateRef
			continue
		}
		// The shared bytes were not available, so this occurrence receives the
		// same concrete failure instead of pretending to be a usable reference.
		results[i].Status = results[canonical].Status
		results[i].Err = results[canonical].Err
	}

	return results
}

func richDedupeKey(f IncomingFile) string {
	return f.FileUniqueID
}

func statusForProcessError(err error) ProcessFileStatus {
	var tooLarge *FileTooLargeError
	var aggregate *AggregateBudgetExceededError
	var sizeMismatch *DeclaredFileSizeExceededError
	var unsupported *UnsupportedFormatError
	switch {
	case errors.As(err, &tooLarge), errors.As(err, &aggregate), errors.As(err, &sizeMismatch):
		return ProcessFileOmittedLimit
	case errors.As(err, &unsupported):
		return ProcessFileUnsupported
	default:
		return ProcessFileFailed
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func normalizeIncomingFile(f IncomingFile) IncomingFile {
	f.MIME = strings.ToLower(strings.TrimSpace(strings.SplitN(f.MIME, ";", 2)[0]))
	switch f.Kind {
	case FileTypePhoto:
		f.MIME = "image/jpeg"
		if f.FileName == "" {
			f.FileName = "photo.jpg"
		}
	case FileTypeVideo:
		if f.MIME == "" {
			f.MIME = inferVideoMIME(f.FileName, "video/mp4")
		}
		if f.FileName == "" {
			f.FileName = "video.mp4"
		}
	case FileTypeAnimation:
		if f.MIME == "" {
			f.MIME = inferVideoMIME(f.FileName, "video/mp4")
		}
		if f.FileName == "" {
			if f.MIME == "image/gif" {
				f.FileName = "animation.gif"
			} else {
				f.FileName = "animation.mp4"
			}
		}
	case FileTypeVideoNote:
		if f.MIME == "" {
			f.MIME = "video/mp4"
		}
	case FileTypeVoice:
		if f.MIME == "" {
			f.MIME = "audio/ogg"
		}
		if f.FileName == "" {
			f.FileName = "voice.ogg"
		}
	case FileTypeAudio:
		if f.MIME == "" {
			f.MIME = "audio/mpeg"
		}
		if f.FileName == "" {
			f.FileName = "audio.mp3"
		}
	}
	return f
}

func inferVideoMIME(fileName, fallback string) string {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".gif":
		return "image/gif"
	case ".mov":
		return "video/quicktime"
	case ".mpeg", ".mpg":
		return "video/mpeg"
	case ".webm":
		return "video/webm"
	case ".mp4", "":
		return fallback
	default:
		return fallback
	}
}

func validateIncomingFile(f IncomingFile) error {
	if f.Size > 0 && !IsFileSizeAllowed(f.Size) {
		return &FileTooLargeError{Size: f.Size, FileName: f.FileName}
	}

	switch f.Kind {
	case FileTypeImage, FileTypePDF, FileTypeVideo:
		if !IsGeminiSupported(f.MIME) {
			return &UnsupportedFormatError{MimeType: f.MIME, FileName: f.FileName}
		}
	case FileTypeAnimation:
		// Telegram animations may be MP4 or GIF. The existing LLM media seam
		// supports video MIME types; GIF conversion is intentionally outside v1.
		if !strings.HasPrefix(f.MIME, "video/") || !IsGeminiSupported(f.MIME) {
			return &UnsupportedFormatError{MimeType: f.MIME, FileName: f.FileName}
		}
	case FileTypeVoice, FileTypeAudio, FileTypeVideoNote, FileTypeDocument, FileTypePhoto:
		// Documents are capability-checked from their bytes after download.
		// The remaining native media kinds need only the size check above.
	default:
		return &UnsupportedFormatError{MimeType: f.MIME, FileName: f.FileName}
	}
	return nil
}

func (p *Processor) logRejectedIncomingFile(userID storage.ScopeID, f IncomingFile, err error) {
	if f.Origin == telegramRichOrigin {
		p.logRichIncomingFileIssue("rich file rejected", f, err)
		return
	}
	var tooLarge *FileTooLargeError
	if errors.As(err, &tooLarge) {
		p.logger.Info("file too large",
			"user_id", userID,
			"file_id", f.SourceID,
			"file_name", f.FileName,
			"size", tooLarge.Size,
			"max_size", MaxFileSize(),
		)
		return
	}
	p.logger.Info("unsupported file format",
		"user_id", userID,
		"file_id", f.SourceID,
		"file_name", f.FileName,
		"mime_type", f.MIME,
		"error", err,
	)
}

// logRichIncomingFileIssue deliberately excludes Telegram file ids, filenames,
// user ids and raw upstream errors. Those values are content-bearing or
// high-cardinality; the semantic occurrence and a coarse class are sufficient
// for operating the bounded rich ingress path.
func (p *Processor) logRichIncomingFileIssue(message string, f IncomingFile, err error) {
	p.logger.Warn(message,
		"ordinal", f.Ordinal,
		"kind", f.Kind,
		"error_class", coarseIncomingFileErrorClass(err),
	)
}

func coarseIncomingFileErrorClass(err error) string {
	var tooLarge *FileTooLargeError
	var aggregate *AggregateBudgetExceededError
	var mismatch *DeclaredFileSizeExceededError
	var unsupported *UnsupportedFormatError
	switch {
	case errors.As(err, &tooLarge), errors.As(err, &aggregate), errors.As(err, &mismatch), errors.Is(err, telegram.ErrFileDownloadTooLarge):
		return "limit"
	case errors.As(err, &unsupported):
		return "unsupported"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "download"
	}
}

// processOne validates, fetches, optionally archives, and assembles a single
// file. Returns (nil, nil) when a download fails after retries (logged, skipped)
// and (nil, err) for validation failures.
func (p *Processor) processOne(ctx context.Context, f IncomingFile, userID storage.ScopeID, groupText string) (*ProcessedFile, error) {
	f = normalizeIncomingFile(f)
	if err := validateIncomingFile(f); err != nil {
		p.logRejectedIncomingFile(userID, f, err)
		return nil, err
	}
	if f.Fetch == nil {
		return nil, fmt.Errorf("file %q has no fetch function", f.SourceID)
	}

	start := time.Now()
	data, err := f.Fetch(ctx, 0)
	duration := time.Since(start)
	if err != nil {
		if f.Origin == telegramRichOrigin {
			p.logRichIncomingFileIssue("rich file fetch failed", f, err)
		} else {
			p.logger.Warn("failed to fetch file after retries",
				"error", err, "file_id", f.SourceID, "kind", f.Kind, "user_id", userID,
			)
		}
		return nil, nil // skip — non-fatal, matches legacy download-failure handling
	}
	if int64(len(data)) > MaxFileSize() {
		return nil, &FileTooLargeError{Size: int64(len(data)), FileName: f.FileName}
	}

	processed, err := p.processData(ctx, f, data, duration, userID, groupText)
	if err != nil {
		p.logRejectedIncomingFile(userID, f, err)
	}
	return processed, err
}

func (p *Processor) processData(
	ctx context.Context,
	f IncomingFile,
	data []byte,
	duration time.Duration,
	userID storage.ScopeID,
	groupText string,
) (*ProcessedFile, error) {
	base64Data := base64.StdEncoding.EncodeToString(data)

	switch f.Kind {
	case FileTypePhoto:
		artifactID := p.saveArtifact(ctx, userID, "image", "photo.jpg", "image/jpeg", data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart("photo.jpg", "image/jpeg", base64Data)},
			FileType: FileTypePhoto, FileID: f.SourceID, MimeType: "image/jpeg",
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	case FileTypeImage:
		artifactID := p.saveArtifact(ctx, userID, "image", f.FileName, f.MIME, data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, f.MIME, base64Data)},
			FileType: FileTypeImage, FileID: f.SourceID, FileName: f.FileName, MimeType: f.MIME,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	case FileTypePDF:
		artifactID := p.saveArtifact(ctx, userID, "pdf", f.FileName, f.MIME, data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, "application/pdf", base64Data)},
			FileType: FileTypePDF, FileID: f.SourceID, FileName: f.FileName, MimeType: "application/pdf",
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	case FileTypeVideo, FileTypeAnimation:
		artifactID := p.saveArtifact(ctx, userID, string(f.Kind), f.FileName, f.MIME, data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, f.MIME, base64Data)},
			FileType: f.Kind, FileID: f.SourceID, FileName: f.FileName, MimeType: f.MIME,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	case FileTypeVideoNote:
		artifactID := p.saveArtifact(ctx, userID, "video_note", f.FileName, "video/mp4", data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, "video/mp4", base64Data)},
			FileType: FileTypeVideoNote, FileID: f.SourceID, FileName: f.FileName, MimeType: "video/mp4",
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	case FileTypeVoice:
		mimeType := f.MIME
		if mimeType == "" {
			mimeType = "audio/ogg"
		}
		artifactID := p.saveVoiceArtifact(ctx, userID, mimeType, data, groupText, f, f.Duration)
		return &ProcessedFile{
			LLMParts:    []interface{}{p.mediaPart("voice.ogg", mimeType, base64Data)},
			Instruction: p.translator.Get(p.language, "bot.voice_instruction"),
			FileType:    FileTypeVoice, FileID: f.SourceID, MimeType: mimeType,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	case FileTypeAudio:
		mimeType := f.MIME
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		artifactID := p.saveArtifact(ctx, userID, "audio", f.FileName, mimeType, data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{p.mediaPart(f.FileName, mimeType, base64Data)},
			FileType: FileTypeAudio, FileID: f.SourceID, FileName: f.FileName, MimeType: mimeType,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil

	default: // FileTypeDocument — text file inlined as a text part
		if !isProbablyText(data) {
			return nil, &UnsupportedFormatError{MimeType: f.MIME, FileName: f.FileName}
		}
		textContent := fmt.Sprintf("%s:\n\n%s", f.FileName, string(data))
		artifactID := p.saveArtifact(ctx, userID, "document", f.FileName, f.MIME, data, groupText, f)
		return &ProcessedFile{
			LLMParts: []interface{}{llm.TextPart{Type: "text", Text: textContent}},
			FileType: FileTypeDocument, FileID: f.SourceID, FileName: f.FileName, MimeType: f.MIME,
			Size: int64(len(data)), Duration: duration, ArtifactID: artifactID,
			Ordinal: f.Ordinal, BlockPath: f.BlockPath, Origin: f.Origin, FileUniqueID: f.FileUniqueID,
		}, nil
	}
}

// isProbablyText reports whether data is readable UTF-8 text. It rejects binaries
// (and NUL bytes) so they don't get inlined as garbage text or break the Postgres
// TEXT columns / artifact store, which reject invalid UTF-8 and 0x00.
func isProbablyText(data []byte) bool {
	return utf8.Valid(data) && !bytes.Contains(data, []byte{0})
}

// mediaPart builds the LLM content part for an inbound file in the configured
// backend format: image_url/video_url for OpenAI-compatible backends, else the
// `file` part (also the shape for pdf/audio regardless of format). The single
// chokepoint for inbound media encoding.
func (p *Processor) mediaPart(fileName, mimeType, base64Data string) interface{} {
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)
	return llm.MediaPart(p.imageInputFormat, mimeType, fileName, dataURL)
}

// saveArtifact persists a file as an artifact when a file handler is configured.
// Returns the artifact id (nil on disabled handler or save failure — non-fatal).
func (p *Processor) saveArtifact(ctx context.Context, userID storage.ScopeID, artifactType, fileName, mimeType string, data []byte, groupText string, incoming IncomingFile) *int64 {
	return p.saveArtifactWithState(ctx, userID, artifactType, fileName, mimeType, data, groupText, incoming, false)
}

// saveArtifactWithState is saveArtifact with control over extraction. When
// skipExtraction is true the raw file is retained (bytes + content_hash) but the
// row is created 'retained' so it is never summarized/embedded or surfaced in RAG.
func (p *Processor) saveArtifactWithState(ctx context.Context, userID storage.ScopeID, artifactType, fileName, mimeType string, data []byte, groupText string, incoming IncomingFile, skipExtraction bool) *int64 {
	if p.fileHandler == nil {
		return nil
	}
	artifactID, err := p.fileHandler.SaveFile(ctx, userID, 0, artifactType, fileName, mimeType, bytes.NewReader(data), groupText, skipExtraction)
	if err != nil {
		if incoming.Origin == telegramRichOrigin {
			p.logRichIncomingFileIssue("rich file artifact save failed", incoming, err)
		} else {
			p.logger.Warn("failed to save artifact", "error", err, "file_id", incoming.SourceID, "file_name", fileName, "user_id", userID)
		}
		return nil
	}
	return artifactID
}

// saveVoiceArtifact applies the voice-duration gating before saving:
// -1 disables voice artifacts entirely, 0 saves and RAG-indexes all voices,
// N saves and indexes voices >= N seconds. Shorter voices are still RETAINED
// (raw file + content_hash) for reproducibility/replay but not RAG-indexed —
// the duration threshold now gates indexing, not retention.
func (p *Processor) saveVoiceArtifact(ctx context.Context, userID storage.ScopeID, mimeType string, data []byte, groupText string, incoming IncomingFile, durationSec int) *int64 {
	if p.fileHandler == nil {
		return nil
	}
	switch {
	case p.minVoiceDurationSec == -1:
		p.logVoiceArtifactDecision("voice artifacts disabled, skipping save", userID, incoming, durationSec)
		return nil
	case p.minVoiceDurationSec == 0 || durationSec >= p.minVoiceDurationSec:
		return p.saveArtifactWithState(ctx, userID, "voice", "voice.ogg", mimeType, data, groupText, incoming, false)
	default:
		p.logVoiceArtifactDecision("voice below RAG threshold, retaining raw file without extraction", userID, incoming, durationSec)
		return p.saveArtifactWithState(ctx, userID, "voice", "voice.ogg", mimeType, data, groupText, incoming, true)
	}
}

func (p *Processor) logVoiceArtifactDecision(message string, userID storage.ScopeID, incoming IncomingFile, durationSec int) {
	if incoming.Origin == telegramRichOrigin {
		p.logger.Debug(message,
			"ordinal", incoming.Ordinal,
			"kind", incoming.Kind,
			"duration", durationSec,
			"min_duration", p.minVoiceDurationSec,
		)
		return
	}
	p.logger.Debug(message,
		"user_id", userID,
		"duration", durationSec,
		"min_duration", p.minVoiceDurationSec,
	)
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
