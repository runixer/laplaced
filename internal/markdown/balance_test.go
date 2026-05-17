package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBalanceOpenMarkers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
	}{
		{"empty", "", ""},
		{"plain text", "hello world", "hello world"},

		// Bold
		{"closed bold passthrough", "say **hi** to mom", "say **hi** to mom"},
		{"open bold gets closed", "say **hi to mom", "say **hi to mom**"},
		{"two bold pairs ok", "**a** and **b**", "**a** and **b**"},
		{"three asterisks (one pair, one orphan) gets closed", "**a", "**a**"},

		// Inline code
		{"closed code passthrough", "use `printf`", "use `printf`"},
		{"open code gets closed", "use `printf to print", "use `printf to print`"},

		// Mixed bold + code
		{"open bold and code", "this **is `mid", "this **is `mid**`"},
		{"closed bold open code", "this **is `mid", "this **is `mid**`"}, // duplicate of above; kept to anchor ordering

		// Triple-backtick fences
		{"closed fence passthrough", "```\ncode\n```", "```\ncode\n```"},
		{
			name: "open fence gets closed on a new line",
			in:   "```python\nprint(",
			out:  "```python\nprint(\n```",
		},
		{
			name: "open fence ending with newline gets closed inline",
			in:   "```\nfoo\n",
			out:  "```\nfoo\n```",
		},

		// Markers inside an open fence must NOT be balanced — they're code.
		{
			name: "no balancing inside an open fence",
			in:   "```\nlet *x = 1; let `y = 2;",
			out:  "```\nlet *x = 1; let `y = 2;\n```",
		},

		// Bold/code inside a CLOSED fence are also untouched.
		{
			name: "no balancing inside a closed fence",
			in:   "before ```\n**x**` y\n``` after **z",
			out:  "before ```\n**x**` y\n``` after **z**",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BalanceOpenMarkers(tt.in)
			assert.Equal(t, tt.out, got)
		})
	}
}

// TestBalanceOpenMarkers_GoldmarkRoundTrip is a smoke test: for every test
// case from TestBalanceOpenMarkers we verify ToHTML doesn't error on the
// balanced output. Streaming uses the balancer specifically to keep ToHTML
// happy; if this regresses, mid-stream edits will fall back to plain text.
func TestBalanceOpenMarkers_GoldmarkRoundTrip(t *testing.T) {
	prefixes := []string{
		"",
		"hello ",
		"a sentence with **partial bo",
		"```\nfn main() {\n    println!(\"hello",
		"see `git status` and **note",
		"`one ",
		"**bold",
		"```python\nx = ",
	}
	for _, p := range prefixes {
		balanced := BalanceOpenMarkers(p)
		_, err := ToHTML(balanced)
		require.NoError(t, err, "ToHTML must accept balanced output for prefix=%q (balanced=%q)", p, balanced)
	}
}

// TestBalanceOpenMarkers_StreamingTokenSequence walks a typical streaming
// progression token-by-token and asserts that every prefix balances to a
// goldmark-clean buffer with no panics. This is the spirit of the streaming
// use case — the user never sees a parser error.
func TestBalanceOpenMarkers_StreamingTokenSequence(t *testing.T) {
	full := "Here is **the answer**: see `cmd` for details.\n```python\nprint('ok')\n```"
	for i := 1; i <= len(full); i++ {
		prefix := full[:i]
		balanced := BalanceOpenMarkers(prefix)
		_, err := ToHTML(balanced)
		require.NoError(t, err, "stream prefix %q failed", prefix)
		// Sanity: balanced version is never shorter than input.
		assert.GreaterOrEqual(t, len(balanced), len(prefix))
		// Sanity: balanced version starts with the input.
		assert.True(t, strings.HasPrefix(balanced, prefix),
			"balanced output must extend the input, not modify it: %q -> %q", prefix, balanced)
	}
}
