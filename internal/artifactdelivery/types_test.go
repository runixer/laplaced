package artifactdelivery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGeneratedMode(t *testing.T) {
	for input, want := range map[string]Mode{
		"": ModePreview, "preview": ModePreview, "original": ModeOriginal,
		"preview_and_original": ModePreviewAndOriginal,
	} {
		got, err := ParseGeneratedMode(input)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := ParseGeneratedMode("auto")
	assert.Error(t, err)
}

func TestParseStoredMode(t *testing.T) {
	for input, want := range map[string]Mode{
		"": ModeAuto, "auto": ModeAuto, "preview": ModePreview,
		"original": ModeOriginal, "preview_and_original": ModePreviewAndOriginal,
	} {
		got, err := ParseStoredMode(input)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := ParseStoredMode("compressed")
	assert.Error(t, err)
}
