// Package imagegen implements an image-generation agent that drives
// OpenRouter image-output models (e.g. google/gemini-3.1-flash-image-preview).
//
// Unlike text agents, imagegen does not implement the generic agent.Agent
// interface: its I/O shape (base64-decoded PNGs) is too specific and wrapping
// it in a generic Response would lose type safety.
package imagegen

import (
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
)

// Request parameters for a single image generation call.
type Request struct {
	UserID int64

	// Prompt is the text description of the image to generate, in any language.
	Prompt string

	// InputImages are reference images for editing/combining. Pass the
	// user's attached photos or artifacts loaded from storage. May be empty
	// for pure text-to-image generation.
	InputImages []openrouter.FilePart

	// AspectRatio is one of the values accepted by the target model, e.g.
	// "1:1", "16:9", "21:9". Empty means model default (typically 1:1).
	AspectRatio string

	// ImageSize is one of "0.5K", "1K", "2K", "4K". Empty means model default.
	ImageSize string
}

// DecodedImage is a single output image with its MIME type.
type DecodedImage struct {
	MimeType string // e.g. "image/png"
	Data     []byte // raw decoded bytes
}

// Response contains generated images and accounting metadata.
type Response struct {
	Images      []DecodedImage
	TextContent string // Optional text the model emitted alongside images

	PromptTokens     int
	CompletionTokens int
	Cost             *float64
	Duration         time.Duration
}
