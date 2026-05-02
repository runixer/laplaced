package laplace

import (
	"testing"

	"github.com/runixer/laplaced/internal/rag"
)

func TestFormatArtifactResults_XMLStructure(t *testing.T) {
	result := rag.ArtifactResult{
		ArtifactID:   1,
		FileType:     "pdf",
		OriginalName: "meeting.pdf",
		Summary:      "Project timeline discussion",
		Keywords:     []string{"project", "timeline", "meeting"},
		Score:        0.85,
	}

	results := []rag.ArtifactResult{result}
	output := formatArtifactResults(results, "project deadline")

	// Check XML structure
	if !containsSubstring(output, "<artifact_context") {
		t.Error("missing <artifact_context> tag")
	}
	if !containsSubstring(output, "</artifact_context>") {
		t.Error("missing closing </artifact_context> tag")
	}
	if !containsSubstring(output, "<artifact") {
		t.Error("missing <artifact> tag")
	}
	if !containsSubstring(output, "</artifact>") {
		t.Error("missing closing </artifact> tag")
	}

	// Check attributes
	if !containsSubstring(output, `id="1"`) {
		t.Error("missing artifact id attribute")
	}
	if !containsSubstring(output, `type="pdf (memory_1_meeting.pdf)"`) {
		t.Error("missing or incorrect type attribute")
	}
	if !containsSubstring(output, `relevance="0.85"`) {
		t.Error("missing or incorrect relevance attribute")
	}

	// Check content
	if !containsSubstring(output, "<summary>") {
		t.Error("missing <summary> tag")
	}
	if !containsSubstring(output, "Project timeline discussion") {
		t.Error("missing summary content")
	}
	// v0.6.0: Keywords are included
	if !containsSubstring(output, "<keywords>") {
		t.Error("missing <keywords> tag")
	}
}

func TestFormatArtifactResults_Escaping(t *testing.T) {
	result := rag.ArtifactResult{
		ArtifactID:   1,
		FileType:     "document",
		OriginalName: "report <2025>.pdf",
		Summary:      "Summary with & quotes \" and ' apostrophes",
		Keywords:     []string{"tag", "test"},
		Score:        0.75,
	}

	results := []rag.ArtifactResult{result}
	output := formatArtifactResults(results, "test")

	// Check XML escaping in attributes (type, summary)
	if !containsSubstring(output, "&lt;") {
		t.Error("angle brackets not escaped in attributes")
	}
	if !containsSubstring(output, "&quot;") {
		t.Error("quotes not escaped in attributes")
	}
	if !containsSubstring(output, "&amp;") {
		t.Error("ampersands not escaped")
	}
}

func TestFormatArtifactResults_EmptyResults(t *testing.T) {
	output := formatArtifactResults([]rag.ArtifactResult{}, "test")
	if output != "" {
		t.Errorf("expected empty string for no results, got: %s", output)
	}
}

func TestFormatArtifactResults_NoSummary(t *testing.T) {
	result := rag.ArtifactResult{
		ArtifactID:   1,
		FileType:     "voice",
		OriginalName: "message.ogg",
		Summary:      "",
		Keywords:     []string{},
		Score:        0.65,
	}

	results := []rag.ArtifactResult{result}
	output := formatArtifactResults(results, "test")

	// Should still have artifact tag even without summary
	if !containsSubstring(output, "<artifact") {
		t.Error("missing <artifact> tag when summary is empty")
	}
}

func TestFormatArtifactResults_QueryEscaping(t *testing.T) {
	result := rag.ArtifactResult{
		ArtifactID:   1,
		FileType:     "pdf",
		OriginalName: "test.pdf",
		Summary:      "Content",
		Keywords:     []string{},
		Score:        0.5,
	}

	results := []rag.ArtifactResult{result}
	queryWithSpecialChars := "query with \"quotes\" and <brackets> & ampersands"
	output := formatArtifactResults(results, queryWithSpecialChars)

	// Check that query in attribute is escaped
	if !containsSubstring(output, "&quot;") {
		t.Error("quotes in query not escaped")
	}
	if !containsSubstring(output, "&lt;") {
		t.Error("angle brackets in query not escaped")
	}
	if !containsSubstring(output, "&amp;") {
		t.Error("ampersands in query not escaped")
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
