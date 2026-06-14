package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
)

// performModelTool executes a custom LLM model call. For web-search models
// (perplexity/sonar-*) it also extracts source citations: the real URLs are
// appended to the returned text as a legend the main model can weave into its
// reply, and returned as []llm.Citation so the orchestrator can verify links.
func (e *ToolExecutor) performModelTool(ctx context.Context, userID storage.ScopeID, modelName string, args map[string]interface{}) (string, []llm.Citation, error) {
	query, _ := args["query"].(string)

	startTime := time.Now()

	req := llm.ChatCompletionRequest{
		Model: modelName,
		Messages: []llm.Message{
			{
				Role: "user",
				Content: []interface{}{
					llm.TextPart{Type: "text", Text: query},
				},
			},
		},
		UserID: string(userID),
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
		return "", nil, err
	}

	result := "No results found."
	var citations []llm.Citation
	if len(resp.Choices) > 0 {
		result = resp.Choices[0].Message.Content
		citations = extractCitations(resp.Choices[0].Message.Annotations)
		if legend := buildSourceLegend(citations); legend != "" {
			result += legend
		}
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

	return result, citations, nil
}

// extractCitations pulls url_citation annotations (in order) into a flat
// []llm.Citation, skipping any with an empty URL.
func extractCitations(annotations []llm.Annotation) []llm.Citation {
	if len(annotations) == 0 {
		return nil
	}
	citations := make([]llm.Citation, 0, len(annotations))
	for _, a := range annotations {
		if a.URLCitation.URL == "" {
			continue
		}
		citations = append(citations, llm.Citation{URL: a.URLCitation.URL, Title: a.URLCitation.Title})
	}
	return citations
}

// buildSourceLegend renders a model-facing legend that maps the search
// result's 1-based [N] markers to their real URLs, with an instruction to use
// only these URLs and never invent any. Returns "" when there are no sources.
func buildSourceLegend(citations []llm.Citation) string {
	if len(citations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<sources note=\"Use ONLY these URLs when linking sources; [N] maps to source N. Never invent or alter a URL.\">\n")
	for i, c := range citations {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, c.URL)
	}
	b.WriteString("</sources>")
	return b.String()
}
