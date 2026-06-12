package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/testutil"
)

// incidentTableReply reproduces the production reply shape that used to lose
// chunks: Cyrillic prose around a markdown table long enough to force splits.
func incidentTableReply() string {
	var b strings.Builder
	b.WriteString(strings.Repeat("Вступительный абзац с подробными пояснениями и выводами. ", 30))
	b.WriteString("\n\n### Временное расписание\n\n")
	b.WriteString("| Время | Действие | Комментарий |\n| :--- | :--- | :--- |\n")
	for i := 0; i < 30; i++ {
		b.WriteString("| **07:15 - 08:45** | **Подъём, кормление и бодрствование** | Стартуем как обычно, активно играем, чтобы накопить усталость перед дорогой к врачу. |\n")
	}
	b.WriteString("\nЗаключительный абзац с выводами и рекомендациями.")
	return b.String()
}

// entityHeavyReply builds markdown whose HTML render expands well past the
// source length: bold fragments full of characters that escape to entities.
func entityHeavyReply() string {
	fragment := "**a & b < c > d \"q\"** и `x & y` тоже. "
	return strings.Repeat(fragment, 120) // ~4700 runes, HTML much longer
}

func TestTelegramRenderer_AllChunksWithinWireLimit(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"incident table reply", incidentTableReply()},
		{"entity-heavy reply", entityHeavyReply()},
		{"plain long cyrillic", strings.Repeat("Просто длинный текст без разметки. ", 400)},
	}

	r := NewTelegramRenderer(testutil.TestLogger())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := r.Render(context.Background(), tt.text)
			require.NoError(t, err)
			require.NotEmpty(t, chunks)
			for i, chunk := range chunks {
				assert.LessOrEqual(t, markdown.UTF16Length(chunk), telegramMessageLimit,
					"chunk %d exceeds Telegram wire limit", i)
				assert.NotEmpty(t, strings.TrimSpace(chunk))
			}
		})
	}
}

func TestTelegramRenderer_TableChunksKeepHeader(t *testing.T) {
	r := NewTelegramRenderer(testutil.TestLogger())
	chunks, err := r.Render(context.Background(), incidentTableReply())
	require.NoError(t, err)

	// Every chunk containing table rows must carry the header (tables render
	// as <pre> monospace, header text included).
	for i, chunk := range chunks {
		if strings.Contains(chunk, "07:15 - 08:45") {
			assert.Contains(t, chunk, "Время", "chunk %d has table rows but no header", i)
		}
	}
}

func TestTelegramRenderer_ResplitSetsAnomalyAttributes(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "bot.processMessageGroup")
	r := NewTelegramRenderer(testutil.TestLogger())
	chunks, err := r.Render(ctx, entityHeavyReply())
	span.End()

	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	spans := getSpans()
	require.NotEmpty(t, spans)
	attrs := make(map[attribute.Key]attribute.Value)
	for _, kv := range spans[len(spans)-1].Attributes {
		attrs[kv.Key] = kv.Value
	}
	v, ok := attrs["bot.anomaly.chunk_resplit"]
	require.True(t, ok, "bot.anomaly.chunk_resplit attribute missing")
	assert.True(t, v.AsBool())
	c, ok := attrs["bot.anomaly.chunk_resplit_count"]
	require.True(t, ok, "bot.anomaly.chunk_resplit_count attribute missing")
	assert.Positive(t, c.AsInt64())
}

func TestTelegramRenderer_NoResplitNoAnomalyAttributes(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "bot.processMessageGroup")
	r := NewTelegramRenderer(testutil.TestLogger())
	_, err := r.Render(ctx, "короткий ответ без таблиц")
	span.End()

	require.NoError(t, err)
	spans := getSpans()
	require.NotEmpty(t, spans)
	for _, kv := range spans[len(spans)-1].Attributes {
		assert.NotEqual(t, attribute.Key("bot.anomaly.chunk_resplit"), kv.Key)
	}
}
