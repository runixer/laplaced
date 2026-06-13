package laplace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func imageFilePart() llm.FilePart {
	return llm.FilePart{Type: "file", File: llm.File{FileName: "photo.jpg", FileData: "data:image/jpeg;base64,AAAA"}}
}

// TestCurrentMediaMarker_LocalesDefined guards the i18n key the fix depends on:
// removing it silently disables the anti-confusion marker.
func TestCurrentMediaMarker_LocalesDefined(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		tr, err := i18n.NewTranslator(lang)
		require.NoError(t, err)
		marker := tr.Get(lang, "bot.current_media_marker")
		assert.NotEmpty(t, marker, "current_media_marker missing for %s", lang)
		assert.True(t, strings.HasPrefix(marker, "📷"), "%s marker must start with 📷, got %q", lang, marker)
	}
}

// TestMarkCurrentMedia covers the helper in isolation: a 📷 marker is inserted
// before every media part (any type), never before text, order is preserved.
func TestMarkCurrentMedia(t *testing.T) {
	cfg, _, lap, _, _ := setupArtifactTest(t)
	cfg.Bot.Language = "en"
	marker := lap.translator.Get("en", "bot.current_media_marker")
	require.NotEmpty(t, marker)

	voice := llm.FilePart{Type: "file", File: llm.File{FileName: "v.ogg", FileData: "data:audio/ogg;base64,AAAA"}}
	pdf := llm.FilePart{Type: "file", File: llm.File{FileName: "d.pdf", FileData: "data:application/pdf;base64,AAAA"}}
	text := llm.TextPart{Type: "text", Text: "edit this"}

	tests := []struct {
		name      string
		in        []interface{}
		wantLen   int
		markerIdx []int // indices in the OUTPUT where the marker text part must sit
	}{
		{"text + image", []interface{}{text, imageFilePart()}, 3, []int{1}},
		{"image only", []interface{}{imageFilePart()}, 2, []int{0}},
		{"voice + pdf (media-agnostic, each marked)", []interface{}{voice, pdf}, 4, []int{0, 2}},
		{"text only — untouched", []interface{}{text}, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := lap.markCurrentMedia(tt.in)
			require.Len(t, out, tt.wantLen)
			for _, idx := range tt.markerIdx {
				tp, ok := out[idx].(llm.TextPart)
				require.True(t, ok, "expected TextPart marker at %d", idx)
				assert.Equal(t, marker, tp.Text)
				// the part right after a marker must be the media it labels
				_, isFile := out[idx+1].(llm.FilePart)
				assert.True(t, isFile, "marker at %d must immediately precede a media part", idx)
			}
			if tt.markerIdx == nil {
				for _, p := range out {
					if tp, ok := p.(llm.TextPart); ok {
						assert.NotEqual(t, marker, tp.Text, "no marker expected for text-only input")
					}
				}
			}
		})
	}
}

// lastUserParts returns the content parts of the final user message.
func lastUserParts(t *testing.T, msgs []llm.Message) []interface{} {
	t.Helper()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			parts, ok := msgs[i].Content.([]interface{})
			require.True(t, ok)
			return parts
		}
	}
	t.Fatal("no user message")
	return nil
}

func hasMarker(parts []interface{}, marker string) bool {
	for _, p := range parts {
		if tp, ok := p.(llm.TextPart); ok && tp.Text == marker {
			return true
		}
	}
	return false
}

// TestBuildMessages_CurrentMediaMarker_Gate proves the marker fires only when a
// memory artifact also rides along (the only ambiguous case) — and not on the
// common single-attachment path.
func TestBuildMessages_CurrentMediaMarker_Gate(t *testing.T) {
	userID := storage.ScopeID("123")
	marker, err := i18n.NewTranslator("en")
	require.NoError(t, err)
	markerText := marker.Get("en", "bot.current_media_marker")
	require.NotEmpty(t, markerText)

	base := func() *ContextData {
		return &ContextData{
			UserID:           userID,
			BaseSystemPrompt: "You are a helpful assistant.",
			RecentHistory:    []storage.Message{{ID: 1, Role: "user", Content: "edit this"}},
		}
	}
	current := []interface{}{imageFilePart()}

	t.Run("no memory artifacts -> no marker (common path unchanged)", func(t *testing.T) {
		cfg, _, lap, _, _ := setupArtifactTest(t)
		cfg.Bot.Language = "en"
		msgs := lap.BuildMessages(context.Background(), base(), "edit this", current, "")
		parts := lastUserParts(t, msgs)
		assert.False(t, hasMarker(parts, markerText), "no marker without memory artifacts")
	})

	t.Run("memory artifact present -> current media marked before it", func(t *testing.T) {
		cfg, _, lap, mockStore, tempDir := setupArtifactTest(t)
		cfg.Bot.Language = "en"
		require.NoError(t, os.WriteFile(filepath.Join(tempDir, "mem.pdf"), []byte("remembered doc"), 0600))
		mockStore.On("GetArtifact", userID, int64(7)).Return(&storage.Artifact{
			ID: 7, State: "ready", FileType: "pdf", FilePath: "mem.pdf", FileSize: 14, CreatedAt: time.Now(),
		}, nil)
		mockStore.On("IncrementContextLoadCount", userID, []int64{int64(7)}).Return(nil)

		cd := base()
		cd.SelectedArtifactIDs = []int64{7}
		msgs := lap.BuildMessages(context.Background(), cd, "edit this", current, "")
		parts := lastUserParts(t, msgs)

		// 📷 marker present, and it sits immediately before the current image part.
		idx := -1
		for i, p := range parts {
			if tp, ok := p.(llm.TextPart); ok && tp.Text == markerText {
				idx = i
				break
			}
		}
		require.GreaterOrEqual(t, idx, 0, "current-media marker must be present")
		fp, ok := parts[idx+1].(llm.FilePart)
		require.True(t, ok, "marker must precede the current media part")
		assert.True(t, strings.HasPrefix(fp.File.FileData, "data:image/"), "marked part is the current image")
		mockStore.AssertExpectations(t)
	})
}
