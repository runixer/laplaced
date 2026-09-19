package laplace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// BuildMessages assembles LLM messages from context data.
func (l *Laplace) BuildMessages(
	ctx context.Context,
	contextData *ContextData,
	currentMessageContent string,
	currentMessageParts []interface{},
	enrichedQuery string,
) []llm.Message {
	var orMessages []llm.Message

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

	contextData.TokenEstimates.SystemPrompt = llm.EstimateTextTokens(fullSystemPrompt)
	if fullSystemPrompt != "" {
		orMessages = append(orMessages, llm.Message{
			Role: "system",
			Content: []interface{}{
				llm.TextPart{Type: "text", Text: fullSystemPrompt},
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
		memoryContext := strings.Join(contextParts, "\n\n")
		contextData.TokenEstimates.MemoryContext = llm.EstimateTextTokens(memoryContext)
		orMessages = append(orMessages, llm.Message{
			Role: "user",
			Content: []interface{}{
				llm.TextPart{Type: "text", Text: memoryContext},
			},
		})
	}

	// v0.6.0: Load full artifact content for selected artifacts (Long Context RAG).
	// These parts ride WITH the current user message below — NOT as a separate
	// pre-history message. Vision models reliably attend to images adjacent to the
	// active question, but disclaim images buried earlier in a long history: with
	// Qwen/litellm the model "talks itself out of" a memory-injected image
	// ("у меня нет глаз для файлов из прошлого диалога"), especially once its own
	// earlier refusals are in the session. Adjacent to the current message it
	// reads them correctly (verified by replaying a failing request both ways).
	//
	// Recalled audio/video is NOT attached when the current message carries
	// its own audio or video track. The prompt asks the model to transcribe
	// only the live recording, but given two tracks it merges them anyway:
	// a memory voice of several minutes gets "spoken" at the end of today's
	// two-minute message, the reported duration is wrong, and old topics
	// leak into the reply. The summary of the skipped file still rides in
	// <artifact_context> above; its bytes return on a text-only turn.
	taggedCurrent := tagCurrentParts(currentMessageParts)
	var recalled []TaggedPart
	if len(contextData.SelectedArtifactIDs) > 0 {
		var err error
		recalled, err = l.artifactLoader().LoadWithOptions(ctx, contextData.UserID, contextData.SelectedArtifactIDs,
			LoadOptions{SkipAudioVideo: hasLiveAudioVideo(taggedCurrent)})
		if err != nil {
			l.logger.Warn("failed to load artifact content", "error", err)
		}
	}

	// Render provenance markers in one place (renderTagged). Current media is
	// marked (🎤/🎥/📷/📎) only when memory artifacts ride alongside — so the
	// model can't confuse a same-named "photo.jpg" pulled from memory (📄)
	// with what the user just sent. Without artifacts there is no ambiguity,
	// and the common single-attachment prompt stays byte-for-byte unchanged.
	currentParts := l.renderTagged(taggedCurrent, len(recalled) > 0)
	artifactParts := l.renderTagged(recalled, false)

	// Add Recent History (active session). The current (last user) message carries
	// the multimodal current-message parts plus any loaded artifact parts.
	currentMessageAdded := false
	for i, hMsg := range contextData.RecentHistory {
		var contentParts []interface{}

		isLastMessage := i == len(contextData.RecentHistory)-1
		if isLastMessage && hMsg.Role == "user" && (len(currentParts) > 0 || len(artifactParts) > 0) {
			if len(currentParts) > 0 {
				contentParts = append(contentParts, currentParts...)
			} else {
				contentParts = append(contentParts, llm.TextPart{Type: "text", Text: hMsg.Content})
			}
			contentParts = append(contentParts, artifactParts...)
			currentMessageAdded = true
		} else {
			contentParts = []interface{}{
				llm.TextPart{Type: "text", Text: hMsg.Content},
			}
		}
		orMessages = append(orMessages, llm.Message{Role: hMsg.Role, Content: contentParts})
	}

	// Fallback: if the current message wasn't added via history (edge case, e.g.
	// empty/assistant-tailed history), add it — with artifacts — as a trailing
	// user message so loaded files still sit adjacent to the turn.
	if !currentMessageAdded && (len(currentParts) > 0 || len(artifactParts) > 0) {
		parts := append([]interface{}{}, currentParts...)
		parts = append(parts, artifactParts...)
		orMessages = append(orMessages, llm.Message{Role: "user", Content: parts})
	}

	contextData.TokenEstimates.FinalTotal = llm.EstimateMessagesTokens(orMessages)
	return orMessages
}

// hasLiveAudioVideo reports whether the current message carries an audio or
// video attachment of its own.
func hasLiveAudioVideo(parts []TaggedPart) bool {
	for _, tp := range parts {
		if tp.Source == SourceCurrent && (tp.Kind == KindAudio || tp.Kind == KindVideo) {
			return true
		}
	}
	return false
}

// platformName maps the configured transport to the human-readable platform
// name shown to the model in the system prompt, so the bot does not always
// claim to be on Telegram. Formatting stays Markdown for every transport — the
// per-transport Renderer adapts it (Telegram→HTML, Time→native markdown).
func platformName(transport string) string {
	switch transport {
	case "mattermost":
		// "mattermost" is the transport (protocol) id; the deployed product users
		// know as "Time", so that is the model-facing platform name.
		return "Time"
	default:
		return "Telegram"
	}
}

// LoadContextData loads all context data needed for LLM generation.
func (l *Laplace) LoadContextData(
	ctx context.Context,
	userID storage.ScopeID,
	rawQuery string,
	currentMessageParts []interface{},
) (*ContextData, error) {
	return l.loadContextData(ctx, userID, rawQuery, currentMessageParts, l.cfg.Telegram.RichMessages.AnyEnabled())
}

func (l *Laplace) loadContextData(
	ctx context.Context,
	userID storage.ScopeID,
	rawQuery string,
	currentMessageParts []interface{},
	richOutput bool,
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
	isChannel := false
	if shared := agent.FromContext(ctx); shared != nil {
		data.ProfileFacts = shared.Profile
		data.RecentTopics = shared.RecentTopics
		data.InnerCircle = shared.InnerCircle
		isChannel = shared.IsChannel
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
	referenceTime := agent.ReferenceTime(ctx, nil)
	if referenceTime.IsZero() {
		referenceTime = time.Now()
	}
	botName := l.cfg.Agents.Chat.Name
	if botName == "" {
		botName = "Bot"
	}
	basePrompt, err := l.translator.GetTemplate(l.cfg.Bot.Language, "bot.system_prompt", prompts.LaplaceParams{
		Date:         referenceTime.Format("2006-01-02"),
		BotName:      botName,
		Platform:     platformName(l.cfg.Transport),
		KatexMath:    l.cfg.Transport == "mattermost" || (l.cfg.Transport == "telegram" && richOutput),
		RichMarkdown: l.cfg.Transport == "telegram" && richOutput,
		IsChannel:    isChannel,
		// The READ protocol section must track actual tool exposure: startup
		// wiring drops read_url from cfg.Tools when its fetcher fails to
		// initialize, and the prompt must not steer the model to a dead tool.
		ReadURL: l.cfg.ToolConfigured("read_url"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}
	data.BaseSystemPrompt = basePrompt
	// Instance-specific rules (deployment identity, support contact, house style)
	// live in config so the public default prompt stays generic. Wrap them in a
	// delimited section and separate with a blank line so they read as an
	// authoritative block rather than text glued onto the end of the prompt.
	if extra := strings.TrimSpace(l.cfg.Bot.SystemPromptExtra); extra != "" {
		data.BaseSystemPrompt += "\n\n<instance_context>\n" + extra + "\n</instance_context>"
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
			if _, ok := part.(llm.FilePart); ok {
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
			memoryQuery := rawQuery
			if debugInfo != nil && debugInfo.EnrichedQuery != "" {
				memoryQuery = debugInfo.EnrichedQuery
			}
			data.ArtifactResults = result.Artifacts               // v0.5.2
			data.SelectedArtifactIDs = result.SelectedArtifactIDs // v0.6.0: IDs selected by reranker for full content loading
			data.RelevantPeople = result.People                   // v0.5.1: selected by reranker
			data.RAGResults = fitTopicsToMemoryBudget(
				deduplicateTopics(result.Topics, recentHistory),
				data.ArtifactResults,
				data.RelevantPeople,
				memoryQuery,
				l.cfg.RAG.AnswerMemoryTokenBudget,
			)
			if debugInfo != nil {
				debugInfo.FinalContext = data.RAGResults
			}
			data.RAGInfo = debugInfo
		}
	}

	return data, nil
}

// fitTopicsToMemoryBudget keeps complete topics in retrieval order.
func fitTopicsToMemoryBudget(topics []rag.TopicSearchResult, artifacts []rag.ArtifactResult, people []storage.Person, query string, budget int) []rag.TopicSearchResult {
	if budget <= 0 || len(topics) == 0 {
		return topics
	}
	var fixed []string
	if len(artifacts) > 0 {
		fixed = append(fixed, formatArtifactResults(artifacts, query))
	}
	if len(people) > 0 {
		fixed = append(fixed, storage.FormatPeople(people, storage.TagRelevantPeople))
	}
	selected := make([]rag.TopicSearchResult, 0, len(topics))
	for _, topic := range topics {
		candidate := append(append([]rag.TopicSearchResult{}, selected...), topic)
		parts := append([]string{formatRAGResults(candidate, query)}, fixed...)
		if llm.EstimateTextTokens(strings.Join(parts, "\n\n")) > budget {
			break
		}
		selected = candidate
	}
	return selected
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
		// Mirror the "memory_<id>_" anchor that loadArtifactFullContent puts on the
		// FilePart filename and the inline 📄 text marker, so the summary block agrees
		// on the same disambiguating token. Without this, an artifact_context entry
		// for a Telegram photo reads `type="image (photo.jpg)"` while the actual
		// FilePart is `memory_<id>_photo.jpg`, which leaves a reasoning-mode model
		// no anchor to map summary → bytes when a live "photo.jpg" is also present.
		fileType := artifactRes.FileType
		if artifactRes.OriginalName != "" {
			fileType = fmt.Sprintf("%s (memory_%d_%s)", fileType, artifactRes.ArtifactID, artifactRes.OriginalName)
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
