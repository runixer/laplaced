// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/storage"
)

// parseTopicID extracts numeric ID from "Topic:N" format.
func parseTopicID(id string) (int64, error) {
	re := regexp.MustCompile(`^(?:Topic:)?(\d+)$`)
	matches := re.FindStringSubmatch(id)
	if len(matches) == 2 {
		return strconv.ParseInt(matches[1], 10, 64)
	}
	return 0, fmt.Errorf("invalid topic ID format: %s", id)
}

// parsePersonID extracts numeric ID from "Person:N" format.
func parsePersonID(id string) (int64, error) {
	re := regexp.MustCompile(`^(?:Person:)?(\d+)$`)
	matches := re.FindStringSubmatch(id)
	if len(matches) == 2 {
		return strconv.ParseInt(matches[1], 10, 64)
	}
	return 0, fmt.Errorf("invalid person ID format: %s", id)
}

// parseArtifactID extracts numeric ID from "Artifact:N" format (v0.6.0).
func parseArtifactID(id string) (int64, error) {
	re := regexp.MustCompile(`^(?:Artifact:)?(\d+)$`)
	matches := re.FindStringSubmatch(id)
	if len(matches) == 2 {
		return strconv.ParseInt(matches[1], 10, 64)
	}
	return 0, fmt.Errorf("invalid artifact ID format: %s", id)
}

// Request parameters for Reranker agent.
const (
	// ParamCandidates is the key for reranker candidates ([]Candidate).
	ParamCandidates = "candidates"
	// ParamPersonCandidates is the key for person candidates ([]PersonCandidate) (v0.5.1).
	ParamPersonCandidates = "person_candidates"
	// ParamArtifactCandidates is the key for artifact candidates ([]ArtifactCandidate) (v0.6.0).
	ParamArtifactCandidates = "artifact_candidates"
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
// ID is stored as string with prefix "Topic:N" for unified ID format.
type TopicSelection struct {
	Reason string `json:"reason"`
	ID     string `json:"id"` // Format: "Topic:N"
}

// GetNumericID extracts numeric ID from "Topic:N" format.
func (t *TopicSelection) GetNumericID() (int64, error) {
	return parseTopicID(t.ID)
}

// PersonSelection represents a selected person with explanation (v0.5.1).
// ID is stored as string with prefix "Person:N" for unified ID format.
type PersonSelection struct {
	Reason string `json:"reason"`
	ID     string `json:"id"` // Format: "Person:N"
}

// GetNumericID extracts numeric ID from "Person:N" format.
func (p *PersonSelection) GetNumericID() (int64, error) {
	return parsePersonID(p.ID)
}

// ArtifactSelection represents a selected artifact with explanation (v0.6.0).
// ID is stored as string with prefix "Artifact:N" for unified ID format.
type ArtifactSelection struct {
	Reason string `json:"reason"`
	ID     string `json:"id"` // Format: "Artifact:N"
}

// GetNumericID extracts numeric ID from "Artifact:N" format.
func (a *ArtifactSelection) GetNumericID() (int64, error) {
	return parseArtifactID(a.ID)
}

// Result contains the output of the reranker.
type Result struct {
	Topics    []TopicSelection    // Final selected topics with reasons
	People    []PersonSelection   // Final selected people with reasons (v0.5.1)
	Artifacts []ArtifactSelection // Final selected artifacts with reasons (v0.6.0)
}

// TopicIDs returns just the topic IDs (for backward compatibility).
func (r *Result) TopicIDs() []int64 {
	ids := make([]int64, 0, len(r.Topics))
	for _, t := range r.Topics {
		if id, err := parseTopicID(t.ID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// PeopleIDs returns just the person IDs (v0.5.1).
func (r *Result) PeopleIDs() []int64 {
	ids := make([]int64, 0, len(r.People))
	for _, p := range r.People {
		if id, err := parsePersonID(p.ID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// ArtifactIDs returns just the artifact IDs (v0.6.0).
func (r *Result) ArtifactIDs() []int64 {
	ids := make([]int64, 0, len(r.Artifacts))
	for _, a := range r.Artifacts {
		if id, err := parseArtifactID(a.ID); err == nil {
			ids = append(ids, id)
		}
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

// ArtifactCandidate is an artifact candidate for reranking (v0.6.0).
//
// IsSession marks the candidate as belonging to the active user session
// (its source message has topic_id IS NULL). Such candidates are added by
// the pipeline regardless of vector-search rank, so freshly created files
// stay addressable even when their summary embedding doesn't match the
// next user query. Score is meaningless for session candidates and the
// formatter renders "(session)" in place of cosine similarity.
type ArtifactCandidate struct {
	ArtifactID   int64
	Score        float32
	IsSession    bool
	FileType     string
	OriginalName string
	Summary      string
	Keywords     []string
	Entities     []string // Named entities (people, companies, code mentioned)
	RAGHints     []string // Questions this artifact might answer
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
	candidates        []storage.RerankerCandidate
	toolCalls         []storage.RerankerToolCall
	selectedTopics    []TopicSelection
	selectedPeople    []PersonSelection   // v0.5.1
	selectedArtifacts []ArtifactSelection // v0.6.0
	fallbackReason    string
	reasoning         []ReasoningEntry
	systemPrompt      string                // Full system prompt for debug UI
	userPrompt        string                // Full user prompt for debug UI
	tracker           *agentlog.TurnTracker // Unified turn tracking for multi-turn visualization

	// Counts from the model's final JSON response, captured before validation.
	// Distinguishes "model explicitly returned empty arrays" (raw == 0) from
	// "model returned IDs but all were filtered as hallucinated" (raw > 0, kept == 0).
	modelRawTopics    int
	modelRawPeople    int
	modelRawArtifacts int
	// Counts after filterValid* — what survived ID validation against candidates.
	modelKeptTopics    int
	modelKeptPeople    int
	modelKeptArtifacts int

	// Session-injection telemetry: how many artifact candidates entered with the
	// IsSession marker, and how many of those survived to the kept set. Lets us
	// answer "did the reranker actually pick freshly-attached files?" without
	// joining storage tables in Tempo. Computed regardless of whether the
	// session feature is enabled (zeros when no session candidates were merged).
	candidatesInArtifactsSession int
	modelKeptArtifactsSession    int
}

// response is the expected JSON response from Flash.
// Supports both old format {"topics": [42, 18]} and new format {"topics": [{"id": 42, "reason": "..."}]}.
type response struct {
	TopicIDs    json.RawMessage `json:"topic_ids"`
	Topics      json.RawMessage `json:"topics"`       // Fallback: old format
	IDs         []int64         `json:"ids"`          // Fallback: LLM sometimes uses bare "ids"
	PeopleIDs   json.RawMessage `json:"people_ids"`   // v0.5.1: person IDs
	People      json.RawMessage `json:"people"`       // v0.5.1: with reasons
	ArtifactIDs json.RawMessage `json:"artifact_ids"` // v0.6.0: artifact IDs
	Artifacts   json.RawMessage `json:"artifacts"`    // v0.6.0: with reasons
}
