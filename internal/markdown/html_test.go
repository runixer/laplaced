package markdown

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Plain text",
			input:    "Hello world",
			expected: "Hello world",
		},
		{
			name:     "Bold",
			input:    "**bold**",
			expected: "<b>bold</b>",
		},
		{
			name:     "Italic",
			input:    "*italic*",
			expected: "<i>italic</i>",
		},
		{
			name:     "Strikethrough",
			input:    "~~strike~~",
			expected: "<s>strike</s>",
		},
		{
			name:     "Spoiler",
			input:    "||secret||",
			expected: "<tg-spoiler>secret</tg-spoiler>",
		},
		{
			name:     "Inline code",
			input:    "`code`",
			expected: "<code>code</code>",
		},
		{
			name:     "Code block without language",
			input:    "```\nfmt.Println()\n```",
			expected: "<pre><code>fmt.Println()\n</code></pre>",
		},
		{
			name:     "Code block with language",
			input:    "```go\nfmt.Println()\n```",
			expected: "<pre><code class=\"language-go\">fmt.Println()\n</code></pre>",
		},
		{
			name:     "Link",
			input:    "[text](http://example.com)",
			expected: "<a href=\"http://example.com\">text</a>",
		},
		{
			name:     "Blockquote",
			input:    "> quote",
			expected: "<blockquote>quote</blockquote>",
		},
		{
			name:     "Unordered list",
			input:    "- item1\n- item2",
			expected: "• item1\n• item2",
		},
		{
			name:     "Ordered list",
			input:    "1. first\n2. second",
			expected: "1. first\n2. second",
		},
		{
			name:     "Nested bold italic",
			input:    "**bold *and italic***",
			expected: "<b>bold <i>and italic</i></b>",
		},
		{
			name:     "Special chars",
			input:    "1 < 2 & 3 > 0",
			expected: "1 &lt; 2 &amp; 3 &gt; 0",
		},
		{
			name:     "Special chars in code",
			input:    "`<script>`",
			expected: "<code>&lt;script&gt;</code>",
		},
		{
			name:     "Empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "Unclosed bold",
			input:    "**unclosed",
			expected: "**unclosed",
		},
		{
			name:     "Heading as bold",
			input:    "# Heading",
			expected: "<b>Heading</b>",
		},
		{
			name:     "Multiple paragraphs",
			input:    "First paragraph\n\nSecond paragraph",
			expected: "First paragraph\n\nSecond paragraph",
		},
		{
			name:     "Bold and italic combined",
			input:    "***bold and italic***",
			expected: "<i><b>bold and italic</b></i>",
		},
		{
			name:     "Link with special chars in text",
			input:    "[text with <>&](http://example.com)",
			expected: "<a href=\"http://example.com\">text with &lt;&gt;&amp;</a>",
		},
		{
			name:     "Code block with special chars",
			input:    "```\n<html>\n  <body>&</body>\n</html>\n```",
			expected: "<pre><code>&lt;html&gt;\n  &lt;body&gt;&amp;&lt;/body&gt;\n&lt;/html&gt;\n</code></pre>",
		},
		{
			name:     "Task list - unchecked",
			input:    "- [ ] Task 1",
			expected: "• ☐ Task 1",
		},
		{
			name:     "Task list - checked",
			input:    "- [x] Task 2",
			expected: "• ☑ Task 2",
		},
		{
			name:     "Task list - mixed",
			input:    "- [ ] Todo\n- [x] Done",
			expected: "• ☐ Todo\n• ☑ Done",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUTF16Length(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{
			name:     "ASCII text",
			input:    "Hello",
			expected: 5,
		},
		{
			name:     "Cyrillic text",
			input:    "Привет",
			expected: 6,
		},
		{
			name:     "Emoji (surrogate pair)",
			input:    "👋",
			expected: 2,
		},
		{
			name:     "Mixed text with emoji",
			input:    "Hello 👋",
			expected: 8, // 6 ASCII + 2 for emoji
		},
		{
			name:     "Multiple emojis",
			input:    "🌱💻",
			expected: 4, // 2 + 2
		},
		{
			name:     "Empty string",
			input:    "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UTF16Length(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestToHTML_ComplexMarkdown(t *testing.T) {
	input := `# Заголовок

Это **жирный текст** и *курсив*.

Вот список:
- Первый пункт
- Второй пункт

И код:
` + "```go\nfunc main() {\n\tfmt.Println(\"Hello\")\n}\n```" + `

> Цитата с **жирным** текстом

Ссылка: [GitHub](https://github.com)

Спойлер: ||секрет||`

	expected := `<b>Заголовок</b>

Это <b>жирный текст</b> и <i>курсив</i>.

Вот список:

• Первый пункт
• Второй пункт

И код:

<pre><code class="language-go">func main() {
	fmt.Println(&#34;Hello&#34;)
}
</code></pre>

<blockquote>Цитата с <b>жирным</b> текстом</blockquote>

Ссылка: <a href="https://github.com">GitHub</a>

Спойлер: <tg-spoiler>секрет</tg-spoiler>`

	result, err := ToHTML(input)
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}

func TestSpacingBetweenBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Paragraph spacing",
			input:    "First paragraph.\n\nSecond paragraph.",
			expected: "First paragraph.\n\nSecond paragraph.",
		},
		{
			name:     "List spacing - before and after",
			input:    "Text before.\n\n- Item 1\n- Item 2\n\nText after.",
			expected: "Text before.\n\n• Item 1\n• Item 2\n\nText after.",
		},
		{
			name:     "Code block spacing",
			input:    "Text before.\n\n```go\ncode\n```\n\nText after.",
			expected: "Text before.\n\n<pre><code class=\"language-go\">code\n</code></pre>\n\nText after.",
		},
		{
			name:     "Heading spacing",
			input:    "# Heading\n\nParagraph.",
			expected: "<b>Heading</b>\n\nParagraph.",
		},
		{
			name:     "Blockquote spacing",
			input:    "Text before.\n\n> Quote\n\nText after.",
			expected: "Text before.\n\n<blockquote>Quote</blockquote>\n\nText after.",
		},
		{
			name:     "Multiple headings",
			input:    "# First\n\n## Second\n\nText.",
			expected: "<b>First</b>\n\n<b>Second</b>\n\nText.",
		},
		{
			name:     "List after heading",
			input:    "# Title\n\n- Item 1\n- Item 2",
			expected: "<b>Title</b>\n\n• Item 1\n• Item 2",
		},
		{
			name:     "Code after list",
			input:    "- Item 1\n- Item 2\n\n```\ncode\n```",
			expected: "• Item 1\n• Item 2\n\n<pre><code>code\n</code></pre>",
		},
		{
			name:     "Three paragraphs",
			input:    "First.\n\nSecond.\n\nThird.",
			expected: "First.\n\nSecond.\n\nThird.",
		},
		{
			name:     "Ordered list spacing",
			input:    "Text.\n\n1. First\n2. Second\n\nMore text.",
			expected: "Text.\n\n1. First\n2. Second\n\nMore text.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result, "Spacing mismatch")
		})
	}
}

func TestTableRendering(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "Simple table",
			input: `| Header 1 | Header 2 |
|----------|----------|
| Cell 1   | Cell 2   |
| Cell 3   | Cell 4   |`,
			expected: `<pre>Header 1: Cell 1
Header 2: Cell 2
──────────
Header 1: Cell 3
Header 2: Cell 4</pre>`,
		},
		{
			name: "Table with alignment",
			input: `| Left | Center | Right |
|:-----|:------:|------:|
| A    | B      | C     |
| AA   | BB     | CC    |`,
			expected: `<pre>Left  : A
Center: B
Right : C
────────
Left  : AA
Center: BB
Right : CC</pre>`,
		},
		{
			name: "Table with code",
			input: `| Function | Description |
|----------|-------------|
| ` + "`fmt.Println()`" + ` | Prints text |`,
			expected: `<pre>Function   : fmt.Println()
Description: Prints text</pre>`,
		},
		{
			name: "Table with bold",
			input: `| Name | Status |
|------|--------|
| **Bold** | Normal |`,
			expected: `<pre>Name  : Bold
Status: Normal</pre>`,
		},
		{
			name: "Table before and after text",
			input: `Text before.

| Col1 | Col2 |
|------|------|
| A    | B    |

Text after.`,
			expected: `Text before.

<pre>Col1: A
Col2: B</pre>
Text after.`,
		},
		{
			name: "Single column table",
			input: `| Header |
|--------|
| Value  |`,
			expected: `<pre>Header: Value</pre>`,
		},
		{
			name: "Table with varying widths",
			input: `| Short | Very Long Header |
|-------|------------------|
| X     | Y                |`,
			expected: `<pre>Short           : X
Very Long Header: Y</pre>`,
		},
		{
			name: "Wide table (vertical format)",
			input: `| Service | Architecture | Resource Usage | Status | Memory |
|---------|--------------|----------------|--------|--------|
| Laplace | Go / SQLite  | ~35MB RAM      | Stable | 32 MB  |
| Infinity | Python / API | ~4GB VRAM      | Dev    | 4 GB   |`,
			expected: `<pre>Service       : Laplace
Architecture  : Go / SQLite
Resource Usage: ~35MB RAM
Status        : Stable
Memory        : 32 MB
────────────────
Service       : Infinity
Architecture  : Python / API
Resource Usage: ~4GB VRAM
Status        : Dev
Memory        : 4 GB</pre>`,
		},
		{
			name: "Table with HTML entities",
			input: `| Command | Risk |
|---------|------|
| <100k records | Low |
| a > b & c < d | High |`,
			expected: `<pre>Command: &lt;100k records
Risk   : Low
─────────
Command: a &gt; b &amp; c &lt; d
Risk   : High</pre>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result, "Table rendering mismatch")
		})
	}
}

func TestAdditionalMarkdownElements(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Autolink URL",
			input:    "<https://example.com>",
			expected: "<a href=\"https://example.com\">https://example.com</a>",
		},
		{
			name:     "Autolink email",
			input:    "<user@example.com>",
			expected: "<a href=\"user@example.com\">user@example.com</a>",
		},
		{
			name:     "Indented code block",
			input:    "Normal text\n\n    code line 1\n    code line 2",
			expected: "Normal text\n\n<pre><code>code line 1\ncode line 2\n</code></pre>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestThematicBreak(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Three dashes",
			input:    "Text before\n---\nText after",
			expected: "<b>Text before</b>\n\nText after", // --- is treated as heading
		},
		{
			name:     "Three asterisks",
			input:    "Text before\n***\nText after",
			expected: "Text before\n\n\nText after", // thematic break adds newlines
		},
		{
			name:     "Three underscores",
			input:    "Text before\n___\nText after",
			expected: "Text before\n\n\nText after",
		},
		{
			name:     "Multiple thematic breaks",
			input:    "First\n---\nMiddle\n---\nLast",
			expected: "<b>First</b>\n\n<b>Middle</b>\n\nLast",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestImageRendering(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Basic image",
			input:    "![alt text](http://example.com/image.png)",
			expected: "", // Images are skipped in Telegram
		},
		{
			name:     "Image with title",
			input:    "![alt](url \"title\")",
			expected: "", // Images are skipped
		},
		{
			name:     "Text before and after image",
			input:    "Before ![img](url) after",
			expected: "Before  after",
		},
		{
			name:     "Image with markdown in alt",
			input:    "![**bold** alt](url)",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRawHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Inline HTML tag",
			input:    "Text with <span>HTML</span> inside",
			expected: "Text with HTML inside", // Raw HTML is skipped but text content remains
		},
		{
			name:     "Multiple inline tags",
			input:    "<b>bold</b> and <i>italic</i>",
			expected: "bold and italic", // Tags removed, text preserved
		},
		{
			name:     "HTML block",
			input:    "Text before\n<div>block</div>\nText after",
			expected: "Text before", // Block-level HTML is skipped entirely
		},
		{
			name:     "Self-closing tag",
			input:    "Text <br/> more",
			expected: "Text  more",
		},
		{
			name:     "HTML comment",
			input:    "Text <!-- comment --> more",
			expected: "Text  more",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTMLBlock(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Div block",
			input:    "Before\n\n<div>\n  content\n</div>\n\nAfter",
			expected: "Before\n\nAfter", // HTML blocks are skipped
		},
		{
			name:     "Script tag (security skip)",
			input:    "Before\n<script>alert(1)</script>\nAfter",
			expected: "Before\n\nAfter",
		},
		{
			name:     "Style block",
			input:    "Text\n<style>body { color: red; }</style>\nMore",
			expected: "Text\n\nMore",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Only spaces",
			input:    "   ",
			expected: "", // Trailing spaces trimmed
		},
		{
			name:     "Newlines only",
			input:    "\n\n\n",
			expected: "",
		},
		{
			name:     "Mixed newlines and spaces",
			input:    "Text \n \n More",
			expected: "Text\n\nMore", // Trailing whitespace trimmed per line
		},
		{
			name:     "Link with underscore",
			input:    "[text](http://example.com/path_with_underscore)",
			expected: "<a href=\"http://example.com/path_with_underscore\">text</a>",
		},
		{
			name:     "Multiple consecutive links",
			input:    "[a](url1) [b](url2) [c](url3)",
			expected: "<a href=\"url1\">a</a> <a href=\"url2\">b</a> <a href=\"url3\">c</a>",
		},
		{
			name:     "Nested emphasis inside link",
			input:    "[**bold** link](url)",
			expected: "<a href=\"url\"><b>bold</b> link</a>",
		},
		{
			name:     "Code in link text",
			input:    "[`code` link](url)",
			expected: "<a href=\"url\"><code>code</code> link</a>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNestedListFormatting(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "List item with bold",
			input:    "- **Bold** item",
			expected: "• <b>Bold</b> item",
		},
		{
			name:     "List item with code",
			input:    "- Item with `code`",
			expected: "• Item with <code>code</code>",
		},
		{
			name:     "List item with link",
			input:    "- [link](url) item",
			expected: "• <a href=\"url\">link</a> item",
		},
		{
			name:     "Ordered list with formatting",
			input:    "1. **Bold** and *italic*",
			expected: "1. <b>Bold</b> and <i>italic</i>",
		},
		{
			name:     "List with spoiler",
			input:    "- Item with ||spoiler||",
			expected: "• Item with <tg-spoiler>spoiler</tg-spoiler>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBlockquoteVariations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Multi-line quote",
			input:    "> Line 1\n> Line 2",
			expected: "<blockquote>Line 1\nLine 2</blockquote>",
		},
		{
			name:     "Quote with nested code",
			input:    "> Quote with `code`",
			expected: "<blockquote>Quote with <code>code</code></blockquote>",
		},
		{
			name:     "Quote with bold",
			input:    "> **Important** quote",
			expected: "<blockquote><b>Important</b> quote</blockquote>",
		},
		{
			name:     "Multiple consecutive quotes",
			input:    "> First quote\n\n> Second quote",
			expected: "<blockquote>First quote</blockquote>\n\n<blockquote>Second quote</blockquote>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
