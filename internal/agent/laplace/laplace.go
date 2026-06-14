package laplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

const (
	maxToolIterations = 10
	maxEmptyRetries   = 2
)

// Laplace is the main chat agent that handles user conversations.
type Laplace struct {
	cfg          *config.Config
	orClient     llm.Client
	ragService   rag.Retriever
	msgRepo      storage.MessageRepository
	factRepo     storage.FactRepository
	artifactRepo storage.ArtifactRepository // v0.6.0: For loading full artifact content
	fileStorage  files.Storage              // v0.6.0: blob store for artifact content (nil = artifacts off)
	translator   *i18n.Translator
	agentLogger  *agentlog.Logger
	logger       *slog.Logger

	// Pre-built tools
	tools []llm.Tool
}

// New creates a new Laplace agent.
func New(
	cfg *config.Config,
	orClient llm.Client,
	ragService rag.Retriever,
	msgRepo storage.MessageRepository,
	factRepo storage.FactRepository,
	artifactRepo storage.ArtifactRepository, // v0.6.0
	translator *i18n.Translator,
	logger *slog.Logger,
) *Laplace {
	return &Laplace{
		cfg:          cfg,
		orClient:     orClient,
		ragService:   ragService,
		msgRepo:      msgRepo,
		factRepo:     factRepo,
		artifactRepo: artifactRepo,
		translator:   translator,
		logger:       logger.With("agent", "laplace"),
		tools:        BuildTools(cfg, translator),
	}
}

// SetFileStorage wires the artifact blob store used to load full artifact
// content into the LLM context. When unset (or artifacts disabled), artifact
// content loading is skipped.
func (l *Laplace) SetFileStorage(fs files.Storage) {
	l.fileStorage = fs
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
func (l *Laplace) Execute(ctx context.Context, req *Request, toolHandler ToolHandler) (resp *Response, err error) {
	logger := l.logger.With("user_id", req.UserID)

	// Wrap the entire chat-agent loop in a span so child llm calls,
	// tool dispatches, and artifact-loading events nest under one clear
	// pipeline boundary. Pre-this commit they hung directly off bot.process-
	// MessageGroup which made it hard to tell laplace turns from enricher /
	// reranker calls in TraceQL.
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/agent/laplace").Start(
		ctx, "laplace.Execute",
		trace.WithAttributes(attribute.String("user.id", string(req.UserID))),
	)
	// Captured by the deferred closure for span attrs. tool/llm counters
	// live with the existing tracker; we copy at end-of-turn into attrs.
	var toolIterationsFinal int
	var totalArtifactsLoaded int
	defer func() {
		llmCalls := 0
		costUSD := 0.0
		if resp != nil {
			llmCalls = resp.TotalTurns
			if resp.TotalCost != nil {
				costUSD = *resp.TotalCost
			}
		}
		span.SetAttributes(
			attribute.Int("laplace.iterations", toolIterationsFinal),
			attribute.Int("laplace.llm_calls", llmCalls),
			attribute.Float64("laplace.cost_usd", costUSD),
			attribute.Int("laplace.artifacts_loaded", totalArtifactsLoaded),
		)
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	// Load context data
	contextData, err := l.LoadContextData(ctx, req.UserID, req.RawQuery, req.CurrentMessageParts)
	if err != nil {
		return nil, fmt.Errorf("failed to load context: %w", err)
	}
	totalArtifactsLoaded = len(contextData.SelectedArtifactIDs)

	// Emit a media inventory event covering BOTH origins of media in the
	// laplace context — current-message attachments and reranker-selected
	// artifacts loaded from storage. Replay reads this to reconstruct the
	// multimodal context without parsing redacted llm.request bodies.
	l.recordMediaParts(ctx, req, contextData)

	// Build initial messages
	enrichedQuery := ""
	if contextData.RAGInfo != nil {
		enrichedQuery = contextData.RAGInfo.EnrichedQuery
	}
	// Surface the enriched query to the caller (e.g. streaming sink shows
	// it as a transient status before the first LLM token arrives). Skipped
	// when enrichment was disabled or returned an empty string.
	if enrichedQuery != "" && req.OnRAGEnriched != nil {
		req.OnRAGEnriched(enrichedQuery)
	}
	orMessages := l.BuildMessages(ctx, contextData, req.HistoryContent, req.CurrentMessageParts, enrichedQuery)

	// Extract image FileParts from current message for tools that operate
	// on attached photos (generate_image without explicit artifact IDs).
	currentMessageImages := extractImageFileParts(req.CurrentMessageParts)
	tcc := ToolCallContext{
		UserID:               req.UserID,
		CurrentMessageImages: currentMessageImages,
	}

	// Prepare plugins
	var plugins []llm.Plugin
	if l.cfg.LLM.PDFParserEngine != "" {
		plugins = append(plugins, llm.Plugin{
			ID: "file-parser",
			PDF: llm.PDFConfig{
				Engine: l.cfg.LLM.PDFParserEngine,
			},
		})
	}

	// Build reasoning config once — constant across tool iterations.
	// Explicit reasoning.effort prevents Gemini 3.1 Pro from leaking internal
	// chain-of-thought into message.content (see docs/bugs/2026-04-22-laplace-thought-leak/).
	// "auto" omits the field, letting Gemini pick its own thinking budget.
	reasoning := llm.ReasoningFor(l.cfg.Agents.GetChatThinkingLevel())

	// Tool loop
	tracker := agentlog.NewTurnTracker()
	var finalResponse string
	var generatedArtifactIDs []int64
	// seenURLs collects every source URL returned by search tools this turn,
	// so the citation guard can strip links the model invented or altered.
	seenURLs := make(map[string]bool)
	emptyRetries := 0

	var totalLLMDuration time.Duration
	var totalToolDuration time.Duration
	var firstContentDelay time.Duration

	for {
		// toolIterationsFinal is the span-captured counter declared at the
		// top of Execute — using it directly means the final value already
		// lives where the deferred closure can read it.
		if toolIterationsFinal >= maxToolIterations {
			logger.Warn("max tool iterations reached", "iterations", toolIterationsFinal)
			break
		}
		toolIterationsFinal++

		orReq := llm.ChatCompletionRequest{
			Model:     l.cfg.Agents.GetChatModel(),
			Messages:  orMessages,
			Plugins:   plugins,
			Tools:     l.tools,
			Reasoning: reasoning,
			UserID:    string(req.UserID),
		}

		tracker.StartTurn()
		llmStart := time.Now()

		// Adapter: streaming and buffered paths produce a uniform
		// `turnOutcome` so the rest of the loop body is path-agnostic.
		outcome, err := l.runChatTurn(ctx, orReq, req, logger)
		llmDuration := time.Since(llmStart)
		totalLLMDuration += llmDuration

		if err != nil {
			logger.Error("failed to get LLM completion", "error", err)
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// Capture TTFT from the first iteration that produced content (stream mode only).
		if firstContentDelay == 0 && outcome.firstContentDelay > 0 {
			firstContentDelay = outcome.firstContentDelay
		}

		tracker.EndTurn(
			outcome.debugRequestBody,
			outcome.debugResponseBody,
			outcome.promptTokens,
			outcome.completionTokens,
			outcome.cost,
		)

		// Check for empty response (no content and no tool calls) - retry
		if strings.TrimSpace(outcome.content) == "" && len(outcome.toolCalls) == 0 {
			logger.Warn("empty LLM response (no content, no tools)",
				"finish_reason", outcome.finishReason,
				"model", outcome.model,
				"retry_attempt", emptyRetries+1,
			)

			if emptyRetries < maxEmptyRetries {
				emptyRetries++

				// Cache-busting: add nonce to system message to bypass poisoned cache
				// This forces OpenRouter/Google to recompute instead of replaying cached failure
				nonce := fmt.Sprintf("\n\n[retry-bust-%d]", time.Now().UnixNano())
				if len(orMessages) > 0 && orMessages[0].Role == "system" {
					if contentSlice, ok := orMessages[0].Content.([]interface{}); ok {
						contentSlice = append(contentSlice,
							llm.TextPart{Type: "text", Text: nonce})
						orMessages[0].Content = contentSlice
					}
				}

				logger.Info("retrying after empty response with cache buster", "attempt", emptyRetries)
				continue
			}

			// Build partial response with error for debugging
			promptTokens, completionTokens := tracker.TotalTokens()
			return &Response{
				Content:           "", // Empty due to error
				Error:             errors.New("max empty response retries reached"),
				PromptTokens:      promptTokens,
				CompletionTokens:  completionTokens,
				TotalCost:         tracker.TotalCost(),
				LLMDuration:       totalLLMDuration,
				ToolDuration:      totalToolDuration,
				TotalTurns:        tracker.TurnCount(),
				FirstContentDelay: firstContentDelay,
				RAGInfo:           contextData.RAGInfo,
				Messages:          orMessages,
				ConversationTurns: tracker.Build(),
				WasEmpty:          true, // retries exhausted — orchestrator surfaces bot.anomaly.empty_response
			}, nil
		}

		// Reset retry counter on successful response
		if emptyRetries > 0 {
			logger.Info("retry successful after empty response", "attempts", emptyRetries)
			emptyRetries = 0
		}

		// Handle tool calls
		if len(outcome.toolCalls) > 0 {
			logger.Info("Model requested tool calls", "count", len(outcome.toolCalls))

			// Send intermediate message if present.
			// In stream mode the buffered content was NOT forwarded to
			// OnContentDelta (forwarding halts on the first tool-call delta);
			// surface it here as a separate message so the user-visible
			// behavior matches the buffered path.
			if outcome.content != "" && strings.TrimSpace(outcome.content) != "" {
				if req.OnIntermediateMessage != nil {
					req.OnIntermediateMessage(outcome.content)
				}
			}

			// Add assistant message with tool calls
			var content interface{} = outcome.content
			if outcome.content == "" {
				content = nil
			}
			orMessages = append(orMessages, llm.Message{
				Role:             "assistant",
				Content:          content,
				ToolCalls:        outcome.toolCalls,
				ReasoningDetails: outcome.reasoningDetails,
			})

			// Execute tools — stamp the iteration so the tool span carries
			// "this dispatch belongs to laplace turn N", queryable as
			// {span.tool.iteration=2} in TraceQL.
			tcc.Iteration = toolIterationsFinal
			toolStart := time.Now()
			toolMessages, toolArtifactIDs, toolCitations := l.executeToolCalls(ctx, toolHandler, tcc, outcome.toolCalls, req.OnToolStart, logger)
			totalToolDuration += time.Since(toolStart)

			generatedArtifactIDs = append(generatedArtifactIDs, toolArtifactIDs...)
			for _, c := range toolCitations {
				seenURLs[c.URL] = true
			}
			orMessages = append(orMessages, toolMessages...)
			continue
		}

		// Final response
		finalResponse = outcome.content
		break
	}

	// Sanitize response
	originalContent := finalResponse
	finalResponse, wasSanitized := SanitizeLLMResponse(finalResponse)
	if wasSanitized {
		logger.Warn("sanitized LLM response (removed hallucination artifacts)")
	}

	// Ground source links: unwrap any link whose URL wasn't returned by a
	// search tool this turn (model invented or altered it). Keeps the text.
	finalResponse, strippedURLs := stripUnverifiedLinks(finalResponse, seenURLs)
	if len(strippedURLs) > 0 {
		logger.Warn("stripped unverified source links", "count", len(strippedURLs))
	}

	// Handle empty final response
	wasEmpty := strings.TrimSpace(finalResponse) == ""
	if wasEmpty {
		finalResponse = l.translator.Get(l.cfg.Bot.Language, "bot.empty_response")
	}

	promptTokens, completionTokens := tracker.TotalTokens()

	resp = &Response{
		Content:              finalResponse,
		GeneratedArtifactIDs: generatedArtifactIDs,
		PromptTokens:         promptTokens,
		CompletionTokens:     completionTokens,
		TotalCost:            tracker.TotalCost(),
		LLMDuration:          totalLLMDuration,
		ToolDuration:         totalToolDuration,
		TotalTurns:           tracker.TurnCount(),
		FirstContentDelay:    firstContentDelay,
		RAGInfo:              contextData.RAGInfo,
		Messages:             orMessages,
		ConversationTurns:    tracker.Build(),
		WasEmpty:             wasEmpty,
		WasSanitized:         wasSanitized,
		StrippedURLs:         strippedURLs,
	}
	if wasSanitized {
		resp.OriginalContent = originalContent
	}
	return resp, nil
}

// turnOutcome is the unified shape produced by both the buffered
// CreateChatCompletion path and the SSE streaming path. It carries just the
// fields the tool loop in Execute reads after each iteration.
type turnOutcome struct {
	content           string
	toolCalls         []llm.ToolCall
	reasoningDetails  interface{}
	finishReason      string
	model             string
	promptTokens      int
	completionTokens  int
	cost              *float64
	debugRequestBody  string
	debugResponseBody string
	firstContentDelay time.Duration // set only when stream mode forwarded content
}

// runChatTurn dispatches one LLM turn through either the buffered or
// streaming LLM API based on req.UseStreaming.
func (l *Laplace) runChatTurn(
	ctx context.Context,
	orReq llm.ChatCompletionRequest,
	req *Request,
	logger *slog.Logger,
) (*turnOutcome, error) {
	if req.UseStreaming {
		stream, err := l.runStreamingTurn(ctx, orReq, req.OnContentDelta, logger)
		if err != nil {
			return nil, err
		}
		return &turnOutcome{
			content:           stream.Content,
			toolCalls:         stream.ToolCalls,
			reasoningDetails:  stream.ReasoningDetails,
			finishReason:      stream.FinishReason,
			model:             stream.Model,
			promptTokens:      stream.PromptTokens,
			completionTokens:  stream.CompletionTokens,
			cost:              stream.Cost,
			debugRequestBody:  stream.DebugRequestBody,
			debugResponseBody: stream.DebugResponseBody,
			firstContentDelay: stream.FirstContentDelay,
		}, nil
	}

	resp, err := l.orClient.CreateChatCompletion(ctx, orReq)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("empty response from LLM")
	}
	choice := resp.Choices[0]
	return &turnOutcome{
		content:           choice.Message.Content,
		toolCalls:         choice.Message.ToolCalls,
		reasoningDetails:  choice.Message.ReasoningDetails,
		finishReason:      choice.FinishReason,
		model:             resp.Model,
		promptTokens:      resp.Usage.PromptTokens,
		completionTokens:  resp.Usage.CompletionTokens,
		cost:              resp.Usage.Cost,
		debugRequestBody:  resp.DebugRequestBody,
		debugResponseBody: resp.DebugResponseBody,
	}, nil
}

// executeToolCalls executes tool calls and returns (tool result messages,
// accumulated generated artifact IDs). onToolStart receives the tool name
// and raw arguments JSON so the bot wiring can show a localized status with
// the tool's primary argument (e.g. the search query or image prompt).
func (l *Laplace) executeToolCalls(
	ctx context.Context,
	handler ToolHandler,
	tcc ToolCallContext,
	toolCalls []llm.ToolCall,
	onToolStart func(toolName, arguments string),
	logger *slog.Logger,
) ([]llm.Message, []int64, []llm.Citation) {
	n := len(toolCalls)
	if n == 0 {
		return nil, nil, nil
	}

	// Each tool call's outcome is stored by its declared index so the assembled
	// result preserves order regardless of execution order.
	type execResult struct {
		msg         llm.Message
		artifactIDs []int64
		citations   []llm.Citation
	}
	results := make([]execResult, n)

	run := func(i int, tc llm.ToolCall) {
		result, err := handler.ExecuteToolCall(ctx, tcc, tc.Function.Name, tc.Function.Arguments)
		if err != nil {
			logger.Error("tool execution failed", "error", err, "tool", tc.Function.Name)
			results[i] = execResult{msg: llm.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Tool execution failed: %v", err),
				ToolCallID: tc.ID,
			}}
			return
		}
		results[i] = execResult{
			msg:         llm.Message{Role: "tool", Content: result.Content, ToolCallID: tc.ID},
			artifactIDs: result.GeneratedArtifactIDs,
			citations:   result.Citations,
		}
	}

	// generate_image calls are independent (each writes its own file + artifact
	// row) and slow (minutes each), so a multi-image turn runs them concurrently,
	// bounded by max_concurrent. Every other tool may mutate shared state
	// (memory/people) and runs inline in declared order. onToolStart fires up
	// front for each call so the UI reflects every dispatched tool.
	var wg sync.WaitGroup
	imageConcurrency := 4
	if l.cfg != nil {
		imageConcurrency = l.cfg.Agents.ImageGenerator.GetMaxConcurrent()
	}
	sem := make(chan struct{}, imageConcurrency)

	for i, tc := range toolCalls {
		if onToolStart != nil {
			onToolStart(tc.Function.Name, tc.Function.Arguments)
		}
		if tc.Function.Name == "generate_image" {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, tc llm.ToolCall) {
				defer wg.Done()
				defer func() { <-sem }()
				run(i, tc)
			}(i, tc)
			continue
		}
		run(i, tc)
	}
	wg.Wait()

	toolMessages := make([]llm.Message, 0, n)
	var artifactIDs []int64
	var citations []llm.Citation
	for i := range results {
		toolMessages = append(toolMessages, results[i].msg)
		artifactIDs = append(artifactIDs, results[i].artifactIDs...)
		citations = append(citations, results[i].citations...)
	}
	return toolMessages, artifactIDs, citations
}

// recordMediaParts emits a laplace.media_parts span event listing every
// piece of multimodal input the agent will see this turn — both attachments
// from the current user message and artifacts the reranker pulled from
// storage. The body is hash-addressed metadata only; replay matches each
// entry to artifact storage by sha256 to reconstruct the original FilePart.
//
// Always emit (even when empty) so absence in the trace is meaningful — it
// means the user's turn truly had no media, not that we forgot to record.
func (l *Laplace) recordMediaParts(ctx context.Context, req *Request, contextData *ContextData) {
	span := trace.SpanFromContext(ctx)
	parts := make([]agent.MediaPartWithSource, 0, len(req.CurrentMessageParts)+len(contextData.SelectedArtifactIDs))
	for _, p := range req.CurrentMessageParts {
		if _, ok := p.(llm.FilePart); ok {
			parts = append(parts, agent.MediaPartWithSource{Part: p, Source: "current_message"})
		}
	}
	// Reranker-selected artifact bytes are loaded later in BuildMessages —
	// at this point we only know the IDs. Emit minimal entries (no hash) so
	// replay sees the count + source; the per-artifact loaded event below
	// fills in mime/size/sha once the file is read off disk.
	span.SetAttributes(
		attribute.Int("laplace.media.current_message", len(parts)),
		attribute.Int("laplace.media.reranker_selected", len(contextData.SelectedArtifactIDs)),
	)
	if len(parts) == 0 {
		return
	}
	if body := agent.FormatMediaPartsWithSources(parts); body != "" {
		obs.RecordContent(span, "laplace.media_parts", body)
	}
}

// extractImageFileParts walks the current-message multimodal parts and
// collects FilePart entries whose file_data is an image MIME. These are
// used by tools that edit/combine attached photos without the LLM having
// to cite artifact IDs.
func extractImageFileParts(parts []interface{}) []llm.FilePart {
	var images []llm.FilePart
	for _, p := range parts {
		fp, ok := p.(llm.FilePart)
		if !ok {
			continue
		}
		if strings.HasPrefix(fp.File.FileData, "data:image/") {
			images = append(images, fp)
		}
	}
	return images
}

// LogExecution logs the execution to agent logger.
func (l *Laplace) LogExecution(ctx context.Context, userID storage.ScopeID, resp *Response, cost float64) {
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

	// Build log entry
	entry := agentlog.Entry{
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
		Success:           resp.Error == nil,
	}

	if resp.Error != nil {
		entry.ErrorMessage = resp.Error.Error()
	}

	l.agentLogger.Log(ctx, entry)
}

// formatMessagesForLog formats LLM messages into a human-readable string.
func formatMessagesForLog(messages []llm.Message) string {
	var sb strings.Builder
	for i, msg := range messages {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		role := strings.ToUpper(msg.Role)
		fmt.Fprintf(&sb, "=== %s ===\n", role)

		switch content := msg.Content.(type) {
		case string:
			sb.WriteString(content)
		case []interface{}:
			for _, part := range content {
				if tp, ok := part.(llm.TextPart); ok {
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
				fmt.Fprintf(&sb, "\n- %s(%s)", tc.Function.Name, tc.Function.Arguments)
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

// thoughtTagPattern matches leading thought-tag wrappers that Gemini 3.x
// occasionally emits into content instead of the reasoning field.
// Covers: `![thought ... ]`, `[thought ... ]`, `<thought>...</thought>`,
// `[light_thought ... ]`, `![light_thought ... ]`.
// Only matches when there's a clear closing bracket/tag — conservative.
var thoughtTagPattern = regexp.MustCompile(
	`(?s)\A\s*(?:!?\s*\[\s*(?:thought|light_thought)\b.*?\]|<thought>.*?</thought>)\s*`,
)

// thoughtProseSentinels are phrases that mark the end of a leaked reasoning
// block when the model emits pure English chain-of-thought without any tag
// wrapper. Each sentinel is specific enough not to collide with legitimate
// Russian answers. See docs/bugs/2026-04-22-laplace-thought-leak/.
//
// Processed in order — first match wins. "Before" behaviour strips the sentinel
// and everything that precedes it. "After" behaviour keeps the sentinel as part
// of the answer (used when the sentinel is the model's own apology line).
var thoughtProseSentinels = []struct {
	marker string
	// if true, the marker itself is the start of the real answer (keep it);
	// if false, the marker ends the reasoning (drop everything up to and
	// including it).
	keepMarker bool
}{
	// Russian apology-start is checked first — it unambiguously marks the real
	// answer even when preceding (Done) loop noise is present.
	{"Системный тайм-аут вызвал дублирование", true},
	{"Thinking Process End.\n\n", false},
	{"Proceeding to generate.\n", false},
	{"End of thought process.\n", false},
	{"(Done)\n(Done)\n(Done)", false},
}

// latinRatioThreshold is the minimum ratio of Latin to (Latin + Cyrillic)
// letters in the first ~200 letters that makes the prose-sentinel tier
// consider stripping. Counting only letters (not punctuation or digits)
// keeps the gate stable regardless of how punctuation-heavy the prose is.
const latinRatioThreshold = 0.9

// isLeadingLatinProse reports whether the first ~100 letters of text are
// overwhelmingly Latin — the signature of a leaked English chain-of-thought
// block preceding a real (usually Russian) answer. A handful of Cyrillic
// characters (quoted draft phrases, proper nouns) won't trip the gate.
// The window is deliberately small so a sentinel boundary close to the
// start of content doesn't drag the ratio down with Russian that follows it.
func isLeadingLatinProse(text string) bool {
	const sampleLetters = 100
	latin, cyrillic := 0, 0
	for _, r := range text {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			latin++
		case (r >= 'А' && r <= 'я') || r == 'Ё' || r == 'ё':
			cyrillic++
		}
		if latin+cyrillic >= sampleLetters {
			break
		}
	}
	total := latin + cyrillic
	if total < 40 {
		// Too short to judge; don't fire tier B.
		return false
	}
	return float64(latin)/float64(total) >= latinRatioThreshold
}

// SanitizeLLMResponse removes hallucination artifacts from LLM response.
// Returns the sanitized text and a boolean indicating if sanitization occurred.
// An empty result is a legitimate outcome — the caller substitutes the
// localized bot.empty_response string when this function returns "".
func SanitizeLLMResponse(text string) (string, bool) {
	sanitized := false

	// Remove Gemini's "default_api:" hallucinated tool calls
	if loc := defaultAPIPattern.FindStringIndex(text); loc != nil {
		startIdx := loc[1] - 1
		endIdx := FindMatchingBrace(text, startIdx)
		if endIdx != -1 {
			before := strings.TrimSpace(text[:loc[0]])
			after := strings.TrimSpace(text[endIdx+1:])
			switch {
			case before != "" && after != "":
				text = before + "\n\n" + after
			case after != "":
				text = after
			default:
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

	// Tier A: strip leading thought-tag wrapper (Gemini 3.x reasoning leak).
	if loc := thoughtTagPattern.FindStringIndex(text); len(loc) == 2 && loc[0] == 0 {
		text = strings.TrimSpace(text[loc[1]:])
		sanitized = true
	}

	// Tier B: untagged reasoning leak — content begins with English prose
	// and contains one of a small set of known sentinel phrases. Only
	// fires when leading content is Latin-dominant; avoids false
	// positives on legitimate Russian responses that happen to quote
	// English.
	if isLeadingLatinProse(text) {
		for _, s := range thoughtProseSentinels {
			idx := strings.Index(text, s.marker)
			if idx < 0 {
				continue
			}
			if s.keepMarker {
				text = text[idx:]
			} else {
				text = text[idx+len(s.marker):]
			}
			text = strings.TrimSpace(text)
			sanitized = true
			break
		}
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

		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}
