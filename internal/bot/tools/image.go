package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

var base64Std = base64.StdEncoding

// performImageGeneration drives the generate_image tool: it resolves input
// images (from input_artifact_ids or the current message), invokes the
// imagegen agent, persists each output image as an artifact, and returns
// a short summary text for the LLM plus the list of created artifact IDs.
//
// On failure (config issue, upstream 400, safety refusal, or disk error) this
// function deliberately returns a non-error *Result whose Content tells the
// LLM to STOP retrying. Returning an err here would instead bubble up as
// "Tool execution failed: …", which the LLM interprets as "try a different
// prompt" and cheerfully burns 5–10 more failing API calls before giving up.
func (e *ToolExecutor) performImageGeneration(ctx context.Context, cc CallContext, args map[string]interface{}) (*Result, error) {
	if e.imageGen == nil || e.artifactRepo == nil || e.fileStorage == nil {
		return stopRetryResult("image generation is not configured on this bot", "", nil), nil
	}

	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		// Empty prompt is an LLM mistake, not a safety/config issue — let
		// the normal error path surface it so the LLM fixes the arguments.
		return nil, fmt.Errorf("generate_image: prompt is required")
	}

	aspectRatio, _ := args["aspect_ratio"].(string)
	imageSize, _ := args["image_size"].(string)

	// Resolve input images:
	//   1. If input_artifact_ids is non-empty, load them from storage.
	//   2. Otherwise, fall back to images attached to the current user message.
	inputImages, err := e.resolveInputImages(ctx, cc, args)
	if err != nil {
		return stopRetryResult("failed to load input images", "", err), nil
	}

	// Respect the configured cap on reference images.
	if cap := e.cfg.Agents.ImageGenerator.MaxInputImages; cap > 0 && len(inputImages) > cap {
		inputImages = inputImages[:cap]
	}

	e.logger.Info("imagegen inputs resolved",
		"user_id", cc.UserID,
		"current_message_images", len(cc.CurrentMessageImages),
		"requested_artifact_ids", parseArtifactIDs(args["input_artifact_ids"]),
		"total_inputs", len(inputImages),
	)

	genResp, err := e.imageGen.Generate(ctx, ImageGenRequest{
		UserID:      cc.UserID,
		Prompt:      prompt,
		InputImages: inputImages,
		AspectRatio: aspectRatio,
		ImageSize:   imageSize,
	})
	if err != nil {
		var failure *ImageGenFailure
		if errors.As(err, &failure) {
			e.logger.Warn("imagegen Generate failed — telling LLM not to retry",
				"user_id", cc.UserID,
				"failure_kind", failure.Kind.String(),
				"provider", failure.Provider,
				"text_chars", len(failure.Text),
				"err", err,
			)
			return stopRetryResultForFailure(failure), nil
		}
		// Untyped error — defensive fallback. Real Generate always returns
		// *ImageGenFailure now, so this path mostly catches legacy/test
		// mocks that pass plain errors.
		e.logger.Warn("imagegen Generate failed (untyped) — telling LLM not to retry",
			"user_id", cc.UserID, "err", err)
		return stopRetryResult("the image-generation model rejected the request", "", err), nil
	}

	// Cap output images per configuration.
	outputs := genResp.Images
	if cap := e.cfg.Agents.ImageGenerator.MaxOutputImages; cap > 0 && len(outputs) > cap {
		outputs = outputs[:cap]
	}

	artifactIDs := make([]int64, 0, len(outputs))
	now := time.Now().UTC().Format("20060102T150405Z")
	for i, img := range outputs {
		ext := extFromMime(img.MimeType)
		filename := fmt.Sprintf("generated_%s_%d%s", now, i+1, ext)
		saved, err := e.fileStorage.SaveFile(ctx, cc.UserID, bytes.NewReader(img.Data), filename)
		if err != nil {
			e.logger.Warn("failed to save generated image", "index", i, "err", err)
			continue
		}
		artifact := storage.Artifact{
			UserID:       cc.UserID,
			MessageID:    0, // Updated later by orchestrator after assistant history row is saved
			FileType:     "image",
			FilePath:     saved.Path,
			FileSize:     saved.Size,
			MimeType:     img.MimeType,
			OriginalName: filename,
			ContentHash:  saved.ContentHash,
			State:        "pending", // Extractor background loop will pick this up
			UserContext:  strPtr(prompt),
		}
		id, err := e.artifactRepo.AddArtifact(artifact)
		if err != nil {
			e.logger.Warn("failed to create artifact row for generated image",
				"index", i, "err", err)
			continue
		}
		artifactIDs = append(artifactIDs, id)
		e.logger.Info("image generated and stored",
			"user_id", cc.UserID,
			"artifact_id", id,
			"filename", filename,
			"size", saved.Size,
		)
	}

	if len(artifactIDs) == 0 {
		e.logger.Error("all generated images failed to persist — telling LLM not to retry",
			"user_id", cc.UserID, "output_count", len(outputs))
		return stopRetryResult("the model produced images but they could not be saved to disk",
			"", fmt.Errorf("saved zero of %d output images", len(outputs))), nil
	}

	return &Result{
		Content:              buildToolReplyForLLM(artifactIDs, genResp.TextContent),
		GeneratedArtifactIDs: artifactIDs,
	}, nil
}

// resolveInputImages collects the full set of input reference images for a
// generate_image call by merging two sources:
//
//  1. Photos attached to the current user message (CurrentMessageImages).
//     These arrive as FileParts — the user just sent them, the LLM doesn't
//     know their artifact IDs yet.
//  2. Artifacts cited explicitly via input_artifact_ids — loaded from
//     storage, user-isolated.
//
// Both sources are merged, not one-or-the-other, so the common case
// "here's a new photo, mix it with that one from memory" works as expected.
// Artifacts whose underlying file matches a current-message photo by content
// hash are de-duplicated so we don't send the same image twice.
func (e *ToolExecutor) resolveInputImages(ctx context.Context, cc CallContext, args map[string]interface{}) ([]openrouter.FilePart, error) {
	parts := make([]openrouter.FilePart, 0, len(cc.CurrentMessageImages)+4)
	parts = append(parts, cc.CurrentMessageImages...)

	ids := parseArtifactIDs(args["input_artifact_ids"])
	if len(ids) == 0 {
		return parts, nil
	}

	// Load each artifact (user-isolated) and convert to a FilePart.
	for _, id := range ids {
		art, err := e.artifactRepo.GetArtifact(cc.UserID, id)
		if err != nil {
			return nil, fmt.Errorf("load artifact %d: %w", id, err)
		}
		if art == nil {
			e.logger.Warn("requested input artifact not found", "artifact_id", id, "user_id", cc.UserID)
			continue
		}
		if !strings.HasPrefix(art.MimeType, "image/") {
			return nil, fmt.Errorf("artifact %d is %q, not an image", id, art.MimeType)
		}
		dataURL, err := e.readArtifactAsDataURL(ctx, art)
		if err != nil {
			return nil, fmt.Errorf("read artifact %d: %w", id, err)
		}
		// Skip if this exact data URL is already in parts (current-message
		// photo matched an artifact by content — rare but possible after
		// Extractor has indexed the attachment).
		if partsContainDataURL(parts, dataURL) {
			continue
		}
		parts = append(parts, openrouter.FilePart{
			Type: "file",
			File: openrouter.File{
				FileName: art.OriginalName,
				FileData: dataURL,
			},
		})
	}
	return parts, nil
}

func partsContainDataURL(parts []openrouter.FilePart, url string) bool {
	for _, p := range parts {
		if p.File.FileData == url {
			return true
		}
	}
	return false
}

// readArtifactAsDataURL reads an artifact file from storage and returns a
// base64 data URL suitable for use as openrouter.FilePart.FileData.
func (e *ToolExecutor) readArtifactAsDataURL(ctx context.Context, art *storage.Artifact) (string, error) {
	data, err := e.fileStorage.ReadFile(ctx, art.FilePath)
	if err != nil {
		return "", err
	}
	// Use base64 std encoding (matches the rest of the codebase's FilePart usage).
	return "data:" + art.MimeType + ";base64," + base64Std.EncodeToString(data), nil
}

func buildToolReplyForLLM(artifactIDs []int64, modelText string) string {
	var sb strings.Builder
	if len(artifactIDs) == 1 {
		fmt.Fprintf(&sb, "Generated 1 image (artifact:%d).", artifactIDs[0])
	} else {
		fmt.Fprintf(&sb, "Generated %d images (", len(artifactIDs))
		for i, id := range artifactIDs {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "artifact:%d", id)
		}
		sb.WriteString(").")
	}
	sb.WriteString(" Describe them briefly for the user in your reply; caption must be ≤1000 characters. ")
	sb.WriteString("The images are already queued for delivery — do NOT tell the user you couldn't generate them.")
	if strings.TrimSpace(modelText) != "" {
		sb.WriteString(" Model note: ")
		sb.WriteString(modelText)
	}
	return sb.String()
}

// parseArtifactIDs accepts either []interface{} (JSON numbers) or []int64
// and returns a normalized int64 slice.
func parseArtifactIDs(v interface{}) []int64 {
	switch x := v.(type) {
	case nil:
		return nil
	case []int64:
		return x
	case []interface{}:
		out := make([]int64, 0, len(x))
		for _, e := range x {
			switch n := e.(type) {
			case float64:
				out = append(out, int64(n))
			case int:
				out = append(out, int64(n))
			case int64:
				out = append(out, n)
			case json.Number:
				if id, err := n.Int64(); err == nil {
					out = append(out, id)
				}
			}
		}
		return out
	}
	return nil
}

// extFromMime picks a filesystem extension for a given MIME type.
// Falls back to ".png" for unknown image mimetypes since nano banana
// returns PNGs by default.
func extFromMime(mimeType string) string {
	exts, _ := mime.ExtensionsByType(mimeType)
	for _, e := range exts {
		if e == ".png" || e == ".jpg" || e == ".jpeg" || e == ".webp" {
			return e
		}
	}
	return ".png"
}

func strPtr(s string) *string { return &s }

// stopRetryResultForFailure is the kind-aware variant of stopRetryResult.
// Same anti-retry guard, but instruction (2) is tailored to the actual
// failure mode so the LLM stops parroting "safety filter / invalid input /
// temporary API issue" when the cause is a timeout or a clear text refusal.
//
// The five wording variants correspond to the five failure modes the
// classifier emits. KindTextRefusal additionally has the model's own
// refusal text quoted verbatim — that string is the highest-signal hint
// the user can act on.
func stopRetryResultForFailure(f *ImageGenFailure) *Result {
	var reason, instructionTwo string
	switch f.Kind {
	case ImageGenKindTimeout:
		reason = "the image-generation server didn't respond in time"
		instructionTwo = "Explain in one sentence that the image-generation server didn't respond in time and the user should try again later."
	case ImageGenKindUpstreamError:
		reason = "the image provider returned an API error"
		instructionTwo = "Explain in one sentence that the image provider returned a transient API error and the user should try again shortly."
	case ImageGenKindTextRefusal:
		reason = "the model refused to generate this image and explained why"
		instructionTwo = fmt.Sprintf(
			"The model itself refused to generate this image and explained why in plain text. Quote its reason verbatim to the user (translate to their language if needed) so they understand exactly what to change. Refusal text: %q",
			truncate(f.Text, 300),
		)
	case ImageGenKindSilentBlockOAI:
		reason = "OpenAI's image model returned no image and no explanation"
		instructionTwo = "OpenAI's image model returned no image and gave no explanation. This almost always means the upstream safety pipeline blocked the output — the model itself doesn't say what was flagged. Tell the user this looks like a content-policy issue and suggest they rephrase the prompt or, if an input image was provided, try a different one."
	default: // ImageGenKindUnknownNoImages, ImageGenKindUnknown
		reason = "the model returned an empty response with no explanation"
		instructionTwo = "The model returned an empty response with no specific reason. Tell the user the generation didn't work and suggest they try again or rephrase — no specific reason is known."
	}

	var sb strings.Builder
	sb.WriteString("IMAGE GENERATION FAILED. ")
	sb.WriteString(reason)
	sb.WriteString(". ")
	if f.Cause != nil {
		fmt.Fprintf(&sb, "Internal detail: %v. ", f.Cause)
	}
	sb.WriteString("IMPORTANT INSTRUCTIONS FOR YOU: ")
	sb.WriteString("(1) Do NOT call generate_image again in this turn — further attempts will almost certainly fail the same way. ")
	sb.WriteString("(2) ")
	sb.WriteString(instructionTwo)
	sb.WriteString(" ")
	sb.WriteString("(3) If appropriate, suggest they try different wording or try again later — but do NOT attempt it yourself now.")
	return &Result{Content: sb.String()}
}

// stopRetryResult builds a tool-result message designed to halt the LLM's
// natural "try again with different wording" instinct after an image-gen
// failure. The LLM sees this content and (per the explicit instruction)
// should apologize to the user in its final reply instead of calling the
// tool again — saving us from 5–10 more failing API calls per turn.
//
// reason is a short plain-English explanation of what went wrong; modelText
// is optional text the model itself emitted (e.g. a safety-policy message);
// underlying is the Go error, stringified for debugging. All three are
// optional and any subset may be empty.
//
// Used for non-imagegen-failure cases (config not present, all output images
// failed to persist) and as the defensive fallback when the imagegen agent
// returns an untyped error (legacy mocks). Kind-aware imagegen failures go
// through stopRetryResultForFailure.
func stopRetryResult(reason, modelText string, underlying error) *Result {
	var sb strings.Builder
	sb.WriteString("IMAGE GENERATION FAILED. ")
	if reason != "" {
		sb.WriteString(reason)
		sb.WriteString(". ")
	}
	if underlying != nil {
		fmt.Fprintf(&sb, "Internal detail: %v. ", underlying)
	}
	if strings.TrimSpace(modelText) != "" {
		fmt.Fprintf(&sb, "Model note: %s ", truncate(strings.TrimSpace(modelText), 300))
	}
	sb.WriteString("IMPORTANT INSTRUCTIONS FOR YOU: ")
	sb.WriteString("(1) Do NOT call generate_image again in this turn — further attempts will almost certainly fail the same way. ")
	sb.WriteString("(2) Apologize to the user briefly in their language, explain in one sentence that generation didn't work (e.g. safety filter, invalid input, temporary API issue). ")
	sb.WriteString("(3) If appropriate, suggest they try different wording or try again later — but do NOT attempt it yourself now.")
	return &Result{Content: sb.String()}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
