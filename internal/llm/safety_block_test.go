package llm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsSafetyBlock covers the public seam the bot uses to distinguish a
// provider content-policy rejection (show "blocked by safety filter") from a
// transient outage (show generic api_error). The canonical case is the
// 2026-07-01 Gemini image-likeness block: HTTP-200 body
// {"error":{"code":400,"message":"Gemini blocked the request: PROHIBITED_CONTENT"}}
// wrapped by laplace as "LLM call failed: %w".
func TestIsSafetyBlock(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "gemini PROHIBITED_CONTENT (bare)",
			err:  &providerBodyError{Message: "Gemini blocked the request: PROHIBITED_CONTENT", Code: 400},
			want: true,
		},
		{
			name: "gemini PROHIBITED_CONTENT (wrapped as laplace does)",
			err:  fmt.Errorf("LLM call failed: %w", &providerBodyError{Message: "Gemini blocked the request: PROHIBITED_CONTENT", Code: 400}),
			want: true,
		},
		{
			name: "recitation (copyright reproduction block)",
			err:  &providerBodyError{Message: "RECITATION", Code: 400},
			want: true,
		},
		{
			name: "harm_category in raw metadata",
			err:  &providerBodyError{Message: "Provider returned error", Raw: `{"finishReason":"SAFETY","safetyRatings":[{"category":"HARM_CATEGORY_SEXUALLY_EXPLICIT"}]}`},
			want: true,
		},
		{
			name: "invalid_argument is not a safety block",
			err:  &providerBodyError{Message: "Corrupted thought signature.", Raw: "INVALID_ARGUMENT", Code: 400},
			want: false,
		},
		{
			name: "rate limit is not a safety block",
			err:  &providerBodyError{Message: "Rate limit exceeded", Code: 429},
			want: false,
		},
		{
			name: "generic wrapped error is not a safety block",
			err:  fmt.Errorf("context deadline exceeded: %w", errors.New("timeout")),
			want: false,
		},
		{name: "plain error", err: errors.New("max empty response retries reached"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSafetyBlock(tt.err))
		})
	}
}
