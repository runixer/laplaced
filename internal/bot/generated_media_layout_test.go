package bot

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGeneratedMediaLayout_MapsOriginalOrdinalGapsAndPreservesSource(t *testing.T) {
	source := "Вступление 👋\r\n\u00a0###MEDIA:3,1###\u00a0\r\n###SPLIT###\r\nПосле\r\n\t###MEDIA:5###\t"

	layout, err := parseGeneratedMediaLayout(source, []int{1, 3, 5})

	require.NoError(t, err)
	assert.Equal(t, generatedMediaLayoutDirected, layout.Mode)
	assert.Equal(t, generatedMediaLayoutReasonNone, layout.Reason)
	assert.Equal(t, "Вступление 👋\r\nПосле\r\n", layout.MarkerFreeSource)
	require.Len(t, layout.ProtocolLines, 3)
	assert.Equal(t, []generatedMediaProtocolLineKind{
		generatedMediaProtocolMedia,
		generatedMediaProtocolSplit,
		generatedMediaProtocolMedia,
	}, []generatedMediaProtocolLineKind{
		layout.ProtocolLines[0].Kind,
		layout.ProtocolLines[1].Kind,
		layout.ProtocolLines[2].Kind,
	})
	assert.Equal(t, "\u00a0###MEDIA:3,1###\u00a0\r\n", sourceSlice(t, source, layout.ProtocolLines[0].SourceRange.Start, layout.ProtocolLines[0].SourceRange.End))
	assert.Equal(t, "###SPLIT###\r\n", sourceSlice(t, source, layout.ProtocolLines[1].SourceRange.Start, layout.ProtocolLines[1].SourceRange.End))
	assert.Equal(t, "\t###MEDIA:5###\t", sourceSlice(t, source, layout.ProtocolLines[2].SourceRange.Start, layout.ProtocolLines[2].SourceRange.End))
	require.Len(t, layout.Placements, 2)
	assert.Equal(t, []int{1, 0}, layout.Placements[0].ItemIndexes)
	assert.Equal(t, []int{2}, layout.Placements[1].ItemIndexes)
	assert.Equal(t, layout.ProtocolLines[0].SourceRange, layout.Placements[0].SourceRange)
	assert.Equal(t, layout.ProtocolLines[2].SourceRange, layout.Placements[1].SourceRange)
}

func TestParseGeneratedMediaLayout_UsesAvailableOrdinalOrderAsItemMapping(t *testing.T) {
	layout, err := parseGeneratedMediaLayout(
		"before\n###MEDIA:1,9,4###\nafter",
		[]int{9, 4, 1},
	)

	require.NoError(t, err)
	require.Equal(t, generatedMediaLayoutDirected, layout.Mode)
	require.Len(t, layout.Placements, 1)
	assert.Equal(t, []int{2, 0, 1}, layout.Placements[0].ItemIndexes)
	assert.Equal(t, "before\nafter", layout.MarkerFreeSource)
}

func TestParseGeneratedMediaLayout_AutoStillRemovesSplitFromHistory(t *testing.T) {
	source := "first\n###SPLIT###\nsecond"

	layout, err := parseGeneratedMediaLayout(source, []int{1})

	require.NoError(t, err)
	assert.Equal(t, generatedMediaLayoutAuto, layout.Mode)
	assert.Empty(t, layout.Placements)
	assert.Equal(t, "first\nsecond", layout.MarkerFreeSource)
	require.Len(t, layout.ProtocolLines, 1)
	assert.Equal(t, generatedMediaProtocolSplit, layout.ProtocolLines[0].Kind)
}

func TestParseGeneratedMediaLayout_AppliesHardBoundsBeforeMarkdownParse(t *testing.T) {
	for _, source := range []string{
		string([]byte{'o', 'k', 0xff}),
		strings.Repeat("x", richMessageMaxSourceBytes+1),
		" \r\n\t ",
	} {
		layout, err := parseGeneratedMediaLayout(source, []int{1})
		assert.Error(t, err)
		assert.Equal(t, generatedMediaLayout{}, layout)
	}
}

func TestParseGeneratedMediaLayout_InvalidIsAtomicAndStripsReservedLines(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		available  []int
		wantReason generatedMediaLayoutReason
	}{
		{name: "empty legacy form", source: "text\n###MEDIA###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "bare stem", source: "text\n###MEDIA\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "missing suffix", source: "text\n###MEDIA:1\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "leading zero", source: "text\n###MEDIA:01###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "zero", source: "text\n###MEDIA:0###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "plus", source: "text\n###MEDIA:+1###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "minus", source: "text\n###MEDIA:-1###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "internal space", source: "text\n###MEDIA:1, 2###\ntail", available: []int{1, 2}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "trailing comma", source: "text\n###MEDIA:1,###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "unicode digit", source: "text\n###MEDIA:١###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonSyntax},
		{name: "duplicate one group", source: "text\n###MEDIA:1,1###\ntail", available: []int{1}, wantReason: generatedMediaLayoutReasonDuplicate},
		{name: "duplicate groups", source: "text\n###MEDIA:1###\n###MEDIA:3,1###\ntail", available: []int{1, 3}, wantReason: generatedMediaLayoutReasonDuplicate},
		{name: "missing loaded slot", source: "text\n###MEDIA:1,2###\ntail", available: []int{1, 3}, wantReason: generatedMediaLayoutReasonUnavailable},
		{name: "unreferenced loaded slot", source: "text\n###MEDIA:1###\ntail", available: []int{1, 3}, wantReason: generatedMediaLayoutReasonUnreferenced},
		{name: "duplicate available ordinal", source: "text\n###MEDIA:1###\ntail", available: []int{1, 1}, wantReason: generatedMediaLayoutReasonAvailableOrdinals},
		{name: "nonpositive available ordinal", source: "text\n###MEDIA:1###\ntail", available: []int{0}, wantReason: generatedMediaLayoutReasonAvailableOrdinals},
		{
			name:       "too many references",
			source:     "text\n###MEDIA:1,2,3,4,5,6,7,8,9,10,11###\ntail",
			available:  []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantReason: generatedMediaLayoutReasonTooMany,
		},
		{
			name:       "integer overflow",
			source:     "text\n###MEDIA:999999999999999999999999999999999999###\ntail",
			available:  []int{1},
			wantReason: generatedMediaLayoutReasonSyntax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout, err := parseGeneratedMediaLayout(tt.source, tt.available)

			require.NoError(t, err)
			assert.Equal(t, generatedMediaLayoutInvalid, layout.Mode)
			assert.Equal(t, tt.wantReason, layout.Reason)
			assert.Empty(t, layout.Placements, "invalid layout must never expose a partial placement")
			assert.Equal(t, "text\ntail", layout.MarkerFreeSource)
			require.NotEmpty(t, layout.ProtocolLines)
			for _, line := range layout.ProtocolLines {
				assert.Equal(t, generatedMediaProtocolMedia, line.Kind)
			}
		})
	}
}

func TestParseGeneratedMediaLayout_ProtectedAndNearMissMarkersStayLiteral(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "inline prose", source: "before ###MEDIA:1### after"},
		{name: "fenced code", source: "```text\n###MEDIA:1###\n```"},
		{name: "indented code", source: "    ###MEDIA:1###"},
		{name: "inline code", source: "before `alpha\n###MEDIA:1###\nomega` after"},
		{name: "emphasis", source: "before *alpha\n###MEDIA:1###\nomega* after"},
		{name: "link label", source: "before [alpha\n###MEDIA:1###\nomega](https://example.com) after"},
		{name: "link title", source: "before [label](https://example.com \"alpha\n###MEDIA:1###\nomega\") after"},
		{name: "raw comment", source: "before <!-- alpha\n###MEDIA:1###\nomega --> after"},
		{name: "table", source: "| Value |\n|---|\n| ###MEDIA:1### |"},
		{name: "list", source: "- ###MEDIA:1###"},
		{name: "blockquote", source: "> ###MEDIA:1###"},
		{name: "display math", source: "$$\n###MEDIA:1###\n$$"},
		{name: "setext heading", source: "###MEDIA:1###\n---"},
		{name: "lowercase", source: "###media:1###"},
		{name: "different namespace", source: "###MEDIA-X###"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout, err := parseGeneratedMediaLayout(tt.source, []int{1})

			require.NoError(t, err)
			assert.Equal(t, generatedMediaLayoutAuto, layout.Mode)
			assert.Empty(t, layout.ProtocolLines)
			assert.Empty(t, layout.Placements)
			assert.Equal(t, tt.source, layout.MarkerFreeSource)
		})
	}
}

func TestParseGeneratedMediaLayout_LineLocalSpoilerDoesNotProtectNextLine(t *testing.T) {
	source := "before ||alpha\n###MEDIA:1###\nomega|| after"

	layout, err := parseGeneratedMediaLayout(source, []int{1})

	require.NoError(t, err)
	assert.Equal(t, generatedMediaLayoutDirected, layout.Mode)
	require.Len(t, layout.Placements, 1)
	assert.Equal(t, "before ||alpha\nomega|| after", layout.MarkerFreeSource)
}

func TestSuppressGeneratedMediaDraftSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "middle LF", source: "before\n###MEDIA:1,2###\nafter", want: "before\nafter"},
		{name: "middle CRLF unicode whitespace", source: "до\r\n\u00a0###MEDIA:2###\u00a0\r\nпосле", want: "до\r\nпосле"},
		{name: "malformed reserved", source: "before\n###MEDIA:1, nope\nafter", want: "before\nafter"},
		{name: "marker only", source: "###MEDIA:1###", want: ""},
		{name: "partial terminal", source: "before\n###MEDIA:1,", want: "before\n"},
		{name: "split middle", source: "before\n###SPLIT###\nafter", want: "before\nafter"},
		{name: "split only", source: "###SPLIT###", want: ""},
		{name: "split partial terminal", source: "before\n###SPLI", want: "before\n"},
		{name: "protected fenced code", source: "```\n###MEDIA:1###\n```", want: "```\n###MEDIA:1###\n```"},
		{name: "protected split fenced code", source: "```\n###SPLIT###\n```", want: "```\n###SPLIT###\n```"},
		{name: "inline remains", source: "before ###MEDIA:1### after", want: "before ###MEDIA:1### after"},
		{name: "near miss remains", source: "before\n###MEDIAx", want: "before\n###MEDIAx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := suppressGeneratedMediaDraftSource(tt.source)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSuppressGeneratedMediaDraftSource_WithholdsEveryMeaningfulTerminalPrefix(t *testing.T) {
	for _, marker := range []string{"###MEDIA:12,3###", "###SPLIT###"} {
		for end := 1; end <= len(marker); end++ {
			t.Run(fmt.Sprintf("%s_bytes_%d", marker, end), func(t *testing.T) {
				source := "visible\n" + marker[:end]
				got, err := suppressGeneratedMediaDraftSource(source)
				require.NoError(t, err)
				assert.Equal(t, "visible\n", got)
			})
		}
	}
}

func TestSuppressGeneratedMediaDraftSource_InvalidUTF8FailsClosed(t *testing.T) {
	got, err := suppressGeneratedMediaDraftSource(string([]byte("ok\n###M\xff")))
	assert.Error(t, err)
	assert.Empty(t, got)
}

func FuzzParseGeneratedMediaLayoutSourceInvariants(f *testing.F) {
	for _, seed := range []string{
		"before\n###MEDIA:3,1###\nafter",
		"###SPLIT###\r\n###MEDIA:1###",
		"```\n###MEDIA:1###\n```",
		"до 👋\n\u00a0###MEDIA:1###\u00a0\nпосле",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		layout, err := parseGeneratedMediaLayout(source, []int{1, 3})
		if !utf8.ValidString(source) {
			assert.Error(t, err)
			return
		}
		if err != nil {
			return
		}
		previousEnd := 0
		for _, line := range layout.ProtocolLines {
			if line.SourceRange.Start < previousEnd || line.SourceRange.End < line.SourceRange.Start ||
				line.SourceRange.End > len(source) {
				t.Fatalf("invalid protocol range %+v after %d for %q", line.SourceRange, previousEnd, source)
			}
			previousEnd = line.SourceRange.End
		}
		if layout.Mode != generatedMediaLayoutDirected && len(layout.Placements) != 0 {
			t.Fatalf("non-directed layout exposed placements: %+v", layout)
		}
		if layout.Mode == generatedMediaLayoutDirected {
			seen := map[int]bool{}
			for _, placement := range layout.Placements {
				for _, itemIndex := range placement.ItemIndexes {
					if itemIndex < 0 || itemIndex >= 2 || seen[itemIndex] {
						t.Fatalf("invalid directed item index %d in %+v", itemIndex, layout)
					}
					seen[itemIndex] = true
				}
			}
			if !seen[0] || !seen[1] {
				t.Fatalf("directed layout did not cover available items: %+v", layout)
			}
		}
	})
}

func sourceSlice(t *testing.T, source string, start, end int) string {
	t.Helper()
	require.GreaterOrEqual(t, start, 0)
	require.GreaterOrEqual(t, end, start)
	require.LessOrEqual(t, end, len(source))
	return source[start:end]
}

func TestParseGeneratedMediaLayout_ProtocolRangesReconstructMarkerFreeSource(t *testing.T) {
	source := "  lead\r\n###MEDIA:2###\r\nbody\n###SPLIT###\n###MEDIA:1###\ntail  "
	layout, err := parseGeneratedMediaLayout(source, []int{1, 2})
	require.NoError(t, err)
	require.Equal(t, generatedMediaLayoutDirected, layout.Mode)

	var reconstructed strings.Builder
	previousEnd := 0
	for _, line := range layout.ProtocolLines {
		reconstructed.WriteString(source[previousEnd:line.SourceRange.Start])
		previousEnd = line.SourceRange.End
	}
	reconstructed.WriteString(source[previousEnd:])
	assert.Equal(t, layout.MarkerFreeSource, reconstructed.String())
	assert.Equal(t, "  lead\r\nbody\ntail  ", reconstructed.String())
}
