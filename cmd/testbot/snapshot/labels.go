package snapshot

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RenderLabelsTodo turns a manifest plus parsed reranker spans into the
// human-fillable markdown described in docs/plans/reranker-replay-eval.md.
//
// All three candidate types — topics, people, artifacts — are rendered as
// fillable tables when their respective `reranker.*_candidates_input` events
// are present on the span. Spans captured before the events were instrumented
// will fall back to a "no candidates parsed" stub for the missing sections.
func RenderLabelsTodo(m *Manifest, spans []*RerankerSpanData) string {
	var b strings.Builder

	writeHeader(&b, m, spans)
	for _, sp := range spans {
		writeTraceSection(&b, sp)
	}
	return b.String()
}

func writeHeader(b *strings.Builder, m *Manifest, spans []*RerankerSpanData) {
	fmt.Fprintf(b, "# Reranker Labels — TODO\n\n")
	if m != nil {
		fmt.Fprintf(b, "- **Snapshot agent:** %s\n", m.Agent)
		fmt.Fprintf(b, "- **Span:** `%s`\n", m.SpanName)
		fmt.Fprintf(b, "- **Tempo filter:** `%s`\n", m.Filter)
		fmt.Fprintf(b, "- **Window:** %s … %s (UTC)\n",
			m.WindowStart.UTC().Format(time.RFC3339),
			m.WindowEnd.UTC().Format(time.RFC3339))
		fmt.Fprintf(b, "- **Captured at:** %s\n", m.CapturedAt.UTC().Format(time.RFC3339))
		fmt.Fprintf(b, "- **Trace count:** %d\n", len(spans))
		if m.DBPath != "" {
			fmt.Fprintf(b, "- **DB snapshot:** `%s`\n", m.DBPath)
		}
	} else {
		fmt.Fprintf(b, "- **Trace count:** %d\n", len(spans))
	}
	fmt.Fprintf(b, "\n")
	fmt.Fprintf(b, "## How to fill\n\n")
	fmt.Fprintf(b, "Score scale (frozen 2026-04-26, see `docs/plans/reranker-replay-eval.md`):\n\n")
	fmt.Fprintf(b, "- **0-2** off-topic / outdated / contradicts query\n")
	fmt.Fprintf(b, "- **3-4** tangential, weak link\n")
	fmt.Fprintf(b, "- **5-6** related but doesn't answer\n")
	fmt.Fprintf(b, "- **7-8** directly answers / critical context\n")
	fmt.Fprintf(b, "- **9-10** ideal match (named in query, exact history)\n\n")
	fmt.Fprintf(b, "Status column legend: `S` = selected by reranker (final), `T` = pulled via tool_call, `ST` = both.\n")
	fmt.Fprintf(b, "Leave Score blank for trivial 0/10 rows. Reason is one phrase, optional.\n")
	fmt.Fprintf(b, "If Pass 2 (full content) reveals a topic is a chunking-split sibling of another, tag the\n")
	fmt.Fprintf(b, "Reason with `chunking_split:<other_id>` so eval-rerank attributes recall miss correctly.\n\n")
	fmt.Fprintf(b, "---\n\n")
}

func writeTraceSection(b *strings.Builder, sp *RerankerSpanData) {
	fmt.Fprintf(b, "## Trace `%s`\n\n", sp.TraceID)

	fmt.Fprintf(b, "- **User:** %d\n", sp.UserID)
	fmt.Fprintf(b, "- **Counts:** topics %d→%d (raw %d) / people %d→%d (raw %d) / artifacts %d→%d (raw %d)\n",
		sp.CandidatesIn.Topics, sp.ModelKept.Topics, sp.ModelRawCount.Topics,
		sp.CandidatesIn.People, sp.ModelKept.People, sp.ModelRawCount.People,
		sp.CandidatesIn.Artifacts, sp.ModelKept.Artifacts, sp.ModelRawCount.Artifacts)
	if sp.FallbackReason != "" {
		fmt.Fprintf(b, "- **Fallback:** `%s`\n", sp.FallbackReason)
	}
	fmt.Fprintf(b, "- **Cost:** $%.4f / **LLM calls:** %d / **Tool calls:** %d / **Duration:** %dms\n",
		sp.CostUSD, sp.LLMCalls, sp.ToolCalls, sp.DurationMs)
	fmt.Fprintf(b, "\n")

	fmt.Fprintf(b, "**Query (raw):**\n\n%s\n\n", quoteForBlock(sp.RawQuery))
	if sp.EnrichedQuery != "" {
		fmt.Fprintf(b, "**Query (enriched):** `%s`\n\n", oneLine(sp.EnrichedQuery))
	}

	writeTopicsTable(b, sp)
	writePeopleTable(b, sp)
	writeArtifactsTable(b, sp)
	writeSelectionSummary(b, sp)

	fmt.Fprintf(b, "---\n\n")
}

func writeTopicsTable(b *strings.Builder, sp *RerankerSpanData) {
	cands := ParseCandidates(sp.CandidatesInput)
	if len(cands) == 0 {
		fmt.Fprintf(b, "### Topics\n\n_No topic candidates parsed from `reranker.candidates_input`._\n\n")
		return
	}

	selected := intSet(sp.SelectedTopics)
	toolCalled := map[int]bool{}
	for _, req := range sp.ToolCallRequests {
		for _, id := range req {
			toolCalled[id] = true
		}
	}

	fmt.Fprintf(b, "### Topics (%d candidates)\n\n", len(cands))
	fmt.Fprintf(b, "| Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |\n")
	fmt.Fprintf(b, "|---|---|---|---|---|---|---|---|---|\n")

	// Preserve producer ordering (already sorted by similarity desc).
	for _, c := range cands {
		status := ""
		switch {
		case selected[c.ID] && toolCalled[c.ID]:
			status = "ST"
		case selected[c.ID]:
			status = "S"
		case toolCalled[c.ID]:
			status = "T"
		}
		fmt.Fprintf(b, "| %s | %d | %.2f | %s | %s | %d | %s | | |\n",
			status, c.ID, c.Similarity, c.Date, c.Size, c.MsgCount,
			escapePipes(truncateRunes(c.Summary, 240)))
	}
	fmt.Fprintf(b, "\n")
}

func writePeopleTable(b *strings.Builder, sp *RerankerSpanData) {
	fmt.Fprintf(b, "### People (cap=%d)\n\n", sp.CandidatesIn.People)
	if sp.PeopleCandidatesInput == "" {
		fmt.Fprintf(b, "_No `reranker.people_candidates_input` event on this span (pre-instrumentation trace)._\n\n")
		return
	}
	people := ParsePersonCandidates(sp.PeopleCandidatesInput)
	if len(people) == 0 {
		fmt.Fprintf(b, "_No person candidates parsed from event body._\n\n")
		return
	}

	selected := intSet(sp.SelectedPeople)
	fmt.Fprintf(b, "| Status | ID | Name | Circle | Bio | Score | Reason |\n")
	fmt.Fprintf(b, "|---|---|---|---|---|---|---|\n")
	for _, p := range people {
		status := ""
		if selected[p.ID] {
			status = "S"
		}
		fmt.Fprintf(b, "| %s | %d | %s | %s | %s | | |\n",
			status, p.ID,
			escapePipes(truncateRunes(p.Name, 80)),
			escapePipes(p.Circle),
			escapePipes(truncateRunes(p.Bio, 220)))
	}
	fmt.Fprintf(b, "\n")
}

func writeArtifactsTable(b *strings.Builder, sp *RerankerSpanData) {
	fmt.Fprintf(b, "### Artifacts (cap=%d)\n\n", sp.CandidatesIn.Artifacts)
	if sp.ArtifactCandidatesInput == "" {
		fmt.Fprintf(b, "_No `reranker.artifacts_candidates_input` event on this span (pre-instrumentation trace)._\n\n")
		return
	}
	arts := ParseArtifactCandidates(sp.ArtifactCandidatesInput)
	if len(arts) == 0 {
		fmt.Fprintf(b, "_No artifact candidates parsed from event body._\n\n")
		return
	}

	selected := intSet(sp.SelectedArtifacts)
	fmt.Fprintf(b, "| Status | ID | Sim | Type | File | Summary | Score | Reason |\n")
	fmt.Fprintf(b, "|---|---|---|---|---|---|---|---|\n")
	for _, a := range arts {
		status := ""
		if selected[a.ID] {
			status = "S"
		}
		fmt.Fprintf(b, "| %s | %d | %.2f | %s | %s | %s | | |\n",
			status, a.ID, a.Similarity,
			escapePipes(a.FileType),
			escapePipes(truncateRunes(a.FileName, 60)),
			escapePipes(truncateRunes(a.Summary, 220)))
	}
	fmt.Fprintf(b, "\n")
}

func writeSelectionSummary(b *strings.Builder, sp *RerankerSpanData) {
	total := len(sp.SelectedTopics) + len(sp.SelectedPeople) + len(sp.SelectedArtifacts)
	if total == 0 {
		fmt.Fprintf(b, "_Reranker selected 0 items._\n\n")
		return
	}

	writeSelectedList(b, "topics", "Topic", sp.SelectedTopics, sp.SelectedReasons)
	writeSelectedList(b, "people", "Person", sp.SelectedPeople, sp.SelectedReasonsP)
	writeSelectedList(b, "artifacts", "Artifact", sp.SelectedArtifacts, sp.SelectedReasonsA)
}

func writeSelectedList(b *strings.Builder, kind, prefix string, ids []int, reasons map[int]string) {
	if len(ids) == 0 {
		return
	}
	sorted := make([]int, len(ids))
	copy(sorted, ids)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	for _, id := range sorted {
		parts = append(parts, fmt.Sprintf("`%d`", id))
	}
	fmt.Fprintf(b, "**Selected %s (post-fallback):** %s\n\n", kind, strings.Join(parts, ", "))

	hasReason := false
	for _, r := range reasons {
		if strings.TrimSpace(r) != "" {
			hasReason = true
			break
		}
	}
	if !hasReason {
		fmt.Fprintf(b, "_(no %s reasons attached — typical of fallback path)_\n\n", kind)
		return
	}
	fmt.Fprintf(b, "**Reasons (model-supplied):**\n\n")
	for _, id := range sorted {
		r := reasons[id]
		if strings.TrimSpace(r) == "" {
			continue
		}
		fmt.Fprintf(b, "- `%s:%d` — %s\n", prefix, id, oneLine(r))
	}
	fmt.Fprintf(b, "\n")
}

func intSet(ids []int) map[int]bool {
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func quoteForBlock(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_(empty)_"
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func truncateRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// escapePipes makes a cell safe for a markdown table; pipes inside summaries
// would otherwise split the row.
func escapePipes(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
