package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// telegramCaptionLimit is the Telegram caption limit for sendPhoto /
// sendMediaGroup items (UTF-16 chars). We stay under to leave room for
// emoji and HTML entities.
const telegramCaptionLimit = 1000

// sendResponseWithGeneratedImages handles the reply path when Laplace
// produced one or more generated images as artifacts.
//
// Flow:
//  1. Load each artifact (user-isolated) and read its file bytes.
//  2. Prepend compact 🎨 markers to the text for history storage.
//  3. Save the assistant message to history and fetch its auto-assigned ID.
//  4. Link each generated artifact to that history row via UpdateMessageID.
//  5. Send photos (sendPhoto for 1, sendMediaGroup for 2–10) using the first
//     ~1000 chars of the response text as the caption.
//  6. Send any remaining text as follow-up text messages through the
//     standard finalizeResponse path.
//
// Returns total Telegram duration and count of API calls made.
func (b *Bot) sendResponseWithGeneratedImages(
	ctx context.Context,
	userID int64,
	chatID int64,
	messageThreadID int,
	replyToMsgID int,
	responseText string,
	artifactIDs []int64,
	logger *slog.Logger,
) (time.Duration, int) {
	if b.artifactRepo == nil {
		logger.Error("artifact repo not configured but generated artifacts present")
		return 0, 0
	}

	if b.fileStorage == nil {
		logger.Error("file storage not configured — cannot send generated images")
		return b.sendTextOnlyFallback(ctx, chatID, messageThreadID, userID, replyToMsgID, responseText, logger)
	}

	// 1. Load artifacts in the order produced and read their bytes.
	loaded := b.loadArtifactBytes(userID, artifactIDs, logger)
	if len(loaded) == 0 {
		logger.Error("no generated artifacts loadable from disk — falling back to text-only reply")
		return b.sendTextOnlyFallback(ctx, chatID, messageThreadID, userID, replyToMsgID, responseText, logger)
	}

	// 2. Compact history markers + text.
	historyContent := buildAssistantHistoryContent(loaded, responseText)

	// 3. Save assistant message to history (so artifacts can be linked).
	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: historyContent}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
	}

	// 4. Link each generated artifact to the new history row.
	if lastMsgs, err := b.msgRepo.GetRecentHistory(userID, 1); err == nil && len(lastMsgs) > 0 {
		assistantMsgID := lastMsgs[0].ID
		for _, la := range loaded {
			if err := b.artifactRepo.UpdateMessageID(userID, la.artifact.ID, assistantMsgID); err != nil {
				logger.Warn("failed to link generated artifact to assistant message",
					"artifact_id", la.artifact.ID, "error", err)
			}
		}
	}

	// 5. Split response text into caption + follow-up, then render the
	// caption as HTML so markdown (**bold**, _italic_, `code`) survives.
	captionPlain, followUp := splitCaption(responseText, telegramCaptionLimit)
	caption, captionParseMode := renderCaptionHTML(captionPlain, logger)

	tgStart := time.Now()
	calls := 0
	tgThread := intPtrOrNil(messageThreadID)

	// Split into Photo-size (Telegram will re-encode to ~1280 px) and
	// Document-size (preserves originals). Threshold default 2 MB covers
	// 2K/4K outputs. Telegram forbids mixing photos & documents in a single
	// media group, so we send two batches when both kinds are present.
	threshold := b.cfg.Agents.ImageGenerator.DocumentThresholdBytes
	var photoBatch, docBatch []loadedArtifact
	for _, la := range loaded {
		if threshold > 0 && len(la.data) > threshold {
			docBatch = append(docBatch, la)
		} else {
			photoBatch = append(photoBatch, la)
		}
	}

	// First batch gets the caption; subsequent batches go without to avoid
	// duplicating the prompt text. Docs usually carry more information
	// per file, so prefer putting caption there when both exist.
	captionOwner := "photo"
	if len(docBatch) > 0 && len(photoBatch) == 0 {
		captionOwner = "doc"
	} else if len(docBatch) > 0 && len(photoBatch) > 0 {
		// Mixed: put caption on docs (they're the "main" high-res result).
		captionOwner = "doc"
	}

	// High-res batch (documents).
	if len(docBatch) > 0 {
		logger.Info("sending generated images as documents (high-res)",
			"count", len(docBatch), "threshold", threshold)
		batchCaption := ""
		batchParseMode := ""
		batchReplyTo := replyToMsgID
		if captionOwner == "doc" {
			batchCaption = caption
			batchParseMode = captionParseMode
		}
		calls += b.sendArtifactsAsDocuments(ctx, chatID, tgThread, batchReplyTo, docBatch, batchCaption, batchParseMode, logger)
	}

	// Normal batch (photos).
	if len(photoBatch) > 0 {
		batchCaption := ""
		batchParseMode := ""
		batchReplyTo := replyToMsgID
		if captionOwner == "photo" {
			batchCaption = caption
			batchParseMode = captionParseMode
		} else {
			// Caption already went with docs; don't reply-to the user message
			// again (docs did that) — let photos sit below.
			batchReplyTo = 0
		}
		calls += b.sendArtifactsAsPhotos(ctx, chatID, tgThread, batchReplyTo, photoBatch, batchCaption, batchParseMode, logger)
	}

	// 6. Send any remaining text as follow-up messages.
	if strings.TrimSpace(followUp) != "" {
		responses, err := b.finalizeResponse(chatID, messageThreadID, userID, 0, followUp, logger)
		if err != nil {
			logger.Error("failed to finalize follow-up", "error", err)
		}
		b.sendResponses(ctx, chatID, responses, logger)
		calls += len(responses)
	}

	return time.Since(tgStart), calls
}

// sendTextOnlyFallback is the emergency path when media cannot be sent.
// Delivers the raw response as plain text.
func (b *Bot) sendTextOnlyFallback(
	ctx context.Context,
	chatID int64,
	messageThreadID int,
	userID int64,
	replyToMsgID int,
	responseText string,
	logger *slog.Logger,
) (time.Duration, int) {
	responses, err := b.finalizeResponse(chatID, messageThreadID, userID, replyToMsgID, responseText, logger)
	if err != nil {
		logger.Error("failed to finalize fallback response", "error", err)
	}
	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: responseText}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
	}
	tgStart := time.Now()
	b.sendResponses(ctx, chatID, responses, logger)
	return time.Since(tgStart), len(responses)
}

// loadedArtifact pairs an Artifact row with its on-disk bytes.
type loadedArtifact struct {
	artifact *storage.Artifact
	data     []byte
}

// sendArtifactsAsPhotos sends a batch as sendPhoto (1 item) or
// sendMediaGroup (2+). Returns the number of Telegram API calls made.
func (b *Bot) sendArtifactsAsPhotos(
	ctx context.Context,
	chatID int64,
	thread *int,
	replyTo int,
	batch []loadedArtifact,
	caption string,
	parseMode string,
	logger *slog.Logger,
) int {
	if len(batch) == 0 {
		return 0
	}
	if len(batch) == 1 {
		_, err := b.api.SendPhoto(ctx, telegram.SendPhotoRequest{
			ChatID:           chatID,
			MessageThreadID:  thread,
			PhotoData:        batch[0].data,
			PhotoFilename:    batch[0].artifact.OriginalName,
			Caption:          caption,
			ParseMode:        parseMode,
			ReplyToMessageID: replyTo,
		})
		if err != nil {
			logger.Error("sendPhoto failed", "error", err)
		}
		return 1
	}
	media := make([]telegram.InputMediaPhoto, 0, len(batch))
	for i, la := range batch {
		item := telegram.InputMediaPhoto{
			Data:     la.data,
			Filename: la.artifact.OriginalName,
		}
		if i == 0 {
			item.Caption = caption
			item.ParseMode = parseMode
		}
		media = append(media, item)
	}
	_, err := b.api.SendMediaGroup(ctx, telegram.SendMediaGroupRequest{
		ChatID:           chatID,
		MessageThreadID:  thread,
		Media:            media,
		ReplyToMessageID: replyTo,
	})
	if err != nil {
		logger.Error("sendMediaGroup failed", "error", err)
	}
	return 1
}

// sendArtifactsAsDocuments sends a batch as sendDocument (1 item) or
// sendMediaGroup-of-documents (2+). Media-group keeps the files visually
// grouped in the chat, which matters when the model returned 2–3 variants.
func (b *Bot) sendArtifactsAsDocuments(
	ctx context.Context,
	chatID int64,
	thread *int,
	replyTo int,
	batch []loadedArtifact,
	caption string,
	parseMode string,
	logger *slog.Logger,
) int {
	if len(batch) == 0 {
		return 0
	}
	if len(batch) == 1 {
		_, err := b.api.SendDocument(ctx, telegram.SendDocumentRequest{
			ChatID:           chatID,
			MessageThreadID:  thread,
			Data:             batch[0].data,
			Filename:         batch[0].artifact.OriginalName,
			Caption:          caption,
			ParseMode:        parseMode,
			ReplyToMessageID: replyTo,
		})
		if err != nil {
			logger.Error("sendDocument failed", "error", err)
		}
		return 1
	}
	media := make([]telegram.InputMediaDocument, 0, len(batch))
	for i, la := range batch {
		item := telegram.InputMediaDocument{
			Data:     la.data,
			Filename: la.artifact.OriginalName,
		}
		if i == 0 {
			item.Caption = caption
			item.ParseMode = parseMode
		}
		media = append(media, item)
	}
	_, err := b.api.SendMediaGroupDocuments(ctx, telegram.SendMediaGroupDocumentsRequest{
		ChatID:           chatID,
		MessageThreadID:  thread,
		Media:            media,
		ReplyToMessageID: replyTo,
	})
	if err != nil {
		logger.Error("sendMediaGroup(documents) failed", "error", err)
	}
	return 1
}

// loadArtifactBytes resolves each artifact by ID (user-isolated) and reads
// its file bytes. Artifacts that fail to load are skipped with a warning —
// the caller decides what to do with an empty result.
func (b *Bot) loadArtifactBytes(userID int64, ids []int64, logger *slog.Logger) []loadedArtifact {
	out := make([]loadedArtifact, 0, len(ids))
	for _, id := range ids {
		art, err := b.artifactRepo.GetArtifact(userID, id)
		if err != nil {
			logger.Warn("failed to load generated artifact", "artifact_id", id, "error", err)
			continue
		}
		if art == nil {
			logger.Warn("generated artifact not found", "artifact_id", id, "user_id", userID)
			continue
		}
		// GetArtifact already enforces user_id = ?. Still, be defensive.
		if art.UserID != userID {
			logger.Error("user isolation violation: artifact user_id mismatch",
				"artifact_id", id, "expected", userID, "got", art.UserID)
			continue
		}
		path := b.fileStorage.GetFullPath(art.FilePath)
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("failed to read generated artifact file",
				"artifact_id", id, "path", path, "error", err)
			continue
		}
		out = append(out, loadedArtifact{artifact: art, data: data})
	}
	return out
}

// buildAssistantHistoryContent formats the assistant history content as
// compact markers for each generated image followed by the free-text reply.
// Matches the existing document marker convention: "📄 filename (artifact:N)".
func buildAssistantHistoryContent(loaded []loadedArtifact, text string) string {
	var sb strings.Builder
	for _, la := range loaded {
		fmt.Fprintf(&sb, "🎨 %s (artifact:%d)\n", la.artifact.OriginalName, la.artifact.ID)
	}
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		sb.WriteString("\n")
		sb.WriteString(trimmed)
	}
	return sb.String()
}

// splitCaption splits a response into a caption suitable for the first
// photo (≤ limit chars) and the remaining text to send as follow-up.
// The split is on the last whitespace boundary before the limit so words
// stay intact.
func splitCaption(text string, limit int) (caption, followUp string) {
	// Escape HTML — Telegram interprets caption with parse_mode HTML if we
	// set that, but plain photo caption has no parse mode by default.
	// We'll use plain text for the caption and HTML for text follow-ups.
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	// Telegram counts caption length in UTF-16 code units; using rune
	// counting as a close-enough proxy keeps us well under the 1024 limit.
	runes := []rune(text)
	if len(runes) <= limit {
		return text, ""
	}
	// Find last whitespace boundary before limit.
	cut := limit
	for cut > 0 && !isSpaceRune(runes[cut-1]) && cut > limit-120 {
		cut--
	}
	if cut <= 0 {
		cut = limit
	}
	caption = strings.TrimSpace(string(runes[:cut]))
	followUp = strings.TrimSpace(string(runes[cut:]))
	return caption, followUp
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\n' || r == '\t' || r == '\r'
}

// renderCaptionHTML converts the caption from markdown to HTML with a
// caption-size budget check. If the HTML render exceeds Telegram's 1024
// UTF-16 char limit, or markdown.ToHTML fails, we fall back to the raw
// plain-text caption (Telegram accepts any text when parse_mode is unset).
//
// Returns the caption string and the parse_mode to use ("HTML" or "").
func renderCaptionHTML(plain string, logger *slog.Logger) (string, string) {
	if plain == "" {
		return "", ""
	}
	htmlStr, err := markdown.ToHTML(plain)
	if err != nil {
		logger.Warn("caption markdown → HTML failed, falling back to plain", "err", err)
		return plain, ""
	}
	// Telegram caption limit is 1024 UTF-16 code units.
	if markdown.UTF16Length(htmlStr) > 1024 {
		logger.Warn("HTML caption over 1024 chars, falling back to plain text",
			"utf16_len", markdown.UTF16Length(htmlStr))
		return plain, ""
	}
	return htmlStr, "HTML"
}
