package laplace

import (
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
)

// BuildTools creates OpenRouter tool definitions from config.
func BuildTools(cfg *config.Config, translator *i18n.Translator) []openrouter.Tool {
	var tools []openrouter.Tool
	lang := cfg.Bot.Language

	for _, toolCfg := range cfg.Tools {
		desc := toolCfg.Description
		if desc == "" {
			desc = translator.Get(lang, "tools."+toolCfg.Name+".description")
		}

		paramDesc := toolCfg.ParameterDescription
		if paramDesc == "" {
			paramDesc = translator.Get(lang, "tools."+toolCfg.Name+".parameter_description")
		}
		if paramDesc == "" {
			paramDesc = "Input prompt for the tool"
		}

		tool := openrouter.Tool{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        toolCfg.Name,
				Description: desc,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": paramDesc,
						},
					},
					"required": []string{"query"},
				},
			},
		}
		tools = append(tools, tool)
	}

	return tools
}
