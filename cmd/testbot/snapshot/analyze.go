package snapshot

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// AnalysisReport summarises the no-LLM-required statistics over a batch of
// reranker spans. Built deterministically from RerankerSpanData fields so it
// can be diffed across snapshots / prompt versions.
type AnalysisReport struct {
	Traces      int                `json:"traces"`
	Fallbacks   FallbackBreakdown  `json:"fallbacks"`
	Topics      TypeStats          `json:"topics"`
	People      TypeStats          `json:"people"`
	Artifacts   TypeStats          `json:"artifacts"`
	CostUSD     Numeric            `json:"cost_usd"`
	LLMCalls    Numeric            `json:"llm_calls"`
	ToolCalls   Numeric            `json:"tool_calls"`
	DurationMs  Numeric            `json:"duration_ms"`
	HallucTopic Numeric            `json:"halluc_rate_topics"` // (raw-kept)/raw, per-trace
	Per         []PerTraceSnapshot `json:"per_trace,omitempty"`
}

// FallbackBreakdown counts traces by fallback_reason. Empty string is the
// success path, kept as "" key for fidelity with the trace data.
type FallbackBreakdown struct {
	Total    int            `json:"total"`
	Success  int            `json:"success"`
	ByReason map[string]int `json:"by_reason"`
}

// TypeStats aggregates the candidate-funnel for one of (topics, people, artifacts).
type TypeStats struct {
	CandidatesIn  Numeric `json:"candidates_in"`
	ModelRawCount Numeric `json:"model_raw_count"`
	ModelKept     Numeric `json:"model_kept"`
	SelectionRate Numeric `json:"selection_rate"` // model_kept / candidates_in (per-trace)
}

// Numeric is a basic distribution summary.
type Numeric struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
	Max    float64 `json:"max"`
	Min    float64 `json:"min"`
	Sum    float64 `json:"sum"`
}

// PerTraceSnapshot is a one-line digest used for `--per-trace` listing.
type PerTraceSnapshot struct {
	TraceID    string  `json:"trace_id"`
	UserID     int64   `json:"user_id"`
	Fallback   string  `json:"fallback,omitempty"`
	TopicsIn   int     `json:"topics_in"`
	TopicsKept int     `json:"topics_kept"`
	PeopleIn   int     `json:"people_in"`
	PeopleKept int     `json:"people_kept"`
	ArtIn      int     `json:"artifacts_in"`
	ArtKept    int     `json:"artifacts_kept"`
	CostUSD    float64 `json:"cost_usd"`
	DurationMs int64   `json:"duration_ms"`
	ToolCalls  int     `json:"tool_calls"`
}

// AnalyzeSpans walks the spans once, collects per-trace floats, then computes
// summary stats. Stable across reruns.
func AnalyzeSpans(spans []*RerankerSpanData) *AnalysisReport {
	r := &AnalysisReport{
		Traces:    len(spans),
		Fallbacks: FallbackBreakdown{ByReason: map[string]int{}},
	}

	var (
		topicsIn, topicsRaw, topicsKept, topicsSelRate []float64
		peopleIn, peopleRaw, peopleKept, peopleSelRate []float64
		artsIn, artsRaw, artsKept, artsSelRate         []float64
		hallucT                                        []float64
		costs, llmCalls, toolCalls, durations          []float64
	)

	for _, sp := range spans {
		r.Fallbacks.Total++
		if sp.FallbackReason == "" {
			r.Fallbacks.Success++
			r.Fallbacks.ByReason[""]++
		} else {
			r.Fallbacks.ByReason[sp.FallbackReason]++
		}

		topicsIn = append(topicsIn, float64(sp.CandidatesIn.Topics))
		topicsRaw = append(topicsRaw, float64(sp.ModelRawCount.Topics))
		topicsKept = append(topicsKept, float64(sp.ModelKept.Topics))
		if sp.CandidatesIn.Topics > 0 {
			topicsSelRate = append(topicsSelRate, float64(sp.ModelKept.Topics)/float64(sp.CandidatesIn.Topics))
		}
		if sp.ModelRawCount.Topics > 0 {
			h := float64(sp.ModelRawCount.Topics-sp.ModelKept.Topics) / float64(sp.ModelRawCount.Topics)
			hallucT = append(hallucT, h)
		}

		peopleIn = append(peopleIn, float64(sp.CandidatesIn.People))
		peopleRaw = append(peopleRaw, float64(sp.ModelRawCount.People))
		peopleKept = append(peopleKept, float64(sp.ModelKept.People))
		if sp.CandidatesIn.People > 0 {
			peopleSelRate = append(peopleSelRate, float64(sp.ModelKept.People)/float64(sp.CandidatesIn.People))
		}

		artsIn = append(artsIn, float64(sp.CandidatesIn.Artifacts))
		artsRaw = append(artsRaw, float64(sp.ModelRawCount.Artifacts))
		artsKept = append(artsKept, float64(sp.ModelKept.Artifacts))
		if sp.CandidatesIn.Artifacts > 0 {
			artsSelRate = append(artsSelRate, float64(sp.ModelKept.Artifacts)/float64(sp.CandidatesIn.Artifacts))
		}

		costs = append(costs, sp.CostUSD)
		llmCalls = append(llmCalls, float64(sp.LLMCalls))
		toolCalls = append(toolCalls, float64(sp.ToolCalls))
		durations = append(durations, float64(sp.DurationMs))

		r.Per = append(r.Per, PerTraceSnapshot{
			TraceID:    sp.TraceID,
			UserID:     sp.UserID,
			Fallback:   sp.FallbackReason,
			TopicsIn:   sp.CandidatesIn.Topics,
			TopicsKept: sp.ModelKept.Topics,
			PeopleIn:   sp.CandidatesIn.People,
			PeopleKept: sp.ModelKept.People,
			ArtIn:      sp.CandidatesIn.Artifacts,
			ArtKept:    sp.ModelKept.Artifacts,
			CostUSD:    sp.CostUSD,
			DurationMs: sp.DurationMs,
			ToolCalls:  sp.ToolCalls,
		})
	}

	r.Topics = TypeStats{
		CandidatesIn:  numStats(topicsIn),
		ModelRawCount: numStats(topicsRaw),
		ModelKept:     numStats(topicsKept),
		SelectionRate: numStats(topicsSelRate),
	}
	r.People = TypeStats{
		CandidatesIn:  numStats(peopleIn),
		ModelRawCount: numStats(peopleRaw),
		ModelKept:     numStats(peopleKept),
		SelectionRate: numStats(peopleSelRate),
	}
	r.Artifacts = TypeStats{
		CandidatesIn:  numStats(artsIn),
		ModelRawCount: numStats(artsRaw),
		ModelKept:     numStats(artsKept),
		SelectionRate: numStats(artsSelRate),
	}
	r.CostUSD = numStats(costs)
	r.LLMCalls = numStats(llmCalls)
	r.ToolCalls = numStats(toolCalls)
	r.DurationMs = numStats(durations)
	r.HallucTopic = numStats(hallucT)

	return r
}

func numStats(xs []float64) Numeric {
	if len(xs) == 0 {
		return Numeric{}
	}
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range xs {
		sum += v
	}
	return Numeric{
		N:      len(xs),
		Mean:   sum / float64(len(xs)),
		Median: percentile(sorted, 0.5),
		P95:    percentile(sorted, 0.95),
		Max:    sorted[len(sorted)-1],
		Min:    sorted[0],
		Sum:    sum,
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	// nearest-rank, simple and reproducible
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// FormatReport renders an AnalysisReport as human-readable text. JSON form is
// produced separately (caller handles persistence).
func FormatReport(r *AnalysisReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Traces analysed: %d\n\n", r.Traces)

	fmt.Fprintf(&b, "Fallbacks:\n")
	fmt.Fprintf(&b, "  success         %d (%s)\n", r.Fallbacks.Success, pct(r.Fallbacks.Success, r.Fallbacks.Total))
	keys := make([]string, 0, len(r.Fallbacks.ByReason))
	for k := range r.Fallbacks.ByReason {
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := r.Fallbacks.ByReason[k]
		fmt.Fprintf(&b, "  %-15s %d (%s)\n", k, n, pct(n, r.Fallbacks.Total))
	}
	fmt.Fprintf(&b, "\n")

	writeTypeStats(&b, "Topics", r.Topics)
	writeTypeStats(&b, "People", r.People)
	writeTypeStats(&b, "Artifacts", r.Artifacts)

	fmt.Fprintf(&b, "Halluc rate (topics, per-trace where raw>0):\n")
	writeNumLine(&b, "  ", r.HallucTopic, "%")
	fmt.Fprintf(&b, "\nCost USD:\n")
	writeNumLine(&b, "  ", r.CostUSD, "$")
	fmt.Fprintf(&b, "\nLLM calls:\n")
	writeNumLine(&b, "  ", r.LLMCalls, "")
	fmt.Fprintf(&b, "\nTool calls:\n")
	writeNumLine(&b, "  ", r.ToolCalls, "")
	fmt.Fprintf(&b, "\nDuration ms:\n")
	writeNumLine(&b, "  ", r.DurationMs, "ms")

	return b.String()
}

func writeTypeStats(b *strings.Builder, label string, ts TypeStats) {
	fmt.Fprintf(b, "%s:\n", label)
	fmt.Fprintf(b, "  candidates_in:   ")
	writeNumLine(b, "", ts.CandidatesIn, "")
	fmt.Fprintf(b, "  model_raw_count: ")
	writeNumLine(b, "", ts.ModelRawCount, "")
	fmt.Fprintf(b, "  model_kept:      ")
	writeNumLine(b, "", ts.ModelKept, "")
	fmt.Fprintf(b, "  selection_rate:  ")
	writeNumLine(b, "", ts.SelectionRate, "%")
	fmt.Fprintf(b, "\n")
}

func writeNumLine(b *strings.Builder, prefix string, n Numeric, suffix string) {
	if n.N == 0 {
		fmt.Fprintf(b, "%sn=0\n", prefix)
		return
	}
	if suffix == "%" {
		fmt.Fprintf(b, "%sn=%d  mean=%.1f%%  median=%.1f%%  p95=%.1f%%  max=%.1f%%\n",
			prefix, n.N, n.Mean*100, n.Median*100, n.P95*100, n.Max*100)
		return
	}
	if suffix == "$" {
		fmt.Fprintf(b, "%sn=%d  mean=$%.4f  median=$%.4f  p95=$%.4f  max=$%.4f  sum=$%.2f\n",
			prefix, n.N, n.Mean, n.Median, n.P95, n.Max, n.Sum)
		return
	}
	fmt.Fprintf(b, "%sn=%d  mean=%.1f%s  median=%.1f%s  p95=%.1f%s  max=%.0f%s\n",
		prefix, n.N, n.Mean, suffix, n.Median, suffix, n.P95, suffix, n.Max, suffix)
}

func pct(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(total))
}
