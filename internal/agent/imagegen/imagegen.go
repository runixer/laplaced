package imagegen

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
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
func (a *Agent) Generate(ctx context.Context, req Request) (*Response, error) {
	if strings.TrimSpace(req.Prompt) == "" {
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
	resp, err := a.client.CreateChatCompletion(ctx, orReq)
	duration := time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("imagegen: chat completion failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("imagegen: model returned no choices")
	}

	msg := resp.Choices[0].Message
	if len(msg.Images) == 0 {
		// Model chose to answer in text only — surface its text to the
		// caller so it can be shown to the user as a graceful refusal.
		return nil, fmt.Errorf("imagegen: model produced no images (text reply: %q)",
			truncateForError(msg.Content, 200))
	}

	decoded := make([]DecodedImage, 0, len(msg.Images))
	for i, img := range msg.Images {
		mime, data, err := decodeDataURL(img.ImageURL.URL)
		if err != nil {
			a.logger.Warn("failed to decode image", "index", i, "err", err)
			continue
		}
		decoded = append(decoded, DecodedImage{MimeType: mime, Data: data})
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("imagegen: all %d output images failed to decode", len(msg.Images))
	}

	return &Response{
		Images:           decoded,
		TextContent:      msg.Content,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		Cost:             resp.Usage.Cost,
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
