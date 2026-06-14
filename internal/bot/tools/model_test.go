package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/llm"
)

func TestExtractCitations(t *testing.T) {
	t.Run("nil annotations", func(t *testing.T) {
		assert.Nil(t, extractCitations(nil))
	})

	t.Run("skips empty URLs, preserves order", func(t *testing.T) {
		ann := []llm.Annotation{
			{Type: "url_citation", URLCitation: llm.URLCitation{URL: "https://a.com", Title: "A"}},
			{Type: "url_citation", URLCitation: llm.URLCitation{URL: "", Title: "blank"}},
			{Type: "url_citation", URLCitation: llm.URLCitation{URL: "https://b.com", Title: "B"}},
		}
		got := extractCitations(ann)
		assert.Equal(t, []llm.Citation{
			{URL: "https://a.com", Title: "A"},
			{URL: "https://b.com", Title: "B"},
		}, got)
	})
}

func TestBuildSourceLegend(t *testing.T) {
	t.Run("no citations -> empty", func(t *testing.T) {
		assert.Equal(t, "", buildSourceLegend(nil))
	})

	t.Run("renders 1-based legend with all URLs", func(t *testing.T) {
		legend := buildSourceLegend([]llm.Citation{
			{URL: "https://a.com", Title: "A"},
			{URL: "https://b.com", Title: "B"},
		})
		assert.Contains(t, legend, "<sources")
		assert.Contains(t, legend, "[1] https://a.com")
		assert.Contains(t, legend, "[2] https://b.com")
		assert.Contains(t, legend, "</sources>")
		// Instruction to never invent URLs must be present for the model.
		assert.True(t, strings.Contains(legend, "Never invent"))
	})
}
