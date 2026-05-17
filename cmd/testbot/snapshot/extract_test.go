package snapshot

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleTrace = `{
  "batches": [
    {
      "scopeSpans": [
        {
          "spans": [
            {
              "name": "reranker.Execute",
              "startTimeUnixNano": "1000000000",
              "endTimeUnixNano": "1500000000",
              "attributes": [
                {"key": "user.id", "value": {"intValue": "12345"}},
                {"key": "reranker.candidates_in.topics", "value": {"intValue": "50"}},
                {"key": "reranker.candidates_in.people", "value": {"intValue": "10"}},
                {"key": "reranker.candidates_in.artifacts", "value": {"intValue": "20"}},
                {"key": "reranker.model_kept.topics", "value": {"intValue": "2"}},
                {"key": "reranker.model_raw_count.topics", "value": {"intValue": "2"}},
                {"key": "reranker.fallback_reason", "value": {"stringValue": ""}},
                {"key": "reranker.cost_usd", "value": {"doubleValue": 0.0112}},
                {"key": "reranker.llm_calls", "value": {"intValue": "2"}},
                {"key": "reranker.tool_calls", "value": {"intValue": "1"}}
              ],
              "events": [
                {
                  "name": "reranker.raw_query",
                  "attributes": [{"key": "body", "value": {"stringValue": "what about Qwen MoE swap?"}}]
                },
                {
                  "name": "reranker.enriched_query",
                  "attributes": [{"key": "body", "value": {"stringValue": "Qwen 3.6 35B-A3B MoE migration"}}]
                },
                {
                  "name": "reranker.candidates_input",
                  "attributes": [{"key": "body", "value": {"stringValue": "[Topic:9695] (0.81) 2026-04-07 | 2 msgs, ~4K chars | sglang vs vLLM\n[Topic:4815] (0.79) 2025-12-24 | 6 msgs, ~6K chars | H100 NVL, Qwen 3 72B"}}]
                },
                {
                  "name": "reranker.tool_call",
                  "attributes": [
                    {"key": "iteration", "value": {"intValue": "1"}},
                    {"key": "requested_count", "value": {"intValue": "2"}},
                    {"key": "valid_count", "value": {"intValue": "2"}},
                    {"key": "requested_ids", "value": {"arrayValue": {"values": [{"intValue": "9695"}, {"intValue": "4815"}]}}}
                  ]
                },
                {
                  "name": "reranker.selection_reasons",
                  "attributes": [{"key": "body", "value": {"stringValue": "[{\"id\":\"Topic:4815\",\"reason\":\"prior H100 history\"},{\"id\":\"Topic:6983\",\"reason\":\"target stack\"}]"}}]
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}`

func TestExtractRerankerSpan_HappyPath(t *testing.T) {
	sp, err := ExtractRerankerSpan("trace-abc", []byte(sampleTrace))
	require.NoError(t, err)

	assert.Equal(t, "trace-abc", sp.TraceID)
	assert.Equal(t, int64(12345), sp.UserID)
	assert.Equal(t, int64(500), sp.DurationMs, "endTimeNano-startTimeNano = 500_000_000ns = 500ms")

	assert.Equal(t, 50, sp.CandidatesIn.Topics)
	assert.Equal(t, 10, sp.CandidatesIn.People)
	assert.Equal(t, 20, sp.CandidatesIn.Artifacts)
	assert.Equal(t, 2, sp.ModelKept.Topics)
	assert.Equal(t, 2, sp.ModelRawCount.Topics)

	assert.Equal(t, "", sp.FallbackReason)
	assert.InDelta(t, 0.0112, sp.CostUSD, 1e-9)
	assert.Equal(t, 2, sp.LLMCalls)
	assert.Equal(t, 1, sp.ToolCalls)

	assert.Equal(t, "what about Qwen MoE swap?", sp.RawQuery)
	assert.Equal(t, "Qwen 3.6 35B-A3B MoE migration", sp.EnrichedQuery)
	assert.Contains(t, sp.CandidatesInput, "[Topic:9695]")

	require.Len(t, sp.ToolCallRequests, 1)
	assert.Equal(t, []int{9695, 4815}, sp.ToolCallRequests[0])

	assert.Equal(t, []int{4815, 6983}, sp.SelectedTopics)
	assert.Equal(t, "prior H100 history", sp.SelectedReasons[4815])
	assert.Equal(t, "target stack", sp.SelectedReasons[6983])
}

func TestExtractRerankerSpan_NoSpan(t *testing.T) {
	const otherSpan = `{"batches":[{"scopeSpans":[{"spans":[{"name":"bot.processMessageGroup"}]}]}]}`
	_, err := ExtractRerankerSpan("trace-x", []byte(otherSpan))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoRerankerSpan))
}

func TestExtractRerankerSpan_FallbackPath(t *testing.T) {
	// `selection_reasons` populated by fallbackFromState: ids present, reasons empty.
	const fallbackJSON = `{"batches":[{"scopeSpans":[{"spans":[{
      "name":"reranker.Execute",
      "startTimeUnixNano":"0","endTimeUnixNano":"0",
      "attributes":[
        {"key":"reranker.fallback_reason","value":{"stringValue":"all_hallucinated"}},
        {"key":"reranker.model_raw_count.topics","value":{"intValue":"0"}},
        {"key":"reranker.model_kept.topics","value":{"intValue":"0"}}
      ],
      "events":[
        {"name":"reranker.selection_reasons","attributes":[{"key":"body","value":{"stringValue":"[{\"id\":\"Topic:1995\",\"reason\":\"\"},{\"id\":\"Topic:8965\",\"reason\":\"\"}]"}}]}
      ]
    }]}]}]}`
	sp, err := ExtractRerankerSpan("c90199c6", []byte(fallbackJSON))
	require.NoError(t, err)
	assert.Equal(t, "all_hallucinated", sp.FallbackReason)
	assert.Equal(t, 0, sp.ModelRawCount.Topics)
	assert.Equal(t, []int{1995, 8965}, sp.SelectedTopics)
	assert.Equal(t, "", sp.SelectedReasons[1995])
}

func TestExtractRerankerSpan_MalformedJSON(t *testing.T) {
	_, err := ExtractRerankerSpan("x", []byte("not json"))
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrNoRerankerSpan), "decode error is distinct from missing-span error")
}

func TestParseCandidates(t *testing.T) {
	body := `[Topic:9695] (0.81) 2026-04-07 | 2 msgs, ~4K chars | sglang vs vLLM
[Topic:4815] (0.79) 2025-12-24 | 6 msgs, ~6K chars | 2xH100 NVL, Qwen 3 72B
[Topic:5135] (0.56) 2026-01-02 | 1 msgs, ~371 chars | small one with non-K size
not a candidate line — should be skipped silently
[Topic:bad] (0.99) ... malformed
`
	got := ParseCandidates(body)
	require.Len(t, got, 3)

	assert.Equal(t, 9695, got[0].ID)
	assert.InDelta(t, 0.81, got[0].Similarity, 1e-9)
	assert.Equal(t, "2026-04-07", got[0].Date)
	assert.Equal(t, 2, got[0].MsgCount)
	assert.Equal(t, "4K", got[0].Size)
	assert.Equal(t, "sglang vs vLLM", got[0].Summary)

	assert.Equal(t, 5135, got[2].ID)
	assert.Equal(t, "371", got[2].Size, "non-K sizes are kept verbatim")
}

func TestParseSelectionReasons_Malformed(t *testing.T) {
	res := parseSelectionReasons("not json")
	assert.Nil(t, res.Topics)
	assert.Empty(t, res.TopicReasons)

	res = parseSelectionReasons("")
	assert.Nil(t, res.Topics)
	assert.Empty(t, res.TopicReasons)
}

func TestParseSelectionReasons_SplitsByType(t *testing.T) {
	// Selection_reasons can include any of Topic/Person/Artifact in one flat
	// array. The parser must route each id into its own bucket and ignore
	// unknown prefixes without polluting other buckets.
	res := parseSelectionReasons(`[
		{"id":"Topic:1","reason":"t1"},
		{"id":"Person:99","reason":"p99"},
		{"id":"Topic:2","reason":"t2"},
		{"id":"Artifact:7","reason":"a7"},
		{"id":"Garbage:5","reason":"x"}
	]`)
	assert.Equal(t, []int{1, 2}, res.Topics)
	assert.Equal(t, []int{99}, res.People)
	assert.Equal(t, []int{7}, res.Artifacts)
	assert.Equal(t, "t1", res.TopicReasons[1])
	assert.Equal(t, "p99", res.PersonReasons[99])
	assert.Equal(t, "a7", res.ArtifactReasons[7])
}

func TestParsePersonCandidates_HappyPath(t *testing.T) {
	body := `[Person:304] Наиля (aka Наэля, Наля) [Work_Outer]: Предпринимательница, участница группы.
[Person:280] AISHA (@aisha_chi) [Work_Outer]: Клиент с апреля 2025.
[Person:163] Лиля [Work_Outer]: Участница созвонов.
[Person:777] NoBio [Friends]
`
	got := ParsePersonCandidates(body)
	require.Len(t, got, 4)

	assert.Equal(t, 304, got[0].ID)
	assert.Equal(t, "Наиля (aka Наэля, Наля)", got[0].Name)
	assert.Equal(t, "Work_Outer", got[0].Circle)
	assert.Equal(t, "Предпринимательница, участница группы.", got[0].Bio)

	assert.Equal(t, "AISHA (@aisha_chi)", got[1].Name, "username segment stays in Name")
	assert.Equal(t, "Лиля", got[2].Name, "no aliases or username is fine")
	assert.Empty(t, got[3].Bio, "trailing colon may be absent when bio is empty")
}

func TestParseArtifactCandidates_HappyPath(t *testing.T) {
	body := `[Artifact:978] (0.74) image: "photo.jpg" | k1, k2 | Entities: e1, e2 | summary one
[Artifact:500] (0.62) voice: "voice.ogg" | only summary here
[Artifact:35] (0.62) document: "_chat.txt" | Entities: only entities | the summary
[Artifact:777] (0.59) image: "p.jpg" | kw1, kw2 | the summary
`
	got := ParseArtifactCandidates(body)
	require.Len(t, got, 4)

	assert.Equal(t, 978, got[0].ID)
	assert.InDelta(t, 0.74, got[0].Similarity, 1e-9)
	assert.Equal(t, "image", got[0].FileType)
	assert.Equal(t, "photo.jpg", got[0].FileName)
	assert.Equal(t, "k1, k2", got[0].Keywords)
	assert.Equal(t, "e1, e2", got[0].Entities)
	assert.Equal(t, "summary one", got[0].Summary)

	assert.Empty(t, got[1].Keywords, "no middle parts means no keywords")
	assert.Empty(t, got[1].Entities)
	assert.Equal(t, "only summary here", got[1].Summary)

	assert.Empty(t, got[2].Keywords, "Entities-prefixed middle goes to Entities")
	assert.Equal(t, "only entities", got[2].Entities)

	assert.Equal(t, "kw1, kw2", got[3].Keywords)
	assert.Empty(t, got[3].Entities)
}

func TestParseArtifactCandidates_BadLineSkipped(t *testing.T) {
	body := `[Artifact:1] (0.5) image: "ok.jpg" | summary
this is not a candidate line at all
[Artifact:bogus] (0.5) image: "x.jpg" | summary
[Artifact:2] (0.6) voice: "ok.ogg" | summary2
`
	got := ParseArtifactCandidates(body)
	require.Len(t, got, 2, "only the two well-formed lines pass through")
	assert.Equal(t, 1, got[0].ID)
	assert.Equal(t, 2, got[1].ID)
}
