package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

// sendResponseWithGeneratedImages handles the reply path when Laplace produced
// one or more generated images as artifacts. It is transport-neutral: it loads
// the artifacts, records history/links, builds a transport.OutgoingMedia, and
// hands it to the active transport. The per-transport delivery policy (Telegram
// photo-vs-document split, Mattermost multi-file posts) lives behind SendMedia.
//
// Flow:
//  1. Load each artifact (user-isolated) and read its file bytes.
//  2. Prepend compact 🎨 markers to the text for history storage.
//  3. Save the assistant message to history and fetch its auto-assigned ID.
//  4. Link each generated artifact to that history row via UpdateMessageID.
//  5. Split the response into a caption (≤ the transport's media caption budget)
//     and follow-up text; send the media with the caption, then the follow-up
//     through the standard rendered-text path.
//
// Returns total send duration and count of transport calls made.
func (b *Bot) sendResponseWithGeneratedImages(
	ctx context.Context,
	userID int64,
	convID, threadRoot, replyTo string,
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
		return b.sendTextOnlyFallback(ctx, convID, threadRoot, replyTo, userID, responseText, logger)
	}

	// 1. Load artifacts in the order produced and read their bytes.
	loaded := b.loadArtifactBytes(userID, artifactIDs, logger)
	if len(loaded) == 0 {
		logger.Error("no generated artifacts loadable from disk — falling back to text-only reply")
		return b.sendTextOnlyFallback(ctx, convID, threadRoot, replyTo, userID, responseText, logger)
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

	// 5. Split the caption to the transport's media caption budget; the rest is
	// sent as follow-up text. Caption is canonical markdown — the transport's
	// SendMedia renders it (HTML for Telegram, pass-through for Mattermost).
	caps := b.transport.Capabilities()
	captionLimit := caps.MaxMediaCaptionLen
	if captionLimit <= 0 {
		captionLimit = len([]rune(responseText)) // no budget configured: keep it all
	}
	caption, followUp := splitCaption(responseText, captionLimit)

	items := make([]OutgoingMediaItem, 0, len(loaded))
	for _, la := range loaded {
		items = append(items, OutgoingMediaItem{
			Data:     la.data,
			Filename: la.artifact.OriginalName,
			MIME:     la.artifact.MimeType,
		})
	}

	tgStart := time.Now()
	calls := 0
	if _, err := b.transport.SendMedia(ctx, OutgoingMedia{
		ConversationID: convID,
		ThreadRoot:     threadRoot,
		ReplyTo:        replyTo,
		Caption:        caption,
		Items:          items,
	}); err != nil {
		logger.Error("failed to send generated media", "error", err)
	} else {
		calls++
	}

	// 6. Send any remaining text as follow-up messages (no reply-to: the media
	// already anchored to the user's message).
	if strings.TrimSpace(followUp) != "" {
		calls += b.sendRendered(ctx, convID, threadRoot, "", followUp, logger)
	}

	return time.Since(tgStart), calls
}

// sendTextOnlyFallback is the emergency path when media cannot be sent.
// Delivers the raw response as plain text through the rendered-text path.
func (b *Bot) sendTextOnlyFallback(
	ctx context.Context,
	convID, threadRoot, replyTo string,
	userID int64,
	responseText string,
	logger *slog.Logger,
) (time.Duration, int) {
	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: responseText}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
	}
	tgStart := time.Now()
	sent := b.sendRendered(ctx, convID, threadRoot, replyTo, responseText, logger)
	return time.Since(tgStart), sent
}

// loadedArtifact pairs an Artifact row with its on-disk bytes.
type loadedArtifact struct {
	artifact *storage.Artifact
	data     []byte
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

// splitCaption splits a response into a caption suitable for the first media
// item (≤ limit chars) and the remaining text to send as follow-up. The split
// is on the last whitespace boundary before the limit so words stay intact.
func splitCaption(text string, limit int) (caption, followUp string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	// Caption length is counted in runes here as a close-enough proxy for the
	// transport's UTF-16/character budget.
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
