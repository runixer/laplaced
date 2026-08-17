package laplace

import (
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
)

// BuildTools creates LLM tool definitions from config.
func BuildTools(cfg *config.Config, translator *i18n.Translator) []llm.Tool {
	var tools []llm.Tool
	lang := cfg.Bot.Language

	for _, toolCfg := range cfg.Tools {
		// send_artifacts is capability-scoped per request. Keeping it out of the
		// shared base slice makes it impossible to leak into legacy/group turns.
		if toolCfg.Name == "send_artifacts" {
			continue
		}
		desc := toolCfg.Description
		if desc == "" {
			desc = translator.Get(lang, "tools."+toolCfg.Name+".description")
		}

		var parameters map[string]interface{}
		switch toolCfg.Name {
		case "generate_image":
			parameters = buildImageGenerationSchema(&cfg.Agents.ImageGenerator)
		case "privacy_mode":
			// privacy_mode takes an enable/disable action, not a query.
			paramDesc := toolCfg.ParameterDescription
			if paramDesc == "" {
				paramDesc = translator.Get(lang, "tools.privacy_mode.parameter_description")
			}
			if paramDesc == "" {
				paramDesc = "Action: 'enable' to exclude new messages from long-term memory, 'disable' to store normally again."
			}
			parameters = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"enable", "disable"},
						"description": paramDesc,
					},
				},
				"required": []string{"action"},
			}
		case "read_url":
			// read_url takes a URL, not a search query — its own parameter
			// name keeps the model from pasting queries into it.
			paramDesc := toolCfg.ParameterDescription
			if paramDesc == "" {
				paramDesc = translator.Get(lang, "tools.read_url.parameter_description")
			}
			if paramDesc == "" {
				paramDesc = "Full URL of the page to read (https://...)."
			}
			parameters = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": paramDesc,
					},
				},
				"required": []string{"url"},
			}
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

		tool := llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        toolCfg.Name,
				Description: desc,
				Parameters:  parameters,
			},
		}
		tools = append(tools, tool)
	}

	return tools
}

// toolsForRequest returns a fresh tool slice for one Execute call. The shared
// base slice is immutable: send_artifacts is appended only for turns that the
// caller has already classified as private Telegram rich output.
func (l *Laplace) toolsForRequest(richOutput bool) []llm.Tool {
	extra := 0
	if richOutput {
		extra = 1
	}
	tools := make([]llm.Tool, 0, len(l.tools)+extra)
	for _, tool := range l.tools {
		// Defense in depth for tests/embedders constructing Laplace manually.
		if tool.Function.Name == "send_artifacts" {
			continue
		}
		if !richOutput && tool.Function.Name == "generate_image" {
			tool = previewOnlyImageTool(tool)
		}
		tools = append(tools, tool)
	}
	if richOutput {
		lang := "en"
		if l.cfg != nil && l.cfg.Bot.Language != "" {
			lang = l.cfg.Bot.Language
		}
		tools = append(tools, buildSendArtifactsTool(lang, l.translator))
	}
	return tools
}

// previewOnlyImageTool keeps the private-only artifact policy structural. A
// group, shadow, legacy or non-Telegram turn may still generate images through
// the existing compatibility path, but cannot ask the model for a Document
// presentation that the caller is not allowed to deliver.
func previewOnlyImageTool(tool llm.Tool) llm.Tool {
	parameters, _ := cloneToolSchemaValue(tool.Function.Parameters).(map[string]interface{})
	properties, _ := parameters["properties"].(map[string]interface{})
	delivery, _ := properties["delivery_mode"].(map[string]interface{})
	if delivery != nil {
		delivery["enum"] = []string{"preview"}
		delivery["description"] = "Use preview. Original-file delivery is available only in an eligible private rich chat."
	}
	tool.Function.Parameters = parameters
	return tool
}

func cloneToolSchemaValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		clone := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			clone[key] = cloneToolSchemaValue(child)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		clone := make([]interface{}, len(typed))
		for i, child := range typed {
			clone[i] = cloneToolSchemaValue(child)
		}
		return clone
	default:
		return value
	}
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
			"delivery_mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"preview", "original", "preview_and_original"},
				"description": "How to deliver this generated image. Use preview by default. Use original only when the user explicitly asks for a file/original/no compression, and preview_and_original only when they explicitly ask for both.",
			},
		},
		"required": []string{"prompt", "delivery_mode"},
	}
}

// buildSendArtifactsTool returns the private-rich-only declarative artifact
// delivery tool. The handler stages a delivery plan; the model never receives
// filesystem paths or transport identifiers.
func buildSendArtifactsTool(lang string, translator *i18n.Translator) llm.Tool {
	description := ""
	if translator != nil {
		description = translator.Get(lang, "tools.send_artifacts.description")
	}
	if description == "" {
		description = "Send existing trusted artifacts to the user in the requested presentation. Use only artifact IDs supplied by the application in the current turn."
	}
	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "send_artifacts",
			Description: description,
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{
						"type":        "array",
						"minItems":    1,
						"maxItems":    10,
						"description": "Artifacts to deliver, in the exact requested order.",
						"items": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": false,
							"properties": map[string]interface{}{
								"artifact_id": map[string]interface{}{
									"type":        "integer",
									"description": "An exact ID from the trusted artifact inventory supplied in this turn. Never guess an ID.",
								},
								"mode": map[string]interface{}{
									"type":        "string",
									"enum":        []string{"auto", "preview", "original", "preview_and_original"},
									"description": "auto chooses preview for a compatible image and original for other files; preview/original select one presentation; preview_and_original requests both.",
								},
							},
							"required": []string{"artifact_id", "mode"},
						},
					},
				},
				"required":             []string{"items"},
				"additionalProperties": false,
			},
		},
	}
}
