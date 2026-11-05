package laplace

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/config"
)

func TestCountGeneratedPreviews_SkipsVideos(t *testing.T) {
	got := countGeneratedPreviews([]artifactdelivery.Generated{
		{ArtifactID: 1, Mode: artifactdelivery.ModePreview, Video: true},
		{ArtifactID: 2, Mode: artifactdelivery.ModePreview},
		{ArtifactID: 3, Mode: artifactdelivery.ModeOriginal},
		{ArtifactID: 4, Mode: artifactdelivery.ModePreviewAndOriginal},
	})
	assert.Equal(t, 2, got, "videos are delivered after the reply and take no MEDIA ordinal")
}

func TestBuildVideoGenerationSchema_EnumsFromConfig(t *testing.T) {
	schema := buildVideoGenerationSchema(&config.VideoGeneratorConfig{
		DefaultAspectRatio:    "9:16",
		DefaultDuration:       6,
		SupportedAspectRatios: []string{"9:16", "16:9"},
		SupportedDurations:    []int{4, 6, 8},
	})
	props := schema["properties"].(map[string]interface{})
	assert.Equal(t, []string{"9:16", "16:9"}, props["aspect_ratio"].(map[string]interface{})["enum"])
	assert.Equal(t, []int{4, 6, 8}, props["duration"].(map[string]interface{})["enum"])
	assert.Equal(t, []string{"prompt"}, schema["required"])
	assert.Contains(t, props["prompt"].(map[string]interface{})["description"], "Reels")
}
