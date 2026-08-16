package bot

import (
	"errors"
	"strings"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// projectTelegramEntityText selects the ordinary Telegram text surface and
// projects its matching MessageEntity array before it can be trimmed, merged,
// or otherwise transformed. Telegram offsets are measured against the raw
// source in UTF-16 code units, so applying them anywhere later would be wrong.
func (b *Bot) projectTelegramEntityText(message *telegram.Message) string {
	text := message.Text
	entities := message.Entities
	field := "text"
	if text == "" {
		text = message.Caption
		entities = message.CaptionEntities
		field = "caption"
	}
	if len(entities) == 0 {
		return text
	}

	projected, err := telegram.ProjectMessageEntities(text, entities)
	if err != nil {
		errorField := "validation"
		var projectionErr *telegram.MessageEntityProjectionError
		if errors.As(err, &projectionErr) {
			errorField = projectionErr.Field
		}
		b.logger.Warn("Telegram message entity projection rejected; using plain text",
			"field", field,
			"entities", len(entities),
			"reason", errorField,
		)
		return text
	}
	if projected.Partial {
		b.logger.Debug("Telegram message entity projection was partial",
			"field", field,
			"entities", projected.EntityCount,
		)
	}
	return projected.Markdown
}

const (
	ingressDispositionProcessable = "processable"
	ingressDispositionPartial     = "partial"
	ingressDispositionUnsupported = "unsupported"
	ingressDispositionInvalid     = "invalid"
)

// projectTelegramRich performs the pure rich projection at the Telegram
// boundary and maps its media descriptors into the existing neutral file
// pipeline. Downloading stays lazy and happens only after grouping.
func (b *Bot) projectTelegramRich(
	message *telegram.Message,
	userID storage.ScopeID,
	legacyHasContent bool,
) (string, []files.IncomingFile, *IngressMetadata) {
	meta := &IngressMetadata{Kind: "rich"}
	projected, err := telegram.ProjectRichMessage(message.RichMessage)
	meta.BlockCount = projected.Stats.Blocks
	meta.MediaCount = projected.Stats.Media
	meta.HasVisibleText = projected.HasVisibleText
	meta.Unknown = len(projected.UnknownKinds) > 0

	if err != nil {
		b.logger.Warn("Telegram rich message projection rejected",
			"disposition", ingressDispositionInvalid,
			"blocks", projected.Stats.Blocks,
			"media", projected.Stats.Media,
			"unknown", len(projected.UnknownKinds) > 0,
			"error", err,
		)
		if legacyHasContent {
			// Preserve independently usable legacy content in an anomalous mixed
			// message, but surface that the rich half was not consumed fully.
			meta.Disposition = ingressDispositionPartial
			return "", nil, meta
		}
		meta.Disposition = ingressDispositionInvalid
		return "", nil, meta
	}

	switch projected.Disposition {
	case telegram.RichDispositionAccepted:
		meta.Disposition = ingressDispositionProcessable
	case telegram.RichDispositionPartial:
		meta.Disposition = ingressDispositionPartial
	case telegram.RichDispositionUnsupported:
		meta.Disposition = ingressDispositionUnsupported
	default:
		meta.Disposition = ingressDispositionInvalid
	}
	if legacyHasContent && meta.Disposition == ingressDispositionProcessable {
		// Both native representations in one Message are anomalous. Preserve
		// both unique visible parts and mark the classification honestly.
		meta.Disposition = ingressDispositionPartial
	}

	return projected.Markdown, b.fileProcessor.ExtractRichMedia(projected.Media, userID), meta
}

func mergeIncomingText(legacy, rich string) string {
	legacy = strings.TrimSpace(legacy)
	rich = strings.TrimSpace(rich)
	switch {
	case legacy == "":
		return rich
	case rich == "", legacy == rich:
		return legacy
	default:
		return legacy + "\n\n" + rich
	}
}

func hasLegacyTelegramContent(message *telegram.Message, files []files.IncomingFile) bool {
	return message.Text != "" || message.Caption != "" || len(files) > 0
}
