package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestVoiceQuoteWords(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"no quote", "Just an answer without a transcript.", 0},
		{"single line quote", "> 🎤 Ты что там лук чистишь?\n\nОтвет.", 5},
		{"multi-line quote", "> 🎤 Первая строка цитаты\n> вторая строка\n\nОтвет тут.", 5},
		{"quote ends at blank line", "> 🎤 Раз два\n\n> обычная цитата не голос", 2},
		{"forwarded attribution counted", "> 🎤 **От Маши:** привет как дела", 5},
		{"two voice quotes", "> 🎤 раз два\n\n> 🎤 три четыре пять", 5},
		{"plain blockquote is ignored", "> цитата из статьи без голоса", 0},
		{"digits count as words", "> 🎤 позвонил на Барклая 8", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, voiceQuoteWords(tt.content))
		})
	}
}

func TestCurrentSpeechSeconds(t *testing.T) {
	processed := []*files.ProcessedFile{
		{FileType: files.FileTypeVoice, MediaSeconds: 7},
		{FileType: files.FileTypeVideoNote, MediaSeconds: 30},
		{FileType: files.FileTypePhoto, MediaSeconds: 0},
		{FileType: files.FileTypeAudio, MediaSeconds: 5},
	}
	assert.Equal(t, 42, currentSpeechSeconds(processed))
	assert.Equal(t, 0, currentSpeechSeconds(nil))
}

func TestDetectOverlongVoiceQuote(t *testing.T) {
	// Production case: a 7-second voice quoted as ~50 words that were never said.
	fabricated := "> 🎤 Так, я сделал ход конём. Я позвонил на Барклая, 8, оставил заявку. " +
		"Сейчас перезвонят, спрошу у них, какой бутик. У меня же там знакомая в Москве живёт — " +
		"скажу, что сейчас человечек подойдёт с наличкой, какой бутик и так далее. " +
		"Короче, я их на чистую воду сейчас выведу.\n\nОтвет."
	honest := "> 🎤 Ты что там, лук чистишь?\n\nОтвет."

	tests := []struct {
		name         string
		content      string
		seconds      int
		wantOverlong bool
	}{
		{"fabricated quote for a short voice", fabricated, 7, true},
		{"honest quote", honest, 7, false},
		{"same long quote fits a long voice", fabricated, 30, false},
		{"unknown duration never flags", fabricated, 0, false},
		{"no quote", "Ответ без цитаты.", 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, overlong := detectOverlongVoiceQuote(tt.content, tt.seconds)
			assert.Equal(t, tt.wantOverlong, overlong)
		})
	}
}

func TestRecordVoiceQuoteAnomaly(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)
	_, span := otel.Tracer("test").Start(context.Background(), "bot.processMessageGroup")
	recordVoiceQuoteAnomaly(span, "> 🎤 "+strings.Repeat("слово ", 60), 7)
	recordVoiceQuoteAnomaly(span, "", 7) // no-op must not clear anything
	span.End()

	spans := getSpans()
	assert.Len(t, spans, 1)
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range spans[0].Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.True(t, attrs["bot.anomaly.voice_quote_overlong"].AsBool())
	assert.Equal(t, int64(60), attrs["bot.anomaly.voice_quote_words"].AsInt64())
	assert.Equal(t, int64(7), attrs["bot.anomaly.voice_seconds"].AsInt64())
}
