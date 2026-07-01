package bot

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
)

// TestErrorReplyText verifies a provider safety block produces the dedicated
// "blocked by safety filter" message and tags the span, while any other error
// falls back to the generic api_error with no safety tag. Motivated by the
// 2026-07-01 incident where a Gemini PROHIBITED_CONTENT block failed the turn
// silently (empty reply) and the user just retried into the same block.
func TestErrorReplyText(t *testing.T) {
	bot, _, _, _, _ := setupBotForErrorTests(t)

	safetyErr := fmt.Errorf("LLM call failed: %w",
		llm.NewSafetyBlockErrorForTest("Gemini blocked the request: PROHIBITED_CONTENT", 400))
	genericErr := fmt.Errorf("network: %w", context.DeadlineExceeded)

	// The test translator has no bot.* copy and returns the key verbatim when a
	// key is missing, so asserting on the key proves which translation
	// errorReplyText selected — independent of the (separately shipped) copy.
	tests := []struct {
		name          string
		err           error
		wantKey       string
		wantSafetyTag bool
	}{
		{"safety block", safetyErr, "bot.safety_blocked", true},
		{"generic error", genericErr, "bot.api_error", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getSpans := testutil.WithTracingCapture(t)
			ctx, span := otel.Tracer("test").Start(context.Background(), "turn")

			got := bot.errorReplyText(span, tt.err)
			span.End()
			_ = ctx

			assert.Equal(t, tt.wantKey, got)

			spans := getSpans()
			assert.Len(t, spans, 1)
			hasTag := false
			for _, kv := range spans[0].Attributes {
				if kv.Key == attribute.Key("bot.anomaly.safety_blocked") {
					hasTag = kv.Value.AsBool()
				}
			}
			assert.Equal(t, tt.wantSafetyTag, hasTag)
		})
	}
}
