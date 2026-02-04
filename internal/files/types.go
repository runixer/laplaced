// Package files provides file processing utilities for Telegram messages.
package files

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// FileType represents the type of file being processed.
type FileType string

const (
	FileTypePhoto     FileType = "photo"
	FileTypeImage     FileType = "image" // image sent as document
	FileTypePDF       FileType = "pdf"
	FileTypeVoice     FileType = "voice"
	FileTypeAudio     FileType = "audio"      // audio files (MP3, etc.)
	FileTypeVideo     FileType = "video"      // video files (MP4, etc.)
	FileTypeVideoNote FileType = "video_note" // video messages (circles)
	FileTypeDocument  FileType = "document"   // text files
)

// ProcessedFile represents a processed file ready for LLM consumption.
type ProcessedFile struct {
	// LLMParts contains the file data formatted for OpenRouter API
	// (FilePart, TextPart from openrouter package) (v0.6.0: unified on FilePart)
	LLMParts []interface{}

	// Instruction is the localized LLM instruction for this file type
	// (e.g., "Quote the transcription..." for voice messages)
	Instruction string

	// FileType indicates the type of file
	FileType FileType

	// FileID is the Telegram file ID
	FileID string

	// FileName is the original file name (if available)
	FileName string

	// MimeType is the MIME type of the file
	MimeType string

	// Size is the file size in bytes
	Size int64

	// Duration is the download time (for metrics)
	Duration time.Duration

	// ArtifactID is the ID of the saved artifact (nil if not saved or disabled)
	ArtifactID *int64
}

const (
	// maxTelegramDownloadSize is the maximum file size Telegram Bot API allows for getFile.
	// From Telegram docs: "The maximum file size to download is 20 MB"
	maxTelegramDownloadSize = 20 * 1024 * 1024
)

// geminiSupportedMimeTypes lists MIME types supported by Gemini API.
// See: https://ai.google.dev/gemini-api/docs/models/gemini-2.0-flash#supported-input-types
var geminiSupportedMimeTypes = map[string]bool{
	// Images
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/heic": true,
	"image/heif": true,
	// PDF
	"application/pdf": true,
	// Text types (specific list from Gemini docs)
	"text/plain":      true,
	"text/html":       true,
	"text/css":        true,
	"text/xml":        true,
	"text/csv":        true,
	"text/rtf":        true,
	"text/javascript": true,
	// Videos
	"video/mp4":       true,
	"video/mpeg":      true,
	"video/mov":       true,
	"video/avi":       true,
	"video/webm":      true,
	"video/3gpp":      true,
	"video/x-flv":     true,
	"video/mpg":       true,
	"video/wmv":       true,
	"video/quicktime": true,
	// All audio types are supported
}

// IsGeminiSupported checks if a MIME type is supported by Gemini API.
func IsGeminiSupported(mimeType string) bool {
	if geminiSupportedMimeTypes[mimeType] {
		return true
	}
	// All audio types are supported
	if strings.HasPrefix(mimeType, "audio/") {
		return true
	}
	// All text types and JSON are supported
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	if mimeType == "application/json" {
		return true
	}
	return false
}

// NormalizeMimeForGemini normalizes a MIME type for Gemini API consumption.
// For text types not explicitly supported by Gemini, returns text/plain.
// For other types, returns the original MIME type unchanged.
func NormalizeMimeForGemini(mimeType string) string {
	if mimeType == "" {
		return mimeType
	}
	// If explicitly supported, return as-is
	if geminiSupportedMimeTypes[mimeType] {
		return mimeType
	}
	// All audio is supported
	if strings.HasPrefix(mimeType, "audio/") {
		return mimeType
	}
	// JSON is supported
	if mimeType == "application/json" {
		return mimeType
	}
	// For other text types (like text/x-web-markdown), normalize to text/plain
	if strings.HasPrefix(mimeType, "text/") {
		return "text/plain"
	}
	return mimeType
}

// IsFileSizeAllowed checks if a file size is within Telegram Bot API limits.
func IsFileSizeAllowed(size int64) bool {
	return size > 0 && size <= maxTelegramDownloadSize
}

// MaxFileSize returns the maximum file size allowed for download.
func MaxFileSize() int64 {
	return maxTelegramDownloadSize
}

// UnsupportedFormatError is returned when a file format is not supported by Gemini.
type UnsupportedFormatError struct {
	MimeType string
	FileName string
}

func (e *UnsupportedFormatError) Error() string {
	ext := ""
	if e.FileName != "" {
		ext = filepath.Ext(e.FileName)
		if ext == "" && e.MimeType != "" {
			ext = "(" + e.MimeType + ")"
		}
	}
	return fmt.Sprintf("unsupported file format: %s (MIME: %s)", ext, e.MimeType)
}

// FileTooLargeError is returned when a file exceeds the size limit.
type FileTooLargeError struct {
	Size     int64
	FileName string
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("file too large: %s (%d bytes, max %d bytes)", e.FileName, e.Size, maxTelegramDownloadSize)
}
