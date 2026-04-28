package reranker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// Reranker uses tool calls to select the most relevant topics from vector search candidates.
type Reranker struct {
	client      openrouter.Client
	cfg         *config.Config
	logger      *slog.Logger
	translator  *i18n.Translator
	msgRepo     MessageRepository
	agentLogger *agentlog.Logger
}

// New creates a new Reranker agent.
func New(
	client openrouter.Client,
	cfg *config.Config,
	logger *slog.Logger,
	translator *i18n.Translator,
	msgRepo MessageRepository,
	agentLogger *agentlog.Logger,
) *Reranker {
	return &Reranker{
		client:      client,
		cfg:         cfg,
		logger:      logger.With("component", "reranker"),
		translator:  translator,
		msgRepo:     msgRepo,
		agentLogger: agentLogger,
	}
}

// Type returns the agent type.
func (r *Reranker) Type() agent.AgentType {
	return agent.TypeReranker
}

// Capabilities returns the agent's capabilities.
func (r *Reranker) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:    true,
		OutputFormat: "json",
	}
}

// Description returns a human-readable description.
func (r *Reranker) Description() string {
	return "Selects relevant topics from candidates using tool calls"
}

// Execute runs the reranker with the given request.
// Required params: candidates, contextualized_query, original_query, current_messages
// Optional params: media_parts, person_candidates (v0.5.1), artifact_candidates (v0.6.0)
// Uses SharedContext for user_profile and recent_topics if available.
func (r *Reranker) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	// Extract parameters
	candidates, ok := req.Params[ParamCandidates].([]Candidate)
	if !ok {
		return nil, fmt.Errorf("candidates parameter required")
	}

	// v0.5.1: Optional person candidates
	personCandidates, _ := req.Params[ParamPersonCandidates].([]PersonCandidate)
	// v0.6.0: Optional artifact candidates
	artifactCandidates, _ := req.Params[ParamArtifactCandidates].([]ArtifactCandidate)

	contextualizedQuery, _ := req.Params[ParamContextualizedQuery].(string)
	originalQuery, _ := req.Params[ParamOriginalQuery].(string)
	currentMessages, _ := req.Params[ParamCurrentMessages].(string)
	mediaParts, _ := req.Params[ParamMediaParts].([]interface{})

	// Get user profile and recent topics from SharedContext
	userProfile, recentTopics, _ := agent.GetSharedContext(ctx, req)
	userID := agent.GetUserID(req)

	result, err := r.rerank(ctx, userID, candidates, personCandidates, artifactCandidates, contextualizedQuery, originalQuery, currentMessages, userProfile, recentTopics, mediaParts)
	if err != nil {
		return nil, err
	}

	return &agent.Response{
		Structured: result,
		Metadata: map[string]any{
			"topics_count":    len(result.Topics),
			"people_count":    len(result.People),
			"artifacts_count": len(result.Artifacts),
		},
	}, nil
}

// rerank is the main reranking logic.
//
// Complexity: MEDIUM-HIGH (CC=42) - agentic loop with tool calls, fallbacks, error handling
// Dependencies: orClient, cfg
// Side effects: Logs to agentlog
// Error handling: Returns fallback result on timeout/API error
//
// Uses agentic loop with tool calls to select relevant topics, people, and artifacts.
func (r *Reranker) rerank(
	ctx context.Context,
	userID int64,
	candidates []Candidate,
	personCandidates []PersonCandidate,
	artifactCandidates []ArtifactCandidate,
	contextualizedQuery string,
	originalQuery string,
	currentMessages string,
	userProfile string,
	recentTopics string,
	mediaParts []interface{},
) (*Result, error) {
	cfg := r.cfg.Agents.Reranker

	// Tracing-observable state hoisted here so the deferred End() sees the
	// right values regardless of which internal return fires. tr stays nil
	// if we bail out via the "disabled or empty candidates" shortcut.
	var (
		iterations int
		tr         *trace
	)
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/agent/reranker").Start(
		ctx, "reranker.Execute",
		oteltrace.WithAttributes(attribute.Int64("user.id", userID)),
	)
	defer func() {
		reason := ""
		var (
			llmCalls                              int
			costUSD                               float64
			rawTopics, rawPeople, rawArtifacts    int
			keptTopics, keptPeople, keptArtifacts int
		)
		if tr != nil {
			reason = tr.fallbackReason
			rawTopics = tr.modelRawTopics
			rawPeople = tr.modelRawPeople
			rawArtifacts = tr.modelRawArtifacts
			keptTopics = tr.modelKeptTopics
			keptPeople = tr.modelKeptPeople
			keptArtifacts = tr.modelKeptArtifacts
			if tr.tracker != nil {
				llmCalls = tr.tracker.TurnCount()
				costUSD = tr.tracker.TotalCostValue()
			}
		}
		span.SetAttributes(
			attribute.Int("reranker.tool_calls", iterations),
			attribute.Int("reranker.llm_calls", llmCalls),
			attribute.String("reranker.fallback_reason", reason),
			attribute.Int("reranker.candidates_in.topics", len(candidates)),
			attribute.Int("reranker.candidates_in.people", len(personCandidates)),
			attribute.Int("reranker.candidates_in.artifacts", len(artifactCandidates)),
			attribute.Int("reranker.candidates_in.media", len(mediaParts)),
			attribute.Int("reranker.model_raw_count.topics", rawTopics),
			attribute.Int("reranker.model_raw_count.people", rawPeople),
			attribute.Int("reranker.model_raw_count.artifacts", rawArtifacts),
			attribute.Int("reranker.model_kept.topics", keptTopics),
			attribute.Int("reranker.model_kept.people", keptPeople),
			attribute.Int("reranker.model_kept.artifacts", keptArtifacts),
			attribute.Float64("reranker.cost_usd", costUSD),
		)
		if obs.ContentEnabled() {
			if originalQuery != "" {
				obs.RecordContent(span, "reranker.raw_query", originalQuery)
			}
			if contextualizedQuery != "" {
				obs.RecordContent(span, "reranker.enriched_query", contextualizedQuery)
			}
			// Shared context that lands in the system prompt — captured so a
			// faithful replay can reconstruct the exact reranker input without
			// drifting through the live DB at replay time.
			if userProfile != "" {
				obs.RecordContent(span, "reranker.user_profile", userProfile)
			}
			if recentTopics != "" {
				obs.RecordContent(span, "reranker.recent_topics", recentTopics)
			}
			if len(candidates) > 0 {
				obs.RecordContent(span, "reranker.candidates_input",
					formatCandidatesForReranker(candidates))
			}
			if len(personCandidates) > 0 {
				obs.RecordContent(span, "reranker.people_candidates_input",
					FormatPeopleForReranker(personCandidates))
			}
			if len(artifactCandidates) > 0 {
				obs.RecordContent(span, "reranker.artifacts_candidates_input",
					formatArtifactCandidates(artifactCandidates))
			}
			// Multimodal inputs (images, voice, PDFs). Recorded as metadata +
			// content_hash so a faithful replay can re-fetch the file from
			// artifact storage by hash without the trace carrying base64.
			if len(mediaParts) > 0 {
				if body := formatMediaParts(mediaParts); body != "" {
					obs.RecordContent(span, "reranker.media_parts", body)
				}
			}
			if tr != nil && len(tr.selectedTopics) > 0 {
				if body, err := json.Marshal(tr.selectedTopics); err == nil {
					obs.RecordContent(span, "reranker.selection_reasons", string(body))
				}
			}
		}
		// Reranker NEVER surfaces as span Error: it always returns a result
		// (LLM failures are reflected via fallback_reason). No ObserveErr.
		span.End()
	}()

	// v0.6.0: Use LLM reranking if we have any candidates (topics, people, or artifacts)
	// Only fallback immediately if ALL candidate lists are empty
	if !cfg.Enabled || (len(candidates) == 0 && len(personCandidates) == 0 && len(artifactCandidates) == 0) {
		return fallbackToVectorTop(r.cfg, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger), nil
	}

	// Parse timeouts
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		timeout = 20 * time.Second
	}

	turnTimeout, err := time.ParseDuration(cfg.TurnTimeout)
	if err != nil || turnTimeout <= 0 {
		turnTimeout = timeout / time.Duration(cfg.MaxToolCalls+1)
	}

	// Determine thinking level
	thinkingLevel := cfg.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = "minimal"
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	st := &state{}
	tr = &trace{
		tracker: agentlog.NewTurnTracker(),
	}
	startTime := time.Now()

	// Build candidate map and collect trace data
	candidateMap := buildCandidateMap(candidates)
	for _, c := range candidates {
		tr.candidates = append(tr.candidates, storage.RerankerCandidate{
			TopicID:      c.TopicID,
			Summary:      c.Topic.Summary,
			Score:        c.Score,
			Date:         c.Topic.CreatedAt.Format("2006-01-02"),
			MessageCount: c.MessageCount,
			SizeChars:    c.SizeChars,
		})
	}

	// v0.5.1: Build person candidate map
	peopleMap := buildPeopleMap(personCandidates)
	// v0.6.0: Build artifact candidate map
	artifactsMap := buildArtifactsMap(artifactCandidates)

	// Build initial prompt
	lang := r.cfg.Bot.Language
	selectCandidatesMax := cfg.Topics.CandidatesLimit / 2
	if selectCandidatesMax < cfg.Topics.Max*2 {
		selectCandidatesMax = cfg.Topics.Max * 2
	}

	systemPrompt, err := r.translator.GetTemplate(lang, "rag.reranker_system_prompt", prompts.RerankerParams{
		Profile:       userProfile,
		RecentTopics:  recentTopics,
		MaxTopics:     cfg.Topics.Max,
		MinCandidates: cfg.Topics.Max,
		MaxCandidates: selectCandidatesMax,
		MaxPeople:     cfg.People.Max,
		MaxArtifacts:  cfg.Artifacts.Max,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reranker system prompt: %w", err)
	}

	candidatesList := formatCandidatesForReranker(candidates)
	peopleCandidatesList := FormatPeopleForReranker(personCandidates)
	artifactsCandidatesList := formatArtifactCandidates(artifactCandidates)

	userPrompt, err := r.translator.GetTemplate(lang, "rag.reranker_user_prompt", prompts.RerankerUserParams{
		Date:               time.Now().Format("2006-01-02"),
		Query:              originalQuery,
		EnrichedQuery:      contextualizedQuery,
		CurrentMessages:    currentMessages,
		Candidates:         candidatesList,
		PeopleCandidates:   peopleCandidatesList,
		ArtifactCandidates: artifactsCandidatesList,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reranker user prompt: %w", err)
	}

	tr.systemPrompt = systemPrompt
	tr.userPrompt = userPrompt

	// Define tool
	tools := []openrouter.Tool{
		{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        "get_topics_content",
				Description: r.translator.Get(lang, "rag.reranker_tool_description"),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"topic_ids": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "integer"},
							"description": r.translator.Get(lang, "rag.reranker_tool_param_description"),
						},
					},
					"required": []string{"topic_ids"},
				},
			},
		},
	}

	// Build user message content
	var userMessageContent interface{}
	if len(mediaParts) > 0 {
		// Add media instruction
		mediaInstruction := r.translator.Get(lang, "rag.reranker_media_instruction")
		promptWithMedia := userPrompt
		if mediaInstruction != "" {
			promptWithMedia = userPrompt + "\n\n" + mediaInstruction
		}
		parts := []interface{}{
			openrouter.TextPart{Type: "text", Text: promptWithMedia},
		}
		parts = append(parts, mediaParts...)
		userMessageContent = parts
	} else {
		userMessageContent = userPrompt
	}

	messages := []openrouter.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessageContent},
	}

	// Agentic loop
	iterations = 0

	for iterations < cfg.MaxToolCalls {
		var toolChoice any
		var responseFormat interface{}

		// v0.6.0: If no topic candidates (only artifacts/people), skip tool call and go directly to JSON
		if iterations == 0 && len(candidates) > 0 {
			toolChoice = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "get_topics_content",
				},
			}
		} else {
			responseFormat = openrouter.ResponseFormat{Type: "json_object"}
		}

		tr.tracker.StartTurn()

		turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)

		var reasoning *openrouter.ReasoningConfig
		if thinkingLevel != "off" && thinkingLevel != "" {
			reasoning = &openrouter.ReasoningConfig{
				Effort: thinkingLevel,
			}
		}

		resp, err := r.client.CreateChatCompletion(turnCtx, openrouter.ChatCompletionRequest{
			Model:      cfg.GetModel(r.cfg.Agents.Default.Model),
			Messages:   messages,
			Tools:      tools,
			ToolChoice: toolChoice,
			// NOTE: response-healing plugin breaks reasoning visibility when combined with json_object format.
			// Reasoning works correctly without plugins.
			ResponseFormat: responseFormat,
			Reasoning:      reasoning,
			UserID:         userID,
		})
		turnCancel()

		if err != nil {
			fallbackReason := "llm_error"
			if ctx.Err() == context.DeadlineExceeded {
				fallbackReason = "timeout"
			} else if turnCtx.Err() == context.DeadlineExceeded {
				fallbackReason = "turn_timeout"
			}
			r.logger.Warn("reranker LLM call failed",
				"error", err,
				"tool_calls", iterations,
				"reason", fallbackReason,
			)
			tr.fallbackReason = fallbackReason
			result := fallbackFromState(r.cfg, st, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger)
			tr.selectedTopics = result.Topics
			tr.selectedPeople = result.People
			tr.selectedArtifacts = result.Artifacts
			saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))
			return result, nil
		}

		tr.tracker.EndTurn(
			resp.DebugRequestBody,
			resp.DebugResponseBody,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.Cost,
		)

		if len(resp.Choices) == 0 {
			r.logger.Warn("reranker got empty response")
			tr.fallbackReason = "empty_response"
			result := fallbackFromState(r.cfg, st, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger)
			tr.selectedTopics = result.Topics
			tr.selectedPeople = result.People
			tr.selectedArtifacts = result.Artifacts
			saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))
			return result, nil
		}

		choice := resp.Choices[0]

		// Log and collect reasoning details
		if choice.Message.ReasoningDetails != nil {
			reasoningText := extractReasoningText(choice.Message.ReasoningDetails)
			r.logger.Debug("reranker reasoning",
				"user_id", userID,
				"iteration", iterations+1,
				"reasoning", openrouter.FilterReasoningForLog(choice.Message.ReasoningDetails),
			)
			if reasoningText != "" {
				tr.reasoning = append(tr.reasoning, ReasoningEntry{
					Iteration: iterations + 1,
					Text:      reasoningText,
				})
			}
		}

		// Check for tool calls
		if len(choice.Message.ToolCalls) > 0 {
			iterations++

			var toolResults []openrouter.Message
			toolCall := storage.RerankerToolCall{Iteration: iterations}

			for _, tc := range choice.Message.ToolCalls {
				if tc.Function.Name == "get_topics_content" {
					ids, err := parseToolCallIDs(tc.Function.Arguments)
					if err != nil {
						r.logger.Warn("failed to parse tool call arguments", "error", err)
						continue
					}

					// Track requested IDs for fallback
					for _, id := range ids {
						isDuplicate := false
						for _, existing := range st.requestedIDs {
							if existing == id {
								isDuplicate = true
								break
							}
						}
						if !isDuplicate {
							st.requestedIDs = append(st.requestedIDs, id)
						}
					}

					toolCall.TopicIDs = append(toolCall.TopicIDs, ids...)
					for _, id := range ids {
						if c, ok := candidateMap[id]; ok {
							toolCall.Topics = append(toolCall.Topics, storage.RerankerToolCallTopic{
								ID:      id,
								Summary: c.Topic.Summary,
							})
						}
					}

					content := loadTopicsContent(ctx, userID, ids, candidateMap, r.msgRepo, r.logger, tr)
					toolResults = append(toolResults, openrouter.Message{
						Role:       "tool",
						Content:    content,
						ToolCallID: tc.ID,
					})
				}
			}

			tr.toolCalls = append(tr.toolCalls, toolCall)

			span.AddEvent("reranker.tool_call",
				oteltrace.WithAttributes(
					attribute.Int("iteration", iterations),
					attribute.Int64Slice("requested_ids", toolCall.TopicIDs),
					attribute.Int("requested_count", len(toolCall.TopicIDs)),
					attribute.Int("valid_count", len(toolCall.Topics)),
				),
			)

			messages = append(messages, openrouter.Message{
				Role:             "assistant",
				Content:          choice.Message.Content,
				ToolCalls:        choice.Message.ToolCalls,
				ReasoningDetails: choice.Message.ReasoningDetails,
			})
			messages = append(messages, toolResults...)
			continue
		}

		// No tool calls - expect final JSON response
		// v0.6.0: Exception: if no topic candidates, skipping tool call is expected
		if iterations == 0 && len(candidates) > 0 {
			r.logger.Warn("reranker protocol violation: no tool calls before final response",
				"user_id", userID,
				"content_preview", truncateForLog(choice.Message.Content, 200),
			)
			tr.fallbackReason = "protocol_violation"
			fallbackResult := fallbackToVectorTop(r.cfg, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger)
			tr.selectedTopics = fallbackResult.Topics
			tr.selectedPeople = fallbackResult.People
			tr.selectedArtifacts = fallbackResult.Artifacts
			saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))
			return fallbackResult, nil
		}

		result, err := parseResponse(choice.Message.Content, r.logger)
		if err != nil {
			r.logger.Warn("failed to parse reranker response", "error", err, "content", choice.Message.Content)
			tr.fallbackReason = "parse_error"
			fallbackResult := fallbackFromState(r.cfg, st, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger)
			tr.selectedTopics = fallbackResult.Topics
			tr.selectedPeople = fallbackResult.People
			tr.selectedArtifacts = fallbackResult.Artifacts
			saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))
			return fallbackResult, nil
		}

		tr.modelRawTopics = len(result.Topics)
		tr.modelRawPeople = len(result.People)
		tr.modelRawArtifacts = len(result.Artifacts)

		// Validate topics and people
		result = filterValidTopics(userID, result, candidateMap, r.logger)
		result = filterValidPeople(userID, result, peopleMap, r.logger)
		result = filterValidArtifacts(userID, result, artifactsMap, r.logger)

		tr.modelKeptTopics = len(result.Topics)
		tr.modelKeptPeople = len(result.People)
		tr.modelKeptArtifacts = len(result.Artifacts)
		if len(result.Topics) == 0 && len(result.People) == 0 && len(result.Artifacts) == 0 {
			r.logger.Warn("reranker returned no valid results (all hallucinated)", "user_id", userID)
			tr.fallbackReason = "all_hallucinated"
			fallbackResult := fallbackFromState(r.cfg, st, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger)
			tr.selectedTopics = fallbackResult.Topics
			tr.selectedPeople = fallbackResult.People
			tr.selectedArtifacts = fallbackResult.Artifacts
			saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))
			return fallbackResult, nil
		}

		r.logger.Info("reranker completed",
			"user_id", userID,
			"topic_candidates_in", len(candidates),
			"topics_out", len(result.Topics),
			"people_candidates_in", len(personCandidates),
			"people_out", len(result.People),
			"artifacts_candidates_in", len(artifactCandidates),
			"artifacts_out", len(result.Artifacts),
			"tool_calls", iterations,
			"duration_ms", int(time.Since(startTime).Milliseconds()),
			"cost_usd", tr.tracker.TotalCostValue(),
		)

		tr.selectedTopics = result.Topics
		tr.selectedPeople = result.People
		tr.selectedArtifacts = result.Artifacts
		saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))

		return result, nil
	}

	// Max tool calls reached
	r.logger.Warn("reranker max tool calls reached", "max", cfg.MaxToolCalls)
	tr.fallbackReason = "max_tool_calls"
	result := fallbackFromState(r.cfg, st, candidates, personCandidates, artifactCandidates, cfg.Topics.Max, r.logger)
	tr.selectedTopics = result.Topics
	tr.selectedPeople = result.People
	tr.selectedArtifacts = result.Artifacts
	saveTrace(ctx, r.agentLogger, r.logger, userID, originalQuery, contextualizedQuery, tr, startTime, cfg.GetModel(r.cfg.Agents.Default.Model))
	return result, nil
}
