package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/openrouter"
)

// Executor provides common execution logic for agents.
type Executor struct {
	client      openrouter.Client
	agentLogger *agentlog.Logger
	logger      *slog.Logger
}

// NewExecutor creates a new Executor.
func NewExecutor(
	client openrouter.Client,
	agentLogger *agentlog.Logger,
	logger *slog.Logger,
) *Executor {
	return &Executor{
		client:      client,
		agentLogger: agentLogger,
		logger:      logger.With("component", "agent_executor"),
	}
}

// SingleShotRequest for simple one-turn agents.
type SingleShotRequest struct {
	AgentType    AgentType
	UserID       int64
	Model        string
	SystemPrompt string
	UserPrompt   string
	Messages     []openrouter.Message // Alternative to SystemPrompt+UserPrompt
	Temperature  *float64
	JSONMode     bool
	JSONSchema   *openrouter.JSONSchema // Optional: strict JSON schema validation
}

// ExecuteSingleShot runs a single LLM call with logging.
func (e *Executor) ExecuteSingleShot(ctx context.Context, req SingleShotRequest) (*Response, error) {
	start := time.Now()

	messages := req.Messages
	if messages == nil {
		messages = []openrouter.Message{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		}
	}

	orReq := openrouter.ChatCompletionRequest{
		Model:    req.Model,
		Messages: messages,
		UserID:   req.UserID,
	}

	if req.JSONMode {
		if req.JSONSchema != nil {
			orReq.ResponseFormat = openrouter.ResponseFormatJSONSchema{
				Type:       "json_schema",
				JSONSchema: *req.JSONSchema,
			}
		} else {
			orReq.ResponseFormat = openrouter.ResponseFormat{Type: "json_object"}
		}
	}

	resp, err := e.client.CreateChatCompletion(ctx, orReq)
	duration := time.Since(start)

	if err != nil {
		e.logError(ctx, req, err, duration)
		return nil, fmt.Errorf("llm call failed: %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		e.logError(ctx, req, errors.New("empty response"), duration)
		return nil, errors.New("empty response from LLM")
	}

	result := &Response{
		Content:  resp.Choices[0].Message.Content,
		Duration: duration,
		Tokens: TokenUsage{
			Prompt:     resp.Usage.PromptTokens,
			Completion: resp.Usage.CompletionTokens,
			Total:      resp.Usage.TotalTokens,
			Cost:       resp.Usage.Cost,
		},
	}

	e.logSuccess(ctx, req, result, &resp)
	return result, nil
}

// AgenticOptions for multi-turn agents.
type AgenticOptions struct {
	MaxTurns    int
	Tools       []openrouter.Tool
	ToolHandler func(ctx context.Context, calls []openrouter.ToolCall) []openrouter.Message
	Timeout     time.Duration
	TurnTimeout time.Duration
	Reasoning   *openrouter.ReasoningConfig
	Plugins     []openrouter.Plugin
	ToolChoice  any // "auto", "none", "required", or specific tool
}

// ExecuteAgentic runs a multi-turn agent loop with tool calls.
func (e *Executor) ExecuteAgentic(ctx context.Context, req SingleShotRequest, opts AgenticOptions) (*Response, error) {
	tracker := agentlog.NewTurnTracker()

	messages := req.Messages
	if messages == nil {
		messages = []openrouter.Message{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		}
	}

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	for turn := 0; turn < opts.MaxTurns; turn++ {
		tracker.StartTurn()

		turnCtx := ctx
		var turnCancel context.CancelFunc
		if opts.TurnTimeout > 0 {
			turnCtx, turnCancel = context.WithTimeout(ctx, opts.TurnTimeout)
		}

		orReq := openrouter.ChatCompletionRequest{
			Model:     req.Model,
			Messages:  messages,
			Tools:     opts.Tools,
			UserID:    req.UserID,
			Reasoning: opts.Reasoning,
			Plugins:   opts.Plugins,
		}

		if opts.ToolChoice != nil {
			orReq.ToolChoice = opts.ToolChoice
		}

		resp, err := e.client.CreateChatCompletion(turnCtx, orReq)
		if turnCancel != nil {
			turnCancel()
		}
		if err != nil {
			return nil, fmt.Errorf("turn %d failed: %w", turn, err)
		}

		tracker.EndTurn(
			resp.DebugRequestBody,
			resp.DebugResponseBody,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.Cost,
		)

		if len(resp.Choices) == 0 {
			return nil, errors.New("empty response")
		}

		choice := resp.Choices[0]

		// No tool calls — we're done
		if len(choice.Message.ToolCalls) == 0 {
			promptTokens, completionTokens := tracker.TotalTokens()
			return &Response{
				Content:  choice.Message.Content,
				Duration: tracker.TotalDuration(),
				Tokens: TokenUsage{
					Prompt:     promptTokens,
					Completion: completionTokens,
					Total:      promptTokens + completionTokens,
					Cost:       tracker.TotalCost(),
				},
				Metadata: map[string]any{
					"turns":              tracker.TurnCount(),
					"conversation_turns": tracker.Build(),
				},
			}, nil
		}

		// Execute tools
		messages = append(messages, openrouter.Message{
			Role:      "assistant",
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		})

		if opts.ToolHandler != nil {
			toolResults := opts.ToolHandler(ctx, choice.Message.ToolCalls)
			messages = append(messages, toolResults...)
		}
	}

	return nil, fmt.Errorf("max turns (%d) exceeded", opts.MaxTurns)
}

func (e *Executor) logSuccess(ctx context.Context, req SingleShotRequest, result *Response, raw *openrouter.ChatCompletionResponse) {
	if e.agentLogger == nil {
		return
	}
	e.agentLogger.LogSuccess(ctx, req.UserID, agentlog.AgentType(req.AgentType),
		req.SystemPrompt, raw.DebugRequestBody,
		result.Content, nil, raw.DebugResponseBody,
		req.Model, result.Tokens.Prompt, result.Tokens.Completion,
		result.Tokens.Cost, int(result.Duration.Milliseconds()), nil)
}

func (e *Executor) logError(ctx context.Context, req SingleShotRequest, err error, duration time.Duration) {
	if e.agentLogger == nil {
		return
	}
	e.agentLogger.LogError(ctx, req.UserID, agentlog.AgentType(req.AgentType),
		req.SystemPrompt, nil, err.Error(), req.Model, int(duration.Milliseconds()), nil)
}

// Client returns the underlying OpenRouter client.
// Useful for agents that need custom request handling.
func (e *Executor) Client() openrouter.Client {
	return e.client
}

// AgentLogger returns the agent logger.
func (e *Executor) AgentLogger() *agentlog.Logger {
	return e.agentLogger
}
