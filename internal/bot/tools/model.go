package tools

import (
	"context"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/openrouter"
)

// performModelTool executes a custom LLM model call.
func (e *ToolExecutor) performModelTool(ctx context.Context, userID int64, modelName string, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)

	startTime := time.Now()

	req := openrouter.ChatCompletionRequest{
		Model: modelName,
		Messages: []openrouter.Message{
			{
				Role: "user",
				Content: []interface{}{
					openrouter.TextPart{Type: "text", Text: query},
				},
			},
		},
		UserID: userID,
	}

	resp, err := e.orClient.CreateChatCompletion(ctx, req)
	duration := time.Since(startTime)

	if err != nil {
		// Log error to Scout agent
		if e.agentLogger != nil {
			e.agentLogger.Log(ctx, agentlog.Entry{
				UserID:       userID,
				AgentType:    agentlog.AgentScout,
				InputPrompt:  query,
				Model:        modelName,
				DurationMs:   int(duration.Milliseconds()),
				Success:      false,
				ErrorMessage: err.Error(),
			})
		}
		return "", err
	}

	result := "No results found."
	if len(resp.Choices) > 0 {
		result = resp.Choices[0].Message.Content
	}

	// Log success to Scout agent
	if e.agentLogger != nil {
		e.agentLogger.Log(ctx, agentlog.Entry{
			UserID:           userID,
			AgentType:        agentlog.AgentScout,
			InputPrompt:      query,
			InputContext:     resp.DebugRequestBody, // Full API request JSON
			OutputResponse:   result,
			OutputContext:    resp.DebugResponseBody, // Full API response JSON
			Model:            modelName,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalCost:        resp.Usage.Cost,
			DurationMs:       int(duration.Milliseconds()),
			Success:          true,
		})
	}

	return result, nil
}
