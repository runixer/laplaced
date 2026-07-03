package laplace

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files with current output")

// TestBuildMessages_Golden pins the exact message assembly BuildMessages
// produces for the media/provenance scenario matrix: current attachments,
// recalled memory artifacts, their disambiguation markers (🎤/📷/🎥/📎 for
// current media, 📄 + memory_<id>_ prefix for recalled files), and part
// ordering. The refactor that moves marker generation into a single renderer
// must keep this output byte-for-byte identical — regenerating goldens during
// that refactor defeats the point of the test.
//
// Regenerate (only for INTENTIONAL prompt-shape changes):
//
//	go test ./internal/agent/laplace/ -run TestBuildMessages_Golden -update
func TestBuildMessages_Golden(t *testing.T) {
	// Fixed timestamps: artifact date is in a PAST year so the loader's
	// current-year short-date branch is not taken and output is stable.
	artifactDate := time.Date(2025, 1, 31, 12, 0, 0, 0, time.UTC)

	history := []storage.Message{
		{ID: 1, Role: "user", Content: "earlier question", CreatedAt: artifactDate},
		{ID: 2, Role: "assistant", Content: "earlier answer", CreatedAt: artifactDate},
		{ID: 3, Role: "user", Content: "current question", CreatedAt: artifactDate},
	}

	textPart := llm.TextPart{Type: "text", Text: "current question"}
	imagePart := llm.FilePart{Type: "file", File: llm.File{
		FileName: "photo.jpg", FileData: "data:image/jpeg;base64,SU1HLUJZVEVT"}}
	voicePart := llm.FilePart{Type: "file", File: llm.File{
		FileName: "voice.ogg", FileData: "data:audio/ogg;base64,QVVELUJZVEVT"}}

	// Artifact fixtures written to the loader's blob store per scenario.
	type artifactFixture struct {
		artifact storage.Artifact
		content  []byte
	}
	pdfArtifact := artifactFixture{
		artifact: storage.Artifact{
			ID: 7, State: "ready", FileType: "pdf", FilePath: "report.pdf",
			MimeType: "application/pdf", OriginalName: "report.pdf",
			FileSize: 9, CreatedAt: artifactDate,
		},
		content: []byte("PDF-BYTES"),
	}
	// No OriginalName → exercises the default-name branches
	// (displayName "artifact_9", filename "audio.ogg").
	audioArtifact := artifactFixture{
		artifact: storage.Artifact{
			ID: 9, State: "ready", FileType: "voice", FilePath: "old_voice.ogg",
			MimeType: "audio/ogg",
			FileSize: 9, CreatedAt: artifactDate,
		},
		content: []byte("AUD-BYTES"),
	}
	imgArtifact := artifactFixture{
		artifact: storage.Artifact{
			ID: 11, State: "ready", FileType: "image", FilePath: "diagram.png",
			MimeType: "image/png", OriginalName: "diagram.png",
			FileSize: 9, CreatedAt: artifactDate,
		},
		content: []byte("IMG-BYTES"),
	}
	videoArtifact := artifactFixture{
		artifact: storage.Artifact{
			ID: 13, State: "ready", FileType: "video", FilePath: "clip.mp4",
			MimeType: "video/mp4", OriginalName: "clip.mp4",
			FileSize: 9, CreatedAt: artifactDate,
		},
		content: []byte("VID-BYTES"),
	}

	tests := []struct {
		name             string
		imageInputFormat string // "" = keep config default ("file")
		currentParts     []interface{}
		history          []storage.Message
		artifacts        []artifactFixture
		artifactResults  []rag.ArtifactResult
		enrichedQuery    string
	}{
		{
			name:         "text_only",
			currentParts: []interface{}{textPart},
			history:      history,
		},
		{
			name:         "image_no_artifacts",
			currentParts: []interface{}{textPart, imagePart},
			history:      history,
		},
		{
			name:          "image_with_recalled_pdf",
			currentParts:  []interface{}{textPart, imagePart},
			history:       history,
			artifacts:     []artifactFixture{pdfArtifact},
			enrichedQuery: "user asks about the report",
			artifactResults: []rag.ArtifactResult{{
				ArtifactID: 7, FileType: "pdf", OriginalName: "report.pdf",
				Summary: "Quarterly report", Keywords: []string{"report", "q4"},
				Score: 0.87,
			}},
		},
		{
			// The bug-cluster configuration: live voice + recalled audio.
			name:         "voice_with_recalled_audio",
			currentParts: []interface{}{textPart, voicePart},
			history:      history,
			artifacts:    []artifactFixture{audioArtifact},
		},
		{
			// No current media: recalled file rides with the plain text turn,
			// and no current-media markers appear.
			name:      "artifact_only",
			history:   history,
			artifacts: []artifactFixture{pdfArtifact},
		},
		{
			// All three loader branches at once + openai part shapes
			// (image_url/video_url) for the litellm/vLLM contour.
			name:             "mixed_artifacts_openai_format",
			imageInputFormat: llm.ImageInputFormatOpenAI,
			currentParts:     []interface{}{textPart, imagePart},
			history:          history,
			artifacts:        []artifactFixture{pdfArtifact, imgArtifact, videoArtifact},
		},
		{
			// Empty history → the trailing-user-message fallback path.
			name:         "fallback_no_history",
			currentParts: []interface{}{textPart, imagePart},
			artifacts:    []artifactFixture{pdfArtifact},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, lap, mockStore, tempDir := setupArtifactTest(t)
			cfg.Bot.Language = "en"
			if tt.imageInputFormat != "" {
				cfg.LLM.ImageInputFormat = tt.imageInputFormat
			}

			var selectedIDs []int64
			for _, fx := range tt.artifacts {
				require.NoError(t, os.WriteFile(
					filepath.Join(tempDir, fx.artifact.FilePath), fx.content, 0600))
				a := fx.artifact
				mockStore.On("GetArtifact", storage.ScopeID("123"), a.ID).Return(&a, nil)
				selectedIDs = append(selectedIDs, a.ID)
			}
			if len(selectedIDs) > 0 {
				mockStore.On("IncrementContextLoadCount", storage.ScopeID("123"), selectedIDs).Return(nil)
			}

			cd := &ContextData{
				UserID:              storage.ScopeID("123"),
				BaseSystemPrompt:    "You are a helpful assistant.",
				RecentHistory:       tt.history,
				SelectedArtifactIDs: selectedIDs,
				ArtifactResults:     tt.artifactResults,
			}

			msgs := lap.BuildMessages(context.Background(), cd, "current question", tt.currentParts, tt.enrichedQuery)

			got, err := json.MarshalIndent(msgs, "", "  ")
			require.NoError(t, err)
			got = append(got, '\n')

			goldenPath := filepath.Join("testdata", "golden_context", tt.name+".json")
			if *updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0750))
				require.NoError(t, os.WriteFile(goldenPath, got, 0600))
				return
			}
			want, err := os.ReadFile(goldenPath)
			require.NoError(t, err, "golden file missing — run with -update to create")
			assert.Equal(t, string(want), string(got),
				"BuildMessages output diverged from golden; if the change is intentional, regenerate with -update")

			mockStore.AssertExpectations(t)
		})
	}
}
