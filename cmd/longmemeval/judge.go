package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/llm"
)

const defaultJudgeModel = "gpt-4o-2024-08-06"

type autoevalLabel struct {
	Model string `json:"model"`
	Label bool   `json:"label"`
}

type judgeStats struct {
	Response         string   `json:"response"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	TotalTokens      int      `json:"total_tokens"`
	Cost             *float64 `json:"cost_usd,omitempty"`
	DurationMS       int64    `json:"duration_ms"`
}

type longMemEvalJudge struct {
	client llm.Client
	model  string
}

func newLongMemEvalJudge(client llm.Client, model string) *longMemEvalJudge {
	return &longMemEvalJudge{client: client, model: model}
}

func (j *longMemEvalJudge) Evaluate(ctx context.Context, eval evalCase, hypothesis string) (*autoevalLabel, *judgeStats, error) {
	prompt, err := buildJudgePrompt(eval.QuestionType, eval.Question, string(eval.Answer), hypothesis, strings.Contains(eval.QuestionID, "_abs"))
	if err != nil {
		return nil, nil, err
	}
	temperature := 0.0
	startedAt := time.Now()
	resp, err := j.client.CreateChatCompletion(ctx, llm.ChatCompletionRequest{
		Model: j.model,
		Messages: []llm.Message{{
			Role:    "user",
			Content: prompt,
		}},
		N:           1,
		Temperature: &temperature,
		MaxTokens:   10,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("call judge: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("judge returned no choices")
	}
	output := strings.TrimSpace(resp.Choices[0].Message.Content)
	return &autoevalLabel{Model: j.model, Label: judgeResponseIsCorrect(output)}, &judgeStats{
		Response:         output,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Cost:             resp.Usage.Cost,
		DurationMS:       time.Since(startedAt).Milliseconds(),
	}, nil
}

func buildJudgePrompt(questionType, question, answer, hypothesis string, abstention bool) (string, error) {
	if abstention {
		return fmt.Sprintf("I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the model correctly identifies the question as unanswerable. The model could say that the information is incomplete, or some other information is given but the asked information is not.\n\nQuestion: %s\n\nExplanation: %s\n\nModel Response: %s\n\nDoes the model correctly identify the question as unanswerable? Answer yes or no only.", question, answer, hypothesis), nil
	}

	var template string
	switch questionType {
	case "single-session-user", "single-session-assistant", "multi-session":
		template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
	case "temporal-reasoning":
		template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), the model's response is still correct. \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
	case "knowledge-update":
		template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response contains some previous information along with an updated answer, the response should be considered as correct as long as the updated answer is the required answer.\n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
	case "single-session-preference":
		template = "I will give you a question, a rubric for desired personalized response, and a response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls and utilizes the user's personal information correctly.\n\nQuestion: %s\n\nRubric: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
	default:
		return "", fmt.Errorf("unsupported question type %q", questionType)
	}
	return fmt.Sprintf(template, question, answer, hypothesis), nil
}

func judgeResponseIsCorrect(response string) bool {
	return strings.Contains(strings.ToLower(response), "yes")
}
