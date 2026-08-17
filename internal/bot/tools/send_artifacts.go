package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/runixer/laplaced/internal/artifactdelivery"
)

const maxStagedArtifactsPerCall = 10

type sendArtifactsArguments struct {
	Items []sendArtifactItem `json:"items"`
}

type sendArtifactItem struct {
	ArtifactID int64  `json:"artifact_id"`
	Mode       string `json:"mode"`
}

// performSendArtifacts validates and stages an immutable delivery selection.
// It deliberately performs no transport call and reads no file bytes: the
// outbound planner resolves every selected artifact atomically after the tool
// loop has finished.
func (e *ToolExecutor) performSendArtifacts(_ context.Context, cc CallContext, arguments string) (*Result, error) {
	if !cc.ArtifactDeliveryEnabled || e.artifactRepo == nil {
		return &Result{Content: "SEND_ARTIFACTS_UNAVAILABLE. Do not call this tool again in this turn; answer without attaching stored files."}, nil
	}
	var args sendArtifactsArguments
	dec := json.NewDecoder(bytes.NewBufferString(arguments))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("send_artifacts: invalid arguments: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("send_artifacts: invalid arguments: %w", err)
	}
	if len(args.Items) == 0 || len(args.Items) > maxStagedArtifactsPerCall {
		return nil, fmt.Errorf("send_artifacts: items must contain 1-%d entries", maxStagedArtifactsPerCall)
	}
	allowed := make(map[int64]struct{}, len(cc.TrustedArtifactIDs))
	for _, id := range cc.TrustedArtifactIDs {
		if id > 0 {
			allowed[id] = struct{}{}
		}
	}
	selected := make([]artifactdelivery.Selected, 0, len(args.Items))
	seen := make(map[int64]struct{}, len(args.Items))
	for i, item := range args.Items {
		if item.ArtifactID <= 0 {
			return nil, fmt.Errorf("send_artifacts: item %d has invalid artifact_id", i)
		}
		if _, ok := allowed[item.ArtifactID]; !ok {
			return &Result{Content: "SEND_ARTIFACTS_REJECTED. One or more requested files are not available in the trusted artifact inventory for this turn. Do not guess IDs or retry; ask the user to identify or attach the file."}, nil
		}
		if _, duplicate := seen[item.ArtifactID]; duplicate {
			return nil, fmt.Errorf("send_artifacts: item %d duplicates an earlier selection; use preview_and_original for both presentations", i)
		}
		seen[item.ArtifactID] = struct{}{}
		mode, err := artifactdelivery.ParseStoredMode(item.Mode)
		if err != nil {
			return nil, fmt.Errorf("send_artifacts: item %d: %w", i, err)
		}
		artifact, err := e.artifactRepo.GetArtifact(cc.UserID, item.ArtifactID)
		if err != nil {
			e.logger.Warn("send_artifacts artifact lookup failed", "artifact_id", item.ArtifactID, "user_id", cc.UserID, "err", err)
			return &Result{Content: "SEND_ARTIFACTS_REJECTED. A requested file could not be verified. Do not retry or guess another ID; ask the user to attach it again."}, nil
		}
		if artifact == nil || artifact.UserID != cc.UserID {
			return &Result{Content: "SEND_ARTIFACTS_REJECTED. One or more requested files are not available in the trusted artifact inventory for this turn. Do not guess IDs or retry."}, nil
		}
		isImage := strings.HasPrefix(strings.ToLower(strings.TrimSpace(artifact.MimeType)), "image/")
		if (mode == artifactdelivery.ModePreview || mode == artifactdelivery.ModePreviewAndOriginal) && !isImage {
			return nil, fmt.Errorf("send_artifacts: item %d is not an image and supports only auto/original", i)
		}
		selected = append(selected, artifactdelivery.Selected{ArtifactID: item.ArtifactID, Mode: mode})
	}
	return &Result{
		Content:           fmt.Sprintf("SEND_ARTIFACTS_STAGED. %d user-owned artifact(s) are queued for delivery after the final reply. Do not reveal artifact IDs or internal delivery details.", len(selected)),
		SelectedArtifacts: selected,
	}, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}
