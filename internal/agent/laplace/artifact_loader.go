package laplace

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
)

// ArtifactLoader turns reranker-selected artifact IDs into recalled
// TaggedParts: fetches each artifact, validates it is loadable, reads the
// bytes from the blob store, and builds the LLM media part with the
// "memory_<id>_" filename anchor. Markers are NOT its job — renderTagged
// generates them from the returned provenance fields.
//
// Limits (max artifacts, cumulative bytes) come from reranker.artifacts
// config and are read per Load call.
type ArtifactLoader struct {
	cfg    *config.Config
	repo   storage.ArtifactRepository
	blobs  files.Storage
	logger *slog.Logger
}

// NewArtifactLoader wires an ArtifactLoader. Either repo or blobs may be nil
// (artifacts disabled) — Load then returns nothing.
func NewArtifactLoader(cfg *config.Config, repo storage.ArtifactRepository, blobs files.Storage, logger *slog.Logger) *ArtifactLoader {
	return &ArtifactLoader{cfg: cfg, repo: repo, blobs: blobs, logger: logger}
}

// artifactLoader assembles a loader from the agent's current wiring. Built
// per call (a cheap struct) because fileStorage arrives via SetFileStorage
// after New, so caching one at construction time would pin a stale nil.
func (l *Laplace) artifactLoader() *ArtifactLoader {
	return NewArtifactLoader(l.cfg, l.artifactRepo, l.fileStorage, l.logger)
}

// Load fetches the selected artifacts and returns them as recalled
// TaggedParts, one per successfully loaded artifact, in selection order.
// Unloadable artifacts (missing, failed extraction, unsupported MIME or file
// type, unreadable bytes) are skipped with a log; hitting the count or size
// limit stops loading. Side effects: emits the laplace.artifacts_loaded span
// event and increments each loaded artifact's usage counter.
func (al *ArtifactLoader) Load(ctx context.Context, userID storage.ScopeID, artifactIDs []int64) ([]TaggedPart, error) {
	if al.repo == nil || len(artifactIDs) == 0 || al.blobs == nil {
		return nil, nil
	}

	maxArtifacts := al.cfg.Agents.Reranker.Artifacts.Max
	if maxArtifacts <= 0 {
		maxArtifacts = 10
	}
	maxBytes := al.cfg.Agents.Reranker.Artifacts.MaxContextBytes
	if maxBytes <= 0 {
		maxBytes = 20 * 1024 * 1024 // 20MB default (aligned with Telegram Bot API limit)
	}

	var out []TaggedPart
	var totalBytes int
	var loadedArtifactIDs []int64
	// Capture each loaded artifact's metadata for the laplace.artifacts_loaded
	// span event. Accumulated during the loop and emitted once at the end so
	// replay sees the full set as a single batch entry. content_hash matches
	// artifacts.content_hash → enables snapshot-DB lookup without raw base64.
	loadedEntries := make([]agent.MediaEntry, 0, len(artifactIDs))

	for _, artifactID := range artifactIDs {
		if len(out) >= maxArtifacts {
			al.logger.Info("artifact count limit reached",
				"loaded", len(out),
				"max", maxArtifacts,
				"remaining", len(artifactIDs)-len(out),
			)
			break
		}

		artifact := al.fetchLoadable(userID, artifactID)
		if artifact == nil {
			continue
		}

		// Check cumulative size limit BEFORE reading the file.
		if totalBytes+int(artifact.FileSize) > maxBytes {
			al.logger.Info("artifact size limit would be exceeded",
				"artifact_id", artifactID,
				"artifact_size", artifact.FileSize,
				"current_total", totalBytes,
				"max_bytes", maxBytes,
			)
			break
		}

		fileData, err := al.blobs.ReadFile(ctx, artifact.FilePath)
		if err != nil {
			al.logger.Warn("failed to read artifact file", "artifact_id", artifactID, "key", artifact.FilePath, "error", err)
			continue
		}

		part, kind, ok := al.recalledPart(artifact, fileData)
		if !ok {
			// Unknown file type: skipped entirely — no budget consumed, no
			// usage counted, no dangling marker (previously the marker
			// TextPart was already appended and counters bumped).
			al.logger.Warn("unknown artifact file type", "artifact_id", artifactID, "file_type", artifact.FileType)
			continue
		}

		totalBytes += len(fileData)
		loadedArtifactIDs = append(loadedArtifactIDs, artifact.ID)
		// Sha256 here — the file is on disk by content_hash already, so this
		// is mainly a defensive recompute (~5MB/ms). Replay validates: if the
		// hash in the trace does not match what's on disk in the snapshot,
		// surface a clear error rather than silently using corrupted bytes.
		fileHash := sha256.Sum256(fileData)
		loadedEntries = append(loadedEntries, agent.MediaEntry{
			Filename:  displayName(artifact),
			Mime:      artifact.MimeType,
			SizeBytes: len(fileData),
			SHA256:    hex.EncodeToString(fileHash[:]),
			Source:    "reranker_selected",
		})

		out = append(out, TaggedPart{
			Part:       part,
			Source:     SourceRecalled,
			Kind:       kind,
			ArtifactID: artifact.ID,
			Name:       displayName(artifact),
			CreatedAt:  artifact.CreatedAt,
		})

		al.logger.Debug("loaded artifact for context", "artifact_id", artifactID, "file_type", artifact.FileType, "size_bytes", len(fileData))
	}

	if len(out) > 0 {
		al.logger.Debug("artifact loading complete",
			"loaded_count", len(out),
			"total_bytes", totalBytes,
			"max_artifacts", maxArtifacts,
			"max_bytes", maxBytes,
		)
	}

	// Emit laplace.artifacts_loaded under the laplace.Execute span — replay
	// reads this to learn which artifacts the agent actually saw this turn,
	// in what order, and with what bytes (sha256 contract). Always emit when
	// we loaded anything; absence in the trace is meaningful.
	if len(loadedEntries) > 0 && obs.ContentEnabled() {
		if body, err := json.Marshal(loadedEntries); err == nil {
			obs.RecordContent(trace.SpanFromContext(ctx), "laplace.artifacts_loaded", string(body))
		}
	}

	// Track artifact usage. Synchronous to avoid a race with shutdown;
	// latency (~5-10ms) is negligible next to the LLM call.
	if len(loadedArtifactIDs) > 0 {
		if err := al.repo.IncrementContextLoadCount(userID, loadedArtifactIDs); err != nil {
			al.logger.Warn("failed to increment artifact load count", "error", err)
		}
	}

	return out, nil
}

// fetchLoadable returns the artifact if it exists and is in a loadable state
// with a Gemini-supported MIME type; nil (with a log) otherwise.
func (al *ArtifactLoader) fetchLoadable(userID storage.ScopeID, artifactID int64) *storage.Artifact {
	artifact, err := al.repo.GetArtifact(userID, artifactID)
	if err != nil {
		al.logger.Warn("failed to load artifact", "artifact_id", artifactID, "error", err)
		return nil
	}
	// 'pending'/'processing' artifacts are loadable — the file exists, only the
	// extracted metadata is missing (session-injected candidates arrive in this
	// state). 'failed' stays excluded: a file that broke extraction (e.g. a
	// safety-blocked one) must never be re-injected into context.
	if artifact == nil || artifact.State == "failed" {
		state := "<nil>"
		if artifact != nil {
			state = artifact.State
		}
		al.logger.Debug("artifact not loadable", "artifact_id", artifactID, "state", state)
		return nil
	}

	// Validate MIME type is supported by Gemini (skip unsupported old artifacts)
	if artifact.MimeType != "" && !files.IsGeminiSupported(artifact.MimeType) {
		al.logger.Debug("skipping artifact with unsupported MIME type",
			"artifact_id", artifactID,
			"mime_type", artifact.MimeType,
			"original_name", artifact.OriginalName,
		)
		return nil
	}

	return artifact
}

// recalledPart builds the LLM media part for a recalled artifact. The
// FilePart filename is prefixed "memory_<id>_" so the binding the model makes
// between the 📄 text marker, the FilePart bytes, and the <artifact_context>
// summary is anchored on a unique token instead of a generic "photo.jpg".
// ok=false means the artifact's file type is unknown.
func (al *ArtifactLoader) recalledPart(artifact *storage.Artifact, fileData []byte) (part interface{}, kind MediaKind, ok bool) {
	fileName, mimeType, kind, ok := recalledFileMeta(artifact)
	if !ok {
		return nil, kind, false
	}
	fileName = fmt.Sprintf("memory_%d_%s", artifact.ID, fileName)
	// Normalize MIME type for Gemini (e.g., text/x-web-markdown -> text/plain)
	mimeType = files.NormalizeMimeForGemini(mimeType)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(fileData))
	return llm.MediaPart(al.cfg.LLM.ImageInputFormat, mimeType, fileName, dataURL), kind, true
}

// recalledFileMeta resolves the per-file-type defaults for filename and MIME
// type, and classifies the artifact's media kind. This collapses what used to
// be three near-identical switch branches: the branches differed only in
// their fallback values.
func recalledFileMeta(a *storage.Artifact) (fileName, mimeType string, kind MediaKind, ok bool) {
	fileName = a.OriginalName
	mimeType = a.MimeType
	switch a.FileType {
	case "pdf", "document":
		if fileName == "" {
			fileName = "artifact.pdf"
		}
		if mimeType == "" {
			if a.FileType == "pdf" {
				mimeType = "application/pdf"
			} else {
				mimeType = "application/octet-stream"
			}
		}
		return fileName, mimeType, KindFile, true
	case "image", "photo":
		if fileName == "" {
			fileName = "image"
		}
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		return fileName, mimeType, KindImage, true
	case "voice", "audio", "video_note", "video":
		if fileName == "" {
			fileName = "audio." + audioExtForMime(a.MimeType)
		}
		if mimeType == "" {
			mimeType = "audio/ogg"
		}
		kind = KindAudio
		if a.FileType == "video_note" || a.FileType == "video" {
			kind = KindVideo
		}
		return fileName, mimeType, kind, true
	default:
		return "", "", KindFile, false
	}
}

// audioExtForMime picks the default filename extension for an audio/video
// artifact with no original name.
func audioExtForMime(mimeType string) string {
	switch mimeType {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/m4a":
		return "m4a"
	case "video/mp4":
		return "mp4"
	default:
		return "ogg"
	}
}

// displayName is the human-readable artifact name used in the 📄 marker and
// telemetry entries.
func displayName(a *storage.Artifact) string {
	if a.OriginalName != "" {
		return a.OriginalName
	}
	return fmt.Sprintf("artifact_%d", a.ID)
}
