package laplace

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
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

	// RAG Results and Relevant People (if any) - combined in user prompt
	var contextParts []string
	if len(contextData.RAGResults) > 0 {
		contextParts = append(contextParts, formatRAGResults(contextData.RAGResults, enrichedQuery))
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

	// Add Recent History (active session)
	for _, hMsg := range contextData.RecentHistory {
		var contentParts []interface{}

		if hMsg.Content == currentMessageContent && currentMessageParts != nil && hMsg.Role == "user" {
			contentParts = currentMessageParts
		} else {
			contentParts = []interface{}{
				openrouter.TextPart{Type: "text", Text: hMsg.Content},
			}
		}
		orMessages = append(orMessages, openrouter.Message{Role: hMsg.Role, Content: contentParts})
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
	data := &ContextData{}

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

		// Extract media parts for multimodal RAG
		var mediaParts []interface{}
		for _, part := range currentMessageParts {
			switch part.(type) {
			case openrouter.ImagePart, openrouter.FilePart:
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
			data.RAGInfo = debugInfo
			data.RelevantPeople = result.People // v0.5.1: selected by reranker
		}
	}

	return data, nil
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
		ragContent.WriteString(fmt.Sprintf("%d", topicRes.Topic.ID))
		ragContent.WriteString("\" summary=\"")
		ragContent.WriteString(escapeXMLAttr(topicRes.Topic.Summary))
		ragContent.WriteString("\" relevance=\"")
		ragContent.WriteString(fmt.Sprintf("%.2f", topicRes.Score))
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

			ragContent.WriteString(fmt.Sprintf("%d. %s\n", i+1, textContent))
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
