package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func judgedResult(id, questionType string, correct bool, finalRecall float64) runResult {
	cost := 0.1
	return runResult{
		Variant: "default", QuestionID: id, QuestionType: questionType,
		AutoevalLabel: &autoevalLabel{Model: defaultJudgeModel, Label: correct},
		Judge:         &judgeStats{Cost: &cost},
		Ingestion:     aggregateStats{Cost: 0.2},
		Answer:        answerStats{Cost: 0.3, PromptTokens: 1000},
		Retrieval: &retrievalEvidence{
			Candidate: evidenceRecall{Recall: 1}, PostReranker: evidenceRecall{Recall: 0.75},
			FinalContext: evidenceRecall{Recall: finalRecall},
		},
		TotalDurationMS: 100,
	}
}

func TestSummarizeResults(t *testing.T) {
	summary := summarizeResults([]runResult{
		judgedResult("q1", "multi-session", true, 1),
		judgedResult("q2", "multi-session", false, 0.5),
	})
	require.Equal(t, 2, summary.Cases)
	require.Equal(t, 2, summary.Judged)
	require.Equal(t, 1, summary.Correct)
	require.Equal(t, 0.5, summary.Accuracy)
	require.Equal(t, 0.75, summary.AverageFinalRecall)
	require.InDelta(t, 1.2, summary.TotalCost, 0.000001)
	require.Equal(t, 0.5, summary.QuestionTypes["multi-session"].Accuracy)
}

func TestCompareResults(t *testing.T) {
	baseline := []runResult{
		judgedResult("q1", "multi-session", false, 0.5),
		judgedResult("q2", "temporal-reasoning", true, 1),
	}
	candidate := []runResult{
		judgedResult("q1", "multi-session", true, 1),
		judgedResult("q2", "temporal-reasoning", false, 0.5),
	}
	report, err := compareResults(baseline, candidate)
	require.NoError(t, err)
	require.Equal(t, 1, report.FailToPass)
	require.Equal(t, 1, report.PassToFail)
	require.Len(t, report.Improvements, 1)
	require.Equal(t, "q1", report.Improvements[0].QuestionID)
	require.Len(t, report.Regressions, 1)
	require.Equal(t, "q2", report.Regressions[0].QuestionID)
}

func TestCompareResultsRejectsDuplicateCases(t *testing.T) {
	result := judgedResult("q1", "multi-session", true, 1)
	_, err := compareResults([]runResult{result, result}, []runResult{result})
	require.ErrorContains(t, err, "duplicate result")
}

func TestCompareResultsRejectsDifferentJudge(t *testing.T) {
	baseline := judgedResult("q1", "multi-session", true, 1)
	candidate := judgedResult("q1", "multi-session", true, 1)
	candidate.AutoevalLabel.Model = "different"
	_, err := compareResults([]runResult{baseline}, []runResult{candidate})
	require.ErrorContains(t, err, "different judge models")
}
