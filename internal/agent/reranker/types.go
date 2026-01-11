// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"encoding/json"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/storage"
)

// Request parameters for Reranker agent.
const (
	// ParamCandidates is the key for reranker candidates ([]Candidate).
	ParamCandidates = "candidates"
	// ParamPersonCandidates is the key for person candidates ([]PersonCandidate) (v0.5.1).
	ParamPersonCandidates = "person_candidates"
	// ParamContextualizedQuery is the key for enriched query (string).
	ParamContextualizedQuery = "contextualized_query"
	// ParamOriginalQuery is the key for original user query (string).
	ParamOriginalQuery = "original_query"
	// ParamCurrentMessages is the key for recent conversation (string).
	ParamCurrentMessages = "current_messages"
	// ParamMediaParts is the key for multimodal content ([]interface{}).
	ParamMediaParts = "media_parts"
)

// Tool call payload thresholds for monitoring (soft limits - warning only).
const (
	warnToolCallTopics = 15      // Warn if more than 15 topics requested
	warnToolCallChars  = 200_000 // Warn if payload exceeds ~200K chars (~50K tokens)
)

// TopicSelection represents a selected topic with explanation.
type TopicSelection struct {
	Reason string `json:"reason"`
	ID     int64  `json:"id"`
}

// PersonSelection represents a selected person with explanation (v0.5.1).
type PersonSelection struct {
	Reason string `json:"reason"`
	ID     int64  `json:"id"`
}

// Result contains the output of the reranker.
type Result struct {
	Topics []TopicSelection  // Final selected topics with reasons
	People []PersonSelection // Final selected people with reasons (v0.5.1)
}

// TopicIDs returns just the topic IDs (for backward compatibility).
func (r *Result) TopicIDs() []int64 {
	ids := make([]int64, len(r.Topics))
	for i, t := range r.Topics {
		ids[i] = t.ID
	}
	return ids
}

// PeopleIDs returns just the person IDs (v0.5.1).
func (r *Result) PeopleIDs() []int64 {
	ids := make([]int64, len(r.People))
	for i, p := range r.People {
		ids[i] = p.ID
	}
	return ids
}

// Candidate is a topic candidate for reranking.
type Candidate struct {
	TopicID      int64
	Score        float32
	Topic        storage.Topic
	MessageCount int
	SizeChars    int // Estimated: MessageCount * avgCharsPerMessage
}

// PersonCandidate is a person candidate for reranking (v0.5.1).
type PersonCandidate struct {
	PersonID int64
	Score    float32
	Person   storage.Person
}

// ReasoningEntry holds reasoning text for one iteration.
type ReasoningEntry struct {
	Iteration int    `json:"iteration"`
	Text      string `json:"text"`
}

// state tracks progress during agentic reranking.
type state struct {
	requestedIDs []int64 // IDs requested via tool calls (for fallback)
}

// trace collects debug data for the reranker UI.
type trace struct {
	candidates     []storage.RerankerCandidate
	toolCalls      []storage.RerankerToolCall
	selectedTopics []TopicSelection
	selectedPeople []PersonSelection // v0.5.1
	fallbackReason string
	reasoning      []ReasoningEntry
	systemPrompt   string                // Full system prompt for debug UI
	userPrompt     string                // Full user prompt for debug UI
	tracker        *agentlog.TurnTracker // Unified turn tracking for multi-turn visualization
}

// response is the expected JSON response from Flash.
// Supports both old format {"topics": [42, 18]} and new format {"topics": [{"id": 42, "reason": "..."}]}.
type response struct {
	TopicIDs  json.RawMessage `json:"topic_ids"`
	Topics    json.RawMessage `json:"topics"`     // Fallback: old format
	IDs       []int64         `json:"ids"`        // Fallback: LLM sometimes uses bare "ids"
	PeopleIDs json.RawMessage `json:"people_ids"` // v0.5.1: person IDs
	People    json.RawMessage `json:"people"`     // v0.5.1: with reasons
}
