package laplace

import (
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// Request contains all inputs for Laplace agent execution.
type Request struct {
	UserID int64

	// Message content
	HistoryContent      string        // Full message content for history storage
	RawQuery            string        // Raw text for RAG query
	CurrentMessageParts []interface{} // Multimodal parts (text, images, audio)

	// Telegram context (for intermediate messages)
	ChatID          int64
	MessageThreadID int
	ReplyToMsgID    int

	// Callbacks for Telegram actions
	OnIntermediateMessage func(text string) // Called when tool call has intermediate text
	OnTypingAction        func()            // Called before tool execution
}

// Response contains the result of Laplace agent execution.
type Response struct {
	// Main response
	Content string

	// Token usage
	PromptTokens     int
	CompletionTokens int
	TotalCost        *float64

	// Timing
	LLMDuration  time.Duration
	ToolDuration time.Duration
	TotalTurns   int

	// RAG info for logging
	RAGInfo *rag.RetrievalDebugInfo

	// Debug info
	Messages          []openrouter.Message // Full conversation for logging
	ConversationTurns *agentlog.ConversationTurns
}

// ToolHandler defines the interface for executing tool calls.
// This allows bot package to provide Telegram-aware implementations.
type ToolHandler interface {
	// ExecuteToolCall executes a single tool call and returns the result.
	ExecuteToolCall(toolName string, arguments string) (string, error)
}

// ContextData contains pre-built context for LLM.
type ContextData struct {
	// System prompt components
	BaseSystemPrompt string
	ProfileFacts     string
	RecentTopics     string

	// Session history
	RecentHistory []storage.Message

	// RAG results
	RAGResults []rag.TopicSearchResult
	RAGInfo    *rag.RetrievalDebugInfo
}
