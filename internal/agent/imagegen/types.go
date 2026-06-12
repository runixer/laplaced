// Package imagegen implements an image-generation agent that drives
// OpenRouter image-output models (e.g. google/gemini-3.1-flash-image-preview).
//
// Unlike text agents, imagegen does not implement the generic agent.Agent
// interface: its I/O shape (base64-decoded PNGs) is too specific and wrapping
// it in a generic Response would lose type safety.
package imagegen

import (
	"fmt"
	"time"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
)

// FailureKind classifies why an image-generation call did not produce an image.
//
// Sources of signal differ by upstream provider:
//   - Google image models (e.g. gemini-2.5-flash-image / nano-banana) return
//     finish_reason=stop for both success and refusal — only the presence of
//     an explicit text in `content` and the absence of `images` distinguishes
//     a refusal. The text is quotable.
//   - OpenAI image models (gpt-5.4-image-2) molchat: empty content, null
//     finish_reason, no refusal field. The only correlate is moderation_latency
//     in the OpenRouter generation API, which we do not consult synchronously.
//
// The classifier therefore prefers shape signals (Images / Content / Provider)
// over finish_reason values. See docs/bugs/2026-05-06-imagegen-empty-stream-no-signal.
type FailureKind int

const (
	// KindUnknown is the zero value — never returned by the classifier on a
	// real failure path. Treat it as a programmer error if observed.
	KindUnknown FailureKind = iota

	// KindTimeout — our context deadline tripped before the upstream
	// responded (typically the per-call timeout in agents.image_generator).
	KindTimeout

	// KindUpstreamError — network failure, 5xx, or OpenRouter error envelope.
	// Distinct from KindTimeout so dashboards can plot them separately.
	KindUpstreamError

	// KindTextRefusal — the model returned a non-empty text explanation but
	// no image (typical of Google nano-banana). Text is quotable to the user.
	KindTextRefusal

	// KindSilentBlockOAI — OpenAI returned an empty stream with no text and
	// no finish_reason. Almost always a content-policy block; cannot be
	// proven without a follow-up generation-API lookup.
	KindSilentBlockOAI

	// KindUnknownNoImages — non-OpenAI provider returned no images and no
	// text. Genuinely unknown cause.
	KindUnknownNoImages
)

// String returns a stable lower_snake identifier suitable for span attrs,
// metric labels, and structured log fields.
func (k FailureKind) String() string {
	switch k {
	case KindTimeout:
		return "timeout"
	case KindUpstreamError:
		return "upstream_error"
	case KindTextRefusal:
		return "text_refusal"
	case KindSilentBlockOAI:
		return "silent_block_oai"
	case KindUnknownNoImages:
		return "unknown_no_images"
	default:
		return "unknown"
	}
}

// ImagegenFailure is the typed error returned by Generate when no image is
// produced. Callers should use errors.As to recover the structured fields.
//
// The tool wrapper uses Kind to pick a tailored instruction for the LLM and
// Text to quote the model's own refusal verbatim where applicable.
type ImagegenFailure struct {
	Kind     FailureKind
	Text     string // msg.Content; non-empty for KindTextRefusal
	Provider string // resp.Provider (e.g. "OpenAI", "Google"); "" if call never reached upstream
	Cause    error  // wrapped network/timeout error; nil for content-shape kinds
}

// Error implements error. The format includes the kind for grep-ability and
// either the model's text (truncated) or the underlying cause.
func (f *ImagegenFailure) Error() string {
	switch {
	case f.Cause != nil:
		return fmt.Sprintf("imagegen: %s: %v", f.Kind, f.Cause)
	case f.Text != "":
		return fmt.Sprintf("imagegen: %s: %q", f.Kind, truncateForError(f.Text, 200))
	default:
		return fmt.Sprintf("imagegen: %s", f.Kind)
	}
}

// Unwrap exposes the underlying network/timeout error so errors.Is/As over
// Cause continues to work (e.g. errors.Is(err, context.DeadlineExceeded)).
func (f *ImagegenFailure) Unwrap() error { return f.Cause }

// Request parameters for a single image generation call.
type Request struct {
	UserID storage.ScopeID

	// Prompt is the text description of the image to generate, in any language.
	Prompt string

	// InputImages are reference images for editing/combining. Pass the
	// user's attached photos or artifacts loaded from storage. May be empty
	// for pure text-to-image generation.
	InputImages []llm.FilePart

	// AspectRatio is one of the values accepted by the target model, e.g.
	// "1:1", "16:9", "21:9". Empty means model default (typically 1:1).
	AspectRatio string

	// ImageSize is one of "1K", "2K", "4K". Empty means model default (1K).
	// Note: "0.5K" passes the OpenRouter validator but is rejected upstream
	// by Google for gemini-3.1-flash-image-preview (verified end-to-end via
	// curl on 2026-04-30). "512" is blocked by OR's validator. Both ranges
	// are unusable today — only the larger sizes work.
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
