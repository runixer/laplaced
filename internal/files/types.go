// Package files provides file processing utilities for Telegram messages.
package files

import "time"

// FileType represents the type of file being processed.
type FileType string

const (
	FileTypePhoto    FileType = "photo"
	FileTypeImage    FileType = "image" // image sent as document
	FileTypePDF      FileType = "pdf"
	FileTypeVoice    FileType = "voice"
	FileTypeDocument FileType = "document" // text files
)

// ProcessedFile represents a processed file ready for LLM consumption.
type ProcessedFile struct {
	// LLMParts contains the file data formatted for OpenRouter API
	// (ImagePart, FilePart, TextPart from openrouter package)
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
}
