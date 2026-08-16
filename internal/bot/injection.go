package bot

import (
	"regexp"
	"strings"
)

// Cross-user assistant-instruction injection detection.
//
// Confirmed failure mode: a bot in one user's scope generates a "system
// profile for an AI assistant" describing a third person; that text is
// forwarded into another user's scope, where the model reads it as
// configuration and background pipelines persist it into topic memory. The
// detector below is a deterministic pre-LLM gate: a flagged message group is
// answered with a fixed localized refusal and never written to history, so
// the injected text can neither steer the current turn nor settle into
// long-term memory.
//
// Detection requires BOTH signals, keeping false positives rare: ordinary
// tech talk about system prompts carries no persist-command, and ordinary
// "remember that I like X" requests are not framed as an instruction block
// addressed to an assistant.

// injectionTarget matches text that presents itself as data/instructions
// addressed to an AI assistant. The bare stems ("ии", "profile") never match
// alone — each alternative anchors them to an assistant/bot addressee.
// NOTE: Go's RE2 \w is ASCII-only, so Cyrillic stems use explicit [а-я]
// classes (case folding via (?i); ё is normalized to е before matching).
var injectionTarget = regexp.MustCompile(`(?i)` +
	`(ии|ai)[\s-]?(ассистент|assistant)` +
	`|(инструкци|профил|данные|настройк)[а-я]*\s+для\s+(ии|бота|ассистента|нейросети)` +
	`|систем[а-я]+\s+(данн|инструкци|промпт)[а-я]*` +
	`|(instructions?|profile|data|configuration)\s+for\s+(the\s+|an?\s+)?(ai|bot|assistant)` +
	`|system\s+(data|prompt|instructions?)`)

// injectionCommand matches the persist/apply imperative that turns such a
// block from quoted text into an attempted command.
var injectionCommand = regexp.MustCompile(`(?i)` +
	`(запомни|сохрани|применяй|используй|исполняй)[а-я]*\s+(эт[а-я]+\s+)?(профиль|инструкци|данные|настройк)[а-я]*` +
	`|при\s+(всех\s+)?будущих\s+(запросах|разговорах|обращениях)` +
	`|(^|\n)\s*(команда|директива|command|directive)\s*:` +
	`|(remember|save|store|apply|use|follow)\s+(th(is|ese|e)\s+)?(profile|instructions?|configuration)` +
	`|in\s+all\s+future\s+(requests?|conversations?|interactions?)`)

// DetectAssistantInjection reports whether text looks like a block of
// instructions addressed to an AI assistant (a forwarded "profile" or
// "system data" block with a persist/apply command) rather than a human
// message. Both the assistant-target and the command signal must be present.
func DetectAssistantInjection(text string) bool {
	if text == "" {
		return false
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "ё", "е"), "Ё", "Е")
	if injectionTarget.MatchString(normalized) && injectionCommand.MatchString(normalized) {
		return true
	}

	// Telegram rich/entity ingress is projected to canonical Markdown before
	// this gate. Presentation delimiters and structural backslash escapes must
	// not let a formatted "AI-assistant" or command evade the same detector
	// that protects legacy plain text. This view is used only for detection;
	// stored/model-visible content remains unchanged.
	markdownView := stripCanonicalMarkdownForDetection(normalized)
	if injectionTarget.MatchString(markdownView) && injectionCommand.MatchString(markdownView) {
		return true
	}

	metadataFreeView := stripCanonicalMarkdownForDetection(stripCanonicalProjectionMetadata(normalized))
	return injectionTarget.MatchString(metadataFreeView) && injectionCommand.MatchString(metadataFreeView)
}

// incomingInjectionDetectionText combines raw transport-visible text across a
// grouped turn. Telegram entity metadata is intentionally absent from this
// view, so inserting an inert URL/user/date descriptor cannot split a safety
// signature. Rich-only and non-Telegram messages fall back to their canonical
// Text and are additionally normalized by DetectAssistantInjection.
func incomingInjectionDetectionText(messages []IncomingMessage) string {
	var out strings.Builder
	for _, message := range messages {
		value := message.DetectionText
		if value == "" {
			value = message.Text
		}
		if value == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(value)
	}
	return out.String()
}

// stripCanonicalProjectionMetadata removes only descriptors emitted by the
// Telegram rich/entity projectors. Their contents are inert model context, not
// user-visible words, and must not interrupt deterministic safety matching.
// Metadata punctuation is backslash-escaped, so an unescaped closing delimiter
// is an unambiguous end marker.
func stripCanonicalProjectionMetadata(text string) string {
	type descriptor struct {
		prefix string
		close  byte
	}
	descriptors := [...]descriptor{
		{prefix: " (URL: ", close: ')'},
		{prefix: " [Telegram user id=", close: ']'},
		{prefix: " [time: ", close: ']'},
		{prefix: " [username: ", close: ']'},
		{prefix: " [hashtag: ", close: ']'},
		{prefix: " [cashtag: ", close: ']'},
		{prefix: " [command: ", close: ']'},
		{prefix: " [anchor: ", close: ']'},
		{prefix: " [reference: ", close: ']'},
	}

	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		matched := false
		for _, candidate := range descriptors {
			if !strings.HasPrefix(text[i:], candidate.prefix) {
				continue
			}
			end := findUnescapedMetadataEnd(text, i+len(candidate.prefix), candidate.close)
			if end < 0 {
				continue
			}
			i = end + 1
			matched = true
			break
		}
		if matched {
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

func findUnescapedMetadataEnd(text string, start int, close byte) int {
	escaped := false
	for i := start; i < len(text); i++ {
		if escaped {
			escaped = false
			continue
		}
		if text[i] == '\\' {
			escaped = true
			continue
		}
		if text[i] == close {
			return i
		}
	}
	return -1
}

func stripCanonicalMarkdownForDetection(text string) string {
	const escapedSpecial = "`*_{}[]<>()#+-.!|~$\\"
	const presentation = "*~|`"

	var out strings.Builder
	out.Grow(len(text))
	escaped := false
	for _, r := range text {
		if escaped {
			if strings.ContainsRune(escapedSpecial, r) {
				out.WriteRune(r)
			} else {
				out.WriteByte('\\')
				out.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if strings.ContainsRune(presentation, r) {
			continue
		}
		out.WriteRune(r)
	}
	if escaped {
		out.WriteByte('\\')
	}
	return out.String()
}
