package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrubModelArtifactReferences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "canonical", in: "before artifact:42 after", want: "before  after"},
		{name: "parenthesized", in: "before (artifact: 42) after", want: "before  after"},
		{name: "square", in: "before [Artifact:42] after", want: "before  after"},
		{name: "spaced id", in: "before ARTIFACT ID: 42 after", want: "before  after"},
		{name: "underscore equals", in: "before artifact_id=42 after", want: "before  after"},
		{name: "dash hash", in: "before artifact-id # 42 after", want: "before  after"},
		{name: "direct hash", in: "before artifact #42 after", want: "before  after"},
		{name: "context xml", in: `before <artifact id="42" filename="secret.png"> after`, want: "before  after"},
		{name: "inside code", in: "`artifact:42`", want: "``"},
		{
			name: "near misses",
			in:   "input_artifact_ids artifact_context artifacts:42 artifact:abc artifact:42x xartifact:42 file42.png",
			want: "input_artifact_ids artifact_context artifacts:42 artifact:abc artifact:42x xartifact:42 file42.png",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scrubModelArtifactReferences(tt.in))
		})
	}
}

func TestSanitizeModelPresentationWithholdsTerminalArtifactPrefixes(t *testing.T) {
	for _, suffix := range []string{
		"artifact", "Artifact ", "artifact:", "artifact:  ", "artifact #",
		"artifact i", "artifact id", "artifact id:", "artifact_", "artifact_i",
		"artifact_id=", "artifact-", "artifact-id #", "[Artifact:", "(artifact id:",
	} {
		t.Run(suffix, func(t *testing.T) {
			assert.Equal(t, "visible ", sanitizeModelPresentation("visible "+suffix))
		})
	}
	assert.Equal(t, "visible artifact:abc", sanitizeModelPresentation("visible artifact:abc"))
	assert.Equal(t, "visible input_artifact", sanitizeModelPresentation("visible input_artifact"))
	assert.Equal(t, "visible ", sanitizeModelPresentation("visible artifact:42"))
	assert.Equal(t, "K visible ", sanitizeModelPresentation("K visible artifact:"))
	for _, suffix := range []string{`<artifact`, `<  artifact id="`, `<artifact id="42`, `<artifact id="42" filename="x"`} {
		assert.Equal(t, "visible ", sanitizeModelPresentation("visible "+suffix), suffix)
	}
}

func TestHTMLSafeArgPreservesOrdinaryArtifactWord(t *testing.T) {
	assert.Equal(t, "draw an ancient artifact", htmlSafeArg("draw an ancient artifact"))
	assert.Equal(t, "compare", htmlSafeArg("compare artifact:42"))
}
