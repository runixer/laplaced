package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// SplitStats contains statistics about topic splitting.
type SplitStats struct {
	TopicsProcessed  int
	TopicsCreated    int
	FactsRelinked    int
	PromptTokens     int
	CompletionTokens int
	EmbeddingTokens  int
	TotalCost        *float64
}

// AddCost adds cost to stats.
func (s *SplitStats) AddCost(cost *float64) {
	if cost != nil {
		if s.TotalCost == nil {
			s.TotalCost = new(float64)
		}
		*s.TotalCost += *cost
	}
}

// SplitLargeTopics finds and splits all topics larger than threshold.
// If userID is 0, processes all users.
func (s *Service) SplitLargeTopics(ctx context.Context, userID int64, thresholdChars int) (*SplitStats, error) {
	ctx = jobtype.WithJobType(ctx, jobtype.Background)

	stats := &SplitStats{}

	// Find large topics (all users or specific user)
	var topics []storage.Topic
	var err error
	if userID == 0 {
		topics, err = s.topicRepo.GetAllTopics()
	} else {
		topics, err = s.topicRepo.GetTopics(userID)
	}
	if err != nil {
		return stats, fmt.Errorf("failed to get topics: %w", err)
	}

	var largeTopics []storage.Topic
	for _, t := range topics {
		if t.SizeChars > thresholdChars {
			largeTopics = append(largeTopics, t)
		}
	}

	s.logger.Info("Found large topics to split", "user_id", userID, "count", len(largeTopics), "threshold", thresholdChars)

	// Sort by size descending (process biggest first)
	sort.Slice(largeTopics, func(i, j int) bool {
		return largeTopics[i].SizeChars > largeTopics[j].SizeChars
	})

	for _, topic := range largeTopics {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}

		newTopicIDs, splitStats, err := s.splitTopic(ctx, topic)
		if err != nil {
			s.logger.Error("failed to split topic", "topic_id", topic.ID, "error", err)
			continue
		}

		stats.TopicsProcessed++
		stats.TopicsCreated += len(newTopicIDs)
		stats.PromptTokens += splitStats.PromptTokens
		stats.CompletionTokens += splitStats.CompletionTokens
		stats.EmbeddingTokens += splitStats.EmbeddingTokens
		stats.AddCost(splitStats.TotalCost)

		s.logger.Info("Split topic successfully",
			"old_topic_id", topic.ID,
			"old_size", topic.SizeChars,
			"new_topics", len(newTopicIDs),
		)
	}

	// Reload vectors after all splits
	if stats.TopicsProcessed > 0 {
		if err := s.ReloadVectors(); err != nil {
			s.logger.Error("failed to reload vectors after split", "error", err)
		}
	}

	return stats, nil
}

// splitTopic splits a single large topic into smaller ones.
func (s *Service) splitTopic(ctx context.Context, topic storage.Topic) ([]int64, *SplitStats, error) {
	stats := &SplitStats{}

	// 1. Load all messages for this topic
	messages, err := s.msgRepo.GetMessagesByTopicID(ctx, topic.ID)
	if err != nil {
		return nil, stats, fmt.Errorf("failed to get messages: %w", err)
	}

	if len(messages) == 0 {
		s.logger.Warn("Topic has no messages, deleting", "topic_id", topic.ID)
		if err := s.topicRepo.DeleteTopicCascade(topic.ID); err != nil {
			return nil, stats, fmt.Errorf("failed to delete empty topic: %w", err)
		}
		return nil, stats, nil
	}

	s.logger.Info("Splitting topic", "topic_id", topic.ID, "messages", len(messages), "size_chars", topic.SizeChars)

	// 2. Extract sub-topics using Flash
	// Use split-specific prompt that encourages breaking into smaller parts
	extractedTopics, usage, err := s.extractTopicsForSplit(ctx, topic.UserID, messages)
	if err != nil {
		return nil, stats, fmt.Errorf("failed to extract topics: %w", err)
	}
	stats.PromptTokens = usage.PromptTokens
	stats.CompletionTokens = usage.CompletionTokens
	stats.AddCost(usage.Cost)

	// If extraction returned only 1 topic with same range, nothing to split
	if len(extractedTopics) <= 1 {
		s.logger.Info("Topic cannot be split further", "topic_id", topic.ID)
		// Mark as checked to avoid re-processing
		return nil, stats, nil
	}

	// 3. Validate coverage
	chunkStartID, chunkEndID := findChunkBounds(messages)

	var validTopics []ExtractedTopic
	for _, t := range extractedTopics {
		if t.StartMsgID < chunkStartID {
			t.StartMsgID = chunkStartID
		}
		if t.EndMsgID > chunkEndID {
			t.EndMsgID = chunkEndID
		}
		if t.StartMsgID > t.EndMsgID {
			continue
		}

		var foundMessages []storage.Message
		for _, msg := range messages {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				foundMessages = append(foundMessages, msg)
			}
		}

		if len(foundMessages) == 0 {
			continue
		}

		t.StartMsgID, t.EndMsgID = findChunkBounds(foundMessages)
		validTopics = append(validTopics, t)
	}

	if len(validTopics) <= 1 {
		s.logger.Info("After validation, topic cannot be split", "topic_id", topic.ID)
		return nil, stats, nil
	}

	// 4. Create embeddings for new topics
	var embeddingInputs []string
	for _, t := range validTopics {
		var contentBuilder strings.Builder
		for _, msg := range messages {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	embResp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: s.cfg.RAG.EmbeddingModel,
		Input: embeddingInputs,
	})
	if err != nil {
		return nil, stats, fmt.Errorf("failed to create embeddings: %w", err)
	}
	stats.EmbeddingTokens = embResp.Usage.TotalTokens
	stats.AddCost(embResp.Usage.Cost)

	if len(embResp.Data) != len(validTopics) {
		return nil, stats, fmt.Errorf("embedding count mismatch: got %d, expected %d", len(embResp.Data), len(validTopics))
	}

	sort.Slice(embResp.Data, func(i, j int) bool {
		return embResp.Data[i].Index < embResp.Data[j].Index
	})

	// 5. Create new topics
	var newTopicIDs []int64
	referenceDate := messages[len(messages)-1].CreatedAt

	for i, t := range validTopics {
		var sizeChars int
		for _, msg := range messages {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				sizeChars += len(msg.Content)
			}
		}

		newTopic := storage.Topic{
			UserID:               topic.UserID,
			Summary:              t.Summary,
			StartMsgID:           t.StartMsgID,
			EndMsgID:             t.EndMsgID,
			SizeChars:            sizeChars,
			Embedding:            embResp.Data[i].Embedding,
			CreatedAt:            referenceDate,
			FactsExtracted:       topic.FactsExtracted, // Inherit from parent
			ConsolidationChecked: true,                 // Prevent merger from re-merging split results
		}

		newID, err := s.topicRepo.AddTopicWithoutMessageUpdate(newTopic)
		if err != nil {
			s.logger.Error("failed to create new topic", "error", err)
			continue
		}
		newTopicIDs = append(newTopicIDs, newID)

		// Update messages to point to new topic (filter by user_id to prevent cross-user contamination)
		if err := s.msgRepo.UpdateMessagesTopicInRange(ctx, topic.UserID, t.StartMsgID, t.EndMsgID, newID); err != nil {
			s.logger.Error("failed to update messages", "error", err)
		}
	}

	// 6. Relink facts to first new topic
	// UpdateFactTopic updates all facts with oldTopicID to newTopicID
	if len(newTopicIDs) > 0 {
		facts, err := s.factRepo.GetFactsByTopicID(topic.ID)
		if err != nil {
			s.logger.Warn("failed to get facts for topic", "topic_id", topic.ID, "error", err)
		} else {
			stats.FactsRelinked = len(facts)
			if len(facts) > 0 {
				if err := s.factRepo.UpdateFactTopic(topic.ID, newTopicIDs[0]); err != nil {
					s.logger.Warn("failed to relink facts", "old_topic", topic.ID, "new_topic", newTopicIDs[0], "error", err)
					stats.FactsRelinked = 0
				}
			}
		}
	}

	// 7. Relink fact_history
	if len(newTopicIDs) > 0 {
		firstNewID := newTopicIDs[0]
		if err := s.factHistoryRepo.UpdateFactHistoryTopic(topic.ID, firstNewID); err != nil {
			s.logger.Warn("failed to update fact history", "old_topic", topic.ID, "new_topic", firstNewID, "error", err)
		}
	}

	// 8. Delete old topic
	if err := s.topicRepo.DeleteTopic(topic.ID); err != nil {
		s.logger.Error("failed to delete old topic", "topic_id", topic.ID, "error", err)
	}

	return newTopicIDs, stats, nil
}

// extractTopicsForSplit extracts topics with emphasis on splitting large conversations.
func (s *Service) extractTopicsForSplit(ctx context.Context, userID int64, messages []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	// Load user profile for context
	profile := s.formatUserProfileCompact(userID)

	// Use the split-specific prompt with profile
	promptTmpl := s.translator.Get(s.cfg.Bot.Language, "rag.topic_split_prompt")
	if promptTmpl == "" {
		// Fallback to regular extraction prompt with hint
		promptTmpl = s.translator.Get(s.cfg.Bot.Language, "rag.topic_extraction_prompt")
		promptTmpl = "%s\n\n" + promptTmpl + "\n\nIMPORTANT: This is a LARGE conversation. Break it into multiple smaller topics (aim for 10-25 messages per topic). Find natural boundaries: topic changes, time gaps, or shifts in focus."
	}
	prompt := fmt.Sprintf(promptTmpl, profile)

	return s.extractTopicsWithPrompt(ctx, userID, messages, prompt)
}

// formatUserProfileCompact creates a compact profile summary for splitter/merger.
func (s *Service) formatUserProfileCompact(userID int64) string {
	facts, err := s.factRepo.GetFacts(userID)
	if err != nil {
		s.logger.Warn("failed to load facts for profile", "error", err)
		return "(профиль не загружен)"
	}

	// Filter to identity and high-importance facts only
	var relevant []storage.Fact
	for _, f := range facts {
		if f.Type == "identity" || f.Importance >= 80 {
			relevant = append(relevant, f)
		}
	}

	if len(relevant) == 0 {
		return "(профиль пуст)"
	}

	var sb strings.Builder
	for _, f := range relevant {
		fmt.Fprintf(&sb, "- [%s] %s\n", f.Entity, f.Content)
	}
	return sb.String()
}

// extractTopicsWithPrompt is like extractTopics but with custom prompt.
func (s *Service) extractTopicsWithPrompt(ctx context.Context, userID int64, chunk []storage.Message, prompt string) ([]ExtractedTopic, UsageInfo, error) {
	type MsgItem struct {
		ID      int64  `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
		Date    string `json:"date"`
	}
	var items []MsgItem
	for _, m := range chunk {
		items = append(items, MsgItem{
			ID:      m.ID,
			Role:    m.Role,
			Content: m.Content,
			Date:    m.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	itemsBytes, _ := json.Marshal(items)

	fullPrompt := fmt.Sprintf("%s\n\nChat Log JSON:\n%s", prompt, string(itemsBytes))

	model := s.cfg.RAG.TopicModel
	if model == "" {
		model = "google/gemini-3-flash-preview"
	}

	schema := map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   "topic_extraction",
			"strict": true,
			"schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topics": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"summary": map[string]interface{}{
									"type":        "string",
									"description": "Подробное описание темы обсуждения.",
								},
								"start_msg_id": map[string]interface{}{
									"type":        "integer",
									"description": "ID первого сообщения в теме.",
								},
								"end_msg_id": map[string]interface{}{
									"type":        "integer",
									"description": "ID последнего сообщения в теме.",
								},
							},
							"required":             []string{"summary", "start_msg_id", "end_msg_id"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"topics"},
				"additionalProperties": false,
			},
		},
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "user", Content: fullPrompt},
		},
		ResponseFormat: schema,
		UserID:         userID,
	})
	if err != nil {
		return nil, UsageInfo{}, err
	}

	usage := UsageInfo{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Cost:             resp.Usage.Cost,
	}

	if len(resp.Choices) == 0 {
		return nil, usage, fmt.Errorf("empty response")
	}

	var result struct {
		Topics []ExtractedTopic `json:"topics"`
	}
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &result); err != nil {
		return nil, usage, fmt.Errorf("json parse error: %w", err)
	}

	return result.Topics, usage, nil
}
