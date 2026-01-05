package ui

import (
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// RerankerReasoningEntry holds reasoning text for one iteration (matches rag.RerankerReasoningEntry)
type RerankerReasoningEntry struct {
	Iteration int    `json:"iteration"`
	Text      string `json:"text"`
}

// RerankerLogView is the parsed view of a RerankerLog for templates
type RerankerLogView struct {
	storage.RerankerLog
	Candidates     []RerankerCandidateView
	ToolCalls      []RerankerToolCallView
	SelectedIDs    map[int64]bool // For quick lookup in template
	SelectedIDList []int64
	SelectedTopics []RerankerTopicSelectionView // Full topic selection with reason/excerpt
	Reasoning      []RerankerReasoningEntry     // Reasoning entries per iteration
}

// RerankerTopicSelectionView represents a selected topic with reason and optional excerpt
type RerankerTopicSelectionView struct {
	ID         int64
	Reason     string
	Excerpt    string // Empty string if none
	SizeChars  int    // Original topic size in chars
	ExcerptLen int    // Length of excerpt (0 if no excerpt)
}

// RerankerCandidateView is a single candidate for template display
type RerankerCandidateView struct {
	TopicID      int64
	Summary      string
	Score        float32
	Date         string
	MessageCount int
	SizeChars    int
	Selected     bool
	Reason       string // Reason for selection (from reranker response)
}

// RerankerToolCallView represents one iteration of tool calls for template display
type RerankerToolCallView struct {
	Iteration int
	Topics    []RerankerToolCallTopicView
}

// RerankerToolCallTopicView contains topic info for tool call display
type RerankerToolCallTopicView struct {
	ID      int64
	Summary string
}

type ContextStats struct {
	SystemPromptPct float64
	UserFactsPct    float64
	EnvFactsPct     float64
	RAGContextPct   float64
	HistoryPct      float64
	TotalSize       int
}

type RAGLogView struct {
	storage.RAGLog
	ParsedContext []openrouter.Message
	ParsedResults interface{}

	// New fields
	SystemPromptPart string
	UserFactsPart    string
	EnvFactsPart     string
	RAGContextPart   string
	LastMessages     []openrouter.Message
	ToolCalls        []openrouter.Message
	Stats            ContextStats
}

type TopicView struct {
	storage.TopicExtended
	Messages []storage.Message
}

type TopicLogView struct {
	storage.RAGLog
	ParsedTopics  []rag.ExtractedTopic
	ParseError    string
	InputMsgCount int
	InputStartID  int64
	InputEndID    int64
	ChunkDate     time.Time
}
