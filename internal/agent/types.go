// Package agent provides a unified interface for all LLM agents in the system.
package agent

import (
	"context"
	"time"
)

// AgentType identifies an agent.
type AgentType string

const (
	TypeLaplace      AgentType = "laplace"
	TypeReranker     AgentType = "reranker"
	TypeEnricher     AgentType = "enricher"
	TypeSplitter     AgentType = "splitter"
	TypeMerger       AgentType = "merger"
	TypeArchivist    AgentType = "archivist"
	TypeDeduplicator AgentType = "deduplicator"
	TypeScout        AgentType = "scout"
)

// String implements fmt.Stringer.
func (t AgentType) String() string {
	return string(t)
}

// Agent is the core interface all agents implement.
type Agent interface {
	// Type returns the agent's type identifier.
	Type() AgentType

	// Execute runs the agent with the given request.
	Execute(ctx context.Context, req *Request) (*Response, error)
}

// CapableAgent extends Agent with metadata for discovery.
type CapableAgent interface {
	Agent

	// Capabilities returns the agent's capabilities.
	Capabilities() Capabilities

	// Description returns a human-readable description for tool use.
	Description() string
}

// Capabilities describes what an agent can do.
type Capabilities struct {
	// Execution model
	IsAgentic         bool // Uses tool calls (multi-turn)
	SupportsStreaming bool // Can stream responses

	// Input capabilities
	SupportedMedia []string // ["image", "audio", "pdf"]
	MaxInputTokens int      // Context limit

	// Output format
	OutputFormat string // "text", "json", "structured"
}

// Request is the unified input for all agents.
type Request struct {
	// Context (from ContextService)
	Shared *SharedContext

	// Query
	Query    string    // Main query/content to process
	Messages []Message // Conversation history (for chat agents)

	// Multimodal
	Media []MediaPart // Images, audio, files

	// Agent-specific
	Params map[string]any // Custom parameters
}

// Response is the unified output from all agents.
type Response struct {
	// Content
	Content    string // Text response
	Structured any    // Parsed result (for JSON agents)

	// Metadata
	Tokens    TokenUsage
	Duration  time.Duration
	Reasoning string // Chain-of-thought (if available)

	// Agent-specific
	Metadata map[string]any
}

// TokenUsage tracks token consumption.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
	Cost       *float64
}

// TotalTokens returns the total token count.
// If Total is set, returns it; otherwise computes Prompt + Completion.
func (t TokenUsage) TotalTokens() int {
	if t.Total > 0 {
		return t.Total
	}
	return t.Prompt + t.Completion
}

// Message represents a conversation message.
type Message struct {
	Role    string
	Content string
}

// MediaPart represents multimodal input.
type MediaPart struct {
	Type     string // "image", "audio", "file"
	MimeType string
	Data     []byte
	URL      string // Alternative to Data
}
