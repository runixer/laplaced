package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReasoningFor(t *testing.T) {
	tests := []struct {
		name  string
		level string
		want  *ReasoningConfig
	}{
		{"empty omits reasoning", "", nil},
		{"off omits reasoning (Gemini dynamic thinking)", "off", nil},
		{"auto omits reasoning (Gemini dynamic thinking)", "auto", nil},
		{"minimal sets effort", "minimal", &ReasoningConfig{Effort: "minimal"}},
		{"low sets effort", "low", &ReasoningConfig{Effort: "low"}},
		{"medium sets effort", "medium", &ReasoningConfig{Effort: "medium"}},
		{"high sets effort", "high", &ReasoningConfig{Effort: "high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ReasoningFor(tt.level))
		})
	}
}
