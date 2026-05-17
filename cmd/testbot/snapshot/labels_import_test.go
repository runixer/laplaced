package snapshot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleFilled = `# Reranker Labels — TODO

stuff in header

---

## Trace ` + "`" + `abc12345` + "`" + `

- ...
- ...

### Topics (3 candidates)

| Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |
|---|---|---|---|---|---|---|---|---|
| T | 9695 | 0.81 | 2026-04-07 | 4K | 2 | sglang vs vLLM | 9 | prequel decision |
| ST | 4815 | 0.79 | 2025-12-24 | 6K | 6 | 2xH100 NVL | 8 | prior H100 |
| S | 6983 | 0.75 | 2026-02-08 | 10K | 2 | Alpha-Chat v2.1 | 7 |  |
|  | 5840 | 0.80 | 2026-01-14 | 11K | 2 | random | | |

### People (cap=10)

stub

### Artifacts (cap=20)

stub

---

## Trace ` + "`" + `c90199c6` + "`" + `

### Topics (1 candidates)

| Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |
|---|---|---|---|---|---|---|---|---|
| ST | 1995 | 0.59 | 2025-12-26 | 3K | 4 | пельмени | 1 | продукты, не доставка |
| ST | 8965 | 0.57 | 2026-03-18 | 6K | 2 | пакеты | 0 |  |
| ST | 9999 | 0.50 | 2026-01-01 | 1K | 1 | summary with \| pipe | 5 | edge case |

### People (cap=15)

stub

---
`

func TestParseLabelsMarkdown_HappyPath(t *testing.T) {
	got, err := ParseLabelsMarkdown(strings.NewReader(sampleFilled))
	require.NoError(t, err)

	require.Contains(t, got, "abc12345")
	abc := got["abc12345"].Topics
	assert.Equal(t, Label{Score: 9, Reason: "prequel decision"}, abc["9695"])
	assert.Equal(t, Label{Score: 8, Reason: "prior H100"}, abc["4815"])
	assert.Equal(t, Label{Score: 7, Reason: ""}, abc["6983"], "empty reason cell preserves empty string")
	_, has := abc["5840"]
	assert.False(t, has, "blank score row must NOT be emitted")

	require.Contains(t, got, "c90199c6")
	c9 := got["c90199c6"].Topics
	assert.Equal(t, Label{Score: 1, Reason: "продукты, не доставка"}, c9["1995"])
	assert.Equal(t, Label{Score: 0, Reason: ""}, c9["8965"], "explicit 0 score is preserved")
	assert.Equal(t, Label{Score: 5, Reason: "edge case"}, c9["9999"], "escaped pipe in summary doesn't shift columns")
}

func TestParseLabelsMarkdown_BadScoreSkipped(t *testing.T) {
	bad := `## Trace ` + "`x`" + `
### Topics
| Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |
|---|---|---|---|---|---|---|---|---|
|  | 1 | 0.5 | 2026 | 1K | 1 | s | 99 | out of range |
|  | 2 | 0.5 | 2026 | 1K | 1 | s | abc | non-numeric |
|  | 3 | 0.5 | 2026 | 1K | 1 | s | 7 | ok |
`
	got, err := ParseLabelsMarkdown(strings.NewReader(bad))
	require.NoError(t, err)
	x := got["x"].Topics
	assert.Len(t, x, 1, "only the valid row survives")
	assert.Equal(t, 7, x["3"].Score)
}

func TestParseLabelsMarkdown_EmptyInput(t *testing.T) {
	got, err := ParseLabelsMarkdown(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, got)
}

const sampleAllSections = "## Trace `t1`\n" +
	"\n" +
	"### Topics\n" +
	"\n" +
	"| Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |\n" +
	"|---|---|---|---|---|---|---|---|---|\n" +
	"| ST | 100 | 0.81 | 2026-04-07 | 4K | 2 | sglang vs vLLM | 9 | direct match |\n" +
	"| T  | 101 | 0.79 | 2025-12-24 | 6K | 6 | H100 NVL | 8 | prior history |\n" +
	"\n" +
	"### People (cap=10)\n" +
	"\n" +
	"| Status | ID | Name | Circle | Bio | Score | Reason |\n" +
	"|---|---|---|---|---|---|---|\n" +
	"| S | 304 | Наиля (aka Наля) | Work_Outer | пред-ца | 7 | named in query |\n" +
	"|   | 280 | AISHA            | Work_Outer | client  | 5 | tangential     |\n" +
	"|   | 999 | Skipped          | Friends    | bio     |   |                |\n" +
	"\n" +
	"### Artifacts (cap=20)\n" +
	"\n" +
	"| Status | ID | Sim | Type | File | Summary | Score | Reason |\n" +
	"|---|---|---|---|---|---|---|---|\n" +
	"| S | 978 | 0.74 | image | photo.jpg | hand of mother | 8 | exact subject |\n" +
	"|   | 500 | 0.62 | voice | voice.ogg | analysis       | 3 | tangential    |\n" +
	"\n"

func TestParseLabelsMarkdown_AllThreeSections(t *testing.T) {
	got, err := ParseLabelsMarkdown(strings.NewReader(sampleAllSections))
	require.NoError(t, err)
	require.Contains(t, got, "t1")

	tl := got["t1"]

	// Topics: both rows scored.
	assert.Equal(t, Label{Score: 9, Reason: "direct match"}, tl.Topics["100"])
	assert.Equal(t, Label{Score: 8, Reason: "prior history"}, tl.Topics["101"])

	// People: 304 + 280 scored, 999 skipped (blank score).
	assert.Equal(t, Label{Score: 7, Reason: "named in query"}, tl.People["304"])
	assert.Equal(t, Label{Score: 5, Reason: "tangential"}, tl.People["280"])
	_, has999 := tl.People["999"]
	assert.False(t, has999, "blank score row not emitted")

	// Artifacts: both scored.
	assert.Equal(t, Label{Score: 8, Reason: "exact subject"}, tl.Artifacts["978"])
	assert.Equal(t, Label{Score: 3, Reason: "tangential"}, tl.Artifacts["500"])
}

func TestParseLabelsMarkdown_ColumnCountIsolation(t *testing.T) {
	// People rows have only 7 columns. If the parser was still hard-coded to
	// the topics layout (11 parts), Score and Reason would land in the wrong
	// cells and this would silently emit garbage. Lock that down.
	body := "## Trace `x`\n" +
		"### People\n" +
		"| Status | ID | Name | Circle | Bio | Score | Reason |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| S | 1 | Alice | Friends | hi | 7 | matched |\n"
	got, err := ParseLabelsMarkdown(strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, Label{Score: 7, Reason: "matched"}, got["x"].People["1"])
	assert.Empty(t, got["x"].Topics, "no topics in this fixture")
	assert.Empty(t, got["x"].Artifacts, "no artifacts in this fixture")
}
