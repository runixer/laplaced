package openrouter_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// imageGenModelPattern matches model literals that require image output.
// The Modalities field is what actually triggers image generation on the
// OpenRouter side — without it the model answers in text only.
//
// Keep in sync with configs/default.yaml:agents.image_generator.model and
// any other call sites that target image-output models.
var imageGenModelPattern = regexp.MustCompile(
	`"(google/gemini-[0-9]+\.?[0-9]*-(?:flash|pro)-image[a-z0-9\-]*|` +
		`black-forest-labs/flux[a-z0-9\.\-]*|` +
		`sourceful/riverflow[a-z0-9\-]*)"`)

// TestAllImageGenerationRequestsSetModalities walks the tree for every
// openrouter.ChatCompletionRequest{...} literal that targets an image-output
// model and asserts it sets a Modalities field.
//
// Why this test exists: Modalities is a shape-changing field. Without it,
// image-output models fall back to text and the response.Message.Images
// slice stays nil — so the caller silently produces no images, no error,
// and the user sees a text reply where an image was expected. Mirrors the
// rationale for TestAllEmbeddingRequestsSetDimensions (v0.7.0 post-mortem):
// a single missing field causes a silent feature regression. We guard it
// statically.
func TestAllImageGenerationRequestsSetModalities(t *testing.T) {
	root := findRepoRoot(t)

	// Capture multi-line literal from opening brace to matching closing brace
	// on its own indented line (gofmt-style layout).
	re := regexp.MustCompile(`openrouter\.ChatCompletionRequest\{[\s\S]*?\n\s*\}`)

	var offenders []string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor", "node_modules", "data", "logs", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllString(string(b), -1) {
			if !imageGenModelPattern.MatchString(m) {
				continue
			}
			if !strings.Contains(m, "Modalities:") {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":\n"+indent(m, "    "))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	if len(offenders) > 0 {
		t.Fatalf("found ChatCompletionRequest literal(s) targeting an image-output model "+
			"without Modalities field — add `Modalities: []string{\"image\", \"text\"}` or the "+
			"model will silently return text-only responses (%d offender(s)):\n\n%s",
			len(offenders), strings.Join(offenders, "\n\n"))
	}
}
