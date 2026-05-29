package bot

import (
	"fmt"

	"github.com/runixer/laplaced/internal/files"
)

// incomingContent builds the textual content line for a message, mirroring the
// legacy telegram.Message.BuildContent exactly but over the neutral envelope:
//
//   - text present              -> "<prefix>: <text>"
//   - empty + photo             -> "<prefix>: (photo)"
//   - empty + voice             -> ""        (transcribed separately downstream)
//   - empty + document-family   -> "<prefix>: "  (image/pdf/video/text doc)
//   - empty + audio/video_note  -> ""        (no content line, legacy parity)
//   - empty + no file           -> ""
//
// im.Prefix is the pre-built display prefix supplied by the transport adapter
// (sender/time, or the forwarded-from label for Telegram forwards).
func (b *Bot) incomingContent(im IncomingMessage) string {
	text := im.Text
	if text == "" {
		switch firstFileKind(im.Files) {
		case files.FileTypePhoto:
			text = "(photo)"
		case files.FileTypeVoice:
			return ""
		case files.FileTypeImage, files.FileTypePDF, files.FileTypeVideo, files.FileTypeDocument:
			// messageText stays empty -> "<prefix>: "
		default:
			return "" // audio, video_note, or no file
		}
	}
	return fmt.Sprintf("%s: %s", im.Prefix, text)
}

// firstFileKind returns the Kind of the first attachment, or "" when none.
// Telegram messages carry at most one file, so this is unambiguous on the home
// path.
func firstFileKind(fs []files.IncomingFile) files.FileType {
	if len(fs) == 0 {
		return ""
	}
	return fs[0].Kind
}
