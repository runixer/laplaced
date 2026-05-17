package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrNoRerankerSpan is returned by ExtractRerankerSpan when the trace JSON
// contains no span named "reranker.Execute".
var ErrNoRerankerSpan = errors.New("no reranker.Execute span in trace")

// RerankerSpanData holds the parts of a reranker.Execute span needed to
// generate a labels-todo.md entry. Mirrors the OTel schema established by
// commit 2aa4245 on feat/otel-migration.
//
// Counts: ModelRawCount is what the model returned in its final JSON,
// before filterValid. ModelKept is post-filterValid. Both are zero when
// the trace ended in a fallback path.
type RerankerSpanData struct {
	TraceID    string
	UserID     int64
	DurationMs int64

	CandidatesIn struct {
		Topics, People, Artifacts int
	}
	ModelKept struct {
		Topics, People, Artifacts int
	}
	ModelRawCount struct {
		Topics, People, Artifacts int
	}

	FallbackReason string
	CostUSD        float64
	LLMCalls       int
	ToolCalls      int

	RawQuery                string
	EnrichedQuery           string
	CandidatesInput         string // raw multi-line body, see ParseCandidates
	PeopleCandidatesInput   string // raw body, see ParsePersonCandidates
	ArtifactCandidatesInput string // raw body, see ParseArtifactCandidates
	UserProfile             string // raw <user_profile> XML at trace time
	RecentTopics            string // raw <recent_topics> XML at trace time

	// ToolCallRequests is one slice per `reranker.tool_call` event, each
	// holding the requested topic IDs for that iteration.
	ToolCallRequests [][]int

	// Selected* are the IDs that ended up in the final selection (post-fallback
	// if any). The *Reasons maps hold the model-supplied reason per ID; entries
	// are empty when the trace ended via a fallback path.
	SelectedTopics    []int
	SelectedPeople    []int
	SelectedArtifacts []int
	SelectedReasons   map[int]string // topic-only, kept for back-compat
	SelectedReasonsP  map[int]string
	SelectedReasonsA  map[int]string
}

// rawTrace is just enough of the Tempo trace JSON to walk it. The schema is
// stable across Tempo versions but we only deserialise what we use.
type rawTrace struct {
	Batches []struct {
		ScopeSpans []struct {
			Spans []rawSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"batches"`
}

type rawSpan struct {
	Name              string         `json:"name"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []rawAttribute `json:"attributes"`
	Events            []rawEvent     `json:"events"`
}

type rawEvent struct {
	Name       string         `json:"name"`
	Attributes []rawAttribute `json:"attributes"`
}

type rawAttribute struct {
	Key   string       `json:"key"`
	Value rawAttrValue `json:"value"`
}

type rawAttrValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"` // Tempo serialises int64 as string
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	ArrayValue  *struct {
		Values []rawAttrValue `json:"values"`
	} `json:"arrayValue,omitempty"`
}

func (v rawAttrValue) asString() string {
	if v.StringValue != nil {
		return *v.StringValue
	}
	return ""
}

func (v rawAttrValue) asInt() int64 {
	if v.IntValue != nil {
		n, err := strconv.ParseInt(*v.IntValue, 10, 64)
		if err == nil {
			return n
		}
	}
	if v.DoubleValue != nil {
		return int64(*v.DoubleValue)
	}
	return 0
}

func (v rawAttrValue) asFloat() float64 {
	if v.DoubleValue != nil {
		return *v.DoubleValue
	}
	if v.IntValue != nil {
		n, _ := strconv.ParseFloat(*v.IntValue, 64)
		return n
	}
	return 0
}

// ExtractRerankerSpan parses the Tempo trace JSON and pulls the first
// `reranker.Execute` span into a RerankerSpanData. Returns ErrNoRerankerSpan
// if the trace contains no such span.
func ExtractRerankerSpan(traceID string, traceJSON []byte) (*RerankerSpanData, error) {
	var doc rawTrace
	if err := json.Unmarshal(traceJSON, &doc); err != nil {
		return nil, fmt.Errorf("decode trace: %w", err)
	}

	for _, b := range doc.Batches {
		for _, ss := range b.ScopeSpans {
			for _, sp := range ss.Spans {
				if sp.Name != "reranker.Execute" {
					continue
				}
				return buildRerankerSpan(traceID, sp), nil
			}
		}
	}
	return nil, ErrNoRerankerSpan
}

func buildRerankerSpan(traceID string, sp rawSpan) *RerankerSpanData {
	out := &RerankerSpanData{
		TraceID:          traceID,
		SelectedReasons:  map[int]string{},
		SelectedReasonsP: map[int]string{},
		SelectedReasonsA: map[int]string{},
	}

	// Span duration from start/end nanos
	if start, end := parseInt64(sp.StartTimeUnixNano), parseInt64(sp.EndTimeUnixNano); end > start {
		out.DurationMs = (end - start) / 1_000_000
	}

	for _, a := range sp.Attributes {
		switch a.Key {
		case "user.id":
			out.UserID = a.Value.asInt()
		case "reranker.candidates_in.topics":
			out.CandidatesIn.Topics = int(a.Value.asInt())
		case "reranker.candidates_in.people":
			out.CandidatesIn.People = int(a.Value.asInt())
		case "reranker.candidates_in.artifacts":
			out.CandidatesIn.Artifacts = int(a.Value.asInt())
		case "reranker.model_kept.topics":
			out.ModelKept.Topics = int(a.Value.asInt())
		case "reranker.model_kept.people":
			out.ModelKept.People = int(a.Value.asInt())
		case "reranker.model_kept.artifacts":
			out.ModelKept.Artifacts = int(a.Value.asInt())
		case "reranker.model_raw_count.topics":
			out.ModelRawCount.Topics = int(a.Value.asInt())
		case "reranker.model_raw_count.people":
			out.ModelRawCount.People = int(a.Value.asInt())
		case "reranker.model_raw_count.artifacts":
			out.ModelRawCount.Artifacts = int(a.Value.asInt())
		case "reranker.fallback_reason":
			out.FallbackReason = a.Value.asString()
		case "reranker.cost_usd":
			out.CostUSD = a.Value.asFloat()
		case "reranker.llm_calls":
			out.LLMCalls = int(a.Value.asInt())
		case "reranker.tool_calls":
			out.ToolCalls = int(a.Value.asInt())
		}
	}

	for _, ev := range sp.Events {
		switch ev.Name {
		case "reranker.raw_query":
			out.RawQuery = eventBody(ev)
		case "reranker.enriched_query":
			out.EnrichedQuery = eventBody(ev)
		case "reranker.candidates_input":
			out.CandidatesInput = eventBody(ev)
		case "reranker.people_candidates_input":
			out.PeopleCandidatesInput = eventBody(ev)
		case "reranker.artifacts_candidates_input":
			out.ArtifactCandidatesInput = eventBody(ev)
		case "reranker.user_profile":
			out.UserProfile = eventBody(ev)
		case "reranker.recent_topics":
			out.RecentTopics = eventBody(ev)
		case "reranker.tool_call":
			out.ToolCallRequests = append(out.ToolCallRequests, parseRequestedIDs(ev))
		case "reranker.selection_reasons":
			sel := parseSelectionReasons(eventBody(ev))
			out.SelectedTopics = sel.Topics
			out.SelectedPeople = sel.People
			out.SelectedArtifacts = sel.Artifacts
			out.SelectedReasons = sel.TopicReasons
			out.SelectedReasonsP = sel.PersonReasons
			out.SelectedReasonsA = sel.ArtifactReasons
		}
	}

	return out
}

func eventBody(ev rawEvent) string {
	for _, a := range ev.Attributes {
		if a.Key == "body" {
			return a.Value.asString()
		}
	}
	return ""
}

// parseRequestedIDs reads the `requested_ids` array attribute on a tool_call
// event. The serialised form is an OTel array of int values.
func parseRequestedIDs(ev rawEvent) []int {
	for _, a := range ev.Attributes {
		if a.Key != "requested_ids" || a.Value.ArrayValue == nil {
			continue
		}
		out := make([]int, 0, len(a.Value.ArrayValue.Values))
		for _, v := range a.Value.ArrayValue.Values {
			out = append(out, int(v.asInt()))
		}
		return out
	}
	return nil
}

// selectionItem is one entry in a selection_reasons body. The producer is
// `json.Marshal(tr.selectedTopics)` in tool_executor.go, where each item
// has form `{"id":"Topic:N","reason":"..."}`.
type selectionItem struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SelectionResult holds the parsed selection_reasons body broken out by type.
// The producer marshals one flat array `[{id:"<Type>:<N>", reason:...}, ...]`
// where Type is Topic / Person / Artifact.
type SelectionResult struct {
	Topics    []int
	People    []int
	Artifacts []int

	TopicReasons    map[int]string
	PersonReasons   map[int]string
	ArtifactReasons map[int]string
}

func parseSelectionReasons(body string) SelectionResult {
	res := SelectionResult{
		TopicReasons:    map[int]string{},
		PersonReasons:   map[int]string{},
		ArtifactReasons: map[int]string{},
	}
	if strings.TrimSpace(body) == "" {
		return res
	}
	var items []selectionItem
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		return res
	}
	for _, it := range items {
		idx := strings.IndexByte(it.ID, ':')
		if idx <= 0 {
			continue
		}
		prefix, suffix := it.ID[:idx], it.ID[idx+1:]
		n, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		switch prefix {
		case "Topic":
			res.Topics = append(res.Topics, n)
			res.TopicReasons[n] = it.Reason
		case "Person":
			res.People = append(res.People, n)
			res.PersonReasons[n] = it.Reason
		case "Artifact":
			res.Artifacts = append(res.Artifacts, n)
			res.ArtifactReasons[n] = it.Reason
		}
	}
	return res
}

// CandidateLine is one parsed row from a `reranker.candidates_input` event.
// Mirrors the producer in internal/agent/reranker/candidates.go's
// formatCandidatesForReranker.
type CandidateLine struct {
	ID         int
	Similarity float64
	Date       string // free-form string from the producer (yyyy-mm-dd)
	MsgCount   int
	Size       string // "12K", "371", etc — kept verbatim
	Summary    string
}

// candidateRE matches one line of formatCandidatesForReranker output:
//
//	[Topic:NN] (S.SS) yyyy-mm-dd | N msgs, ~XK chars | summary text
//
// Tightening this regex must stay in sync with the producer.
var candidateRE = regexp.MustCompile(`^\[Topic:(\d+)\]\s+\(([\d.]+)\)\s+(\S+)\s+\|\s+(\d+)\s+msgs,\s+~(\S+)\s+chars\s+\|\s+(.+)$`)

// ParseCandidates splits a candidates_input body into one CandidateLine per
// recognised row. Unrecognised lines are silently skipped — a malformed
// candidate must never abort label generation for the whole snapshot.
func ParseCandidates(body string) []CandidateLine {
	var out []CandidateLine
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := candidateRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		sim, _ := strconv.ParseFloat(m[2], 64)
		msgs, _ := strconv.Atoi(m[4])
		out = append(out, CandidateLine{
			ID:         id,
			Similarity: sim,
			Date:       m[3],
			MsgCount:   msgs,
			Size:       m[5],
			Summary:    m[6],
		})
	}
	return out
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// PersonCandidateLine is one parsed row from a `reranker.people_candidates_input`
// event. Mirrors storage.FormatPeople: `[Person:N] Name [Circle][: Bio]`.
// The Name column is kept verbatim and may include `(@username)` and
// `(aka aliases)` segments.
type PersonCandidateLine struct {
	ID     int
	Name   string
	Circle string
	Bio    string
}

// personRE: lazy `.+?` for Name so the first `[…]` it meets is the Circle
// bracket, not anything inside the name. Bio is optional — `storage.FormatPeople`
// omits the trailing colon when Bio is empty.
var personRE = regexp.MustCompile(`^\[Person:(\d+)\]\s+(.+?)\s+\[([^\]]+)\](?::\s*(.*))?$`)

// ParsePersonCandidates splits a people_candidates_input body into rows. Lines
// that don't match the producer format are silently skipped (same contract as
// ParseCandidates).
func ParsePersonCandidates(body string) []PersonCandidateLine {
	var out []PersonCandidateLine
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := personRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		out = append(out, PersonCandidateLine{
			ID:     id,
			Name:   strings.TrimSpace(m[2]),
			Circle: strings.TrimSpace(m[3]),
			Bio:    strings.TrimSpace(m[4]),
		})
	}
	return out
}

// ArtifactCandidateLine is one parsed row from a `reranker.artifacts_candidates_input`
// event. Mirrors formatArtifactCandidates:
//
//	[Artifact:N] (sim) type: "filename" [| keywords] [| Entities: …] | summary
type ArtifactCandidateLine struct {
	ID         int
	Similarity float64
	FileType   string
	FileName   string
	Keywords   string // empty if absent
	Entities   string // empty if absent
	Summary    string
}

// artifactHeadRE matches the leading `[Artifact:N] (sim) type: "filename"` segment.
// FileType is `\S+` to absorb hyphenated forms like `voice_note`. FileName is
// captured between literal quotes.
var artifactHeadRE = regexp.MustCompile(`^\[Artifact:(\d+)\]\s+\(([\d.]+)\)\s+(\S+):\s+"([^"]*)"$`)

// ParseArtifactCandidates splits an artifacts_candidates_input body into rows.
// Splits on ` | ` and identifies parts by position (head first, summary last)
// and prefix (`Entities: ` marks the entities cell).
func ParseArtifactCandidates(body string) []ArtifactCandidateLine {
	var out []ArtifactCandidateLine
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, " | ")
		if len(parts) < 2 {
			continue
		}
		m := artifactHeadRE.FindStringSubmatch(parts[0])
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		sim, _ := strconv.ParseFloat(m[2], 64)
		row := ArtifactCandidateLine{
			ID:         id,
			Similarity: sim,
			FileType:   m[3],
			FileName:   m[4],
			Summary:    parts[len(parts)-1],
		}
		// Middle parts (between head and summary) are keywords / entities.
		for _, p := range parts[1 : len(parts)-1] {
			switch {
			case strings.HasPrefix(p, "Entities: "):
				row.Entities = strings.TrimPrefix(p, "Entities: ")
			default:
				row.Keywords = p
			}
		}
		out = append(out, row)
	}
	return out
}
