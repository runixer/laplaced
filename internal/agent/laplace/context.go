package laplace

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// BuildMessages assembles OpenRouter messages from context data.
func (l *Laplace) BuildMessages(
	ctx context.Context,
	contextData *ContextData,
	currentMessageContent string,
	currentMessageParts []interface{},
	enrichedQuery string,
) []openrouter.Message {
	var orMessages []openrouter.Message

	// System Prompt + Core Identity + Inner Circle
	fullSystemPrompt := contextData.BaseSystemPrompt
	if contextData.ProfileFacts != "" {
		fullSystemPrompt += "\n\n" + contextData.ProfileFacts
	}
	if contextData.InnerCircle != "" {
		fullSystemPrompt += "\n\n" + contextData.InnerCircle
	}
	if contextData.RecentTopics != "" {
		fullSystemPrompt += "\n\n" + contextData.RecentTopics
	}

	if fullSystemPrompt != "" {
		orMessages = append(orMessages, openrouter.Message{
			Role: "system",
			Content: []interface{}{
				openrouter.TextPart{Type: "text", Text: fullSystemPrompt},
			},
		})
	}

	// RAG Results, Artifacts, and Relevant People (if any) - combined in user prompt
	var contextParts []string
	if len(contextData.RAGResults) > 0 {
		contextParts = append(contextParts, formatRAGResults(contextData.RAGResults, enrichedQuery))
	}
	if len(contextData.ArtifactResults) > 0 {
		contextParts = append(contextParts, formatArtifactResults(contextData.ArtifactResults, enrichedQuery))
	}
	if len(contextData.RelevantPeople) > 0 {
		contextParts = append(contextParts, storage.FormatPeople(contextData.RelevantPeople, storage.TagRelevantPeople))
	}
	if len(contextParts) > 0 {
		orMessages = append(orMessages, openrouter.Message{
			Role: "user",
			Content: []interface{}{
				openrouter.TextPart{Type: "text", Text: strings.Join(contextParts, "\n\n")},
			},
		})
	}

	// v0.6.0: Load full artifact content for selected artifacts (Long Context RAG)
	if len(contextData.SelectedArtifactIDs) > 0 {
		artifactParts, err := l.loadArtifactFullContent(ctx, contextData.UserID, contextData.SelectedArtifactIDs)
		if err != nil {
			l.logger.Warn("failed to load artifact content", "error", err)
		}
		// Insert loaded content before recent history so the LLM has full file context
		if len(artifactParts) > 0 {
			orMessages = append(orMessages, openrouter.Message{
				Role:    "user",
				Content: artifactParts,
			})
		}
	}

	// Add Recent History (active session)
	currentMessageAdded := false
	for i, hMsg := range contextData.RecentHistory {
		var contentParts []interface{}

		// For the last user message in history, use multimodal parts if available
		// We check if this is the last message AND it's a user message AND we have multimodal content
		isLastMessage := i == len(contextData.RecentHistory)-1
		if isLastMessage && hMsg.Role == "user" && currentMessageParts != nil && len(currentMessageParts) > 0 {
			// Use multimodal content for the current user message
			contentParts = currentMessageParts
			currentMessageAdded = true
		} else {
			contentParts = []interface{}{
				openrouter.TextPart{Type: "text", Text: hMsg.Content},
			}
		}
		orMessages = append(orMessages, openrouter.Message{Role: hMsg.Role, Content: contentParts})
	}

	// Fallback: if current message wasn't added via history (edge case), add it separately
	if !currentMessageAdded && currentMessageParts != nil && len(currentMessageParts) > 0 {
		orMessages = append(orMessages, openrouter.Message{Role: "user", Content: currentMessageParts})
	}

	return orMessages
}

// LoadContextData loads all context data needed for LLM generation.
func (l *Laplace) LoadContextData(
	ctx context.Context,
	userID int64,
	rawQuery string,
	currentMessageParts []interface{},
) (*ContextData, error) {
	data := &ContextData{
		UserID: userID, // v0.6.0: Store userID for artifact loading
	}

	// Get unprocessed messages (session history)
	unprocessedHistory, err := l.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		l.logger.Error("failed to get unprocessed messages", "error", err)
	}

	// Load profile, recent topics, and inner circle from SharedContext if available
	if shared := agent.FromContext(ctx); shared != nil {
		data.ProfileFacts = shared.Profile
		data.RecentTopics = shared.RecentTopics
		data.InnerCircle = shared.InnerCircle
	} else {
		// Fallback: load directly
		allFacts, err := l.factRepo.GetFacts(userID)
		if err == nil {
			coreIdentityFacts := rag.FilterProfileFacts(allFacts)
			data.ProfileFacts = rag.FormatUserProfile(coreIdentityFacts)
		} else {
			l.logger.Error("failed to get facts", "error", err)
		}

		// Get Recent Topics
		if l.cfg.RAG.Enabled {
			recentTopicsCount := l.cfg.RAG.GetRecentTopicsInContext()
			if recentTopicsCount > 0 {
				recentTopics, err := l.ragService.GetRecentTopics(userID, recentTopicsCount)
				if err != nil {
					l.logger.Warn("failed to get recent topics", "error", err)
				} else {
					data.RecentTopics = rag.FormatRecentTopics(recentTopics)
				}
			}
		}
	}

	// Limit recent history
	recentHistory := unprocessedHistory
	if l.cfg.RAG.MaxContextMessages > 0 && len(recentHistory) > l.cfg.RAG.MaxContextMessages {
		recentHistory = recentHistory[len(recentHistory)-l.cfg.RAG.MaxContextMessages:]
	}
	data.RecentHistory = recentHistory

	// Build base system prompt
	botName := l.cfg.Agents.Chat.Name
	if botName == "" {
		botName = "Bot"
	}
	basePrompt, err := l.translator.GetTemplate(l.cfg.Bot.Language, "bot.system_prompt", prompts.LaplaceParams{
		BotName: botName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}
	data.BaseSystemPrompt = basePrompt
	if l.cfg.Bot.SystemPromptExtra != "" {
		data.BaseSystemPrompt += " " + l.cfg.Bot.SystemPromptExtra
	}

	// RAG Retrieval
	if l.cfg.RAG.Enabled {
		var enrichmentContext []storage.Message
		if len(recentHistory) > 1 {
			availableHistory := recentHistory[:len(recentHistory)-1]
			count := 5
			if len(availableHistory) < count {
				count = len(availableHistory)
			}
			enrichmentContext = availableHistory[len(availableHistory)-count:]
		}

		// Extract media parts for multimodal RAG (v0.6.0: unified on FilePart)
		var mediaParts []interface{}
		for _, part := range currentMessageParts {
			if _, ok := part.(openrouter.FilePart); ok {
				mediaParts = append(mediaParts, part)
			}
		}

		opts := &rag.RetrievalOptions{
			History:        enrichmentContext,
			SkipEnrichment: false,
			Source:         "auto",
			MediaParts:     mediaParts,
		}

		result, debugInfo, err := l.ragService.Retrieve(ctx, userID, rawQuery, opts)
		if err != nil {
			l.logger.Error("RAG retrieval failed", "error", err)
		} else if result != nil {
			data.RAGResults = deduplicateTopics(result.Topics, recentHistory)
			data.ArtifactResults = result.Artifacts               // v0.5.2
			data.SelectedArtifactIDs = result.SelectedArtifactIDs // v0.6.0: IDs selected by reranker for full content loading
			data.RAGInfo = debugInfo
			data.RelevantPeople = result.People // v0.5.1: selected by reranker
		}
	}

	return data, nil
}

// loadArtifactFullContent loads full artifact content as multimodal parts (v0.6.0).
// loadArtifactFullContent loads full file content for artifacts as FilePart structs.
//
// Complexity: MEDIUM (CC=37) - artifact loading, validation, size tracking
// Dependencies: artifactRepo, files storage
// Side effects: Reads files from disk
// Error handling: Skips failed artifacts, logs warnings
//
// Returns a slice of content parts (FilePart for all file types) for the given artifact IDs.
// Respects max and max_context_bytes from reranker.artifacts config (v0.6.0).
func (l *Laplace) loadArtifactFullContent(ctx context.Context, userID int64, artifactIDs []int64) ([]interface{}, error) {
	if l.artifactRepo == nil || len(artifactIDs) == 0 || l.storagePath == "" {
		return nil, nil
	}

	// Get limits from reranker config with defaults (v0.6.0)
	maxArtifacts := l.cfg.Agents.Reranker.Artifacts.Max
	if maxArtifacts <= 0 {
		maxArtifacts = 10
	}
	maxBytes := l.cfg.Agents.Reranker.Artifacts.MaxContextBytes
	if maxBytes <= 0 {
		maxBytes = 20 * 1024 * 1024 // 20MB default (aligned with Telegram Bot API limit)
	}

	var contentParts []interface{}
	var totalBytes int
	loadedCount := 0
	// Track successfully loaded artifact IDs for usage counting (v0.6.0)
	var loadedArtifactIDs []int64

	for _, artifactID := range artifactIDs {
		// Check artifact count limit (v0.6.0)
		if loadedCount >= maxArtifacts {
			l.logger.Info("artifact count limit reached",
				"loaded", loadedCount,
				"max", maxArtifacts,
				"remaining", len(artifactIDs)-loadedCount,
			)
			break
		}

		artifact, err := l.artifactRepo.GetArtifact(userID, artifactID)
		if err != nil {
			l.logger.Warn("failed to load artifact", "artifact_id", artifactID, "error", err)
			continue
		}
		if artifact == nil || artifact.State != "ready" {
			l.logger.Debug("artifact not ready", "artifact_id", artifactID, "state", artifact.State)
			continue
		}

		// Validate MIME type is supported by Gemini (skip unsupported old artifacts)
		if artifact.MimeType != "" && !files.IsGeminiSupported(artifact.MimeType) {
			l.logger.Debug("skipping artifact with unsupported MIME type",
				"artifact_id", artifactID,
				"mime_type", artifact.MimeType,
				"original_name", artifact.OriginalName,
			)
			continue
		}

		// Check cumulative size limit BEFORE reading file (v0.6.0)
		if totalBytes+int(artifact.FileSize) > maxBytes {
			l.logger.Info("artifact size limit would be exceeded",
				"artifact_id", artifactID,
				"artifact_size", artifact.FileSize,
				"current_total", totalBytes,
				"max_bytes", maxBytes,
			)
			break
		}

		// Build full file path
		fullPath := filepath.Join(l.storagePath, artifact.FilePath)

		// Read file content
		fileData, err := os.ReadFile(fullPath)
		if err != nil {
			l.logger.Warn("failed to read artifact file", "artifact_id", artifactID, "path", fullPath, "error", err)
			continue
		}

		// Update tracking (v0.6.0)
		totalBytes += len(fileData)
		loadedCount++
		loadedArtifactIDs = append(loadedArtifactIDs, artifact.ID)

		// Encode as base64
		base64Data := base64.StdEncoding.EncodeToString(fileData)

		// Build display name for the artifact
		displayName := artifact.OriginalName
		if displayName == "" {
			displayName = fmt.Sprintf("artifact_%d", artifact.ID)
		}

		// Format date for display (e.g., "31 янв 2026")
		dateStr := artifact.CreatedAt.Format("2 Jan 2006")
		if artifact.CreatedAt.Year() == time.Now().Year() {
			// For current year, show shorter format (e.g., "31 янв")
			dateStr = artifact.CreatedAt.Format("2 Jan")
		}

		// Add text label to identify the artifact (helps LLM follow artifact_protocol)
		contentParts = append(contentParts, openrouter.TextPart{
			Type: "text",
			Text: fmt.Sprintf("📄 %s (%s)", displayName, dateStr),
		})

		// Create appropriate content part based on file type
		switch artifact.FileType {
		case "pdf", "document":
			// PDF and documents use FilePart with data URL format
			fileName := artifact.OriginalName
			if fileName == "" {
				fileName = fmt.Sprintf("artifact_%d.pdf", artifact.ID)
			}
			// Build MIME type for data URL
			mimeType := artifact.MimeType
			if mimeType == "" {
				if artifact.FileType == "pdf" {
					mimeType = "application/pdf"
				} else {
					mimeType = "application/octet-stream"
				}
			}
			// Normalize MIME type for Gemini (e.g., text/x-web-markdown -> text/plain)
			mimeType = files.NormalizeMimeForGemini(mimeType)
			contentParts = append(contentParts, openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: fileName,
					FileData: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data),
				},
			})
		case "image", "photo":
			// Images use FilePart (v0.6.0: unified format)
			mimeType := artifact.MimeType
			if mimeType == "" {
				mimeType = "image/jpeg"
			}
			fileName := artifact.OriginalName
			if fileName == "" {
				fileName = fmt.Sprintf("image_%d", artifact.ID)
			}
			// Normalize MIME type for Gemini
			mimeType = files.NormalizeMimeForGemini(mimeType)
			contentParts = append(contentParts, openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: fileName,
					FileData: "data:" + mimeType + ";base64," + base64Data,
				},
			})
		case "voice", "audio", "video_note", "video":
			// Audio and video use FilePart
			fileName := artifact.OriginalName
			if fileName == "" {
				// Generate filename from type
				ext := "ogg" // default
				if artifact.MimeType != "" {
					switch artifact.MimeType {
					case "audio/mpeg", "audio/mp3":
						ext = "mp3"
					case "audio/wav":
						ext = "wav"
					case "audio/ogg":
						ext = "ogg"
					case "audio/m4a":
						ext = "m4a"
					case "video/mp4":
						ext = "mp4"
					}
				}
				fileName = fmt.Sprintf("artifact_%d.%s", artifact.ID, ext)
			}
			mimeType := artifact.MimeType
			if mimeType == "" {
				mimeType = "audio/ogg"
			}
			// Normalize MIME type for Gemini
			mimeType = files.NormalizeMimeForGemini(mimeType)
			contentParts = append(contentParts, openrouter.FilePart{
				Type: "file",
				File: openrouter.File{
					FileName: fileName,
					FileData: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data),
				},
			})
		default:
			l.logger.Warn("unknown artifact file type", "artifact_id", artifactID, "file_type", artifact.FileType)
			continue
		}

		l.logger.Debug("loaded artifact for context", "artifact_id", artifactID, "file_type", artifact.FileType, "size_bytes", len(fileData))
	}

	if loadedCount > 0 {
		l.logger.Debug("artifact loading complete",
			"loaded_count", loadedCount,
			"total_bytes", totalBytes,
			"max_artifacts", maxArtifacts,
			"max_bytes", maxBytes,
		)
	}

	// Track artifact usage (v0.6.0)
	// Synchronous to avoid race condition on shutdown (CRIT-1 fix).
	// Latency impact is negligible (~5-10ms) compared to LLM call.
	if len(loadedArtifactIDs) > 0 && l.artifactRepo != nil {
		if err := l.artifactRepo.IncrementContextLoadCount(userID, loadedArtifactIDs); err != nil {
			l.logger.Warn("failed to increment artifact load count", "error", err)
		}
	}

	return contentParts, nil
}

// deduplicateTopics removes messages from retrieved topics that are already present in recent history.
func deduplicateTopics(topics []rag.TopicSearchResult, recentHistory []storage.Message) []rag.TopicSearchResult {
	recentIDs := make(map[int64]bool, len(recentHistory))
	for _, msg := range recentHistory {
		recentIDs[msg.ID] = true
	}

	var filtered []rag.TopicSearchResult
	for _, topicRes := range topics {
		var filteredMsgs []storage.Message
		for _, msg := range topicRes.Messages {
			if !recentIDs[msg.ID] {
				filteredMsgs = append(filteredMsgs, msg)
			}
		}
		if len(filteredMsgs) > 0 {
			topicRes.Messages = filteredMsgs
			filtered = append(filtered, topicRes)
		}
	}
	return filtered
}

// formatRAGResults formats RAG results as XML for the LLM context.
func formatRAGResults(results []rag.TopicSearchResult, query string) string {
	if len(results) == 0 {
		return ""
	}

	var ragContent strings.Builder
	ragContent.WriteString("<retrieved_context query=\"")
	ragContent.WriteString(escapeXMLAttr(query))
	ragContent.WriteString("\">\n")

	for _, topicRes := range results {
		topicDate := topicRes.Topic.CreatedAt.Format("2006-01-02")
		if len(topicRes.Messages) > 0 {
			topicDate = topicRes.Messages[0].CreatedAt.Format("2006-01-02")
		}

		ragContent.WriteString("  <topic id=\"")
		fmt.Fprintf(&ragContent, "%d", topicRes.Topic.ID)
		ragContent.WriteString("\" summary=\"")
		ragContent.WriteString(escapeXMLAttr(topicRes.Topic.Summary))
		ragContent.WriteString("\" relevance=\"")
		fmt.Fprintf(&ragContent, "%.2f", topicRes.Score)
		ragContent.WriteString("\" date=\"")
		ragContent.WriteString(topicDate)
		ragContent.WriteString("\">\n")

		for i, msg := range topicRes.Messages {
			var textContent string

			if msg.Role == "user" {
				textContent = msg.Content
			} else {
				dateStr := msg.CreatedAt.Format("2006-01-02 15:04:05")
				roleTitle := cases.Title(language.English).String(msg.Role)
				textContent = fmt.Sprintf("[%s (%s)]: %s", roleTitle, dateStr, msg.Content)
			}

			fmt.Fprintf(&ragContent, "%d. %s\n", i+1, textContent)
		}

		ragContent.WriteString("  </topic>\n")
	}

	ragContent.WriteString("</retrieved_context>")
	return ragContent.String()
}

// escapeXMLAttr escapes special characters for use in XML attribute values.
func escapeXMLAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// formatArtifactResults formats artifact summaries as XML for the LLM context (v0.6.0).
func formatArtifactResults(artifacts []rag.ArtifactResult, query string) string {
	if len(artifacts) == 0 {
		return ""
	}

	var content strings.Builder
	content.WriteString("<artifact_context query=\"")
	content.WriteString(escapeXMLAttr(query))
	content.WriteString("\">\n")

	for _, artifactRes := range artifacts {
		fileType := artifactRes.FileType
		if artifactRes.OriginalName != "" {
			fileType = fmt.Sprintf("%s (%s)", fileType, artifactRes.OriginalName)
		}

		content.WriteString("  <artifact id=\"")
		fmt.Fprintf(&content, "%d", artifactRes.ArtifactID)
		content.WriteString("\" type=\"")
		content.WriteString(escapeXMLAttr(fileType))
		content.WriteString("\" relevance=\"")
		fmt.Fprintf(&content, "%.2f", artifactRes.Score)
		content.WriteString("\">\n")

		if artifactRes.Summary != "" {
			content.WriteString("    <summary>")
			content.WriteString(escapeXMLAttr(artifactRes.Summary))
			content.WriteString("</summary>\n")
		}

		if len(artifactRes.Keywords) > 0 {
			content.WriteString("    <keywords>")
			for i, kw := range artifactRes.Keywords {
				if i > 0 {
					content.WriteString(", ")
				}
				content.WriteString(escapeXMLText(kw))
			}
			content.WriteString("</keywords>\n")
		}

		content.WriteString("  </artifact>\n")
	}

	content.WriteString("</artifact_context>")
	return content.String()
}

// escapeXMLText escapes special characters for XML text content.
func escapeXMLText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
