package markdown

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRichListBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ordered list starting at three after bold label",
			input: "**Нумерованный список (начинается с 3):**\n3. Подготовка\n4. Выполнение",
			want:  "**Нумерованный список (начинается с 3):**\n\n3. Подготовка\n4. Выполнение",
		},
		{
			name:  "already separated input is unchanged",
			input: "Этапы:\n\n3. Подготовка\n4. Выполнение",
			want:  "Этапы:\n\n3. Подготовка\n4. Выполнение",
		},
		{
			name:  "list starting at one needs no repair",
			input: "Этапы:\n1. Подготовка\n2. Выполнение",
			want:  "Этапы:\n1. Подготовка\n2. Выполнение",
		},
		{
			name:  "single numbered prose line is unchanged",
			input: "Количество окон:\n14. Количество дверей — 6.",
			want:  "Количество окон:\n14. Количество дверей — 6.",
		},
		{
			name:  "years are unchanged",
			input: "Хронология:\n2024. Первый релиз\n2025. Поддержка",
			want:  "Хронология:\n2024. Первый релиз\n2025. Поддержка",
		},
		{
			name:  "semantic versions are unchanged",
			input: "Версии:\n3.10 Python\n4.0 Python",
			want:  "Версии:\n3.10 Python\n4.0 Python",
		},
		{
			name:  "non sequential markers are unchanged",
			input: "Этапы:\n3. Подготовка\n5. Выполнение",
			want:  "Этапы:\n3. Подготовка\n5. Выполнение",
		},
		{
			name:  "prose without colon is unchanged",
			input: "Последующие шаги\n3. Подготовка\n4. Выполнение",
			want:  "Последующие шаги\n3. Подготовка\n4. Выполнение",
		},
		{
			name:  "blockquote label is not root level",
			input: "> Этапы:\n3. Подготовка\n4. Выполнение",
			want:  "> Этапы:\n3. Подготовка\n4. Выполнение",
		},
		{
			name:  "list item label is not root level",
			input: "- Этапы:\n3. Подготовка\n4. Выполнение",
			want:  "- Этапы:\n3. Подготовка\n4. Выполнение",
		},
		{
			name:  "backtick fenced code is unchanged",
			input: "```md\nЭтапы:\n3. Подготовка\n4. Выполнение\n```",
			want:  "```md\nЭтапы:\n3. Подготовка\n4. Выполнение\n```",
		},
		{
			name:  "tilde fenced code is unchanged",
			input: "~~~md\nЭтапы:\n3. Подготовка\n4. Выполнение\n~~~",
			want:  "~~~md\nЭтапы:\n3. Подготовка\n4. Выполнение\n~~~",
		},
		{
			name:  "normalization resumes after closing fence",
			input: "```md\nВ коде:\n3. Без изменения\n4. Без изменения\n```\nСнаружи:\n3. Подготовка\n4. Выполнение",
			want:  "```md\nВ коде:\n3. Без изменения\n4. Без изменения\n```\nСнаружи:\n\n3. Подготовка\n4. Выполнение",
		},
		{
			name:  "upper ordinary bound may continue to one hundred",
			input: "Пункты:\n99. Предпоследний\n100. Последний",
			want:  "Пункты:\n\n99. Предпоследний\n100. Последний",
		},
		{
			name:  "start above ordinary bound is unchanged",
			input: "Пункты:\n100. Первый\n101. Второй",
			want:  "Пункты:\n100. Первый\n101. Второй",
		},
		{
			name:  "crlf is preserved",
			input: "Этапы:\r\n3. Подготовка\r\n4. Выполнение",
			want:  "Этапы:\r\n\r\n3. Подготовка\r\n4. Выполнение",
		},
		{
			name:  "mixed newlines remain mixed",
			input: "Этапы:\r\n3. Подготовка\n4. Выполнение",
			want:  "Этапы:\r\n\n3. Подготовка\n4. Выполнение",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRichListBoundaries(tt.input)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, normalizeRichListBoundaries(got), "normalization must be idempotent")
		})
	}
}

func TestToRichHTMLRepairsOrderedListBoundary(t *testing.T) {
	input := "**Нумерованный список (начинается с 3):**\n3. Подготовка\n4. Выполнение"

	got, _, err := ToRichHTML(input)

	require.NoError(t, err)
	assert.Equal(t,
		"<p><strong>Нумерованный список (начинается с 3):</strong></p>"+
			`<ol start="3"><li>Подготовка</li><li>Выполнение</li></ol>`,
		got,
	)
	assert.Equal(t, "**Нумерованный список (начинается с 3):**\n3. Подготовка\n4. Выполнение", input)
}
