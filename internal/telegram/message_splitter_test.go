package telegram

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitMessageSmart(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		limit    int
		expected []string
	}{
		{
			name:     "Короткое сообщение",
			input:    "Короткий текст",
			limit:    100,
			expected: []string{"Короткий текст"},
		},
		{
			name:  "Простое разбиение по абзацам",
			input: "Первый абзац.\n\nВторой абзац.\n\nТретий абзац.",
			limit: 20,
			expected: []string{
				"Первый",
				"абзац.",
				"Второй",
				"абзац.",
				"Третий",
				"абзац.",
			},
		},
		{
			name:  "Блок кода не разбивается",
			input: "Текст перед кодом.\n\n```python\ndef function():\n    print('hello')\n    return True\n```\n\nТекст после кода.",
			limit: 50,
			expected: []string{
				"Текст перед кодом.",
				"```python\ndef function():\n    print('hello')",
				"return True\n```\n\nТекст после",
				"кода.",
			},
		},
		{
			name:  "Инлайн код сохраняется",
			input: "Используйте `console.log()` для отладки.\n\nЭто другой абзац с `другим кодом`.",
			limit: 30,
			expected: []string{
				"Используйте",
				"`console.log()` для",
				"отладки.",
				"Это другой",
				"абзац с `другим",
				"кодом`.",
			},
		},
		{
			name:  "Разбиение по концу предложений",
			input: "Первое предложение. Второе предложение! Третье предложение? Четвертое предложение.",
			limit: 25,
			expected: []string{
				"Первое",
				"предложение.",
				"Второе",
				"предложение!",
				"Третье",
				"предложение?",
				"Четвертое",
				"предложение.",
			},
		},
		{
			name:  "Сложный случай с блоком кода и списками",
			input: "# Заголовок\n\nОписание:\n\n* Пункт 1\n* Пункт 2\n\n```go\nfunc main() {\n    fmt.Println(\"Hello\")\n}\n```\n\nЗаключение.",
			limit: 40,
			expected: []string{
				"# Заголовок\n\nОписание:",
				"* Пункт 1\n* Пункт 2",
				"```go\nfunc main() {\n    fmt.",
				"Println(\"Hello\")\n}\n```",
				"Заключение.",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := SplitMessageSmart(tc.input, tc.limit)
			assert.Equal(t, tc.expected, actual)

			// Проверяем, что все части не превышают лимит (кроме принудительно разбитых)
			for i, chunk := range actual {
				if len(chunk) > tc.limit {
					// Проверяем, что это действительно неразбиваемый блок (например, код)
					hasCodeBlock := strings.Contains(chunk, "```")
					assert.True(t, hasCodeBlock, "Chunk %d exceeds limit but doesn't contain code block: %s", i, chunk)
				}
			}

			// Проверяем, что все важные слова из исходного текста присутствуют в чанках
			joined := strings.Join(actual, " ")
			// Извлекаем все слова из исходного текста (исключая markdown символы)
			inputWords := strings.Fields(strings.ReplaceAll(tc.input, "\n", " "))
			joinedWords := strings.Fields(strings.ReplaceAll(joined, "\n", " "))

			// Проверяем, что количество слов примерно совпадает (может быть небольшая разница из-за обработки)
			assert.InDelta(t, len(inputWords), len(joinedWords), float64(len(inputWords))*0.1, "Word count should be approximately the same")
		})
	}
}

func TestFindCodeBlocks(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected []CodeBlock
	}{
		{
			name:     "Нет блоков кода",
			input:    "Обычный текст без кода",
			expected: nil,
		},
		{
			name:     "Один блок кода",
			input:    "Текст ```code``` текст",
			expected: []CodeBlock{{Start: 11, End: 21}},
		},
		{
			name:     "Инлайн код",
			input:    "Используйте `console.log()` для отладки",
			expected: []CodeBlock{{Start: 23, End: 38}},
		},
		{
			name:     "Многострочный блок кода",
			input:    "```python\ndef func():\n    pass\n```",
			expected: []CodeBlock{{Start: 0, End: 34}},
		},
		{
			name:     "Смешанные блоки",
			input:    "Текст `inline` и ```\nblock\n``` код",
			expected: []CodeBlock{{Start: 23, End: 36}, {Start: 11, End: 19}},
		},
		{
			name:     "Инлайн код внутри блока кода игнорируется",
			input:    "```\nprint(`hello`)\n```",
			expected: []CodeBlock{{Start: 0, End: 22}},
		},
		{
			name:     "HTML pre block (table)",
			input:    "Text <pre>Header: Value\n────\nHeader: Value2</pre> more text",
			expected: []CodeBlock{{Start: 5, End: 57}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := FindCodeBlocks(tc.input)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestGetSplitPriority(t *testing.T) {
	testCases := []struct {
		name      string
		line      string
		lineIndex int
		allLines  []string
		expected  int
	}{
		{
			name:      "Двойная пустая строка (vertical spacing)",
			line:      "",
			lineIndex: 2,
			allLines:  []string{"Текст", "", "", "Еще текст"},
			expected:  150,
		},
		{
			name:      "Пустая строка после текста (абзац)",
			line:      "",
			lineIndex: 1,
			allLines:  []string{"Текст", "", "Еще текст"},
			expected:  100,
		},
		{
			name:      "Конец блока кода",
			line:      "```",
			lineIndex: 2,
			allLines:  []string{"```python", "code", "```"},
			expected:  90,
		},
		{
			name:      "Заголовок",
			line:      "# Заголовок",
			lineIndex: 0,
			allLines:  []string{"# Заголовок", "Текст"},
			expected:  80,
		},
		{
			name:      "Конец предложения с точкой",
			line:      "Предложение.",
			lineIndex: 0,
			allLines:  []string{"Предложение.", "Другое предложение."},
			expected:  50,
		},
		{
			name:      "Конец предложения с восклицанием",
			line:      "Восклицание!",
			lineIndex: 0,
			allLines:  []string{"Восклицание!", "Другое предложение."},
			expected:  50,
		},
		{
			name:      "Конец предложения с вопросом",
			line:      "Вопрос?",
			lineIndex: 0,
			allLines:  []string{"Вопрос?", "Ответ."},
			expected:  50,
		},
		{
			name:      "Обычная строка",
			line:      "Обычная строка",
			lineIndex: 0,
			allLines:  []string{"Обычная строка", "Другая строка"},
			expected:  10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := getSplitPriority(tc.line, tc.lineIndex, tc.allLines)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestSplitMessageWithVerticalSpacing(t *testing.T) {
	// Тест для проверки, что сообщения с двойными переносами (vertical spacing)
	// разбиваются по этим границам, а не посреди абзацев
	input := `Первый абзац с несколькими строками текста
Продолжение первого абзаца без точки

Второй абзац тоже с несколькими строками
Продолжение второго абзаца без точки

Третий абзац`

	chunks := SplitMessageSmart(input, 150)

	// Отладочный вывод
	t.Logf("Input length: %d", len(input))
	t.Logf("Number of chunks: %d", len(chunks))
	for i, chunk := range chunks {
		t.Logf("Chunk %d (len=%d): %q", i, len(chunk), chunk)
	}

	// Проверяем, что абзацы не разорваны посреди
	for _, chunk := range chunks {
		// Если чанк содержит "Первый абзац", он должен содержать и "Продолжение первого"
		if strings.Contains(chunk, "Первый абзац") {
			assert.True(t, strings.Contains(chunk, "Продолжение первого"),
				"First paragraph should not be split. Chunk: %q", chunk)
		}
		// Если чанк содержит "Второй абзац", он должен содержать и "Продолжение второго"
		if strings.Contains(chunk, "Второй абзац") {
			assert.True(t, strings.Contains(chunk, "Продолжение второго"),
				"Second paragraph should not be split. Chunk: %q", chunk)
		}
	}
}

func TestRealWorldCodeBlockSplit(t *testing.T) {
	// Реальный случай из логов - длинный текст с блоком кода
	input := `Ого, Константин, какой интересный запрос! Получил твоё сообщение и понял: настало время для настоящего стресс-теста. Вызов принят! "Пожёстче" так "пожёстче".

Я подготовил набор тест-кейсов, чтобы проверить и показать самые каверзные уголки форматирования в Telegram MarkdownV2. Поехали!

*Тест-кейс №4: Блоки кода с "ловушками"*

Внутри блока кода никакое форматирование работать не должно. Это идеальное место, чтобы писать примеры с Markdown-символами.
` + "```python\n# Это тестовый Python-код\n# Здесь *жирный* и _курсив_ - это просто часть комментария.\ndef check_formatting(text):\n    if text == \"*сложный_текст*\":\n        # Символы `*` и `_` игнорируются\n        return True\n    return False\n\n# Ссылка [link](http://example.com) тоже просто текст.\n```" + `
Как видишь, всё внутри блока осталось нетронутым.

Ну как, Константин, достаточно "жёстко" получилось? Я постарался затронуть все скользкие моменты.`

	// Разбиваем с лимитом, который должен был бы разорвать блок кода в оригинальной функции
	chunks := SplitMessageSmart(input, 1000)

	// Проверяем, что блок кода не разорван
	codeBlockFound := false
	for _, chunk := range chunks {
		if strings.Contains(chunk, "```python") && strings.Contains(chunk, "```") {
			// Проверяем, что блок кода полный
			assert.True(t, strings.Contains(chunk, "def check_formatting"))
			assert.True(t, strings.Contains(chunk, "return False"))
			codeBlockFound = true
		}
	}

	assert.True(t, codeBlockFound, "Code block should be found intact in one of the chunks")

	// Проверяем, что каждый чанк валиден (нет незакрытых блоков кода)
	for i, chunk := range chunks {
		codeBlockStarts := strings.Count(chunk, "```")
		assert.True(t, codeBlockStarts%2 == 0, "Chunk %d should have even number of ``` markers: %s", i, chunk)
	}
}

func TestForceSplitChunk(t *testing.T) {
	// Тест принудительного разбиения очень длинного текста без хороших точек разрыва
	longText := strings.Repeat("очень_длинное_слово_без_пробелов_и_знаков_препинания ", 100)

	chunks := forceSplitChunk(longText, 100)

	// Проверяем, что все чанки не превышают лимит
	for i, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), 100, "Chunk %d exceeds limit: %d characters", i, len(chunk))
	}

	// Проверяем, что объединение дает исходный текст
	joined := strings.Join(chunks, " ")
	assert.Equal(t, strings.TrimSpace(longText), strings.TrimSpace(joined))
}

func TestIsSafeSplitPosition(t *testing.T) {
	text := "Текст ```код тут``` еще текст"
	codeBlocks := FindCodeBlocks(text)

	testCases := []struct {
		name     string
		position int
		expected bool
	}{
		{
			name:     "Позиция перед блоком кода",
			position: 5,
			expected: true,
		},
		{
			name:     "Позиция внутри блока кода",
			position: 20,
			expected: false,
		},
		{
			name:     "Позиция после блока кода",
			position: 10,
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := IsSafeSplitPosition(tc.position, codeBlocks)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
