package reranker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/runixer/laplaced/internal/openrouter"
)

// formatMediaParts produces a JSON description of the media inputs that arrived
// at the reranker as a span event body. It records filename, mime, decoded
// size, and sha256 — enough to recover the corresponding artifact from the
// snapshot DB by content_hash during replay, without dragging the raw base64
// payload through Tempo.
//
// Non-FilePart entries (defensive: callers always pass FilePart today) are
// skipped silently. Returns an empty string when nothing recordable is found.
func formatMediaParts(mediaParts []interface{}) string {
	type entry struct {
		Filename  string `json:"filename"`
		Mime      string `json:"mime"`
		SizeBytes int    `json:"size_bytes"`
		SHA256    string `json:"sha256,omitempty"`
	}
	out := make([]entry, 0, len(mediaParts))
	for _, p := range mediaParts {
		fp, ok := p.(openrouter.FilePart)
		if !ok {
			continue
		}
		mime, decoded := decodeDataURL(fp.File.FileData)
		e := entry{
			Filename:  fp.File.FileName,
			Mime:      mime,
			SizeBytes: len(decoded),
		}
		if len(decoded) > 0 {
			sum := sha256.Sum256(decoded)
			e.SHA256 = hex.EncodeToString(sum[:])
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return ""
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(body)
}

// decodeDataURL parses "data:<mime>;base64,<payload>" into (mime, decoded
// bytes). Returns ("", nil) when the input does not look like a data URL.
// On a malformed base64 payload the mime is still returned but bytes are nil.
func decodeDataURL(s string) (string, []byte) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return "", nil
	}
	rest := s[len(prefix):]
	semi := strings.IndexByte(rest, ';')
	if semi < 0 {
		return "", nil
	}
	mime := rest[:semi]
	rest = rest[semi+1:]
	const b64Marker = "base64,"
	if !strings.HasPrefix(rest, b64Marker) {
		return mime, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(rest[len(b64Marker):])
	if err != nil {
		return mime, nil
	}
	return mime, decoded
}
