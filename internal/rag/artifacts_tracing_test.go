package rag

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/llm"
)

// TestClassifyExtractionErr keeps the TraceQL filter labels stable. Adding a
// new error shape upstream means appending here so dashboards/queries stay
// honest. "other" is the fallback bucket.
func TestClassifyExtractionErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil err returns empty", nil, ""},
		{"JSON parse failure", errors.New("failed to parse extraction JSON"), "parse"},
		{"embedding generation failure", errors.New("failed to generate summary embedding"), "embedding"},
		{"vertex 429", errors.New("HTTP 429: RESOURCE_EXHAUSTED"), "rate_limit"},
		{"file too large", errors.New("file too large for extraction"), "file_too_large"},
		{"empty file", errors.New("empty file: cannot process zero-size artifact"), "empty_file"},
		{"timeout", errors.New("context deadline exceeded"), "timeout"},
		{"llm call failed", errors.New("LLM call failed: stream ended"), "llm"},
		// Real poisoned-artifact shape: extractor wraps the safety block as
		// "LLM call failed: %w". Must win over the generic "llm" case so the
		// landmine is queryable via error_kind="safety".
		{"safety block beats llm wrapper", fmt.Errorf("LLM call failed: %w", llm.NewSafetyBlockErrorForTest("Gemini blocked the request: PROHIBITED_CONTENT", 400)), "safety"},
		{"unknown shape", errors.New("disk full"), "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyExtractionErr(tt.err))
		})
	}
}
