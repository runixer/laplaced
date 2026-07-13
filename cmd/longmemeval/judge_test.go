package main

import (
	"context"
	"errors"
	"testing"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestBuildJudgePrompt(t *testing.T) {
	tests := []struct {
		name         string
		questionType string
		abstention   bool
		contains     string
	}{
		{name: "standard", questionType: "single-session-user", contains: "Correct Answer: answer"},
		{name: "assistant", questionType: "single-session-assistant", contains: "Correct Answer: answer"},
		{name: "multi-session", questionType: "multi-session", contains: "subset of the information"},
		{name: "temporal", questionType: "temporal-reasoning", contains: "do not penalize off-by-one errors"},
		{name: "knowledge update", questionType: "knowledge-update", contains: "previous information along with an updated answer"},
		{name: "preference", questionType: "single-session-preference", contains: "Rubric: answer"},
		{name: "abstention", questionType: "unknown", abstention: true, contains: "unanswerable question"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, err := buildJudgePrompt(tt.questionType, "question", "answer", "hypothesis", tt.abstention)
			require.NoError(t, err)
			assert.Contains(t, prompt, tt.contains)
		})
	}

	_, err := buildJudgePrompt("unknown", "question", "answer", "hypothesis", false)
	require.ErrorContains(t, err, "unsupported question type")
}

func TestJudgeResponseIsCorrect(t *testing.T) {
	tests := []struct {
		response string
		want     bool
	}{
		{response: "yes", want: true},
		{response: "YES", want: true},
		{response: "Yes.", want: true},
		{response: "The answer is yes", want: true},
		{response: "no", want: false},
		{response: "", want: false},
		{response: "yesterday", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.response, func(t *testing.T) {
			assert.Equal(t, tt.want, judgeResponseIsCorrect(tt.response))
		})
	}
}

func TestLongMemEvalJudgeEvaluate(t *testing.T) {
	client := new(testutil.MockLLMClient)
	cost := 0.001
	client.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return req.Model == defaultJudgeModel && req.N == 1 && req.Temperature != nil && *req.Temperature == 0 && req.MaxTokens == 10 && len(req.Messages) == 1
	})).Return(llm.ChatCompletionResponse{
		Choices: []llm.ResponseChoice{{Message: llm.ResponseMessage{Role: "assistant", Content: "Yes"}}},
		Usage:   llm.Usage{PromptTokens: 100, CompletionTokens: 1, TotalTokens: 101, Cost: &cost},
	}, nil)

	label, stats, err := newLongMemEvalJudge(client, defaultJudgeModel).Evaluate(context.Background(), evalCase{
		QuestionID: "case-1", QuestionType: "single-session-user", Question: "Question?", Answer: "Answer",
	}, "Answer")
	require.NoError(t, err)
	assert.True(t, label.Label)
	assert.Equal(t, defaultJudgeModel, label.Model)
	assert.Equal(t, "Yes", stats.Response)
	assert.Equal(t, 100, stats.PromptTokens)
	assert.Equal(t, 1, stats.CompletionTokens)
	assert.Equal(t, 101, stats.TotalTokens)
	assert.Equal(t, cost, *stats.Cost)
	client.AssertExpectations(t)
}

func TestLongMemEvalJudgeErrors(t *testing.T) {
	t.Run("client error", func(t *testing.T) {
		client := new(testutil.MockLLMClient)
		client.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(nil, errors.New("unavailable"))
		_, _, err := newLongMemEvalJudge(client, defaultJudgeModel).Evaluate(context.Background(), evalCase{
			QuestionID: "case-1", QuestionType: "single-session-user", Question: "Question?", Answer: "Answer",
		}, "Answer")
		require.ErrorContains(t, err, "call judge")
		client.AssertExpectations(t)
	})

	t.Run("empty choices", func(t *testing.T) {
		client := new(testutil.MockLLMClient)
		client.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(llm.ChatCompletionResponse{}, nil)
		_, _, err := newLongMemEvalJudge(client, defaultJudgeModel).Evaluate(context.Background(), evalCase{
			QuestionID: "case-1", QuestionType: "single-session-user", Question: "Question?", Answer: "Answer",
		}, "Answer")
		require.ErrorContains(t, err, "no choices")
		client.AssertExpectations(t)
	})
}
