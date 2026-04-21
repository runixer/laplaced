package openrouter_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAllEmbeddingRequestsSetDimensions walks the tree for every
// `openrouter.EmbeddingRequest{...}` literal in non-test production code and
// asserts it sets a `Dimensions:` field.
//
// Why this test exists (v0.7.0 post-mortem): the re-embed migration used the
// configured dim (1536) but the per-query embedding path still used the API
// default (3072). On-disk vectors and query vectors ended up in different
// spaces, `cosineSimilarity` returned 0 for every pair, RAG retrieval
// returned no topic candidates, and the reranker was silently never invoked
// because `shouldUseReranker` gates on candidate count > 0.
//
// A single missing field caused a full retrieval regression. We prevent it
// from recurring with a static check: every EmbeddingRequest literal must
// explicitly opt in to the configured dim (or opt out with an explicit `0`
// comment if some future caller has a reason).
func TestAllEmbeddingRequestsSetDimensions(t *testing.T) {
	root := findRepoRoot(t)

	// Fields we care about are multi-line literals. Capture from the opening
	// brace until the matching closing brace on its own indented line.
	// This is good enough for our codebase, which uses gofmt-style layout.
	re := regexp.MustCompile(`openrouter\.EmbeddingRequest\{[\s\S]*?\n\s*\}`)

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
			if !strings.Contains(m, "Dimensions:") {
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
		t.Fatalf("found EmbeddingRequest literal(s) without Dimensions field — "+
			"every call site must set Dimensions: cfg.Embedding.Dimensions to "+
			"stay in the configured embedding space (%d offender(s)):\n\n%s",
			len(offenders), strings.Join(offenders, "\n\n"))
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
