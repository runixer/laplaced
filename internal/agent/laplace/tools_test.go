package laplace

import (
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTools(t *testing.T) {
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	tests := []struct {
		name     string
		cfg      *config.Config
		expected int
		verify   func(t *testing.T, tools []string)
	}{
		{
			name:     "no tools",
			cfg:      &config.Config{Tools: []config.ToolConfig{}},
			expected: 0,
		},
		{
			name: "single tool with config description",
			cfg: &config.Config{
				Tools: []config.ToolConfig{
					{
						Name:                 "search_web",
						Description:          "Search the web",
						ParameterDescription: "Query to search",
					},
				},
			},
			expected: 1,
			verify: func(t *testing.T, tools []string) {
				assert.Equal(t, []string{"search_web"}, tools)
			},
		},
		{
			name: "multiple tools",
			cfg: &config.Config{
				Tools: []config.ToolConfig{
					{Name: "search_web", Description: "Search web"},
					{Name: "search_history", Description: "Search history"},
					{Name: "manage_memory", Description: "Manage memory"},
				},
			},
			expected: 3,
			verify: func(t *testing.T, tools []string) {
				assert.ElementsMatch(t, []string{"search_web", "search_history", "manage_memory"}, tools)
			},
		},
		{
			name: "tool with default parameter description",
			cfg: &config.Config{
				Tools: []config.ToolConfig{
					{Name: "test_tool", Description: "A test tool"},
				},
			},
			expected: 1,
			verify: func(t *testing.T, tools []string) {
				// Should have the default parameter description
				assert.Equal(t, []string{"test_tool"}, tools)
			},
		},
		{
			name: "tool structure",
			cfg: &config.Config{
				Tools: []config.ToolConfig{
					{
						Name:                 "my_tool",
						Description:          "My description",
						ParameterDescription: "My param",
					},
				},
			},
			expected: 1,
			verify: func(t *testing.T, tools []string) {
				assert.Equal(t, []string{"my_tool"}, tools)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := BuildTools(tt.cfg, translator)
			assert.Len(t, tools, tt.expected)

			if tt.verify != nil {
				names := make([]string, len(tools))
				for i, tool := range tools {
					names[i] = tool.Function.Name
					// Verify basic structure
					assert.Equal(t, "function", tool.Type)
					assert.NotEmpty(t, tool.Function.Name)
					assert.NotEmpty(t, tool.Function.Description)
					assert.NotNil(t, tool.Function.Parameters)
				}
				tt.verify(t, names)
			}
		})
	}
}

func TestBuildTools_ParameterStructure(t *testing.T) {
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	cfg := &config.Config{
		Tools: []config.ToolConfig{
			{Name: "test_tool", Description: "Test description"},
		},
	}

	tools := BuildTools(cfg, translator)
	require.Len(t, tools, 1)

	params, ok := tools[0].Function.Parameters.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)
	queryParam, ok := props["query"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "string", queryParam["type"])
	assert.NotEmpty(t, queryParam["description"])

	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"query"}, required)
}

// TestImageGenerationSchemaEnums guards the generate_image schema against
// silently advertising values the upstream model rejects. The schema is now
// derived from ImageGeneratorConfig.SupportedImageSizes / SupportedAspectRatios,
// so the test verifies the wiring across two real-world fixtures (nano banana
// and openai/gpt-5.4-image-2) plus the empty-list fallback.
//
// When the upstream enum doesn't match what the LLM advertised, callers see a
// 400 INVALID_ARGUMENT and the bot makes up a "safety filter" story to the user
// — the regressions that motivated this guard. Both enum sets were verified
// end-to-end via curl on 2026-04-30.
func TestImageGenerationSchemaEnums(t *testing.T) {
	geminiSizes := []string{"1K", "2K", "4K"}
	geminiAspects := []string{
		"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4",
		"9:16", "16:9", "21:9",
		"1:4", "4:1", "1:8", "8:1",
	}
	openaiSizes := []string{"1K", "2K"}
	openaiAspects := []string{
		"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4",
		"9:16", "16:9", "21:9",
	}

	tests := []struct {
		name        string
		cfg         *config.ImageGeneratorConfig
		wantSizes   []string
		wantAspects []string
		// forbidden values that MUST NOT appear in the schema for this fixture.
		forbiddenSizes   []string
		forbiddenAspects []string
	}{
		{
			name: "nano banana — full set",
			cfg: &config.ImageGeneratorConfig{
				SupportedImageSizes:   geminiSizes,
				SupportedAspectRatios: geminiAspects,
			},
			wantSizes:        geminiSizes,
			wantAspects:      geminiAspects,
			forbiddenSizes:   []string{"0.5K", "512"},
			forbiddenAspects: nil,
		},
		{
			name: "openai/gpt-5.4-image-2 — narrowed set",
			cfg: &config.ImageGeneratorConfig{
				SupportedImageSizes:   openaiSizes,
				SupportedAspectRatios: openaiAspects,
			},
			wantSizes:        openaiSizes,
			wantAspects:      openaiAspects,
			forbiddenSizes:   []string{"4K", "0.5K"},
			forbiddenAspects: []string{"1:4", "4:1", "1:8", "8:1"},
		},
		{
			name:             "empty config — falls back to gemini superset",
			cfg:              &config.ImageGeneratorConfig{},
			wantSizes:        geminiSizes,
			wantAspects:      geminiAspects,
			forbiddenSizes:   []string{"0.5K", "512"},
			forbiddenAspects: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := buildImageGenerationSchema(tt.cfg)
			props, ok := schema["properties"].(map[string]interface{})
			require.True(t, ok)

			imageSize, ok := props["image_size"].(map[string]interface{})
			require.True(t, ok, "image_size property missing from schema")
			gotSizes, ok := imageSize["enum"].([]string)
			require.True(t, ok)
			assert.Equal(t, tt.wantSizes, gotSizes)
			for _, v := range tt.forbiddenSizes {
				assert.NotContains(t, gotSizes, v,
					"image_size enum must not include %q for this model", v)
			}

			aspectRatio, ok := props["aspect_ratio"].(map[string]interface{})
			require.True(t, ok, "aspect_ratio property missing from schema")
			gotAspects, ok := aspectRatio["enum"].([]string)
			require.True(t, ok)
			assert.Equal(t, tt.wantAspects, gotAspects)
			for _, v := range tt.forbiddenAspects {
				assert.NotContains(t, gotAspects, v,
					"aspect_ratio enum must not include %q for this model", v)
			}
		})
	}
}
