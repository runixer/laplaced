package imagegen

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/openrouter"
)

// Outcome classifies the terminal state of a Generate call. It lives on
// the imagegen.Generate span as imagegen.outcome and powers TraceQL queries
// like {span.imagegen.outcome="provider_error"} for triage. New shapes
// land in OutcomeUnknown — surfaces in error.upstream_message rather than
// inventing a new bucket silently.
const (
	OutcomeSuccess        = "success"
	OutcomeProviderError  = "provider_error"
	OutcomeNoImages       = "no_images" // model returned text only or all decodes failed
	OutcomeEmptyPrompt    = "empty_prompt"
	OutcomeNoChoices      = "no_choices"
	OutcomeUnknownFailure = "unknown"
)

// Agent wraps an OpenRouter client and emits image-generation requests with
// modalities=["image","text"] set.
type Agent struct {
	client openrouter.Client
	cfg    *config.ImageGeneratorConfig
	logger *slog.Logger
}

// New constructs an image-generation Agent.
func New(client openrouter.Client, cfg *config.ImageGeneratorConfig, logger *slog.Logger) *Agent {
	return &Agent{
		client: client,
		cfg:    cfg,
		logger: logger.With("component", "imagegen"),
	}
}

// Generate runs a single image-generation call. It never returns partial
// results: if the model produced zero images it returns an error the caller
// can surface to the user.
func (a *Agent) Generate(ctx context.Context, req Request) (resp *Response, err error) {
	// Span attrs let TraceQL pinpoint the 2026-04-29-style failure mode:
	// {span.imagegen.outcome="provider_error" && span.imagegen.image_size="0.5K"}
	// surfaces every image-edit-with-explicit-size that Google rejected, no
	// log scraping. Outcome resolves in the deferred closure below.
	outcome := OutcomeUnknownFailure
	var inputBytes int
	for _, img := range req.InputImages {
		// Rough estimate: data URL length ≈ base64 encoding overhead 4/3.
		// Used only for span attr observability; not load-bearing.
		inputBytes += len(img.File.FileData) * 3 / 4
	}
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/agent/imagegen").Start(
		ctx, "imagegen.Generate",
		trace.WithAttributes(
			attribute.Int64("user.id", req.UserID),
			attribute.String("imagegen.aspect_ratio_requested", req.AspectRatio),
			attribute.String("imagegen.image_size_requested", req.ImageSize),
			attribute.Int("imagegen.input_count", len(req.InputImages)),
			attribute.Int("imagegen.input_bytes_est", inputBytes),
		),
	)
	defer func() {
		span.SetAttributes(attribute.String("imagegen.outcome", outcome))
		if resp != nil {
			span.SetAttributes(attribute.Int("imagegen.output_count", len(resp.Images)))
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	if strings.TrimSpace(req.Prompt) == "" {
		outcome = OutcomeEmptyPrompt
		return nil, fmt.Errorf("imagegen: prompt is empty")
	}

	timeout := a.cfg.GetTimeout()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Build multimodal user message: prompt text first, then any input images.
	// Image-generation models require input images in OpenAI-compatible
	// "image_url" shape — the unified "file" FilePart shape used by text
	// Gemini models produces "Invalid file type: image/…" 400 errors here.
	parts := make([]interface{}, 0, 1+len(req.InputImages))
	parts = append(parts, openrouter.TextPart{Type: "text", Text: req.Prompt})
	for _, img := range req.InputImages {
		parts = append(parts, openrouter.ImageURLPart{
			Type:     "image_url",
			ImageURL: openrouter.ImageURLValue{URL: img.File.FileData},
		})
	}

	aspectRatio := req.AspectRatio
	// Default aspect ratio only applies for text-to-image (no input images).
	// When editing, passing an explicit aspect would force the model to reframe
	// the input — users expect edits to preserve the original photo's ratio.
	if aspectRatio == "" && len(req.InputImages) == 0 {
		aspectRatio = a.cfg.DefaultAspectRatio
	}
	imageSize := req.ImageSize
	if imageSize == "" {
		imageSize = a.cfg.DefaultImageSize
	}

	var imgCfg *openrouter.ImageConfig
	if aspectRatio != "" || imageSize != "" {
		imgCfg = &openrouter.ImageConfig{
			AspectRatio: aspectRatio,
			ImageSize:   imageSize,
		}
	}
	// Effective values (after defaulting) — separate from *_requested attrs
	// so triage can distinguish "user/LLM asked for 0.5K" from "we defaulted
	// to 1K because nothing was set". 2026-04-29 incident specifically came
	// from edit-with-explicit-0.5K, so this distinction matters.
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("imagegen.aspect_ratio", aspectRatio),
		attribute.String("imagegen.image_size", imageSize),
	)

	orReq := openrouter.ChatCompletionRequest{
		Model:       a.cfg.Model,
		Modalities:  []string{"image", "text"},
		ImageConfig: imgCfg,
		Messages: []openrouter.Message{
			{Role: "user", Content: parts},
		},
		UserID: req.UserID,
	}

	start := time.Now()
	orResp, callErr := a.client.CreateChatCompletion(ctx, orReq)
	duration := time.Since(start)
	if callErr != nil {
		// Differentiate provider-side reject (most common: 4xx with body
		// envelope) from local issues (timeout, transport). The OR client
		// already wraps both as one error type; we use string match as the
		// stable signal since openrouter.* package types are unexported.
		if errors.Is(callErr, context.DeadlineExceeded) {
			outcome = OutcomeUnknownFailure
		} else {
			outcome = OutcomeProviderError
		}
		return nil, fmt.Errorf("imagegen: chat completion failed: %w", callErr)
	}
	if len(orResp.Choices) == 0 {
		outcome = OutcomeNoChoices
		return nil, fmt.Errorf("imagegen: model returned no choices")
	}

	msg := orResp.Choices[0].Message
	if len(msg.Images) == 0 {
		// Model chose to answer in text only — surface its text to the
		// caller so it can be shown to the user as a graceful refusal.
		outcome = OutcomeNoImages
		return nil, fmt.Errorf("imagegen: model produced no images (text reply: %q)",
			truncateForError(msg.Content, 200))
	}

	decoded := make([]DecodedImage, 0, len(msg.Images))
	for i, img := range msg.Images {
		mime, data, decodeErr := decodeDataURL(img.ImageURL.URL)
		if decodeErr != nil {
			a.logger.Warn("failed to decode image", "index", i, "err", decodeErr)
			continue
		}
		decoded = append(decoded, DecodedImage{MimeType: mime, Data: data})
	}
	if len(decoded) == 0 {
		outcome = OutcomeNoImages
		return nil, fmt.Errorf("imagegen: all %d output images failed to decode", len(msg.Images))
	}

	outcome = OutcomeSuccess
	return &Response{
		Images:           decoded,
		TextContent:      msg.Content,
		PromptTokens:     orResp.Usage.PromptTokens,
		CompletionTokens: orResp.Usage.CompletionTokens,
		Cost:             orResp.Usage.Cost,
		Duration:         duration,
	}, nil
}

// decodeDataURL parses a "data:<mime>;base64,<payload>" URL.
func decodeDataURL(dataURL string) (mime string, data []byte, err error) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return "", nil, fmt.Errorf("not a data URL (no %q prefix)", prefix)
	}
	rest := dataURL[len(prefix):]
	semicolon := strings.Index(rest, ";")
	if semicolon < 0 {
		return "", nil, fmt.Errorf("data URL missing ';' after mime type")
	}
	mime = rest[:semicolon]
	after := rest[semicolon+1:]
	const b64 = "base64,"
	if !strings.HasPrefix(after, b64) {
		return "", nil, fmt.Errorf("data URL encoding is not base64")
	}
	payload := after[len(b64):]
	data, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("base64 decode: %w", err)
	}
	return mime, data, nil
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
