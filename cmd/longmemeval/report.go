package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type reportSummary struct {
	Cases                  int                    `json:"cases"`
	Judged                 int                    `json:"judged"`
	Correct                int                    `json:"correct"`
	Accuracy               float64                `json:"accuracy"`
	CacheHits              int                    `json:"cache_hits"`
	TotalCost              float64                `json:"total_cost_usd"`
	AverageDurationMS      float64                `json:"average_duration_ms"`
	AverageAnswerTokens    float64                `json:"average_answer_prompt_tokens"`
	AverageMemoryTokens    float64                `json:"average_memory_context_tokens_estimated"`
	AverageFinalTokens     float64                `json:"average_final_context_tokens_estimated"`
	AverageRerankerTokens  float64                `json:"average_reranker_input_tokens_estimated"`
	AverageCandidateRecall float64                `json:"average_candidate_recall"`
	AverageRerankerRecall  float64                `json:"average_post_reranker_recall"`
	AverageFinalRecall     float64                `json:"average_final_context_recall"`
	RetrievalCases         int                    `json:"retrieval_cases"`
	QuestionTypes          map[string]typeSummary `json:"question_types"`
}

type typeSummary struct {
	Cases    int     `json:"cases"`
	Judged   int     `json:"judged"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
}

type comparisonReport struct {
	Baseline             reportSummary      `json:"baseline"`
	Candidate            reportSummary      `json:"candidate"`
	AccuracyDelta        float64            `json:"accuracy_delta"`
	CandidateRecallDelta float64            `json:"candidate_recall_delta"`
	RerankerRecallDelta  float64            `json:"post_reranker_recall_delta"`
	FinalRecallDelta     float64            `json:"final_context_recall_delta"`
	AnswerTokensDelta    float64            `json:"answer_prompt_tokens_delta"`
	MemoryTokensDelta    float64            `json:"memory_context_tokens_estimated_delta"`
	FinalTokensDelta     float64            `json:"final_context_tokens_estimated_delta"`
	RerankerTokensDelta  float64            `json:"reranker_input_tokens_estimated_delta"`
	CostDelta            float64            `json:"total_cost_delta_usd"`
	FailToPass           int                `json:"fail_to_pass"`
	PassToFail           int                `json:"pass_to_fail"`
	Improvements         []caseTransition   `json:"improvements,omitempty"`
	Regressions          []caseTransition   `json:"regressions,omitempty"`
	QuestionTypeDeltas   map[string]float64 `json:"question_type_accuracy_deltas"`
}

type caseTransition struct {
	Variant           string  `json:"variant"`
	QuestionID        string  `json:"question_id"`
	QuestionType      string  `json:"question_type"`
	Baseline          bool    `json:"baseline_correct"`
	Candidate         bool    `json:"candidate_correct"`
	FinalRecallBefore float64 `json:"final_recall_before"`
	FinalRecallAfter  float64 `json:"final_recall_after"`
}

func loadRunResults(path string) ([]runResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read results %s: %w", path, err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("results %s are empty", path)
	}
	if trimmed[0] == '[' {
		var results []runResult
		if err := json.Unmarshal(trimmed, &results); err != nil {
			return nil, fmt.Errorf("decode results %s: %w", path, err)
		}
		return results, nil
	}
	var results []runResult
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var result runResult
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			return nil, fmt.Errorf("decode results %s line %d: %w", path, len(results)+1, err)
		}
		results = append(results, result)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan results %s: %w", path, err)
	}
	return results, nil
}

func summarizeResults(results []runResult) reportSummary {
	summary := reportSummary{Cases: len(results), QuestionTypes: make(map[string]typeSummary)}
	var recallCases int
	for _, result := range results {
		typeStats := summary.QuestionTypes[result.QuestionType]
		typeStats.Cases++
		if result.AutoevalLabel != nil {
			summary.Judged++
			typeStats.Judged++
			if result.AutoevalLabel.Label {
				summary.Correct++
				typeStats.Correct++
			}
		}
		if result.CacheHit {
			summary.CacheHits++
		}
		summary.TotalCost += result.Ingestion.Cost + result.Answer.Cost
		if result.Judge != nil && result.Judge.Cost != nil {
			summary.TotalCost += *result.Judge.Cost
		}
		summary.AverageDurationMS += float64(result.TotalDurationMS)
		summary.AverageAnswerTokens += float64(result.Answer.PromptTokens)
		if result.Answer.TokenBreakdown != nil {
			summary.AverageMemoryTokens += float64(result.Answer.TokenBreakdown.MemoryContext)
			summary.AverageFinalTokens += float64(result.Answer.TokenBreakdown.FinalTotalContext)
			summary.AverageRerankerTokens += float64(result.Answer.TokenBreakdown.RerankerInput)
		}
		if result.Retrieval != nil {
			recallCases++
			summary.AverageCandidateRecall += result.Retrieval.Candidate.Recall
			summary.AverageRerankerRecall += result.Retrieval.PostReranker.Recall
			summary.AverageFinalRecall += result.Retrieval.FinalContext.Recall
		}
		summary.QuestionTypes[result.QuestionType] = typeStats
	}
	if summary.Judged > 0 {
		summary.Accuracy = float64(summary.Correct) / float64(summary.Judged)
	}
	if summary.Cases > 0 {
		summary.AverageDurationMS /= float64(summary.Cases)
		summary.AverageAnswerTokens /= float64(summary.Cases)
		summary.AverageMemoryTokens /= float64(summary.Cases)
		summary.AverageFinalTokens /= float64(summary.Cases)
		summary.AverageRerankerTokens /= float64(summary.Cases)
	}
	summary.RetrievalCases = recallCases
	if recallCases > 0 {
		summary.AverageCandidateRecall /= float64(recallCases)
		summary.AverageRerankerRecall /= float64(recallCases)
		summary.AverageFinalRecall /= float64(recallCases)
	}
	for questionType, stats := range summary.QuestionTypes {
		if stats.Judged > 0 {
			stats.Accuracy = float64(stats.Correct) / float64(stats.Judged)
		}
		summary.QuestionTypes[questionType] = stats
	}
	return summary
}

func compareResults(baseline, candidate []runResult) (*comparisonReport, error) {
	baselineByKey, err := indexResults(baseline)
	if err != nil {
		return nil, err
	}
	candidateByKey, err := indexResults(candidate)
	if err != nil {
		return nil, err
	}
	if len(baselineByKey) != len(candidateByKey) {
		return nil, fmt.Errorf("result sets contain different numbers of cases: %d and %d", len(baselineByKey), len(candidateByKey))
	}
	report := &comparisonReport{
		Baseline: summarizeResults(baseline), Candidate: summarizeResults(candidate),
		QuestionTypeDeltas: make(map[string]float64),
	}
	for key, before := range baselineByKey {
		after, ok := candidateByKey[key]
		if !ok {
			return nil, fmt.Errorf("candidate results are missing %s", key)
		}
		if before.QuestionType != after.QuestionType || before.Mode != after.Mode {
			return nil, fmt.Errorf("case %s uses incompatible question type or mode", key)
		}
		if before.AutoevalLabel == nil || after.AutoevalLabel == nil {
			return nil, fmt.Errorf("case %s is not judged in both result sets", key)
		}
		if before.AutoevalLabel.Model != after.AutoevalLabel.Model {
			return nil, fmt.Errorf("case %s uses different judge models", key)
		}
		if before.AutoevalLabel.Label == after.AutoevalLabel.Label {
			continue
		}
		transition := caseTransition{
			Variant: before.Variant, QuestionID: before.QuestionID, QuestionType: before.QuestionType,
			Baseline: before.AutoevalLabel.Label, Candidate: after.AutoevalLabel.Label,
			FinalRecallBefore: finalRecall(before), FinalRecallAfter: finalRecall(after),
		}
		if after.AutoevalLabel.Label {
			report.FailToPass++
			report.Improvements = append(report.Improvements, transition)
		} else {
			report.PassToFail++
			report.Regressions = append(report.Regressions, transition)
		}
	}
	report.AccuracyDelta = report.Candidate.Accuracy - report.Baseline.Accuracy
	report.CandidateRecallDelta = report.Candidate.AverageCandidateRecall - report.Baseline.AverageCandidateRecall
	report.RerankerRecallDelta = report.Candidate.AverageRerankerRecall - report.Baseline.AverageRerankerRecall
	report.FinalRecallDelta = report.Candidate.AverageFinalRecall - report.Baseline.AverageFinalRecall
	report.AnswerTokensDelta = report.Candidate.AverageAnswerTokens - report.Baseline.AverageAnswerTokens
	report.MemoryTokensDelta = report.Candidate.AverageMemoryTokens - report.Baseline.AverageMemoryTokens
	report.FinalTokensDelta = report.Candidate.AverageFinalTokens - report.Baseline.AverageFinalTokens
	report.RerankerTokensDelta = report.Candidate.AverageRerankerTokens - report.Baseline.AverageRerankerTokens
	report.CostDelta = report.Candidate.TotalCost - report.Baseline.TotalCost
	for questionType, before := range report.Baseline.QuestionTypes {
		after, ok := report.Candidate.QuestionTypes[questionType]
		if ok {
			report.QuestionTypeDeltas[questionType] = after.Accuracy - before.Accuracy
		}
	}
	sort.Slice(report.Improvements, func(i, j int) bool { return report.Improvements[i].QuestionID < report.Improvements[j].QuestionID })
	sort.Slice(report.Regressions, func(i, j int) bool { return report.Regressions[i].QuestionID < report.Regressions[j].QuestionID })
	return report, nil
}

func indexResults(results []runResult) (map[string]runResult, error) {
	indexed := make(map[string]runResult, len(results))
	for _, result := range results {
		key := result.Variant + "/" + result.QuestionID
		if _, exists := indexed[key]; exists {
			return nil, fmt.Errorf("duplicate result %s", key)
		}
		indexed[key] = result
	}
	return indexed, nil
}

func finalRecall(result runResult) float64 {
	if result.Retrieval == nil {
		return 0
	}
	return result.Retrieval.FinalContext.Recall
}

func writeOfflineReport(writer io.Writer, value any, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	switch report := value.(type) {
	case reportSummary:
		if _, err := fmt.Fprintf(writer, "# LongMemEval report\n\n- Cases: %d\n- Judged: %d\n- Accuracy: %.2f%%\n", report.Cases, report.Judged, report.Accuracy*100); err != nil {
			return err
		}
		if report.RetrievalCases == 0 {
			if _, err := fmt.Fprintln(writer, "- Evidence recall: n/a (no retrieval diagnostics in these results)"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(writer, "- Candidate recall: %.2f%%\n- Post-reranker recall: %.2f%%\n- Final-context recall: %.2f%%\n", report.AverageCandidateRecall*100, report.AverageRerankerRecall*100, report.AverageFinalRecall*100); err != nil {
			return err
		}
		_, err := fmt.Fprintf(writer, "- Total cost: $%.6f\n- Average answer prompt: %.0f tokens\n- Average memory context estimate: %.0f tokens\n- Average final context estimate: %.0f tokens\n- Average reranker input estimate: %.0f tokens\n", report.TotalCost, report.AverageAnswerTokens, report.AverageMemoryTokens, report.AverageFinalTokens, report.AverageRerankerTokens)
		return err
	case *comparisonReport:
		_, err := fmt.Fprintf(writer, "# LongMemEval comparison\n\n- Baseline accuracy: %.2f%%\n- Candidate accuracy: %.2f%%\n- Accuracy delta: %+.2f pp\n- Fail → pass: %d\n- Pass → fail: %d\n- Final-context recall delta: %+.2f pp\n- Answer prompt delta: %+.0f tokens\n- Memory context estimate delta: %+.0f tokens\n- Final context estimate delta: %+.0f tokens\n- Reranker input estimate delta: %+.0f tokens\n- Cost delta: $%+.6f\n", report.Baseline.Accuracy*100, report.Candidate.Accuracy*100, report.AccuracyDelta*100, report.FailToPass, report.PassToFail, report.FinalRecallDelta*100, report.AnswerTokensDelta, report.MemoryTokensDelta, report.FinalTokensDelta, report.RerankerTokensDelta, report.CostDelta)
		return err
	default:
		return fmt.Errorf("unsupported markdown report type %T", value)
	}
}
