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

func (s *Service) runConsolidationLoop(ctx context.Context) {
	defer s.wg.Done()
	// Check every 10 minutes
	interval := 10 * time.Minute

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial run
	s.processConsolidation(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.processConsolidation(ctx)
		case <-s.consolidationTrigger:
			s.processConsolidation(ctx)
		}
	}
}

func (s *Service) processConsolidation(ctx context.Context) {
	// Mark as background job for metrics (consolidation is a maintenance task)
	ctx = jobtype.WithJobType(ctx, jobtype.Background)

	users := s.cfg.Bot.AllowedUserIDs
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}

		candidates, err := s.findMergeCandidates(userID)
		if err != nil {
			s.logger.Error("failed to find merge candidates", "user_id", userID, "error", err)
			continue
		}

		for _, candidate := range candidates {
			if ctx.Err() != nil {
				return
			}

			// Verify with LLM
			shouldMerge, newSummary, _, err := s.verifyMerge(ctx, candidate)
			if err != nil {
				s.logger.Error("failed to verify merge", "error", err)
				continue
			}

			if shouldMerge {
				if _, err := s.mergeTopics(ctx, candidate, newSummary); err != nil {
					s.logger.Error("failed to merge topics", "error", err)
				} else {
					s.logger.Info("Merged topics", "t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID, "new_summary", newSummary)
					// Reload vectors after successful merge
					s.wg.Add(1)
					go func() {
						defer s.wg.Done()
						if err := s.ReloadVectors(); err != nil {
							s.logger.Error("failed to reload vectors after merge", "error", err)
						}
					}()
					// Trigger new consolidation to handle the new state
					s.TriggerConsolidation()
					// Break to refresh candidates
					break
				}
			} else {
				// Mark T1 as checked
				if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic1.ID, true); err != nil {
					s.logger.Error("failed to mark topic checked", "id", candidate.Topic1.ID, "error", err)
				}
			}
		}
	}
}

func (s *Service) findMergeCandidates(userID int64) ([]storage.MergeCandidate, error) {
	candidates, err := s.topicRepo.GetMergeCandidates(userID)
	if err != nil {
		return nil, err
	}

	// Get max merged size from config (default 50K chars)
	maxMergedSize := s.cfg.RAG.MaxMergedSizeChars
	if maxMergedSize == 0 {
		maxMergedSize = 50000
	}

	var filteredCandidates []storage.MergeCandidate

	for _, candidate := range candidates {
		// Size check: don't create monster topics
		combinedSize := candidate.Topic1.SizeChars + candidate.Topic2.SizeChars
		if combinedSize > maxMergedSize {
			s.logger.Debug("Skipping merge due to size limit",
				"t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID,
				"combined_size", combinedSize, "max_size", maxMergedSize)
			// Mark both as checked to avoid re-processing
			if !candidate.Topic1.ConsolidationChecked {
				if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic1.ID, true); err != nil {
					s.logger.Error("failed to mark topic checked (size)", "id", candidate.Topic1.ID, "error", err)
				}
			}
			if !candidate.Topic2.ConsolidationChecked {
				if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic2.ID, true); err != nil {
					s.logger.Error("failed to mark topic checked (size)", "id", candidate.Topic2.ID, "error", err)
				}
			}
			continue
		}

		// Similarity check
		if len(candidate.Topic1.Embedding) > 0 && len(candidate.Topic2.Embedding) > 0 {
			sim := cosineSimilarity(candidate.Topic1.Embedding, candidate.Topic2.Embedding)
			threshold := s.cfg.RAG.ConsolidationSimilarityThreshold
			if threshold == 0 {
				threshold = 0.75
			}
			if float64(sim) < threshold {
				// Heuristic failed: topics are not similar enough.
				s.logger.Debug("Skipping merge due to low similarity", "t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID, "similarity", sim, "threshold", threshold)
				// Mark T1 as checked if not already.
				if !candidate.Topic1.ConsolidationChecked {
					if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic1.ID, true); err != nil {
						s.logger.Error("failed to mark topic checked (heuristic)", "id", candidate.Topic1.ID, "error", err)
					}
				}
				continue
			}
		} else {
			// If no embeddings, skip
			continue
		}

		filteredCandidates = append(filteredCandidates, candidate)
	}

	return filteredCandidates, nil
}

func (s *Service) verifyMerge(ctx context.Context, candidate storage.MergeCandidate) (bool, string, UsageInfo, error) {
	// Load user profile for context
	profile := s.formatUserProfileCompact(candidate.Topic1.UserID)

	promptTmpl := s.translator.Get(s.cfg.Bot.Language, "rag.topic_consolidation_prompt")
	prompt := fmt.Sprintf(promptTmpl, profile, candidate.Topic1.Summary, candidate.Topic2.Summary)

	model := s.cfg.RAG.TopicModel // Use same model as topic extraction
	if model == "" {
		model = "google/gemini-3-flash-preview"
	}

	resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: map[string]interface{}{"type": "json_object"},
		UserID:         candidate.Topic1.UserID,
	})
	if err != nil {
		return false, "", UsageInfo{}, err
	}

	usage := UsageInfo{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Cost:             resp.Usage.Cost,
	}

	if len(resp.Choices) == 0 {
		return false, "", usage, fmt.Errorf("empty response")
	}

	var result struct {
		ShouldMerge bool   `json:"should_merge"`
		NewSummary  string `json:"new_summary"`
	}
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &result); err != nil {
		return false, "", usage, err
	}

	return result.ShouldMerge, result.NewSummary, usage, nil
}

func (s *Service) mergeTopics(ctx context.Context, candidate storage.MergeCandidate, newSummary string) (UsageInfo, error) {
	// Fetch messages from T1 and T2 separately (don't include intermediate topics!)
	msgs1, err := s.msgRepo.GetMessagesByTopicID(ctx, candidate.Topic1.ID)
	if err != nil {
		return UsageInfo{}, fmt.Errorf("failed to get messages for topic1: %w", err)
	}
	msgs2, err := s.msgRepo.GetMessagesByTopicID(ctx, candidate.Topic2.ID)
	if err != nil {
		return UsageInfo{}, fmt.Errorf("failed to get messages for topic2: %w", err)
	}

	// Combine and sort by ID
	msgs := make([]storage.Message, 0, len(msgs1)+len(msgs2))
	msgs = append(msgs, msgs1...)
	msgs = append(msgs, msgs2...)
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })

	if len(msgs) == 0 {
		return UsageInfo{}, fmt.Errorf("no messages found for topics %d and %d", candidate.Topic1.ID, candidate.Topic2.ID)
	}

	// Create content for embedding (only messages from T1 and T2)
	var contentBuilder strings.Builder
	var sizeChars int
	for _, msg := range msgs {
		contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
		sizeChars += len(msg.Content)
	}
	embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", newSummary, contentBuilder.String())

	// Generate embedding
	resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: s.cfg.RAG.EmbeddingModel,
		Input: []string{embeddingInput},
	})
	if err != nil {
		return UsageInfo{}, err
	}
	usage := UsageInfo{
		TotalTokens: resp.Usage.TotalTokens,
		Cost:        resp.Usage.Cost,
	}
	if len(resp.Data) == 0 {
		return usage, fmt.Errorf("no embedding returned")
	}

	newTopic := storage.Topic{
		UserID:         candidate.Topic1.UserID,
		Summary:        newSummary,
		StartMsgID:     candidate.Topic1.StartMsgID,
		EndMsgID:       candidate.Topic2.EndMsgID,
		SizeChars:      sizeChars,
		Embedding:      resp.Data[0].Embedding,
		FactsExtracted: candidate.Topic1.FactsExtracted && candidate.Topic2.FactsExtracted,
		IsConsolidated: true,
		CreatedAt:      candidate.Topic2.CreatedAt,
	}

	// Add new topic WITHOUT updating all messages in range (to preserve intermediate topics)
	newTopicID, err := s.topicRepo.AddTopicWithoutMessageUpdate(newTopic)
	if err != nil {
		return usage, err
	}

	// Update only messages that belonged to T1 and T2 (filter by user_id to prevent cross-user contamination)
	if err := s.msgRepo.UpdateMessagesTopicInRange(ctx, candidate.Topic1.UserID, candidate.Topic1.StartMsgID, candidate.Topic1.EndMsgID, newTopicID); err != nil {
		s.logger.Error("failed to update messages for topic 1", "id", candidate.Topic1.ID, "error", err)
	}
	if err := s.msgRepo.UpdateMessagesTopicInRange(ctx, candidate.Topic2.UserID, candidate.Topic2.StartMsgID, candidate.Topic2.EndMsgID, newTopicID); err != nil {
		s.logger.Error("failed to update messages for topic 2", "id", candidate.Topic2.ID, "error", err)
	}

	// Update structured_facts
	if err := s.factRepo.UpdateFactTopic(candidate.Topic1.ID, newTopicID); err != nil {
		s.logger.Error("failed to update facts for topic 1", "id", candidate.Topic1.ID, "error", err)
	}
	if err := s.factRepo.UpdateFactTopic(candidate.Topic2.ID, newTopicID); err != nil {
		s.logger.Error("failed to update facts for topic 2", "id", candidate.Topic2.ID, "error", err)
	}

	// Update fact_history
	if err := s.factHistoryRepo.UpdateFactHistoryTopic(candidate.Topic1.ID, newTopicID); err != nil {
		s.logger.Error("failed to update fact history for topic 1", "id", candidate.Topic1.ID, "error", err)
	}
	if err := s.factHistoryRepo.UpdateFactHistoryTopic(candidate.Topic2.ID, newTopicID); err != nil {
		s.logger.Error("failed to update fact history for topic 2", "id", candidate.Topic2.ID, "error", err)
	}

	// Delete old topics
	if err := s.topicRepo.DeleteTopicCascade(candidate.Topic1.ID); err != nil {
		s.logger.Error("failed to delete topic 1", "id", candidate.Topic1.ID, "error", err)
	}
	if err := s.topicRepo.DeleteTopicCascade(candidate.Topic2.ID); err != nil {
		s.logger.Error("failed to delete topic 2", "id", candidate.Topic2.ID, "error", err)
	}

	return usage, nil
}
