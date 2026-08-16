package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToRichHTMLNativeStructures(t *testing.T) {
	input := `# Heading 1

Paragraph with **bold**, *italic*, ~~strike~~, ||spoiler|| and ` + "`code`" + `.

- [ ] todo
- [x] done
    1. nested

3. third
4. fourth

> quoted

---

` + "```go\nfmt.Println(\"<ok>\")\n```"

	want := `<h1>Heading 1</h1>` +
		`<p>Paragraph with <strong>bold</strong>, <em>italic</em>, <s>strike</s>, <tg-spoiler>spoiler</tg-spoiler> and <code>code</code>.</p>` +
		`<ul><li><input type="checkbox">todo</li><li><input type="checkbox" checked>done<ol><li>nested</li></ol></li></ul>` +
		`<ol start="3"><li>third</li><li>fourth</li></ol>` +
		`<blockquote><p>quoted</p></blockquote>` +
		`<hr/>` +
		`<pre><code class="language-go">fmt.Println(&#34;&lt;ok&gt;&#34;)
</code></pre>`

	got, stats, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Greater(t, stats.Characters, 0)
	assert.GreaterOrEqual(t, stats.Blocks, 10)
	assert.GreaterOrEqual(t, stats.MaxDepth, 3)
}

func TestToRichHTMLHeadings(t *testing.T) {
	input := "# one\n\n## two\n\n### three\n\n#### four\n\n##### five\n\n###### six"
	want := "<h1>one</h1><h2>two</h2><h3>three</h3><h4>four</h4><h5>five</h5><h6>six</h6>"

	got, stats, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 6, stats.Blocks)
	assert.Equal(t, 1, stats.MaxDepth)
}

func TestToRichHTMLNativeTable(t *testing.T) {
	input := `| Left | Center | Right |
|:-----|:------:|------:|
| a | **b** | c |`
	want := `<table><tr><th align="left">Left</th><th align="center">Center</th><th align="right">Right</th></tr>` +
		`<tr><td align="left">a</td><td align="center"><strong>b</strong></td><td align="right">c</td></tr></table>`

	got, stats, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 3, stats.Blocks) // table + header row + body row
	assert.Equal(t, 3, stats.MaxTableColumns)
	assert.Equal(t, 4, stats.MaxDepth) // table/tr/cell/strong
}

func TestToRichHTMLSafeLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https",
			in:   `[safe](https://example.com/a?x=1&y=2)`,
			want: `<p><a href="https://example.com/a?x=1&amp;y=2">safe</a></p>`,
		},
		{
			name: "mailto tel anchor and model-authored user mention",
			in:   `[mail](mailto:a@example.com) [phone](tel:+123-45) [part](#section-1) [user](tg://user?id=123)`,
			want: `<p><a href="mailto:a@example.com">mail</a> <a href="tel:+123-45">phone</a> <a href="#section-1">part</a> user</p>`,
		},
		{
			name: "unsafe schemes lose wrapper",
			in:   `[js](javascript:alert) [data](data:text/plain,x) [photo](tg://photo?id=x) [relative](/local)`,
			want: `<p>js data photo relative</p>`,
		},
		{
			name: "math-like URL is not rewritten",
			in:   `[price](https://example.com/$5$)`,
			want: `<p><a href="https://example.com/$5$">price</a></p>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ToRichHTML(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToRichHTMLPreviewSuppressesAllLinksAndAnchors(t *testing.T) {
	input := `[web **label**](https://example.com/a?x=1&y=2) ` +
		`[mail](mailto:a@example.com) [phone](tel:+123-45) ` +
		`[anchor](#section-1) <https://auto.example/path?q=1> ` +
		`<preview@example.com> https://bare.example/path`
	want := `<p>web <strong>label</strong> mail phone anchor https://auto.example/path?q=1 ` +
		`preview@example.com https://bare.example/path</p>`

	got, stats, err := ToRichHTMLPreview(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NotContains(t, strings.ToLower(got), "<a")
	assert.Equal(t, 1, stats.Blocks)
	assert.Equal(t, 2, stats.MaxDepth) // paragraph + preserved strong label
}

func TestToRichHTMLPreviewRetainsRichSafetyAndFormatting(t *testing.T) {
	input := `before <a href="https://evil.example">raw</a> ` +
		`![cat **photo**](https://evil.example/cat.jpg) ` +
		`<img src="https://evil.example/injected.jpg"> ` +
		`[tg](tg://user?id=123) [js](javascript:alert) ` +
		`$x_i < y$ ` + "`[code](https://code.example) <b>`" + ` after`
	want := `<p>before raw cat <strong>photo</strong>  tg js ` +
		`<tg-math>x_i &lt; y</tg-math> ` +
		`<code>[code](https://code.example) &lt;b&gt;</code> after</p>`

	got, _, err := ToRichHTMLPreview(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	lower := strings.ToLower(got)
	assert.NotContains(t, lower, "<a")
	assert.NotContains(t, lower, "<img")
	assert.NotContains(t, lower, "tg://")
	assert.NotContains(t, lower, "javascript:")
}

func TestToRichHTMLPreviewKeepsCodeAndDisplayMathSemantics(t *testing.T) {
	input := "$$\n\\frac{x_1}{2} & y < z\n$$\n\n" +
		"```html\n<a href=\"https://example.com\">code</a>\n```"
	want := `<tg-math-block>\frac{x_1}{2} &amp; y &lt; z</tg-math-block>` +
		"<pre><code class=\"language-html\">&lt;a href=&#34;https://example.com&#34;&gt;code&lt;/a&gt;\n</code></pre>"

	got, stats, err := ToRichHTMLPreview(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 2, stats.Blocks)
	assert.NotContains(t, got, "<a ")
}

func TestToRichHTMLBoundsLinkDestination(t *testing.T) {
	destination := "https://example.com/" + strings.Repeat("a", 4096)
	got, _, err := ToRichHTML("[visible](" + destination + ")")
	require.NoError(t, err)
	assert.Equal(t, "<p>visible</p>", got)
}

func TestToRichHTMLNeverCreatesMediaFromModelMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "markdown image keeps only alt text",
			in:   `![cat **photo**](https://evil.example/cat.jpg "title")`,
			want: `<p>cat <strong>photo</strong></p>`,
		},
		{
			name: "inline raw media tag omitted",
			in:   `before <img src="https://evil.example/cat.jpg"> after`,
			want: `<p>before  after</p>`,
		},
		{
			name: "inline raw media wrapper omitted but text retained",
			in:   "<video src=\"https://evil.example/v.mp4\">fallback</video>\n",
			want: "<p>fallback</p>",
		},
		{
			name: "raw HTML block omitted",
			in:   "<script>\nalert('nope')\n</script>\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ToRichHTML(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			lower := strings.ToLower(got)
			assert.NotContains(t, lower, "<img")
			assert.NotContains(t, lower, "<video")
			assert.NotContains(t, lower, "<audio")
			assert.NotContains(t, lower, "<figure")
			assert.NotContains(t, lower, "tg://photo")
		})
	}
}

func TestToRichHTMLMath(t *testing.T) {
	input := "Inline $x_i < y & \\\"z\\\"$; prices $100 or $200.\n\n" +
		"$$\nE = mc^2 & x < y\n$$\n\n" +
		`\[\frac{1}{2}\]` + "\n\n" +
		"`$code$`\n\n```math\n$also_code$\n```"
	want := `<p>Inline <tg-math>x_i &lt; y &amp; \&#34;z\&#34;</tg-math>; prices $100 or $200.</p>` +
		`<tg-math-block>E = mc^2 &amp; x &lt; y</tg-math-block>` +
		`<tg-math-block>\frac{1}{2}</tg-math-block>` +
		`<p><code>$code$</code></p>` +
		"<pre><code class=\"language-math\">$also_code$\n</code></pre>"

	got, stats, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 5, stats.Blocks) // two paragraphs, two math blocks, one pre
	assert.NotContains(t, got, "xᵢ") // native formula keeps LaTeX source
}

func TestToRichHTMLCurrencyIsNotMath(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "exact Russian regression",
			input: "цена $100, диапазон $5–$10, USD $20.",
		},
		{
			name:  "production Russian prose regression",
			input: "базовая цена $100 за передачу телеметрии, диапазон $5–$10 за киловатт-час и USD $20 за грамм",
		},
		{
			name:  "English prose regression",
			input: "base price $100 per transfer, range $5-$10 per kWh and USD $20 per gram",
		},
		{
			name:  "non-breaking space before prose",
			input: "цена $100\u00a0за пакет",
		},
		{
			name:  "space after currency symbol",
			input: "цена $ 100 за рейс, затем $ 20 за грамм",
		},
		{
			name:  "currency symbol after amount",
			input: "цена 100 $ за рейс и 20 $ за грамм",
		},
		{
			name:  "ASCII currency range",
			input: "Prices $5-$10, USD $20, EUR $18.",
		},
		{
			name:  "spaced en dash currency range",
			input: "Prices $5 – $10; USD $20; EUR $18.",
		},
		{
			name:  "multiple currencies",
			input: "USD $20, EUR $18, GBP $16.",
		},
		{
			name:  "word-delimited currency range",
			input: "Prices range from $5 to $10 per kg.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ToRichHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, "<p>"+tt.input+"</p>", got)
			assert.NotContains(t, got, "<tg-math>")
		})
	}
}

func TestToRichHTMLCurrencyGuardPreservesMathCodeAndURL(t *testing.T) {
	input := `Math $5$, $1737,1$, $5-10$, $5 + x$, $5 \times x$, $5 x$, $5, x$, $5. x$, $x-y$; code ` + "`$5–$10`" + `; [price](https://example.com/$5$).`
	want := `<p>Math <tg-math>5</tg-math>, <tg-math>1737,1</tg-math>, <tg-math>5-10</tg-math>, <tg-math>5 + x</tg-math>, <tg-math>5 \times x</tg-math>, <tg-math>5 x</tg-math>, <tg-math>5, x</tg-math>, <tg-math>5. x</tg-math>, <tg-math>x-y</tg-math>; code <code>$5–$10</code>; <a href="https://example.com/$5$">price</a>.</p>`

	got, _, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestToRichHTMLCurrencyProseBeforeFormula(t *testing.T) {
	input := "цена $100 за пакет; формула $x^2$ верна"
	want := `<p>цена $100 за пакет; формула <tg-math>x^2</tg-math> верна</p>`

	got, _, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	preview, _, err := ToRichHTMLPreview(input)
	require.NoError(t, err)
	assert.Equal(t, want, preview)
}

func TestToRichHTMLCurrencyBeforeInlineCodeAndFormula(t *testing.T) {
	input := "цена $100 за пакет; код `$x_i$`; формула $y^2$ верна"
	want := `<p>цена $100 за пакет; код <code>$x_i$</code>; формула <tg-math>y^2</tg-math> верна</p>`

	got, _, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestToRichHTMLClassicEntityRoundTripRegression(t *testing.T) {
	t.Parallel()

	input := "Формула $x^2+y^2=r^2$; цена $100 за передачу, диапазон $5–$10 и USD $20; " +
		"emoji 🙂; код ` $x_i$ `; числовая формула $5 x$; ссылка [пример](https://example.com)."
	want := `<p>Формула <tg-math>x^2+y^2=r^2</tg-math>; цена $100 за передачу, диапазон $5–$10 и USD $20; ` +
		`emoji 🙂; код <code>$x_i$</code>; числовая формула <tg-math>5 x</tg-math>; ` +
		`ссылка <a href="https://example.com">пример</a>.</p>`

	got, _, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestToRichHTMLStyledLatexBackslashRoundTrip(t *testing.T) {
	t.Parallel()

	got, _, err := ToRichHTML(`**Формула $\frac{x^2}{y}$**`)
	require.NoError(t, err)
	assert.Equal(t, `<p><strong>Формула <tg-math>\frac{x^2}{y}</tg-math></strong></p>`, got)
}

func TestToRichHTMLMathInNestedBlock(t *testing.T) {
	input := "> $$\n> x < y & z\n> $$"

	got, stats, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, `<blockquote><tg-math-block>x &lt; y &amp; z</tg-math-block></blockquote>`, got)
	assert.Equal(t, 2, stats.Blocks) // blockquote + formula block
}

func TestToRichHTMLFormulaCannotInjectTags(t *testing.T) {
	input := `$</tg-math><img src="https://evil.example/x"> & value$`

	got, _, err := ToRichHTML(input)
	require.NoError(t, err)
	assert.Equal(t, `<p><tg-math>&lt;/tg-math&gt;&lt;img src=&#34;https://evil.example/x&#34;&gt; &amp; value</tg-math></p>`, got)
	assert.NotContains(t, got, "<img")
}

func TestToRichHTMLStats(t *testing.T) {
	got, stats, err := ToRichHTML("Hello 👋 $x<y$")
	require.NoError(t, err)
	assert.Equal(t, `<p>Hello 👋 <tg-math>x&lt;y</tg-math></p>`, got)
	assert.Equal(t, RichStats{
		Characters: 11,
		Blocks:     1,
		MaxDepth:   2,
	}, stats)
}

func TestToRichHTMLRejectsInvalidUTF8(t *testing.T) {
	got, stats, err := ToRichHTML(string([]byte{'o', 'k', 0xff}))
	require.Error(t, err)
	assert.Empty(t, got)
	assert.Equal(t, RichStats{}, stats)
}
