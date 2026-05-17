package obs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DefaultPreviewLen is the truncation threshold used by TextPreview when the
// caller doesn't override it. ~160 chars fits inside one Tempo attribute slot
// without bloating index bytes, while still leaving enough text for direct
// TraceQL substring search (e.g. `{span.bot.user_query_preview =~ ".*Isotonic.*"}`).
const DefaultPreviewLen = 160

// TextPreview returns a short, single-line, base64-redacted preview of text
// suitable for use as a span attribute, plus the sha256-hex of the original
// (untruncated, pre-redaction) bytes. Both are intended to be set on a span
// alongside each other so an investigator can:
//
//   - search by readable substring via the preview attribute, and
//   - join different spans on the same input via the hash, even after the
//     preview gets truncated or collides between similar queries.
//
// The function is deterministic and allocation-light: it short-circuits on
// strings already under maxLen with no redaction matches.
//
// If maxLen <= 0, DefaultPreviewLen is used. The hash is always over the
// raw input — truncation must not change the hash for the same input.
func TextPreview(text string, maxLen int) (preview, hashHex string) {
	if maxLen <= 0 {
		maxLen = DefaultPreviewLen
	}
	sum := sha256.Sum256([]byte(text))
	hashHex = hex.EncodeToString(sum[:8]) // 16 hex chars — collision-safe enough for one user

	redacted := RedactBase64Payloads(text)
	// Newlines and tabs make span attrs hard to read in TraceQL output; collapse them.
	flat := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, redacted)
	flat = strings.TrimSpace(flat)
	// Collapse runs of whitespace so the preview is dense.
	flat = strings.Join(strings.Fields(flat), " ")

	if len(flat) <= maxLen {
		return flat, hashHex
	}
	// Truncate on a byte boundary, then trim back to a rune boundary to avoid
	// invalid UTF-8 in the attribute value (Tempo accepts it but TraceQL regex
	// will get confused).
	cut := maxLen
	for cut > 0 && !isRuneStart(flat[cut]) {
		cut--
	}
	return flat[:cut] + "…", hashHex
}

// isRuneStart reports whether b is a leading byte of a UTF-8 rune.
func isRuneStart(b byte) bool {
	// 0xxxxxxx (ASCII) or 11xxxxxx (multi-byte leading). Continuation bytes
	// are 10xxxxxx — those we want to skip past.
	return b < 0x80 || b >= 0xC0
}
