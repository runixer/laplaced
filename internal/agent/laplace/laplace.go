package laplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

const (
	maxToolIterations = 10
	maxEmptyRetries   = 2
)

// Laplace is the main chat agent that handles user conversations.
type Laplace struct {
	cfg         *config.Config
	orClient    openrouter.Client
	ragService  *rag.Service
	msgRepo     storage.MessageRepository
	factRepo    storage.FactRepository
	translator  *i18n.Translator
	agentLogger *agentlog.Logger
	logger      *slog.Logger

	// Pre-built tools
	tools []openrouter.Tool
}

// New creates a new Laplace agent.
func New(
	cfg *config.Config,
	orClient openrouter.Client,
	ragService *rag.Service,
	msgRepo storage.MessageRepository,
	factRepo storage.FactRepository,
	translator *i18n.Translator,
	logger *slog.Logger,
) *Laplace {
	return &Laplace{
		cfg:        cfg,
		orClient:   orClient,
		ragService: ragService,
		msgRepo:    msgRepo,
		factRepo:   factRepo,
		translator: translator,
		logger:     logger.With("agent", "laplace"),
		tools:      BuildTools(cfg, translator),
	}
}

// SetAgentLogger sets the agent logger for debug logging.
func (l *Laplace) SetAgentLogger(logger *agentlog.Logger) {
	l.agentLogger = logger
}

// Type returns the agent type.
func (l *Laplace) Type() agent.AgentType {
	return agent.TypeLaplace
}

// Execute runs the main chat loop with tool calls.
func (l *Laplace) Execute(ctx context.Context, req *Request, toolHandler ToolHandler) (*Response, error) {
	logger := l.logger.With("user_id", req.UserID)

	// Load context data
	contextData, err := l.LoadContextData(ctx, req.UserID, req.RawQuery, req.CurrentMessageParts)
	if err != nil {
		return nil, fmt.Errorf("failed to load context: %w", err)
	}

	// Build initial messages
	enrichedQuery := ""
	if contextData.RAGInfo != nil {
		enrichedQuery = contextData.RAGInfo.EnrichedQuery
	}
	orMessages := l.BuildMessages(ctx, contextData, req.HistoryContent, req.CurrentMessageParts, enrichedQuery)

	// Prepare plugins
	var plugins []openrouter.Plugin
	if l.cfg.OpenRouter.PDFParserEngine != "" {
		plugins = append(plugins, openrouter.Plugin{
			ID: "file-parser",
			PDF: openrouter.PDFConfig{
				Engine: l.cfg.OpenRouter.PDFParserEngine,
			},
		})
	}

	// Tool loop
	tracker := agentlog.NewTurnTracker()
	var finalResponse string
	toolIterations := 0
	emptyRetries := 0

	var totalLLMDuration time.Duration
	var totalToolDuration time.Duration

	for {
		if toolIterations >= maxToolIterations {
			logger.Warn("max tool iterations reached", "iterations", toolIterations)
			break
		}
		toolIterations++

		orReq := openrouter.ChatCompletionRequest{
			Model:    l.cfg.Agents.Chat.GetModel(l.cfg.Agents.Default.Model),
			Messages: orMessages,
			Plugins:  plugins,
			Tools:    l.tools,
			UserID:   req.UserID,
		}

		tracker.StartTurn()
		llmStart := time.Now()
		resp, err := l.orClient.CreateChatCompletion(ctx, orReq)
		llmDuration := time.Since(llmStart)
		totalLLMDuration += llmDuration

		if err != nil {
			logger.Error("failed to get completion from OpenRouter", "error", err)
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		if len(resp.Choices) == 0 {
			logger.Warn("empty choices from OpenRouter",
				"model", resp.Model,
				"prompt_tokens", resp.Usage.PromptTokens,
				"completion_tokens", resp.Usage.CompletionTokens,
			)
			return nil, errors.New("empty response from LLM")
		}

		tracker.EndTurn(
			resp.DebugRequestBody,
			resp.DebugResponseBody,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.Cost,
		)

		choice := resp.Choices[0]

		// Check for empty response (no content and no tool calls) - retry
		if strings.TrimSpace(choice.Message.Content) == "" && len(choice.Message.ToolCalls) == 0 {
			logger.Warn("empty LLM response (no content, no tools)",
				"finish_reason", choice.FinishReason,
				"model", resp.Model,
				"retry_attempt", emptyRetries+1,
			)

			if emptyRetries < maxEmptyRetries {
				emptyRetries++
				logger.Info("retrying after empty response", "attempt", emptyRetries)
				continue
			}

			return nil, errors.New("max empty response retries reached")
		}

		// Reset retry counter on successful response
		if emptyRetries > 0 {
			logger.Info("retry successful after empty response", "attempts", emptyRetries)
			emptyRetries = 0
		}

		// Handle tool calls
		if len(choice.Message.ToolCalls) > 0 {
			logger.Info("Model requested tool calls", "count", len(choice.Message.ToolCalls))

			// Send intermediate message if present
			if choice.Message.Content != "" && strings.TrimSpace(choice.Message.Content) != "" {
				if req.OnIntermediateMessage != nil {
					req.OnIntermediateMessage(choice.Message.Content)
				}
			}

			// Add assistant message with tool calls
			var content interface{} = choice.Message.Content
			if choice.Message.Content == "" {
				content = nil
			}
			orMessages = append(orMessages, openrouter.Message{
				Role:             "assistant",
				Content:          content,
				ToolCalls:        choice.Message.ToolCalls,
				ReasoningDetails: choice.Message.ReasoningDetails,
			})

			// Execute tools
			toolStart := time.Now()
			toolMessages := l.executeToolCalls(ctx, toolHandler, choice.Message.ToolCalls, req.OnTypingAction, logger)
			totalToolDuration += time.Since(toolStart)

			orMessages = append(orMessages, toolMessages...)
			continue
		}

		// Final response
		finalResponse = choice.Message.Content
		break
	}

	// Sanitize response
	finalResponse, wasSanitized := SanitizeLLMResponse(finalResponse)
	if wasSanitized {
		logger.Warn("sanitized LLM response (removed hallucination artifacts)")
	}

	// Handle empty final response
	if strings.TrimSpace(finalResponse) == "" {
		finalResponse = l.translator.Get(l.cfg.Bot.Language, "bot.empty_response")
	}

	promptTokens, completionTokens := tracker.TotalTokens()

	return &Response{
		Content:           finalResponse,
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		TotalCost:         tracker.TotalCost(),
		LLMDuration:       totalLLMDuration,
		ToolDuration:      totalToolDuration,
		TotalTurns:        tracker.TurnCount(),
		RAGInfo:           contextData.RAGInfo,
		Messages:          orMessages,
		ConversationTurns: tracker.Build(),
	}, nil
}

// executeToolCalls executes tool calls and returns tool result messages.
func (l *Laplace) executeToolCalls(
	ctx context.Context,
	handler ToolHandler,
	toolCalls []openrouter.ToolCall,
	onTypingAction func(),
	logger *slog.Logger,
) []openrouter.Message {
	var toolMessages []openrouter.Message

	for _, toolCall := range toolCalls {
		if onTypingAction != nil {
			onTypingAction()
		}

		result, err := handler.ExecuteToolCall(toolCall.Function.Name, toolCall.Function.Arguments)
		if err != nil {
			logger.Error("tool execution failed", "error", err, "tool", toolCall.Function.Name)
			toolMessages = append(toolMessages, openrouter.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Tool execution failed: %v", err),
				ToolCallID: toolCall.ID,
			})
		} else {
			toolMessages = append(toolMessages, openrouter.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: toolCall.ID,
			})
		}
	}

	return toolMessages
}

// LogExecution logs the execution to agent logger.
func (l *Laplace) LogExecution(ctx context.Context, userID int64, resp *Response, cost float64) {
	if l.agentLogger == nil {
		return
	}

	fullPrompt := formatMessagesForLog(resp.Messages)

	var resultsJSON []byte
	if resp.RAGInfo != nil && len(resp.RAGInfo.Results) > 0 {
		resultsJSON, _ = json.Marshal(resp.RAGInfo.Results)
	}

	metadata := map[string]interface{}{}
	if resp.RAGInfo != nil {
		metadata["original_query"] = resp.RAGInfo.OriginalQuery
		metadata["enriched_query"] = resp.RAGInfo.EnrichedQuery
		metadata["retrieval_results"] = string(resultsJSON)
	}

	var firstRequest, lastResponse interface{}
	if resp.ConversationTurns != nil && len(resp.ConversationTurns.Turns) > 0 {
		firstRequest = resp.ConversationTurns.Turns[0].Request
		lastTurn := resp.ConversationTurns.Turns[len(resp.ConversationTurns.Turns)-1]
		lastResponse = lastTurn.Response
	}

	l.agentLogger.Log(ctx, agentlog.Entry{
		UserID:            userID,
		AgentType:         agentlog.AgentLaplace,
		InputPrompt:       fullPrompt,
		InputContext:      firstRequest,
		OutputResponse:    resp.Content,
		OutputContext:     lastResponse,
		Model:             l.cfg.Agents.Chat.GetModel(l.cfg.Agents.Default.Model),
		PromptTokens:      resp.PromptTokens,
		CompletionTokens:  resp.CompletionTokens,
		TotalCost:         &cost,
		DurationMs:        int(resp.LLMDuration.Milliseconds()),
		ConversationTurns: resp.ConversationTurns,
		Metadata:          metadata,
		Success:           true,
	})
}

// formatMessagesForLog formats OpenRouter messages into a human-readable string.
func formatMessagesForLog(messages []openrouter.Message) string {
	var sb strings.Builder
	for i, msg := range messages {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		role := strings.ToUpper(msg.Role)
		sb.WriteString(fmt.Sprintf("=== %s ===\n", role))

		switch content := msg.Content.(type) {
		case string:
			sb.WriteString(content)
		case []interface{}:
			for _, part := range content {
				if tp, ok := part.(openrouter.TextPart); ok {
					sb.WriteString(tp.Text)
				} else if m, ok := part.(map[string]interface{}); ok {
					if t, ok := m["type"].(string); ok && t == "text" {
						if text, ok := m["text"].(string); ok {
							sb.WriteString(text)
						}
					} else if t == "image_url" {
						sb.WriteString("[IMAGE]")
					} else if t == "input_audio" {
						sb.WriteString("[AUDIO]")
					}
				}
			}
		}

		if len(msg.ToolCalls) > 0 {
			sb.WriteString("\n\n[TOOL CALLS]")
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("\n- %s(%s)", tc.Function.Name, tc.Function.Arguments))
			}
		}
	}
	return sb.String()
}

// Hallucination sanitization

// HallucinationTags contains known hallucination markers that should be removed from LLM output.
var HallucinationTags = []string{
	"</tool_code>",
	"</tool_call>",
	"</s>",
	"<|endoftext|>",
	"<|end|>",
	"default_api:",
}

var consecutiveJSONBlocksPattern = regexp.MustCompile("(?s)(```json\\s*\\{[^`]*\\}\\s*```\\s*){3,}")
var defaultAPIPattern = regexp.MustCompile(`default_api:\w+\{`)

// SanitizeLLMResponse removes hallucination artifacts from LLM response.
// Returns the sanitized text and a boolean indicating if sanitization occurred.
func SanitizeLLMResponse(text string) (string, bool) {
	original := text
	sanitized := false

	// Remove Gemini's "default_api:" hallucinated tool calls
	if loc := defaultAPIPattern.FindStringIndex(text); loc != nil {
		startIdx := loc[1] - 1
		endIdx := FindMatchingBrace(text, startIdx)
		if endIdx != -1 {
			before := strings.TrimSpace(text[:loc[0]])
			after := strings.TrimSpace(text[endIdx+1:])
			if before != "" && after != "" {
				text = before + "\n\n" + after
			} else if after != "" {
				text = after
			} else {
				text = before
			}
			sanitized = true
		}
	}

	// Truncate on other hallucination tags
	for _, tag := range HallucinationTags {
		if tag == "default_api:" {
			continue
		}
		if idx := strings.Index(text, tag); idx != -1 {
			text = strings.TrimSpace(text[:idx])
			sanitized = true
		}
	}

	// Truncate on 3+ consecutive JSON blocks
	if loc := consecutiveJSONBlocksPattern.FindStringIndex(text); loc != nil {
		text = strings.TrimSpace(text[:loc[0]])
		sanitized = true
	}

	if sanitized && text == "" {
		return original, false
	}

	return text, sanitized
}

// FindMatchingBrace finds the index of the closing brace matching the opening brace at startIdx.
// Returns -1 if no matching brace is found.
func FindMatchingBrace(text string, startIdx int) int {
	if startIdx >= len(text) || text[startIdx] != '{' {
		return -1
	}

	depth := 0
	inString := false
	escaped := false

	for i := startIdx; i < len(text); i++ {
		c := text[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}
