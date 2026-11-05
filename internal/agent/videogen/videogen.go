// Package videogen implements the video-generation agent behind the
// generate_video tool. Jobs run asynchronously upstream (submit → poll →
// download); Generate blocks until the clip is ready, fails, or the configured
// timeout trips.
package videogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register the PNG decoder for input frames
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
)

// FailureKind classifies why a video job produced no clip.
type FailureKind int

const (
	KindUnknown FailureKind = iota
	// KindTimeout — the job did not finish within the configured timeout.
	KindTimeout
	// KindUpstreamError — network failure or provider API error.
	KindUpstreamError
	// KindSafetyBlock — the submit was rejected by a content policy.
	KindSafetyBlock
	// KindJobFailed — the job ran and ended failed/cancelled/expired; Text
	// carries the provider's reason when it gave one.
	KindJobFailed
)

// String returns the stable span/log identifier.
func (k FailureKind) String() string {
	switch k {
	case KindTimeout:
		return "timeout"
	case KindUpstreamError:
		return "upstream_error"
	case KindSafetyBlock:
		return "safety_block"
	case KindJobFailed:
		return "job_failed"
	default:
		return "unknown"
	}
}

// Failure is the typed error Generate returns when no clip was produced.
type Failure struct {
	Kind  FailureKind
	Text  string // provider reason for KindJobFailed
	Cause error
}

func (f *Failure) Error() string {
	switch {
	case f.Cause != nil:
		return fmt.Sprintf("videogen: %s: %v", f.Kind, f.Cause)
	case f.Text != "":
		return fmt.Sprintf("videogen: %s: %q", f.Kind, f.Text)
	default:
		return "videogen: " + f.Kind.String()
	}
}

func (f *Failure) Unwrap() error { return f.Cause }

// Request describes one clip.
type Request struct {
	UserID storage.ScopeID
	Prompt string
	// AspectRatio and Duration fall back to the config defaults when empty/0.
	AspectRatio string
	Duration    int
	// Audio overrides the configured generate_audio default when non-nil.
	Audio *bool
	// FirstFrame / LastFrame are optional input images (data URLs).
	FirstFrame *llm.FilePart
	LastFrame  *llm.FilePart
}

// Response is a finished clip.
type Response struct {
	Data     []byte
	MimeType string
	JobID    string
	Cost     *float64
	Duration time.Duration // wall time of the whole job
}

// Agent drives the asynchronous video endpoint.
type Agent struct {
	client llm.Client
	cfg    *config.VideoGeneratorConfig
	logger *slog.Logger
}

// New constructs a video-generation Agent.
func New(client llm.Client, cfg *config.VideoGeneratorConfig, logger *slog.Logger) *Agent {
	return &Agent{client: client, cfg: cfg, logger: logger.With("component", "videogen")}
}

// Generate submits a job, polls until it finishes and downloads the clip.
func (a *Agent) Generate(ctx context.Context, req Request) (resp *Response, err error) {
	outcome := "unknown"
	ratio := req.AspectRatio
	if ratio == "" {
		ratio = a.cfg.DefaultAspectRatio
	}
	duration := req.Duration
	if duration == 0 {
		duration = a.cfg.DefaultDuration
	}
	audio := a.cfg.GenerateAudio
	if req.Audio != nil {
		audio = *req.Audio
	}
	timeout := a.cfg.GetTimeout()

	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/agent/videogen").Start(
		ctx, "videogen.Generate",
		trace.WithAttributes(
			attribute.String("user.id", string(req.UserID)),
			attribute.String("videogen.model", a.cfg.Model),
			attribute.String("videogen.aspect_ratio", ratio),
			attribute.Int("videogen.duration", duration),
			attribute.String("videogen.resolution", a.cfg.Resolution),
			attribute.Bool("videogen.audio", audio),
			attribute.Bool("videogen.first_frame", req.FirstFrame != nil),
			attribute.Bool("videogen.last_frame", req.LastFrame != nil),
			attribute.Float64("videogen.timeout_seconds", timeout.Seconds()),
		),
	)
	start := time.Now()
	defer func() {
		span.SetAttributes(attribute.String("videogen.outcome", outcome))
		if resp != nil {
			span.SetAttributes(attribute.Int("videogen.output_bytes", len(resp.Data)))
			if resp.Cost != nil {
				span.SetAttributes(attribute.Float64("videogen.cost", *resp.Cost))
			}
		}
		var cost *float64
		if resp != nil {
			cost = resp.Cost
		}
		llm.RecordLLMRequest(string(req.UserID), a.cfg.Model, time.Since(start).Seconds(), err == nil, 0, 0, cost,
			jobtype.FromContext(ctx).String())
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	if strings.TrimSpace(req.Prompt) == "" {
		outcome = "empty_prompt"
		return nil, errors.New("videogen: prompt is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	submit := llm.VideoGenerationRequest{
		Model:         a.cfg.Model,
		Prompt:        req.Prompt,
		Duration:      duration,
		Resolution:    a.cfg.Resolution,
		AspectRatio:   ratio,
		GenerateAudio: &audio,
		UserID:        string(req.UserID),
	}
	for _, frame := range []struct {
		part *llm.FilePart
		role string
	}{{req.FirstFrame, llm.VideoFrameFirst}, {req.LastFrame, llm.VideoFrameLast}} {
		if frame.part == nil {
			continue
		}
		url, cropped := cropDataURLToRatio(frame.part.File.FileData, ratio)
		span.SetAttributes(attribute.Bool("videogen."+frame.role+"_cropped", cropped))
		submit.FrameImages = append(submit.FrameImages, llm.VideoFrameImage{
			Type:      "image_url",
			ImageURL:  llm.ImageURLValue{URL: url},
			FrameType: frame.role,
		})
	}

	job, err := a.client.SubmitVideo(ctx, submit)
	if err != nil {
		f := classifyCallError(err)
		outcome = f.Kind.String()
		return nil, f
	}
	span.SetAttributes(attribute.String("videogen.job_id", job.ID))

	polls := 0
	ticker := time.NewTicker(a.cfg.GetPollInterval())
	defer ticker.Stop()
	for !job.Terminal() {
		select {
		case <-ctx.Done():
			span.SetAttributes(attribute.Int("videogen.polls", polls))
			outcome = KindTimeout.String()
			return nil, &Failure{Kind: KindTimeout, Cause: ctx.Err()}
		case <-ticker.C:
		}
		polls++
		next, pollErr := a.client.GetVideo(ctx, job.ID)
		if pollErr != nil {
			if errors.Is(pollErr, context.DeadlineExceeded) || ctx.Err() != nil {
				continue // the select above reports the timeout
			}
			// A transient poll failure (already retried by the client) must not
			// abandon a paid job; keep polling until the deadline.
			a.logger.Warn("video poll failed; will retry", "job_id", job.ID, "err", pollErr)
			continue
		}
		job = next
	}
	span.SetAttributes(attribute.Int("videogen.polls", polls), attribute.String("videogen.job_status", job.Status))

	if job.Status != llm.VideoStatusCompleted {
		outcome = KindJobFailed.String()
		return nil, &Failure{Kind: KindJobFailed, Text: job.Error}
	}

	data, err := a.client.GetVideoContent(ctx, job.ID, 0)
	if err != nil {
		f := classifyCallError(err)
		outcome = f.Kind.String()
		return nil, f
	}
	mimeType := http.DetectContentType(data)
	if !strings.HasPrefix(mimeType, "video/") {
		mimeType = "video/mp4"
	}
	outcome = "success"
	a.logger.Info("video generated", "job_id", job.ID, "bytes", len(data), "polls", polls, "duration", time.Since(start))
	return &Response{
		Data:     data,
		MimeType: mimeType,
		JobID:    job.ID,
		Cost:     job.Usage.Cost,
		Duration: time.Since(start),
	}, nil
}

func classifyCallError(err error) *Failure {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Failure{Kind: KindTimeout, Cause: err}
	case llm.IsSafetyBlock(err):
		return &Failure{Kind: KindSafetyBlock, Cause: err}
	default:
		return &Failure{Kind: KindUpstreamError, Cause: err}
	}
}

// cropDataURLToRatio center-crops a JPEG/PNG data URL to the "a:b" ratio and
// re-encodes it as JPEG. Models that support only a few ratios (Veo: 16:9,
// 9:16) letterbox any other input shape with black bars. Inputs that cannot
// be decoded, or already match within 1%, are returned unchanged.
func cropDataURLToRatio(dataURL, ratio string) (string, bool) {
	var rw, rh int
	if n, err := fmt.Sscanf(ratio, "%d:%d", &rw, &rh); err != nil || n != 2 || rw <= 0 || rh <= 0 {
		return dataURL, false
	}
	comma := strings.Index(dataURL, ";base64,")
	if !strings.HasPrefix(dataURL, "data:") || comma < 0 {
		return dataURL, false
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+len(";base64,"):])
	if err != nil {
		return dataURL, false
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return dataURL, false
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	target := float64(rw) / float64(rh)
	current := float64(w) / float64(h)
	if current/target > 0.99 && current/target < 1.01 {
		return dataURL, false
	}
	cw, ch := w, h
	if current > target {
		cw = int(float64(h) * target)
	} else {
		ch = int(float64(w) / target)
	}
	if cw <= 0 || ch <= 0 {
		return dataURL, false
	}
	x0 := b.Min.X + (w-cw)/2
	y0 := b.Min.Y + (h-ch)/2
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(dst, dst.Bounds(), src, image.Point{X: x0, Y: y0}, draw.Src)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 92}); err != nil {
		return dataURL, false
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), true
}
