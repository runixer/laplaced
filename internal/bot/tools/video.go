package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent/videogen"
	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/textutil"
)

// VideoGenerator is the narrow interface performVideoGeneration needs;
// *videogen.Agent satisfies it directly.
type VideoGenerator interface {
	Generate(ctx context.Context, req videogen.Request) (*videogen.Response, error)
}

// SetVideoGenerator wires the video-generation agent. Required for the
// generate_video tool; without it the tool reports a configuration error.
func (e *ToolExecutor) SetVideoGenerator(gen VideoGenerator) {
	e.videoGen = gen
}

// performVideoGeneration drives the generate_video tool: it resolves the
// optional first/last frame images, runs the (blocking, ~1-2 min) video job
// and stores the clip as a "video" artifact delivered as a native Telegram
// video after the reply. Like generate_image, every failure returns a
// non-error Result that tells the LLM to stop retrying in this turn.
func (e *ToolExecutor) performVideoGeneration(ctx context.Context, cc CallContext, args map[string]interface{}) (*Result, error) {
	if e.videoGen == nil || e.artifactRepo == nil || e.fileStorage == nil {
		return videoStopRetryResult("video generation is not configured on this bot", ""), nil
	}
	prompt := stringArg(args, "prompt")
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("generate_video: prompt is required")
	}

	frames, err := e.resolveInputImages(ctx, cc, args)
	if err != nil {
		return videoStopRetryResult("failed to load the input image", ""), nil
	}
	req := videogen.Request{
		UserID:      cc.UserID,
		Prompt:      prompt,
		AspectRatio: stringArg(args, "aspect_ratio"),
		Duration:    intArg(args, "duration"),
	}
	if audio, ok := args["audio"].(bool); ok {
		req.Audio = &audio
	}
	if len(frames) > 0 {
		req.FirstFrame = &frames[0]
	}
	if len(frames) > 1 {
		req.LastFrame = &frames[1]
	}

	resp, err := e.videoGen.Generate(ctx, req)
	if err != nil {
		var failure *videogen.Failure
		if errors.As(err, &failure) {
			e.logger.Warn("videogen failed — telling LLM not to retry",
				"user_id", cc.UserID, "failure_kind", failure.Kind.String(), "err", err)
			return videoStopRetryResultForFailure(failure), nil
		}
		e.logger.Warn("videogen failed (untyped) — telling LLM not to retry", "user_id", cc.UserID, "err", err)
		return videoStopRetryResult("the video model rejected the request", ""), nil
	}

	ext := ".mp4"
	if resp.MimeType == "video/webm" {
		ext = ".webm"
	}
	filename := fmt.Sprintf("generated_%s_1%s", time.Now().UTC().Format("20060102T150405Z"), ext)
	saved, err := e.fileStorage.SaveFile(ctx, cc.UserID, bytes.NewReader(resp.Data), filename)
	if err != nil {
		e.logger.Error("failed to save generated video", "user_id", cc.UserID, "err", err)
		return videoStopRetryResult("the video was generated but could not be saved", ""), nil
	}
	id, err := e.artifactRepo.AddArtifact(storage.Artifact{
		UserID:       cc.UserID,
		FileType:     "video",
		FilePath:     saved.Path,
		FileSize:     saved.Size,
		MimeType:     resp.MimeType,
		OriginalName: filename,
		ContentHash:  saved.ContentHash,
		State:        "pending",
		UserContext:  strPtr(prompt),
	})
	if err != nil {
		e.logger.Error("failed to create artifact row for generated video", "user_id", cc.UserID, "err", err)
		return videoStopRetryResult("the video was generated but could not be saved", ""), nil
	}
	e.logger.Info("video generated and stored",
		"user_id", cc.UserID, "artifact_id", id, "size", saved.Size, "job_id", resp.JobID, "duration", resp.Duration)

	return &Result{
		Content: fmt.Sprintf("Generated 1 video (artifact:%d). The artifact ID is an internal reference; never show it to the user. "+
			"The video is already queued and will be sent automatically as a separate video message right after your reply — "+
			"do not add MEDIA markers for it and do not tell the user you couldn't generate it. Describe it briefly and naturally.", id),
		GeneratedArtifactIDs: []int64{id},
		// For a video artifact "preview" means a native, inline-playing video
		// message; delivery never routes it through the photo compositor.
		GeneratedArtifacts: []artifactdelivery.Generated{{ArtifactID: id, Mode: artifactdelivery.ModePreview, Video: true}},
	}, nil
}

func intArg(args map[string]interface{}, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func videoStopRetryResultForFailure(f *videogen.Failure) *Result {
	switch f.Kind {
	case videogen.KindTimeout:
		return videoStopRetryResult("the video service didn't finish in time",
			"Explain in one sentence that video generation took too long and the user can try again later.")
	case videogen.KindSafetyBlock:
		return videoStopRetryResult("the video provider's safety system rejected the request",
			"Tell the user the request was blocked by the content policy and suggest rephrasing it or using a different image.")
	case videogen.KindJobFailed:
		reason := "The video job failed without a stated reason; tell the user it didn't work and they can try again or rephrase."
		if strings.TrimSpace(f.Text) != "" {
			reason = fmt.Sprintf("The video provider gave this reason; explain it to the user in their language: %q",
				textutil.TruncateRunes(f.Text, 300, "..."))
		}
		return videoStopRetryResult("the video job failed", reason)
	default:
		return videoStopRetryResult("the video provider returned an API error",
			"Explain in one sentence that the video service returned a temporary error and the user can try again shortly.")
	}
}

// videoStopRetryResult mirrors stopRetryResult for generate_video: a video
// job costs tens of cents and a minute or more, so a blind retry storm is
// far more expensive than for images.
func videoStopRetryResult(reason, instruction string) *Result {
	var sb strings.Builder
	sb.WriteString("VIDEO GENERATION FAILED. ")
	sb.WriteString(reason)
	sb.WriteString(". IMPORTANT INSTRUCTIONS FOR YOU: (1) Do NOT call generate_video again in this turn. (2) ")
	if instruction == "" {
		instruction = "Apologize briefly in the user's language and explain in one sentence that the video didn't work."
	}
	sb.WriteString(instruction)
	sb.WriteString(" (3) If appropriate, suggest trying again later — but do NOT attempt it yourself now.")
	return &Result{Content: sb.String()}
}
