package laplace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeLLMResponse_NoSanitizationNeeded(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"normal text", "Hello, this is a normal response."},
		{"with markdown", "# Header\n\nSome **bold** text."},
		{"single json block", "Here's some JSON:\n```json\n{\"key\": \"value\"}\n```"},
		{"two json blocks", "First:\n```json\n{\"a\": 1}\n```\nSecond:\n```json\n{\"b\": 2}\n```"},
		{"empty string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.input, result)
			assert.False(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_TruncatesOnHallucinationTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"tool_code tag",
			"Normal response.</tool_code> garbage after",
			"Normal response.",
		},
		{
			"tool_call tag",
			"Good text</tool_call>\n\nmore garbage",
			"Good text",
		},
		{
			"end of sequence tag",
			"Valid content</s> invalid content",
			"Valid content",
		},
		{
			"endoftext tag",
			"Response here<|endoftext|>more text",
			"Response here",
		},
		{
			"end tag",
			"Some text<|end|>garbage",
			"Some text",
		},
		{
			"tag in middle of text",
			"First part.\n\nSecond part.</tool_code></s>\n```json\n{}\n```",
			"First part.\n\nSecond part.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_TruncatesOnConsecutiveJSONBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"three consecutive json blocks",
			"Good response.\n\n```json\n{\"a\": 1}\n```\n```json\n{\"b\": 2}\n```\n```json\n{\"c\": 3}\n```",
			"Good response.",
		},
		{
			"many json blocks",
			"Normal text.\n```json\n{\"q\": \"test1\"}\n```\n```json\n{\"q\": \"test2\"}\n```\n```json\n{\"q\": \"test3\"}\n```\n```json\n{\"q\": \"test4\"}\n```\n```json\n{\"q\": \"test5\"}\n```",
			"Normal text.",
		},
		{
			"real world example",
			"Response here.\n\n```json\n{\"query\": \"search 1\"}\n```\n\n```json\n{\"query\": \"search 2\"}\n```\n\n```json\n{\"query\": \"search 3\"}\n```\n\n```json\n{\"query\": \"search 4\"}\n```",
			"Response here.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_CombinedArtifacts(t *testing.T) {
	// This simulates the real bug: tag followed by JSON blocks
	input := `Баланс Силы восстановлен. ⚖️😈</tool_code></s>
` + "```json\n{\"query\": \"test 1\"}\n```\n```json\n{\"query\": \"test 2\"}\n```\n```json\n{\"query\": \"test 3\"}\n```"

	result, sanitized := SanitizeLLMResponse(input)
	assert.Equal(t, "Баланс Силы восстановлен. ⚖️😈", result)
	assert.True(t, sanitized)
}

func TestSanitizeLLMResponse_EmptyAfterStripIsLegitimate(t *testing.T) {
	// When sanitization strips everything, the empty result is valid — the
	// caller substitutes a localized empty-response message. Prior behaviour
	// reverted to the original garbage; that was a workaround that conflicted
	// with thought-leak handling (where the whole content can be reasoning).
	input := "</tool_code>"
	result, sanitized := SanitizeLLMResponse(input)
	assert.Equal(t, "", result)
	assert.True(t, sanitized)
}

func TestSanitizeLLMResponse_RemovesDefaultAPIBlock(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"default_api at start with text after",
			`default_api:manage_memory{query:{"operations": [{"action": "add"}]}}Отличные новости!`,
			"Отличные новости!",
		},
		{
			"default_api at start with newline and text",
			"default_api:internet_search{query:home assistant golang}\n\nHere are the results:",
			"Here are the results:",
		},
		{
			"text before and after default_api",
			"Let me search for that. default_api:internet_search{query:test} Found it!",
			"Let me search for that.\n\nFound it!",
		},
		{
			"nested braces in JSON",
			`default_api:manage_memory{query:{"data": {"nested": {"deep": true}}}}Done!`,
			"Done!",
		},
		{
			"default_api with escaped quotes in JSON",
			`default_api:manage_memory{query:{"content": "He said \"hello\""}}Text after`,
			"Text after",
		},
		{
			"only default_api block (strips to empty)",
			`default_api:manage_memory{query:{"action": "test"}}`,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized, "sanitization should occur for inputs containing default_api:")
		})
	}
}

func TestSanitizeLLMResponse_ThoughtLeakTierA(t *testing.T) {
	// Tier A: leading `[thought ... ]`, `![thought ... ]`, `<thought>...</thought>`
	// wrappers are stripped. Only fires when the tag has a clear closing bracket.
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"bang-bracket thought with russian answer after",
			"![thought\nCRITICAL INSTRUCTION 1: ...\nThe user asked about X. Let me answer.]Погода сегодня отличная, солнечно и тепло.",
			"Погода сегодня отличная, солнечно и тепло.",
		},
		{
			"bracket thought without bang",
			"[thought\nI should keep this short.]Маша, вот твой ответ: 42.",
			"Маша, вот твой ответ: 42.",
		},
		{
			"bang-bracket light_thought",
			"![light_thought\nShort reasoning.]Да, рецепт подходит, можешь смело готовить.",
			"Да, рецепт подходит, можешь смело готовить.",
		},
		{
			"angle-bracket thought tag",
			"<thought>Some internal reasoning about Петя's question.</thought>\nПетя, вот что я думаю по этому поводу — всё в порядке.",
			"Петя, вот что я думаю по этому поводу — всё в порядке.",
		},
		{
			"thought with leading whitespace",
			"  \n![thought\nreasoning here]\nОтвет начинается тут.",
			"Ответ начинается тут.",
		},
		{
			"thought block is entire content (empty result is valid)",
			"![thought\nThis content never got to an answer.]",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_ThoughtLeakTierB(t *testing.T) {
	// Tier B: untagged English chain-of-thought followed by a known sentinel
	// marking the transition to the real answer. Only fires when the leading
	// content is overwhelmingly Latin (isLeadingLatinProse gate).
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"Thinking Process End sentinel",
			"Wait, looking at the structure. The user sent a forwarded chat log and wants my take. I should acknowledge the outcome and summarize action items. The tone should be supportive and professional.\n\nThinking Process End.\n\nМаша, отличная работа — ты всё правильно сделала.",
			"Маша, отличная работа — ты всё правильно сделала.",
		},
		{
			"Proceeding to generate sentinel",
			"Let me structure the response. First, validate the concern. Second, provide actionable steps. Third, reassure. Double-checking the facts one more time before output.\nProceeding to generate.\n> 📄 **photo.jpg:** Описание файла.\n\nПогода за окном отличная.",
			"> 📄 **photo.jpg:** Описание файла.\n\nПогода за окном отличная.",
		},
		{
			"End of thought process sentinel",
			"I have all the facts. I have all the style guidelines. The language is Russian. No JSON in output. The answer is short and to the point.\nEnd of thought process.\nКороткий ответ: да, это так.",
			"Короткий ответ: да, это так.",
		},
		{
			"Done-loop with Russian apology-start sentinel",
			"I see the second search misinterpreted the query. I don't need to search further. I have the physiological basis. Let me build the response carefully now.\n(Done)\n(Done)\n(Done)\n(Done)\nСистемный тайм-аут вызвал дублирование завершающих инструкций, приступаю к выдаче запрошенного контента.\n\nНастоящий ответ на вопрос Пети.",
			"Системный тайм-аут вызвал дублирование завершающих инструкций, приступаю к выдаче запрошенного контента.\n\nНастоящий ответ на вопрос Пети.",
		},
		{
			"Done loop without apology fallback",
			"I see the second search failed. Let me think about this differently. The user wants a short answer. I have enough context to proceed with a physiological explanation.\n(Done)\n(Done)\n(Done)\nКороткий ответ на вопрос.",
			"Короткий ответ на вопрос.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_ThoughtLeak_NoFalsePositive(t *testing.T) {
	// Legitimate Russian responses must pass through unchanged, including
	// responses that happen to quote English phrases or contain markdown
	// structures that look similar to leak shapes.
	tests := []struct {
		name  string
		input string
	}{
		{"plain russian greeting", "Привет! Как твои дела сегодня?"},
		{"short factual answer", "Два плюс два равно четыре."},
		{"artifact citation", "> 📄 **photo.jpg:** Описание загруженного файла.\n\nВот что я вижу на фото."},
		{"multi-paragraph explanation", "Начнём с контекста. Этот вопрос часто задают.\n\nПричин несколько: во-первых, физика; во-вторых, биология.\n\nИтог: всё просто."},
		{"russian with english quote", "Классическая фраза \"Thinking Process End.\" встречается в документации, но это не означает конец рассуждений."},
		{"code block in answer", "Вот пример кода:\n```go\nfunc main() { fmt.Println(\"hello\") }\n```\nПопробуй запустить."},
		{"very short response", "Да."},
		{"markdown header + list", "### Мой ответ\n\n1. Первый пункт\n2. Второй пункт\n3. Третий пункт"},
		{"response quoting done", "Когда ты видишь (Done) в логе, это значит что задача завершена."},
		{"mixed cyrillic and latin", "Фреймворк React — отличный выбор для UI. Компоненты переиспользуются."},
		{"russian with emoji", "Отлично получилось! 🎉 Можешь похвалить себя."},
		{"long russian paragraph", "Если внимательно изучить вопрос, становится ясно, что ответ лежит в самой природе физических процессов, которые происходят при нагревании. Поэтому важно учитывать температуру."},
		{"markdown bold at start", "**Важно:** не пропускай этот шаг при настройке."},
		{"empty string", ""},
		{"whitespace only", "   \n  \n "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.input, result, "content should pass through unchanged")
			assert.False(t, sanitized, "sanitizer should not fire on legitimate content")
		})
	}
}

func TestFindMatchingBrace(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		startIdx int
		expected int
	}{
		{"simple", `{"a": 1}`, 0, 7},
		{"nested", `{"a": {"b": 2}}`, 0, 14},
		{"with string", `{"a": "hello"}`, 0, 13},
		{"with escaped quote", `{"a": "say \"hi\""}`, 0, 18},
		{"deeply nested", `{"a": {"b": {"c": {"d": 1}}}}`, 0, 28},
		{"no closing brace", `{"a": 1`, 0, -1},
		{"invalid start", `hello`, 0, -1},
		{"start not at brace", `hello {world}`, 0, -1},
		{"correct start index", `hello {world}`, 6, 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindMatchingBrace(tt.text, tt.startIdx)
			assert.Equal(t, tt.expected, result)
		})
	}
}
