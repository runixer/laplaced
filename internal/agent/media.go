package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/runixer/laplaced/internal/openrouter"
)

// MediaEntry is the JSON shape of one file in an *.media_parts span event.
// Replay relies on this exact field layout to reconstruct openrouter.FilePart
// from a snapshot DB by content_hash — keep changes coordinated with
// cmd/testbot/snapshot/extract.go.
type MediaEntry struct {
	Filename  string `json:"filename"`
	Mime      string `json:"mime"`
	SizeBytes int    `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
	// Source distinguishes where in the prompt the part lives —
	// laplace uses it to mark current_message vs reranker_selected vs
	// input_artifact_id; reranker/enricher pass empty string.
	Source string `json:"source,omitempty"`
}

// MediaPartWithSource pairs an OR FilePart with an optional source label.
// Pass source="" for agents that don't differentiate origin (enricher,
// reranker). Laplace uses it to attribute each entry to the right pipeline
// stage so triage can answer "which artifact came from RAG vs from the user".
type MediaPartWithSource struct {
	Part   interface{}
	Source string
}

// FormatMediaParts produces the JSON body for *.media_parts span events.
//
// Each entry records filename + mime + decoded size + sha256 of decoded
// bytes. The hash matches artifacts.content_hash, so a faithful replay can
// look up the underlying file in the snapshotted artifact storage and
// reconstruct the original FilePart byte-for-byte without dragging the raw
// base64 payload through Tempo.
//
// Non-FilePart entries are skipped silently — callers may pass a heterogenous
// content slice (e.g., openrouter.Message.Content as []interface{} mixing
// TextPart and FilePart). Returns "" when nothing recordable is found.
//
// Use FormatMediaPartsWithSources when entries have distinct origins.
func FormatMediaParts(parts []interface{}, source string) string {
	withSource := make([]MediaPartWithSource, len(parts))
	for i, p := range parts {
		withSource[i] = MediaPartWithSource{Part: p, Source: source}
	}
	return FormatMediaPartsWithSources(withSource)
}

// FormatMediaPartsWithSources is the multi-source variant for laplace where
// current-message media and reranker-selected artifacts coexist in one event.
func FormatMediaPartsWithSources(parts []MediaPartWithSource) string {
	out := make([]MediaEntry, 0, len(parts))
	for _, p := range parts {
		fp, ok := p.Part.(openrouter.FilePart)
		if !ok {
			continue
		}
		mime, decoded := decodeDataURL(fp.File.FileData)
		entry := MediaEntry{
			Filename:  fp.File.FileName,
			Mime:      mime,
			SizeBytes: len(decoded),
			Source:    p.Source,
		}
		if len(decoded) > 0 {
			sum := sha256.Sum256(decoded)
			entry.SHA256 = hex.EncodeToString(sum[:])
		}
		out = append(out, entry)
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
// bytes). Returns ("", nil) on a non-data-URL input. On a malformed base64
// payload the mime is still returned but bytes are nil — callers can still
// surface mime/size from the original FilePart fields.
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
