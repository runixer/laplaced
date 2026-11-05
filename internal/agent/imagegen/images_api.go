package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders for input-ratio detection
	_ "image/png"
	"math"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/llm"
)

// Aspect-ratio sources recorded as imagegen.aspect_ratio_source.
const (
	aspectSourceRequested = "requested" // the tool call named a ratio
	aspectSourceDefault   = "default"   // text-to-image with no ratio: config default
	aspectSourceInput     = "input"     // edit on the images API: nearest to the first input image
	aspectSourceNone      = "none"      // edit on the chat API, or an undecodable input: provider decides
)

// effectiveAspectRatio resolves the ratio to request. Text-to-image falls
// back to the configured default. Edits must keep the input photo's framing:
// the chat API does that when no ratio is sent, but the images API needs
// explicit pixels, so there the ratio is inferred from the first input image.
func (a *Agent) effectiveAspectRatio(req Request) (ratio, source string) {
	if req.AspectRatio != "" {
		return req.AspectRatio, aspectSourceRequested
	}
	if len(req.InputImages) == 0 {
		if a.cfg.DefaultAspectRatio == "" {
			return "", aspectSourceNone
		}
		return a.cfg.DefaultAspectRatio, aspectSourceDefault
	}
	if !a.cfg.UsesImagesAPI() {
		return "", aspectSourceNone
	}
	w, h, ok := dataURLDimensions(req.InputImages[0].File.FileData)
	if !ok {
		return "", aspectSourceNone
	}
	if r := nearestAspectRatio(w, h, a.cfg.SupportedAspectRatios); r != "" {
		return r, aspectSourceInput
	}
	return "", aspectSourceNone
}

// generateViaImages drives the dedicated /images endpoint. The pixel size
// comes from the config table because the endpoint silently coarsens
// aspect_ratio and ignores resolution tiers for models such as gpt-image.
func (a *Agent) generateViaImages(ctx context.Context, span trace.Span, req Request, aspectRatio, imageSize string) (*Response, string, error) {
	size := ""
	if aspectRatio != "" {
		size = a.cfg.PixelSize(imageSize, aspectRatio)
		if size == "" {
			a.logger.Warn("no pixel size configured; letting the provider pick",
				"image_size", imageSize, "aspect_ratio", aspectRatio)
		}
	}
	span.SetAttributes(
		attribute.String("imagegen.pixel_size", size),
		attribute.String("imagegen.quality", a.cfg.Quality),
	)

	refs := make([]llm.ImageURLPart, 0, len(req.InputImages))
	for _, img := range req.InputImages {
		refs = append(refs, llm.ImageURLPart{
			Type:     "image_url",
			ImageURL: llm.ImageURLValue{URL: img.File.FileData},
		})
	}

	start := time.Now()
	imgResp, callErr := a.client.CreateImages(ctx, llm.ImageGenerationRequest{
		Model:           a.cfg.Model,
		Prompt:          req.Prompt,
		Size:            size,
		Quality:         a.cfg.Quality,
		OutputFormat:    a.cfg.OutputFormat,
		InputReferences: refs,
		UserID:          string(req.UserID),
	})
	duration := time.Since(start)
	if callErr != nil {
		failure := &ImagegenFailure{
			Kind:  classifyFailure(callErr, llm.ResponseMessage{}, ""),
			Cause: callErr,
		}
		return nil, outcomeFromKind(failure.Kind), failure
	}
	if len(imgResp.Data) == 0 {
		return nil, OutcomeUnknownNoImages, &ImagegenFailure{Kind: KindUnknownNoImages}
	}

	decoded := make([]DecodedImage, 0, len(imgResp.Data))
	for i, d := range imgResp.Data {
		data, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil || len(data) == 0 {
			a.logger.Warn("failed to decode image", "index", i, "err", err)
			continue
		}
		mimeType := d.MediaType
		if !strings.HasPrefix(mimeType, "image/") {
			mimeType = http.DetectContentType(data)
		}
		decoded = append(decoded, DecodedImage{MimeType: mimeType, Data: data})
	}
	if len(decoded) == 0 {
		return nil, OutcomeAllDecodesFailed, fmt.Errorf("imagegen: all %d output images failed to decode", len(imgResp.Data))
	}

	return &Response{
		Images:           decoded,
		PromptTokens:     imgResp.Usage.PromptTokens,
		CompletionTokens: imgResp.Usage.CompletionTokens,
		Cost:             imgResp.Usage.Cost,
		Duration:         duration,
	}, OutcomeSuccess, nil
}

// dataURLDimensions reads the pixel size of a JPEG/PNG data URL without
// decoding the full image. ok is false for other formats or malformed input.
func dataURLDimensions(dataURL string) (w, h int, ok bool) {
	_, data, err := decodeDataURL(dataURL)
	if err != nil {
		return 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// nearestAspectRatio picks the supported "a:b" ratio closest to w:h on a log
// scale, so 2:1 and 1:2 are equally far from 1:1. Malformed entries are
// skipped; "" means none parsed.
func nearestAspectRatio(w, h int, supported []string) string {
	target := math.Log(float64(w) / float64(h))
	best, bestDist := "", math.Inf(1)
	for _, r := range supported {
		var a, b int
		if n, err := fmt.Sscanf(r, "%d:%d", &a, &b); err != nil || n != 2 || a <= 0 || b <= 0 {
			continue
		}
		if d := math.Abs(math.Log(float64(a)/float64(b)) - target); d < bestDist {
			best, bestDist = r, d
		}
	}
	return best
}
