package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/obs"
)

// ImageGenerationRequest is the body of POST /images (OpenRouter's dedicated
// image-generation endpoint). Some image models — e.g. the openai/gpt-image
// family — are served only there and are not reachable through
// chat-completions with modalities=["image"].
//
// Size must be an explicit "WxH" pixel string: the endpoint silently coarsens
// aspect_ratio and ignores resolution tiers for those models, so callers map
// their own ratio × tier to pixels (see config image_generator.pixel_sizes).
type ImageGenerationRequest struct {
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	N            int    `json:"n,omitempty"`
	Size         string `json:"size,omitempty"`
	Quality      string `json:"quality,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
	// InputReferences are reference images for editing/combining, as
	// image_url parts carrying data URLs.
	InputReferences []ImageURLPart `json:"input_references,omitempty"`

	Provider *ProviderRouting `json:"provider,omitempty"`
	User     string           `json:"user,omitempty"`
	Trace    map[string]any   `json:"trace,omitempty"`

	// UserID labels metrics and the OpenRouter broadcast user; not serialized
	// directly.
	UserID string `json:"-"`
}

// GeneratedImageData is one output image of POST /images.
type GeneratedImageData struct {
	B64JSON       string `json:"b64_json"`
	MediaType     string `json:"media_type"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImageGenerationResponse is the body returned by POST /images.
type ImageGenerationResponse struct {
	Created int64                `json:"created"`
	Data    []GeneratedImageData `json:"data"`
	Usage   Usage                `json:"usage"`
}

// CreateImages calls the dedicated image-generation endpoint. The call is
// synchronous: the response carries base64 image bytes. Content-policy
// rejections arrive as HTTP 400 with an error envelope; IsSafetyBlock
// recognizes them.
func (c *clientImpl) CreateImages(ctx context.Context, req ImageGenerationRequest) (resp ImageGenerationResponse, err error) {
	startTime := time.Now()
	jt := jobtype.FromContext(ctx).String()
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/llm").Start(
		ctx, "llm.CreateImages",
		trace.WithAttributes(append(c.spanBaseAttributes(),
			attribute.String("gen_ai.request.model", req.Model),
			attribute.String("gen_ai.operation.name", "image_generation"),
			attribute.String("images.size", req.Size),
			attribute.String("images.quality", req.Quality),
			attribute.Int("images.n", req.N),
			attribute.Int("images.input_count", len(req.InputReferences)),
			attribute.String("job.type", jt),
		)...),
	)
	defer func() {
		span.SetAttributes(attribute.Int("images.output_count", len(resp.Data)))
		if resp.Usage.Cost != nil {
			span.SetAttributes(attribute.Float64("gen_ai.usage.cost", *resp.Usage.Cost))
		}
		if obs.ContentEnabled() && req.Prompt != "" {
			obs.RecordContent(span, "images.prompt", req.Prompt)
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	endpoint, err := url.JoinPath(c.apiEndpoint, "images")
	if err != nil {
		return ImageGenerationResponse{}, err
	}
	if req.Provider == nil && c.defaultProvider != nil {
		req.Provider = c.defaultProvider
	}
	req.Trace, req.User = withBroadcastFields(ctx, req.Trace, req.User, req.UserID)

	body, err := json.Marshal(req)
	if err != nil {
		return ImageGenerationResponse{}, fmt.Errorf("failed to marshal image request: %w", err)
	}

	c.logger.Debug("Sending image generation request",
		"model", req.Model,
		"size", req.Size,
		"quality", req.Quality,
		"input_count", len(req.InputReferences),
	)

	outcome, err := c.doRequestWithRetry(ctx, span, retryRequest{
		endpoint: endpoint,
		body:     body,
		opName:   "image generation request",
		model:    req.Model,
	})
	span.SetAttributes(attribute.Int("llm.attempts", outcome.attempts))
	if len(outcome.retryDelays) > 0 {
		span.SetAttributes(attribute.Int64Slice("llm.retry_delays_ms", outcome.retryDelays))
	}
	if err != nil {
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		return ImageGenerationResponse{}, err
	}

	var imgResp ImageGenerationResponse
	if err := json.Unmarshal(outcome.body, &imgResp); err != nil {
		c.logger.Error("Failed to decode image generation response", "error", err, "body_bytes", len(outcome.body))
		recordProviderError(span, outcome.body, http.StatusOK)
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		return ImageGenerationResponse{}, fmt.Errorf("failed to decode image response: %w", err)
	}

	logAttrs := []any{"model", req.Model, "images", len(imgResp.Data), "size", req.Size}
	if imgResp.Usage.Cost != nil {
		logAttrs = append(logAttrs, "cost_usd", *imgResp.Usage.Cost)
	}
	c.logger.Info("Image generation response parsed", logAttrs...)

	RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), true,
		imgResp.Usage.PromptTokens, imgResp.Usage.CompletionTokens, imgResp.Usage.Cost, jt)
	return imgResp, nil
}
