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
	return injectionTarget.MatchString(normalized) && injectionCommand.MatchString(normalized)
}
