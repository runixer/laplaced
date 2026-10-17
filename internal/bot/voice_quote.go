package bot

import (
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/files"
)

// A cleaned transcript of real speech stays under about 3 words per second
// (p95 ≈ 2.2 on production voices). Quoting far more words than the audio can
// hold means the model wrote the "transcript" itself rather than hearing it.
// The slack keeps very short voices from tripping on a few extra words.
const (
	maxQuotedWordsPerSecond = 4
	quotedWordsSlack        = 8
)

var quoteWordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// voiceQuoteWords counts the words inside the reply's voice transcript
// blockquotes: a quote line containing 🎤 plus the quote lines that follow it.
func voiceQuoteWords(content string) int {
	total := 0
	inQuote := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, ">") {
			inQuote = false
			continue
		}
		if _, after, found := strings.Cut(trimmed, "🎤"); found {
			inQuote = true
			total += len(quoteWordRe.FindAllString(after, -1))
			continue
		}
		if inQuote {
			total += len(quoteWordRe.FindAllString(trimmed, -1))
		}
	}
	return total
}

// currentSpeechSeconds sums the declared playback length of the turn's
// speech-bearing attachments. Counting every audio/video kind only makes the
// check less sensitive, never more, when a turn mixes several of them.
func currentSpeechSeconds(processed []*files.ProcessedFile) int {
	total := 0
	for _, f := range processed {
		switch f.FileType {
		case files.FileTypeVoice, files.FileTypeAudio, files.FileTypeVideoNote, files.FileTypeVideo:
			total += f.MediaSeconds
		}
	}
	return total
}

// detectOverlongVoiceQuote reports whether the quoted transcript has more
// words than the attached speech could contain.
func detectOverlongVoiceQuote(content string, speechSeconds int) (words int, overlong bool) {
	if speechSeconds <= 0 {
		return 0, false
	}
	words = voiceQuoteWords(content)
	return words, words > maxQuotedWordsPerSecond*speechSeconds+quotedWordsSlack
}

// recordVoiceQuoteAnomaly sets bot.anomaly.voice_quote_overlong when the
// reply quotes a transcript too long for the attached audio, the signature of
// a model that did not hear the voice and invented its content.
func recordVoiceQuoteAnomaly(span trace.Span, content string, speechSeconds int) {
	words, overlong := detectOverlongVoiceQuote(content, speechSeconds)
	if !overlong {
		return
	}
	span.SetAttributes(
		attribute.Bool("bot.anomaly.voice_quote_overlong", true),
		attribute.Int("bot.anomaly.voice_quote_words", words),
		attribute.Int("bot.anomaly.voice_seconds", speechSeconds),
	)
}
