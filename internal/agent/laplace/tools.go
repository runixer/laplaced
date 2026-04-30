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

		var parameters map[string]interface{}
		switch toolCfg.Name {
		case "generate_image":
			parameters = buildImageGenerationSchema(&cfg.Agents.ImageGenerator)
		default:
			paramDesc := toolCfg.ParameterDescription
			if paramDesc == "" {
				paramDesc = translator.Get(lang, "tools."+toolCfg.Name+".parameter_description")
			}
			if paramDesc == "" {
				paramDesc = "Input prompt for the tool"
			}
			parameters = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": paramDesc,
					},
				},
				"required": []string{"query"},
			}
		}

		tool := openrouter.Tool{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        toolCfg.Name,
				Description: desc,
				Parameters:  parameters,
			},
		}
		tools = append(tools, tool)
	}

	return tools
}

// buildImageGenerationSchema returns the JSON schema for the generate_image
// tool, with the aspect_ratio and image_size enums derived from
// ImageGeneratorConfig so the LLM only ever sees options the upstream model
// accepts. Empty lists fall back to the nano-banana superset for safety.
func buildImageGenerationSchema(cfg *config.ImageGeneratorConfig) map[string]interface{} {
	sizes := cfg.SupportedImageSizes
	if len(sizes) == 0 {
		sizes = []string{"1K", "2K", "4K"}
	}
	aspects := cfg.SupportedAspectRatios
	if len(aspects) == 0 {
		aspects = []string{
			"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4",
			"9:16", "16:9", "21:9",
			"1:4", "4:1", "1:8", "8:1",
		}
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Detailed image description in any language. Be specific about subject, style, composition, lighting.",
			},
			"aspect_ratio": map[string]interface{}{
				"type":        "string",
				"enum":        aspects,
				"description": "Aspect ratio. Default is 9:16 (vertical, optimized for phone screens) — leave unset for portraits, selfies, and most everyday shots. Override only when the framing actively wants something else: 16:9 for wide/landscape, 1:1 for square, 21:9 for cinematic.",
			},
			"image_size": map[string]interface{}{
				"type":        "string",
				"enum":        sizes,
				"description": "Output resolution. Default 1K. Higher sizes cost more and take longer.",
			},
			"input_artifact_ids": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "integer"},
				"description": "Optional artifact IDs from <artifact_context> or history to use as reference/edit source. When omitted, any photos attached to the current user message are used automatically.",
			},
		},
		"required": []string{"prompt"},
	}
}
