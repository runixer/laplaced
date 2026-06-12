package laplace

import (
	"context"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// Request contains all inputs for Laplace agent execution.
type Request struct {
	UserID storage.ScopeID

	// Message content
	HistoryContent      string        // Full message content for history storage
	RawQuery            string        // Raw text for RAG query
	CurrentMessageParts []interface{} // Multimodal parts (text, images, audio)

	// Telegram context (for intermediate messages)
	ChatID          int64
	MessageThreadID int
	ReplyToMsgID    int

	// UseStreaming switches the agent to SSE streaming mode. When true, the
	// final iteration's content is delivered via OnContentDelta as deltas
	// arrive (instead of returned as a single buffered string). The non-final
	// (tool-call) iterations still buffer normally and route any pre-tool
	// content via OnIntermediateMessage.
	UseStreaming bool

	// Callbacks for Telegram actions
	OnIntermediateMessage func(text string)                // Called when tool call has intermediate text
	OnContentDelta        func(text string)                // Streaming: called for each user-visible content fragment in the final iteration
	OnToolStart           func(toolName, arguments string) // Called before each tool execution; receives the tool name and raw arguments JSON so callers can show a localized status
	OnRAGEnriched         func(enrichedQuery string)       // Called once after context loading completes, when the enricher produced a non-empty rephrased query
}

// Response contains the result of Laplace agent execution.
type Response struct {
	// Main response
	Content string

	// GeneratedArtifactIDs accumulates artifact IDs produced by tools during
	// this Execute call (e.g. generate_image). The orchestrator uses them to
	// attach photos to the Telegram reply. Empty for text-only responses.
	GeneratedArtifactIDs []int64

	// Error (non-nil if execution failed after partial completion)
	// When set, Content may be empty or partial, but other fields (tokens, timing, turns) are valid
	Error error

	// Token usage
	PromptTokens     int
	CompletionTokens int
	TotalCost        *float64

	// Timing
	LLMDuration  time.Duration
	ToolDuration time.Duration
	TotalTurns   int

	// FirstContentDelay is the time from the start of the final-iteration
	// LLM call to the first user-visible content delta. Set only when
	// streaming was used and the final iteration produced content.
	// Zero in non-streaming mode and when the response was content-empty.
	FirstContentDelay time.Duration

	// RAG info for logging
	RAGInfo *rag.RetrievalDebugInfo

	// Debug info
	Messages          []llm.Message // Full conversation for logging
	ConversationTurns *agentlog.ConversationTurns

	// Anomaly signals for the orchestrator to surface as bot.anomaly.*
	// span attributes on bot.processMessageGroup. WasEmpty marks an
	// originally-empty completion that was replaced with the localized
	// fallback. WasSanitized marks a completion that had hallucination
	// artifacts stripped; OriginalContent then carries the pre-strip text
	// for triage (recorded as a span event when content tracing is on).
	WasEmpty        bool
	WasSanitized    bool
	OriginalContent string
}

// ToolCallContext carries execution context for a tool call: the owning user
// and any image parts from the current user message (so tools that edit/combine
// images — e.g. generate_image — can access attached photos automatically).
type ToolCallContext struct {
	UserID               storage.ScopeID
	CurrentMessageImages []llm.FilePart
	// Iteration is the 1-based tool-loop iteration this dispatch belongs
	// to. Recorded on the tool_executor span as tool.iteration so traces
	// can answer "which turn dispatched this tool" without matching by
	// timestamp. Zero/unset is acceptable for non-laplace callers.
	Iteration int
}

// ToolResult is the richer return type for tool execution. Content is what
// gets fed back to the LLM; GeneratedArtifactIDs is a side-channel carrying
// artifact IDs produced during the call (e.g. generated images), which the
// orchestrator collects to attach to the final user reply.
type ToolResult struct {
	Content              string
	GeneratedArtifactIDs []int64
}

// ToolHandler defines the interface for executing tool calls.
// This allows bot package to provide Telegram-aware implementations.
type ToolHandler interface {
	// ExecuteToolCall executes a single tool call and returns the result.
	ExecuteToolCall(ctx context.Context, tcc ToolCallContext, toolName, arguments string) (*ToolResult, error)
}

// ContextData contains pre-built context for LLM.
type ContextData struct {
	// User identification
	UserID storage.ScopeID // v0.6.0: User ID for artifact loading

	// System prompt components
	BaseSystemPrompt string
	ProfileFacts     string
	RecentTopics     string
	InnerCircle      string // v0.5.1: People from Work_Inner + Family circles

	// Session history
	RecentHistory []storage.Message

	// RAG results
	RAGResults          []rag.TopicSearchResult
	ArtifactResults     []rag.ArtifactResult // v0.6.0: Artifact summary matches
	SelectedArtifactIDs []int64              // v0.6.0: Artifact IDs selected by reranker for full content loading
	RAGInfo             *rag.RetrievalDebugInfo
	RelevantPeople      []storage.Person // v0.5.1: People selected by reranker
}
