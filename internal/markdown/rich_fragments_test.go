package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRichFragmentsTopLevelAtomicStructures(t *testing.T) {
	input := "# Heading\n\n" +
		"Paragraph with **bold** and $x_i+y_i$.\n\n" +
		"- first\n  - nested\n- second\n\n" +
		"| Left | Right |\n|:-----|------:|\n| a | b |\n\n" +
		"```go\nfmt.Println(\"<ok>\")\n```\n\n" +
		"$$\n\\int_0^1 x^2 dx\n$$\n\n" +
		"> quoted\n> across two lines\n"

	document, err := ParseRichFragments(input)
	require.NoError(t, err)
	require.Len(t, document.Fragments, 7)

	wantKinds := []RichFragmentKind{
		RichFragmentHeading,
		RichFragmentParagraph,
		RichFragmentList,
		RichFragmentTable,
		RichFragmentCode,
		RichFragmentMath,
		RichFragmentBlockquote,
	}
	for i, fragment := range document.Fragments {
		assert.Equal(t, wantKinds[i], fragment.Kind, "fragment %d", i)
		assert.True(t, fragment.Atomic, "fragment %d", i)
		assert.Equal(t, input[fragment.SourceStart:fragment.SourceEnd], fragment.LegacySource, "fragment %d", i)
		rerendered, rerenderedStats, err := ToRichHTML(fragment.LegacySource)
		require.NoError(t, err, "fragment %d", i)
		assert.Equal(t, fragment.HTML, rerendered, "fragment %d", i)
		assert.Equal(t, fragment.Stats, rerenderedStats, "fragment %d", i)
		if i > 0 {
			assert.Equal(t, document.Fragments[i-1].SourceEnd, fragment.SourceStart, "fragment %d", i)
		}
	}

	assert.Equal(t, input, document.LegacySource)
	assert.Equal(t, input, concatenateLegacySources(document.Fragments))
	assert.Equal(t, "<h1>Heading</h1>", document.Fragments[0].HTML)
	assert.Equal(t, `<ul><li>first<ul><li>nested</li></ul></li><li>second</li></ul>`, document.Fragments[2].HTML)
	assert.Equal(t, `<table><tr><th align="left">Left</th><th align="right">Right</th></tr><tr><td align="left">a</td><td align="right">b</td></tr></table>`, document.Fragments[3].HTML)
	assert.Equal(t, "<pre><code class=\"language-go\">fmt.Println(&#34;&lt;ok&gt;&#34;)\n</code></pre>", document.Fragments[4].HTML)
	assert.Equal(t, `<tg-math-block>\int_0^1 x^2 dx</tg-math-block>`, document.Fragments[5].HTML)
	assert.Equal(t, "<blockquote><p>quoted\nacross two lines</p></blockquote>", document.Fragments[6].HTML)
	assert.True(t, strings.HasPrefix(document.Fragments[2].LegacySource, "- first"))
	assert.True(t, strings.HasPrefix(document.Fragments[3].LegacySource, "| Left"))
	assert.True(t, strings.HasPrefix(document.Fragments[4].LegacySource, "```go"))
	assert.True(t, strings.HasPrefix(document.Fragments[6].LegacySource, "> quoted"))

	assert.Equal(t, 3, document.Fragments[3].Stats.Blocks)
	assert.Equal(t, 2, document.Fragments[3].Stats.MaxTableColumns)
	assert.Equal(t, 1, document.Fragments[4].Stats.Blocks)
	assert.Equal(t, 1, document.Fragments[5].Stats.Blocks)
	assert.Equal(t, 2, document.Fragments[6].Stats.Blocks)
}

func TestParseRichFragmentsAggregateMatchesToRichHTML(t *testing.T) {
	input := "\n# One\n\nParagraph 👋 with [link](https://example.com?a=1&b=2).\n\n" +
		"3. third\n4. fourth\n\n---\n\n" +
		"    indented <code>\n\n" +
		"| A | B | C |\n|---|:--:|--:|\n| 1 | **2** | 3 |\n"

	wantHTML, wantStats, err := ToRichHTML(input)
	require.NoError(t, err)

	document, err := ParseRichFragments(input)
	require.NoError(t, err)
	assert.Equal(t, wantHTML, document.HTML)
	assert.Equal(t, wantStats, document.Stats)
	assert.Equal(t, wantHTML, concatenateFragmentHTML(document.Fragments))
	assert.Equal(t, wantStats, aggregateFragmentStats(document.Fragments))
	assert.Equal(t, input, concatenateLegacySources(document.Fragments))
}

func TestParseRichFragmentsPreservesRichOnlyListNormalizationBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "LF",
			input: "Stages:\n3. prepare\n4. launch\n\nTail.\n",
		},
		{
			name:  "CRLF",
			input: "Stages:\r\n3. prepare\r\n4. launch\r\n\r\nTail.\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := ParseRichFragments(tt.input)
			require.NoError(t, err)
			require.Len(t, document.Fragments, 3)
			assert.Equal(t, []RichFragmentKind{
				RichFragmentParagraph,
				RichFragmentList,
				RichFragmentParagraph,
			}, []RichFragmentKind{
				document.Fragments[0].Kind,
				document.Fragments[1].Kind,
				document.Fragments[2].Kind,
			})
			assert.Equal(t, strings.Index(tt.input, "3. prepare"), document.Fragments[1].SourceStart)
			assert.Equal(t, tt.input, concatenateLegacySources(document.Fragments))
			assert.Equal(t, "3. prepare", strings.Split(document.Fragments[1].LegacySource, richMarkdownNewline(tt.input))[0])

			wantHTML, wantStats, renderErr := ToRichHTML(tt.input)
			require.NoError(t, renderErr)
			assert.Equal(t, wantHTML, document.HTML)
			assert.Equal(t, wantStats, document.Stats)
		})
	}
}

func TestParseRichFragmentsExcludesMultilineInlineCodeFromBoundaryText(t *testing.T) {
	input := "before `alpha\n###SPLIT###\nomega` after"

	document, err := ParseRichFragments(input)
	require.NoError(t, err)
	require.Len(t, document.Fragments, 1)
	fragment := document.Fragments[0]
	require.Equal(t, RichFragmentParagraph, fragment.Kind)
	assertMarkerNotBoundaryEligible(t, input, fragment.BoundaryTextRanges)
	assert.Equal(t, "<p>before <code>alpha ###SPLIT### omega</code> after</p>", fragment.HTML)
}

func TestParseRichFragmentsBoundaryTextRequiresDirectParagraphText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHTML string
	}{
		{
			name:     "emphasis",
			input:    "before *alpha\n###SPLIT###\nomega* after",
			wantHTML: "<em>alpha\n###SPLIT###\nomega</em>",
		},
		{
			name:     "strong emphasis",
			input:    "before **alpha\n###SPLIT###\nomega** after",
			wantHTML: "<strong>alpha\n###SPLIT###\nomega</strong>",
		},
		{
			name:     "link label",
			input:    "before [alpha\n###SPLIT###\nomega](https://example.com) after",
			wantHTML: `<a href="https://example.com">alpha` + "\n###SPLIT###\n" + `omega</a>`,
		},
		{
			name:     "image label",
			input:    "before ![alpha\n###SPLIT###\nomega](https://example.com/image.png) after",
			wantHTML: "alpha\n###SPLIT###\nomega",
		},
		{
			name:     "strikethrough",
			input:    "before ~~alpha\n###SPLIT###\nomega~~ after",
			wantHTML: "<s>alpha\n###SPLIT###\nomega</s>",
		},
		{
			name:     "link label after code span containing closing bracket",
			input:    "before [alpha `]`\n###SPLIT###\nomega](https://example.com) after",
			wantHTML: `<a href="https://example.com">alpha <code>]</code>` + "\n###SPLIT###\nomega</a>",
		},
		{
			name:     "image label after code span containing closing bracket",
			input:    "before ![alpha `]`\n###SPLIT###\nomega](https://example.com/image.png) after",
			wantHTML: "alpha <code>]</code>\n###SPLIT###\nomega",
		},
		{
			name:     "link label after raw HTML containing closing bracket",
			input:    "before [alpha <!-- ] -->\n###SPLIT###\nomega](https://example.com) after",
			wantHTML: `<a href="https://example.com">alpha ` + "\n###SPLIT###\nomega</a>",
		},
		{
			name:     "image label after raw HTML containing closing bracket",
			input:    "before ![alpha <!-- ] -->\n###SPLIT###\nomega](https://example.com/image.png) after",
			wantHTML: "alpha \n###SPLIT###\nomega",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := ParseRichFragments(tt.input)
			require.NoError(t, err)
			require.Len(t, document.Fragments, 1)
			fragment := document.Fragments[0]
			require.Equal(t, RichFragmentParagraph, fragment.Kind)
			assert.Contains(t, fragment.HTML, tt.wantHTML, "fixture must actually parse as an inline container")

			assertMarkerNotBoundaryEligible(t, tt.input, fragment.BoundaryTextRanges)
		})
	}

	t.Run("ordinary soft-break paragraph is eligible", func(t *testing.T) {
		document, err := ParseRichFragments("before\n###SPLIT###\nafter")
		require.NoError(t, err)
		require.Len(t, document.Fragments, 1)
		assertMarkerBoundaryEligible(t, "before\n###SPLIT###\nafter", document.Fragments[0].BoundaryTextRanges)
	})

	t.Run("indented CRLF marker is direct paragraph text", func(t *testing.T) {
		input := "before\r\n  ###SPLIT###  \r\nafter"
		document, err := ParseRichFragments(input)
		require.NoError(t, err)
		require.Len(t, document.Fragments, 1)
		assertMarkerBoundaryEligible(t, input, document.Fragments[0].BoundaryTextRanges)
	})

	t.Run("inline raw HTML", func(t *testing.T) {
		input := "before <!-- alpha\n###SPLIT###\nomega --> after"
		document, err := ParseRichFragments(input)
		require.NoError(t, err)
		require.Len(t, document.Fragments, 1)
		fragment := document.Fragments[0]
		assert.Equal(t, "<p>before  after</p>", fragment.HTML, "fixture must parse as one sanitized raw HTML node")
		assertMarkerNotBoundaryEligible(t, input, fragment.BoundaryTextRanges)
	})

	t.Run("raw HTML nested in emphasis", func(t *testing.T) {
		input := "before *alpha <!-- x\n###SPLIT###\ny -->* after"
		document, err := ParseRichFragments(input)
		require.NoError(t, err)
		require.Len(t, document.Fragments, 1)
		fragment := document.Fragments[0]
		assert.Equal(t, "<p>before <em>alpha </em> after</p>", fragment.HTML)
		assertMarkerNotBoundaryEligible(t, input, fragment.BoundaryTextRanges)
	})

	t.Run("spoiler parser is line local", func(t *testing.T) {
		input := "before ||alpha\n###SPLIT###\nomega|| after"
		document, err := ParseRichFragments(input)
		require.NoError(t, err)
		require.Len(t, document.Fragments, 1)
		fragment := document.Fragments[0]
		assert.NotContains(t, fragment.HTML, "<tg-spoiler>")
		assertMarkerBoundaryEligible(t, input, fragment.BoundaryTextRanges)
	})
}

func TestParseRichFragmentsExcludesMultilineLinkAndImageTitlesFromBoundaryText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHTML string
	}{
		{
			name:     "link title",
			input:    "before [label](https://example.com \"alpha\n###SPLIT###\nomega\") after",
			wantHTML: `<p>before <a href="https://example.com">label</a> after</p>`,
		},
		{
			name:     "image title",
			input:    "before ![label](https://example.com/image.png \"alpha\n###SPLIT###\nomega\") after",
			wantHTML: "<p>before label after</p>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := ParseRichFragments(tt.input)
			require.NoError(t, err)
			require.Len(t, document.Fragments, 1)
			fragment := document.Fragments[0]
			assert.Equal(t, tt.wantHTML, fragment.HTML, "fixture must parse as a link/image with a hidden title")
			assert.Equal(t, tt.input, fragment.LegacySource)
			assertMarkerNotBoundaryEligible(t, tt.input, fragment.BoundaryTextRanges)
		})
	}

	t.Run("next physical line stays outside link envelope", func(t *testing.T) {
		input := "[label](https://example.com)\n###SPLIT###\nafter"
		document, err := ParseRichFragments(input)
		require.NoError(t, err)
		require.Len(t, document.Fragments, 1)
		assertMarkerBoundaryEligible(t, input, document.Fragments[0].BoundaryTextRanges)
	})
}

func TestParseRichFragmentsExcludesMultilineFullReferencesFromBoundaryText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHTML string
	}{
		{
			name: "link reference",
			input: "before [label][alpha\n###SPLIT###\nomega] after\n\n" +
				"[alpha ###SPLIT### omega]: https://example.com",
			wantHTML: `<p>before <a href="https://example.com">label</a> after</p>`,
		},
		{
			name: "image reference",
			input: "before ![label][alpha\n###SPLIT###\nomega] after\n\n" +
				"[alpha ###SPLIT### omega]: https://example.com/image.png",
			wantHTML: "<p>before label after</p>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := ParseRichFragments(tt.input)
			require.NoError(t, err)
			require.Len(t, document.Fragments, 2)
			fragment := document.Fragments[0]
			assert.Equal(t, tt.wantHTML, fragment.HTML, "fixture must resolve through its reference definition")
			assert.Empty(t, document.Fragments[1].HTML, "the definition itself is source-only")
			assert.Equal(t, tt.input, concatenateLegacySources(document.Fragments))
			assertMarkerNotBoundaryEligible(t, tt.input, fragment.BoundaryTextRanges)
		})
	}
}

func assertMarkerBoundaryEligible(t *testing.T, input string, ranges []RichSourceRange) {
	t.Helper()
	markerStart := strings.Index(input, "###SPLIT###")
	require.GreaterOrEqual(t, markerStart, 0)
	markerEnd := markerStart + len("###SPLIT###")
	for _, sourceRange := range ranges {
		if sourceRange.Start <= markerStart && markerEnd <= sourceRange.End {
			return
		}
	}
	assert.Fail(t, "standalone marker line is not eligible boundary text", "ranges: %+v", ranges)
}

func assertMarkerNotBoundaryEligible(t *testing.T, input string, ranges []RichSourceRange) {
	t.Helper()
	markerStart := strings.Index(input, "###SPLIT###")
	require.GreaterOrEqual(t, markerStart, 0)
	markerEnd := markerStart + len("###SPLIT###")
	for _, sourceRange := range ranges {
		if sourceRange.Start <= markerStart && markerEnd <= sourceRange.End {
			assert.Fail(t, "inline-container marker unexpectedly became eligible boundary text", "range: %+v", sourceRange)
			return
		}
	}
}

func TestParseRichFragmentsKeepsSanitizedEmptyRawFragment(t *testing.T) {
	input := "before\n\n<script>\nalert('nope')\n</script>\n\nafter"

	document, err := ParseRichFragments(input)
	require.NoError(t, err)
	require.Len(t, document.Fragments, 3)
	assert.Equal(t, RichFragmentRaw, document.Fragments[1].Kind)
	assert.True(t, document.Fragments[1].Atomic)
	assert.Empty(t, document.Fragments[1].HTML)
	assert.Equal(t, RichStats{}, document.Fragments[1].Stats)
	assert.Equal(t, input, concatenateLegacySources(document.Fragments))
	assert.Equal(t, "<p>before</p><p>after</p>", document.HTML)
}

func TestParseRichFragmentsEmptyAndInvalidUTF8(t *testing.T) {
	document, err := ParseRichFragments(" \n\t\n")
	require.NoError(t, err)
	assert.Equal(t, " \n\t\n", document.LegacySource)
	assert.Empty(t, document.HTML)
	assert.Empty(t, document.Fragments)
	assert.Equal(t, RichStats{}, document.Stats)

	document, err = ParseRichFragments(string([]byte{'o', 'k', 0xff}))
	require.Error(t, err)
	assert.Equal(t, RichDocument{}, document)
}

func concatenateLegacySources(fragments []RichFragment) string {
	var out strings.Builder
	for _, fragment := range fragments {
		out.WriteString(fragment.LegacySource)
	}
	return out.String()
}

func concatenateFragmentHTML(fragments []RichFragment) string {
	var out strings.Builder
	for _, fragment := range fragments {
		out.WriteString(fragment.HTML)
	}
	return out.String()
}

func aggregateFragmentStats(fragments []RichFragment) RichStats {
	var stats RichStats
	for _, fragment := range fragments {
		mergeRichStats(&stats, fragment.Stats)
	}
	return stats
}
