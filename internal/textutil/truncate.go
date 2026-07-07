// Package textutil provides small string helpers shared across packages.
package textutil

// TruncateRunes caps s at max runes, appending suffix when it was cut.
// Truncation happens on rune (not byte) boundaries: much of the bot's
// content is Russian, and a byte-level slice can split a multibyte rune
// and corrupt the tail.
//
// Allocation-free until the cut: callers pass multi-megabyte strings on hot
// paths (serialized LLM request bodies with base64 media), where a
// []rune(s) conversion would transiently cost ~4x the input size.
func TruncateRunes(s string, max int, suffix string) string {
	if len(s) <= max {
		return s // max runes always span at least max bytes — nothing to cut
	}
	if max <= 0 {
		return suffix
	}
	count := 0
	for i := range s { // i walks rune start offsets
		if count == max {
			return s[:i] + suffix
		}
		count++
	}
	return s // exactly max runes (multibyte), no cut needed
}
