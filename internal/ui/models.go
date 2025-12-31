package ui

import (
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

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
