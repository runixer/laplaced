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
