package llm

import (
	"encoding/json"
	"testing"
)

// TestUsageUnmarshal_PolymorphicCost verifies that usage.cost decodes from every
// shape backends emit: a bare number (OpenRouter / most litellm models), an
// object with total_cost (litellm for Perplexity), null, and absent.
func TestUsageUnmarshal_PolymorphicCost(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wantCost *float64
		wantPT   int
	}{
		{
			name:     "bare number cost",
			json:     `{"prompt_tokens":6,"completion_tokens":1,"total_tokens":7,"cost":0.0042}`,
			wantCost: ptr(0.0042),
			wantPT:   6,
		},
		{
			name:     "object cost with total_cost (litellm/Perplexity)",
			json:     `{"prompt_tokens":6,"completion_tokens":1,"total_tokens":7,"cost":{"input_tokens_cost":2e-05,"output_tokens_cost":2e-05,"request_cost":0.006,"total_cost":0.00603}}`,
			wantCost: ptr(0.00603),
			wantPT:   6,
		},
		{
			name:     "null cost",
			json:     `{"prompt_tokens":6,"total_tokens":6,"cost":null}`,
			wantCost: nil,
			wantPT:   6,
		},
		{
			name:     "absent cost",
			json:     `{"prompt_tokens":6,"total_tokens":6}`,
			wantCost: nil,
			wantPT:   6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u Usage
			if err := json.Unmarshal([]byte(tt.json), &u); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if u.PromptTokens != tt.wantPT {
				t.Errorf("PromptTokens = %d, want %d", u.PromptTokens, tt.wantPT)
			}
			switch {
			case tt.wantCost == nil && u.Cost != nil:
				t.Errorf("Cost = %v, want nil", *u.Cost)
			case tt.wantCost != nil && u.Cost == nil:
				t.Errorf("Cost = nil, want %v", *tt.wantCost)
			case tt.wantCost != nil && *u.Cost != *tt.wantCost:
				t.Errorf("Cost = %v, want %v", *u.Cost, *tt.wantCost)
			}
		})
	}
}

// TestUsageUnmarshal_InResponse verifies the full chat response decodes with the
// Perplexity-style object cost (the exact shape that broke internet_search).
func TestUsageUnmarshal_InResponse(t *testing.T) {
	body := `{"id":"x","model":"perplexity/sonar-pro","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":6,"completion_tokens":1,"total_tokens":7,"cost":{"total_cost":0.00603}}}`
	var resp ChatCompletionResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode failed (regression): %v", err)
	}
	if resp.Usage.Cost == nil || *resp.Usage.Cost != 0.00603 {
		t.Errorf("Cost = %v, want 0.00603", resp.Usage.Cost)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("choices not decoded: %+v", resp.Choices)
	}
}

func ptr(f float64) *float64 { return &f }
