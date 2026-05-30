package openrouter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/openrouter"
)

// mediaEncodingDirs are the packages that encode artifact/inbound files into LLM
// content parts. They MUST route visual media through openrouter.MediaPart so the
// image_url/video_url shape is selected on OpenAI-compatible backends — a raw
// FilePart{Type:"file"} silently 400s there (litellm/vLLM reject "file"). See
// CLAUDE.md "config-driven data shape" and the Stage D.1 plan.
var mediaEncodingDirs = []string{
	"internal/files",
	"internal/agent/laplace",
	"internal/agent/extractor",
}

// TestMediaEncodingGoesThroughMediaPart fails if any production file in the
// media-encoding packages hand-builds an openrouter.FilePart literal instead of
// calling openrouter.MediaPart. Cheap insurance against a new call site
// reintroducing the Gemini-only "file" shape on the Qwen contour.
func TestMediaEncodingGoesThroughMediaPart(t *testing.T) {
	root := findRepoRoot(t)
	var offenders []string
	for _, d := range mediaEncodingDirs {
		dir := filepath.Join(root, d)
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "openrouter.FilePart{") {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("found raw openrouter.FilePart{ literal(s) in media-encoding packages — "+
			"encode files via openrouter.MediaPart(cfg.OpenRouter.ImageInputFormat, …) so images/"+
			"videos use image_url/video_url on OpenAI-compatible backends (%d offender(s)): %s",
			len(offenders), strings.Join(offenders, ", "))
	}
}

func TestMediaPart_ShapeByFormat(t *testing.T) {
	const png = "data:image/png;base64,AAAA"
	const mp4 = "data:video/mp4;base64,BBBB"
	const pdf = "data:application/pdf;base64,CCCC"

	t.Run("openai format: image -> image_url", func(t *testing.T) {
		p := openrouter.MediaPart(openrouter.ImageInputFormatOpenAI, "image/png", "x.png", png)
		iu, ok := p.(openrouter.ImageURLPart)
		if !ok {
			t.Fatalf("want ImageURLPart, got %T", p)
		}
		if iu.Type != "image_url" || iu.ImageURL.URL != png {
			t.Errorf("bad ImageURLPart: %+v", iu)
		}
	})

	t.Run("openai format: video -> video_url", func(t *testing.T) {
		p := openrouter.MediaPart(openrouter.ImageInputFormatOpenAI, "video/mp4", "x.mp4", mp4)
		vu, ok := p.(openrouter.VideoURLPart)
		if !ok {
			t.Fatalf("want VideoURLPart, got %T", p)
		}
		if vu.Type != "video_url" || vu.VideoURL.URL != mp4 {
			t.Errorf("bad VideoURLPart: %+v", vu)
		}
	})

	t.Run("openai format: pdf -> file (non-visual)", func(t *testing.T) {
		if _, ok := openrouter.MediaPart(openrouter.ImageInputFormatOpenAI, "application/pdf", "x.pdf", pdf).(openrouter.FilePart); !ok {
			t.Error("pdf should stay FilePart even under openai format")
		}
	})

	t.Run("file format: image -> file", func(t *testing.T) {
		p := openrouter.MediaPart(openrouter.ImageInputFormatFile, "image/png", "x.png", png)
		fp, ok := p.(openrouter.FilePart)
		if !ok {
			t.Fatalf("want FilePart, got %T", p)
		}
		if fp.Type != "file" || fp.File.FileData != png || fp.File.FileName != "x.png" {
			t.Errorf("bad FilePart: %+v", fp)
		}
	})

	t.Run("empty format defaults to file", func(t *testing.T) {
		if _, ok := openrouter.MediaPart("", "image/png", "x.png", png).(openrouter.FilePart); !ok {
			t.Error("empty format should default to FilePart")
		}
	})
}
