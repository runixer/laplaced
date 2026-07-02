// Package textutil provides small string helpers shared across packages.
package textutil

// TruncateRunes caps s at max runes, appending suffix when it was cut.
// Truncation happens on rune (not byte) boundaries: much of the bot's
// content is Russian, and a byte-level slice can split a multibyte rune
// and corrupt the tail.
func TruncateRunes(s string, max int, suffix string) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + suffix
}
