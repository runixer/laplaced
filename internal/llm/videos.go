package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/obs"
)

// Video job statuses reported by the asynchronous /videos endpoint.
const (
	VideoStatusPending    = "pending"
	VideoStatusInProgress = "in_progress"
	VideoStatusCompleted  = "completed"
	VideoStatusFailed     = "failed"
	VideoStatusCancelled  = "cancelled"
	VideoStatusExpired    = "expired"
)

// Frame roles for VideoFrameImage.FrameType.
const (
	VideoFrameFirst = "first_frame"
	VideoFrameLast  = "last_frame"
)

// VideoFrameImage pins an input image to the first or last frame of an
// image-to-video job. URL may be a data URL.
type VideoFrameImage struct {
	Type      string        `json:"type"` // "image_url"
	ImageURL  ImageURLValue `json:"image_url"`
	FrameType string        `json:"frame_type"`
}

// VideoGenerationRequest is the body of POST /videos. The job runs
// asynchronously: submit, poll GetVideo until a terminal status, then fetch
// the bytes with GetVideoContent.
type VideoGenerationRequest struct {
	Model         string            `json:"model"`
	Prompt        string            `json:"prompt"`
	Duration      int               `json:"duration,omitempty"`
	Resolution    string            `json:"resolution,omitempty"`
	AspectRatio   string            `json:"aspect_ratio,omitempty"`
	GenerateAudio *bool             `json:"generate_audio,omitempty"`
	FrameImages   []VideoFrameImage `json:"frame_images,omitempty"`

	Provider *ProviderRouting `json:"provider,omitempty"`
	User     string           `json:"user,omitempty"`
	Trace    map[string]any   `json:"trace,omitempty"`

	UserID string `json:"-"`
}

// VideoJob is the submit/poll response of the /videos endpoint.
type VideoJob struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Error is a plain string on failed jobs (e.g. a provider policy reason).
	Error string `json:"error,omitempty"`
	Usage Usage  `json:"usage"`
}

// Terminal reports whether the job reached a final status.
func (j VideoJob) Terminal() bool {
	switch j.Status {
	case VideoStatusCompleted, VideoStatusFailed, VideoStatusCancelled, VideoStatusExpired:
		return true
	}
	return false
}

// SubmitVideo starts an asynchronous video generation job.
func (c *clientImpl) SubmitVideo(ctx context.Context, req VideoGenerationRequest) (job VideoJob, err error) {
	jt := jobtype.FromContext(ctx).String()
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/llm").Start(
		ctx, "llm.SubmitVideo",
		trace.WithAttributes(append(c.spanBaseAttributes(),
			attribute.String("gen_ai.request.model", req.Model),
			attribute.String("gen_ai.operation.name", "video_generation"),
			attribute.Int("videos.duration", req.Duration),
			attribute.String("videos.resolution", req.Resolution),
			attribute.String("videos.aspect_ratio", req.AspectRatio),
			attribute.Int("videos.frame_count", len(req.FrameImages)),
			attribute.String("job.type", jt),
		)...),
	)
	defer func() {
		span.SetAttributes(attribute.String("videos.job_id", job.ID), attribute.String("videos.status", job.Status))
		if obs.ContentEnabled() && req.Prompt != "" {
			obs.RecordContent(span, "videos.prompt", req.Prompt)
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	endpoint, err := url.JoinPath(c.apiEndpoint, "videos")
	if err != nil {
		return VideoJob{}, err
	}
	if req.Provider == nil && c.defaultProvider != nil {
		req.Provider = c.defaultProvider
	}
	req.Trace, req.User = withBroadcastFields(ctx, req.Trace, req.User, req.UserID)
	body, err := json.Marshal(req)
	if err != nil {
		return VideoJob{}, fmt.Errorf("failed to marshal video request: %w", err)
	}
	outcome, err := c.doRequestWithRetry(ctx, span, retryRequest{
		endpoint: endpoint,
		body:     body,
		opName:   "video submit request",
		model:    req.Model,
	})
	span.SetAttributes(attribute.Int("llm.attempts", outcome.attempts))
	if err != nil {
		return VideoJob{}, err
	}
	if err := json.Unmarshal(outcome.body, &job); err != nil {
		recordProviderError(span, outcome.body, http.StatusOK)
		return VideoJob{}, fmt.Errorf("failed to decode video submit response: %w", err)
	}
	if job.ID == "" {
		return VideoJob{}, fmt.Errorf("video submit response has no job id")
	}
	c.logger.Info("Video job submitted", "model", req.Model, "job_id", job.ID, "status", job.Status)
	return job, nil
}

// GetVideo polls the status of a video job.
func (c *clientImpl) GetVideo(ctx context.Context, jobID string) (job VideoJob, err error) {
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/llm").Start(
		ctx, "llm.GetVideo",
		trace.WithAttributes(append(c.spanBaseAttributes(), attribute.String("videos.job_id", jobID))...),
	)
	defer func() {
		span.SetAttributes(attribute.String("videos.status", job.Status))
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	endpoint, err := url.JoinPath(c.apiEndpoint, "videos", jobID)
	if err != nil {
		return VideoJob{}, err
	}
	outcome, err := c.doRequestWithRetry(ctx, span, retryRequest{
		method:   http.MethodGet,
		endpoint: endpoint,
		opName:   "video poll request",
	})
	if err != nil {
		return VideoJob{}, err
	}
	if err := json.Unmarshal(outcome.body, &job); err != nil {
		return VideoJob{}, fmt.Errorf("failed to decode video poll response: %w", err)
	}
	return job, nil
}

// GetVideoContent downloads output index of a completed video job and
// returns the raw bytes.
func (c *clientImpl) GetVideoContent(ctx context.Context, jobID string, index int) (data []byte, err error) {
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/llm").Start(
		ctx, "llm.GetVideoContent",
		trace.WithAttributes(append(c.spanBaseAttributes(), attribute.String("videos.job_id", jobID))...),
	)
	defer func() {
		span.SetAttributes(attribute.Int("videos.content_bytes", len(data)))
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	endpoint, err := url.JoinPath(c.apiEndpoint, "videos", jobID, "content")
	if err != nil {
		return nil, err
	}
	endpoint += "?index=" + strconv.Itoa(index)
	outcome, err := c.doRequestWithRetry(ctx, span, retryRequest{
		method:   http.MethodGet,
		endpoint: endpoint,
		opName:   "video content request",
	})
	if err != nil {
		return nil, err
	}
	if len(outcome.body) == 0 {
		return nil, fmt.Errorf("video content for job %s is empty", jobID)
	}
	return outcome.body, nil
}
