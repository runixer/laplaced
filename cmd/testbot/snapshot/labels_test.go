package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderLabelsTodo_BasicShape(t *testing.T) {
	m := &Manifest{
		Agent:       "reranker",
		SpanName:    "reranker.Execute",
		TempoURL:    "https://tempo.example",
		Filter:      `{ name = "reranker.Execute" }`,
		WindowStart: time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
		CapturedAt:  time.Date(2026, 4, 28, 5, 22, 0, 0, time.UTC),
		DBPath:      "laplaced.db",
	}

	sp := &RerankerSpanData{
		TraceID:    "abc12345",
		UserID:     "12345",
		DurationMs: 5500,
	}
	sp.CandidatesIn.Topics = 50
	sp.CandidatesIn.People = 10
	sp.CandidatesIn.Artifacts = 20
	sp.ModelKept.Topics = 2
	sp.CostUSD = 0.0112
	sp.LLMCalls = 2
	sp.ToolCalls = 1
	sp.RawQuery = "what about MoE?\nsecond line"
	sp.EnrichedQuery = "Qwen MoE migration vLLM"
	sp.CandidatesInput = `[Topic:9695] (0.81) 2026-04-07 | 2 msgs, ~4K chars | sglang vs vLLM
[Topic:4815] (0.79) 2025-12-24 | 6 msgs, ~6K chars | 2xH100 NVL, Qwen 3 72B
[Topic:6983] (0.75) 2026-02-08 | 2 msgs, ~10K chars | Alpha-Chat v2.1`
	// 9695: T only (tool-called but rejected by model in final).
	// 4815: ST (tool-called and selected).
	// 6983: S only (selected without tool_call — possible if a future schema
	//   adds in-system summary-only selection, kept here to lock down the
	//   status-flag matrix even though it isn't produced by current code).
	sp.ToolCallRequests = [][]int{{9695, 4815}}
	sp.SelectedTopics = []int{4815, 6983}
	sp.SelectedReasons = map[int]string{
		4815: "prior H100 history",
		6983: "target stack",
	}

	md := RenderLabelsTodo(m, []*RerankerSpanData{sp})

	assert.Contains(t, md, "# Reranker Labels — TODO")
	assert.Contains(t, md, "**Trace count:** 1")
	assert.Contains(t, md, "## Trace `abc12345`")
	assert.Contains(t, md, "topics 50→2 (raw 0)")
	assert.Contains(t, md, "Cost:** $0.0112")

	// Block-quoted raw query, including newline-continuation
	assert.Contains(t, md, "> what about MoE?")
	assert.Contains(t, md, "> second line")

	// Enriched query is one-line
	assert.Contains(t, md, "**Query (enriched):** `Qwen MoE migration vLLM`")

	// Candidates table with status flags
	assert.Contains(t, md, "| Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |")
	// 9695: tool-called only
	assert.Regexp(t, `\| T \| 9695 \| 0\.81 \|`, md)
	// 4815: selected AND tool-called
	assert.Regexp(t, `\| ST \| 4815 \| 0\.79 \|`, md)
	// 6983: selected only (was not in tool_call request)
	assert.Regexp(t, `\| S \| 6983 \| 0\.75 \|`, md)

	// Selected summary
	assert.Contains(t, md, "**Selected topics (post-fallback):** `4815`, `6983`")
	assert.Contains(t, md, "`Topic:4815` — prior H100 history")
	assert.Contains(t, md, "`Topic:6983` — target stack")

	// People / Artifacts sections present but pre-instrumentation (no event body
	// supplied on this span fixture) — must show the explicit fallback stub,
	// not the old "schema doesn't preserve" message.
	assert.Contains(t, md, "### People (cap=10)")
	assert.Contains(t, md, "### Artifacts (cap=20)")
	assert.Contains(t, md, "_No `reranker.people_candidates_input` event on this span")
	assert.Contains(t, md, "_No `reranker.artifacts_candidates_input` event on this span")
}

func TestRenderLabelsTodo_PeopleAndArtifactsTables(t *testing.T) {
	sp := &RerankerSpanData{
		TraceID: "withcands",
		UserID:  "42",
		PeopleCandidatesInput: `[Person:304] Наиля (aka Наля) [Work_Outer]: short bio.
[Person:280] AISHA (@aisha_chi) [Work_Outer]: client since 2025.
[Person:163] Лиля [Friends]: helper.`,
		ArtifactCandidatesInput: `[Artifact:978] (0.74) image: "photo.jpg" | k1, k2 | Entities: e1 | sum one
[Artifact:500] (0.62) voice: "voice.ogg" | sum two`,
		SelectedPeople:    []int{280},
		SelectedArtifacts: []int{978},
		SelectedReasonsP:  map[int]string{280: "client matches query"},
		SelectedReasonsA:  map[int]string{978: "matches photo request"},
	}
	sp.CandidatesIn.People = 3
	sp.CandidatesIn.Artifacts = 2

	md := RenderLabelsTodo(nil, []*RerankerSpanData{sp})

	// People table: header + one S-flag row for ID 280, blank for the others.
	assert.Contains(t, md, "| Status | ID | Name | Circle | Bio | Score | Reason |")
	assert.Regexp(t, `\| S \| 280 \| AISHA \(@aisha_chi\) \|`, md)
	assert.Regexp(t, `\|  \| 304 \| Наиля \(aka Наля\) \|`, md)

	// Artifacts table.
	assert.Contains(t, md, "| Status | ID | Sim | Type | File | Summary | Score | Reason |")
	assert.Regexp(t, `\| S \| 978 \| 0\.74 \| image \| photo\.jpg \|`, md)
	assert.Regexp(t, `\|  \| 500 \| 0\.62 \| voice \| voice\.ogg \|`, md)

	// Per-type selected lists with reasons.
	assert.Contains(t, md, "**Selected people (post-fallback):** `280`")
	assert.Contains(t, md, "`Person:280` — client matches query")
	assert.Contains(t, md, "**Selected artifacts (post-fallback):** `978`")
	assert.Contains(t, md, "`Artifact:978` — matches photo request")
}

func TestRenderLabelsTodo_FallbackPath(t *testing.T) {
	sp := &RerankerSpanData{
		TraceID:          "c90199c6",
		UserID:           "67890",
		FallbackReason:   "all_hallucinated",
		RawQuery:         "поищи в Москве доставки",
		CandidatesInput:  `[Topic:1995] (0.59) 2025-12-26 | 4 msgs, ~3K chars | Расчёт пельменей`,
		ToolCallRequests: [][]int{{1995, 8965}},
		SelectedTopics:   []int{1995, 8965},
		SelectedReasons:  map[int]string{1995: "", 8965: ""},
	}

	md := RenderLabelsTodo(nil, []*RerankerSpanData{sp})

	assert.Contains(t, md, "**Fallback:** `all_hallucinated`")
	assert.Contains(t, md, "**Selected topics (post-fallback):** `1995`, `8965`")
	assert.Contains(t, md, "_(no topics reasons attached — typical of fallback path)_")
}

func TestRenderLabelsTodo_EmptyCandidates(t *testing.T) {
	sp := &RerankerSpanData{TraceID: "empty", CandidatesInput: ""}
	md := RenderLabelsTodo(nil, []*RerankerSpanData{sp})
	assert.Contains(t, md, "_No topic candidates parsed from `reranker.candidates_input`._")
}

func TestEscapePipes_DoesntBreakTable(t *testing.T) {
	sp := &RerankerSpanData{
		TraceID:         "x",
		CandidatesInput: `[Topic:1] (0.5) 2026-01-01 | 1 msgs, ~1K chars | summary with | pipe inside`,
	}
	md := RenderLabelsTodo(nil, []*RerankerSpanData{sp})
	// Each row must have exactly the expected number of column separators —
	// embedded pipes inside summary must be escaped, not literal.
	row := findRow(t, md, "| 1 |")
	assert.Contains(t, row, `\|`, "embedded pipe was escaped")
}

func findRow(t *testing.T, md, prefixContains string) string {
	t.Helper()
	for _, line := range strings.Split(md, "\n") {
		if strings.Contains(line, prefixContains) && strings.HasPrefix(strings.TrimSpace(line), "|") {
			return line
		}
	}
	require.Failf(t, "row not found", "no row containing %q in:\n%s", prefixContains, md)
	return ""
}
