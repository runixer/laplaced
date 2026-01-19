package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/merger"
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
				if newTopicID, _, err := s.mergeTopics(ctx, candidate, newSummary); err != nil {
					s.logger.Error("failed to merge topics", "error", err)
				} else {
					s.logger.Info("Merged topics", "t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID, "new_topic_id", newTopicID, "new_summary", newSummary)
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

		// Mark orphan topics (no potential merge partner) as checked
		// This unblocks fact extraction for topics at the end of the queue
		s.markOrphanTopicsChecked(userID)
	}
}

// markOrphanTopicsChecked marks topics that have no potential merge partner as consolidation-checked.
// A topic is an "orphan" if there's no unchecked topic within 100 message IDs after it.
func (s *Service) markOrphanTopicsChecked(userID int64) {
	pendingTopics, err := s.topicRepo.GetTopicsPendingFacts(userID)
	if err != nil {
		s.logger.Error("failed to get pending topics for orphan check", "error", err)
		return
	}

	for _, topic := range pendingTopics {
		if topic.ConsolidationChecked {
			continue
		}

		// Check if this topic has any potential merge partner
		hasPartner := false
		for _, other := range pendingTopics {
			if other.ID <= topic.ID {
				continue
			}
			// Check proximity (same logic as GetMergeCandidates: gap < 100 messages)
			gap := other.StartMsgID - topic.EndMsgID
			if gap > 0 && gap < 100 && !other.ConsolidationChecked {
				hasPartner = true
				break
			}
		}

		if !hasPartner {
			// No potential partner found, mark as checked
			if err := s.topicRepo.SetTopicConsolidationChecked(topic.ID, true); err != nil {
				s.logger.Error("failed to mark orphan topic checked", "id", topic.ID, "error", err)
			} else {
				s.logger.Debug("Marked orphan topic as checked", "id", topic.ID)
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
	if s.mergerAgent == nil {
		return false, "", UsageInfo{}, fmt.Errorf("merger agent not configured")
	}
	return s.verifyMergeViaAgent(ctx, candidate)
}

// verifyMergeViaAgent delegates merge verification to the Merger agent.
func (s *Service) verifyMergeViaAgent(ctx context.Context, candidate storage.MergeCandidate) (bool, string, UsageInfo, error) {
	userID := candidate.Topic1.UserID

	req := &agent.Request{
		Params: map[string]any{
			merger.ParamTopic1Summary: candidate.Topic1.Summary,
			merger.ParamTopic2Summary: candidate.Topic2.Summary,
			"user_id":                 userID,
		},
	}

	// Try to get SharedContext from ctx
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	}

	resp, err := s.mergerAgent.Execute(ctx, req)
	if err != nil {
		return false, "", UsageInfo{}, err
	}

	result, ok := resp.Structured.(*merger.Result)
	if !ok {
		return false, "", UsageInfo{}, fmt.Errorf("unexpected result type from merger agent")
	}

	usage := UsageInfo{
		PromptTokens:     resp.Tokens.Prompt,
		CompletionTokens: resp.Tokens.Completion,
		TotalTokens:      resp.Tokens.Total,
		Cost:             resp.Tokens.Cost,
	}

	return result.ShouldMerge, result.NewSummary, usage, nil
}

func (s *Service) mergeTopics(ctx context.Context, candidate storage.MergeCandidate, newSummary string) (int64, UsageInfo, error) {
	// Fetch messages from T1 and T2 separately (don't include intermediate topics!)
	msgs1, err := s.msgRepo.GetMessagesByTopicID(ctx, candidate.Topic1.ID)
	if err != nil {
		return 0, UsageInfo{}, fmt.Errorf("failed to get messages for topic1: %w", err)
	}
	msgs2, err := s.msgRepo.GetMessagesByTopicID(ctx, candidate.Topic2.ID)
	if err != nil {
		return 0, UsageInfo{}, fmt.Errorf("failed to get messages for topic2: %w", err)
	}

	// Combine and sort by ID
	msgs := make([]storage.Message, 0, len(msgs1)+len(msgs2))
	msgs = append(msgs, msgs1...)
	msgs = append(msgs, msgs2...)
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })

	if len(msgs) == 0 {
		return 0, UsageInfo{}, fmt.Errorf("no messages found for topics %d and %d", candidate.Topic1.ID, candidate.Topic2.ID)
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
		Model: s.cfg.Embedding.Model,
		Input: []string{embeddingInput},
	})
	if err != nil {
		return 0, UsageInfo{}, err
	}
	usage := UsageInfo{
		TotalTokens: resp.Usage.TotalTokens,
		Cost:        resp.Usage.Cost,
	}
	if len(resp.Data) == 0 {
		return 0, usage, fmt.Errorf("no embedding returned")
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
		// ConsolidationChecked stays false: merged topics can still merge with adjacent topics.
		// Fact extraction uses is_consolidated=true to proceed without waiting.
		CreatedAt: candidate.Topic2.CreatedAt,
	}

	// Add new topic WITHOUT updating all messages in range (to preserve intermediate topics)
	newTopicID, err := s.topicRepo.AddTopicWithoutMessageUpdate(newTopic)
	if err != nil {
		return 0, usage, err
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

	return newTopicID, usage, nil
}
