package laplace

import (
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/llm"
)

// MediaSource says where a content part entered the turn from. It replaces
// the implicit convention where provenance lived only in emoji markers and
// "memory_<id>_" filename prefixes: the assembly code can now apply
// structural rules ("don't load recalled audio next to a live voice") on a
// typed field instead of prompt instructions.
type MediaSource int

const (
	// SourceCurrent is a live part (text or attachment) from the message
	// group being answered right now.
	SourceCurrent MediaSource = iota
	// SourceRecalled is a memory artifact selected by the reranker and
	// loaded back from the artifact store.
	SourceRecalled
)

// MediaKind classifies a part's payload, derived once (from the data-URL MIME
// for current attachments, from the artifact's file type for recalled ones)
// so downstream code never re-parses data URLs to learn what a part is.
type MediaKind int

const (
	KindText MediaKind = iota
	KindImage
	KindAudio
	KindVideo
	// KindFile covers documents, PDFs and any payload that is not
	// image/audio/video — mirrors the 📎 marker bucket.
	KindFile
)

// TaggedPart pairs an LLM content part with typed provenance. BuildMessages
// assembles the turn from TaggedParts and renders them to raw LLM parts in
// exactly one place (renderTagged), which is where every disambiguation
// marker is generated.
type TaggedPart struct {
	Part   interface{} // llm.TextPart / llm.FilePart / llm.ImageURLPart / llm.VideoURLPart
	Source MediaSource
	Kind   MediaKind

	// ArtifactID, Name and CreatedAt are set on recalled parts and drive the
	// 📄 memory-artifact marker rendering.
	ArtifactID int64
	Name       string
	CreatedAt  time.Time
}

// tagCurrentParts wraps the raw current-message parts with provenance. Kind
// derivation absorbs the shape/MIME sniffing previously scattered through the
// marker code; unknown shapes tag as KindText, which renders marker-free.
func tagCurrentParts(parts []interface{}) []TaggedPart {
	out := make([]TaggedPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, TaggedPart{Part: p, Source: SourceCurrent, Kind: partKind(p)})
	}
	return out
}

// partKind classifies a raw LLM content part.
func partKind(p interface{}) MediaKind {
	switch part := p.(type) {
	case llm.FilePart:
		return mediaKindFromDataURL(part.File.FileData)
	case llm.ImageURLPart:
		return KindImage
	case llm.VideoURLPart:
		return KindVideo
	default:
		return KindText
	}
}

// mediaKindFromDataURL maps a data-URL MIME prefix to a MediaKind.
func mediaKindFromDataURL(dataURL string) MediaKind {
	switch {
	case strings.HasPrefix(dataURL, "data:audio/"):
		return KindAudio
	case strings.HasPrefix(dataURL, "data:video/"):
		return KindVideo
	case strings.HasPrefix(dataURL, "data:image/"):
		return KindImage
	default:
		return KindFile
	}
}

// renderTagged flattens tagged parts into raw LLM content parts. This is the
// single place provenance markers are generated:
//
//   - recalled parts get the "📄 <name> — memory artifact #<id> (<date>)"
//     TextPart in front, pairing with the "memory_<id>_" FilePart filename
//     prefix the loader bakes in;
//   - current media parts get the kind-matched marker (🎤/🎥/📷/📎) when
//     markCurrent is set — the caller sets it only when recalled artifacts
//     ride in the same turn, so the common single-attachment prompt stays
//     byte-for-byte unchanged.
func (l *Laplace) renderTagged(parts []TaggedPart, markCurrent bool) []interface{} {
	out := make([]interface{}, 0, len(parts)*2)
	for _, tp := range parts {
		switch tp.Source {
		case SourceRecalled:
			out = append(out, llm.TextPart{Type: "text", Text: memoryArtifactMarker(tp)})
		case SourceCurrent:
			if markCurrent && tp.Kind != KindText {
				if marker := l.currentMediaMarker(tp.Kind); marker != "" {
					out = append(out, llm.TextPart{Type: "text", Text: marker})
				}
			}
		}
		out = append(out, tp.Part)
	}
	return out
}

// memoryArtifactMarker renders the 📄 provenance marker for a recalled part.
// The explicit "memory artifact #ID" cue lets the model distinguish a
// historical FilePart from a live attachment that may share the same name —
// Telegram photos default to "photo.jpg" both on download and in storage.
func memoryArtifactMarker(tp TaggedPart) string {
	dateStr := tp.CreatedAt.Format("2 Jan 2006")
	if tp.CreatedAt.Year() == time.Now().Year() {
		// For current year, show shorter format (e.g., "31 Jan")
		dateStr = tp.CreatedAt.Format("2 Jan")
	}
	return fmt.Sprintf("📄 %s — memory artifact #%d (%s)", tp.Name, tp.ArtifactID, dateStr)
}

// currentMediaMarker returns the localized "from the CURRENT message" marker
// for a media kind. Falls back to the generic bot.current_media_marker if a
// per-kind key is missing. Paired with the 📄 memory marker, it lets the model
// tell a live attachment apart from a recalled one — and, for voice, pairs
// with voice_instruction to stop the model transcribing a recalled 📄 audio as
// the current one.
func (l *Laplace) currentMediaMarker(kind MediaKind) string {
	if marker := l.translator.Get(l.cfg.Bot.Language, currentMediaMarkerKeyFor(kind)); marker != "" {
		return marker
	}
	return l.translator.Get(l.cfg.Bot.Language, "bot.current_media_marker")
}

// currentMediaMarkerKeyFor maps a media kind to its per-kind marker i18n key.
func currentMediaMarkerKeyFor(kind MediaKind) string {
	switch kind {
	case KindAudio:
		return "bot.current_media_marker_voice"
	case KindVideo:
		return "bot.current_media_marker_video"
	case KindImage:
		return "bot.current_media_marker_image"
	default:
		return "bot.current_media_marker_file"
	}
}

// currentMediaMarkerKey maps a data-URL MIME prefix to its per-kind marker
// key. Kept as a composition of the typed helpers so the MIME→marker mapping
// stays pinned by its test.
func currentMediaMarkerKey(dataURL string) string {
	return currentMediaMarkerKeyFor(mediaKindFromDataURL(dataURL))
}
