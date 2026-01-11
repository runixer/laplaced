package rag

import (
	"context"
	"time"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/storage"
)

func (s *Service) backgroundLoop(ctx context.Context) {
	defer s.wg.Done()
	interval, err := time.ParseDuration(s.cfg.RAG.BackfillInterval) // We can reuse this or use ChunkInterval
	if err != nil {
		interval = 1 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial run
	s.processAllUsers(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.processAllUsers(ctx)
		}
	}
}

func (s *Service) processAllUsers(ctx context.Context) {
	users := s.cfg.Bot.AllowedUserIDs
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}
		s.processTopicChunking(ctx, userID)
	}
}

func (s *Service) processTopicChunking(ctx context.Context, userID int64) {
	chunkDuration := s.cfg.RAG.GetChunkDuration()

	// 1. Fetch unprocessed messages (topic_id IS NULL)
	messages, err := s.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		s.logger.Error("failed to fetch unprocessed messages", "error", err)
		return
	}

	if len(messages) == 0 {
		return
	}

	// 2. Identify chunks
	maxChunkSize := s.cfg.RAG.MaxChunkSize
	if maxChunkSize == 0 {
		maxChunkSize = 400
	}
	var currentChunk []storage.Message
	if len(messages) > 0 {
		currentChunk = append(currentChunk, messages[0])
	}

	for i := 1; i < len(messages); i++ {
		prev := messages[i-1]
		curr := messages[i]

		// Time diff
		diff := curr.CreatedAt.Sub(prev.CreatedAt)

		if diff >= chunkDuration || len(currentChunk) >= maxChunkSize {
			// End of chunk found
			if ctx.Err() != nil {
				return
			}

			// Mark as background job for metrics (topic extraction is maintenance)
			runCtx := jobtype.WithJobType(context.Background(), jobtype.Background)
			runCtx, cancel := context.WithTimeout(runCtx, 10*time.Minute)
			err := s.processChunk(runCtx, userID, currentChunk)
			cancel()

			if err != nil {
				s.logger.Error("failed to process chunk", "error", err)
				// If failed, we return to retry later.
				return
			}
			currentChunk = []storage.Message{curr}
		} else {
			currentChunk = append(currentChunk, curr)
		}
	}

	// Process final chunk if enough time passed
	if len(currentChunk) > 0 {
		lastMsg := currentChunk[len(currentChunk)-1]
		if time.Since(lastMsg.CreatedAt) > chunkDuration {
			if ctx.Err() != nil {
				return
			}

			// Mark as background job for metrics (topic extraction is maintenance)
			runCtx := jobtype.WithJobType(context.Background(), jobtype.Background)
			runCtx, cancel := context.WithTimeout(runCtx, 10*time.Minute)
			err := s.processChunk(runCtx, userID, currentChunk)
			cancel()

			if err != nil {
				s.logger.Error("failed to process final chunk", "error", err)
			}
		}
	}
}

func (s *Service) factExtractionLoop(ctx context.Context) {
	defer s.wg.Done()
	interval := 1 * time.Minute // Check every minute

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial run
	s.processFactExtraction(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.processFactExtraction(ctx)
		}
	}
}

func (s *Service) processFactExtraction(ctx context.Context) {
	users := s.cfg.Bot.AllowedUserIDs
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}

		// Acquire user lock to prevent concurrent people merges (BUG-013)
		if !s.tryStartProcessingUser(userID) {
			s.logger.Debug("user already being processed for facts, skipping", "user_id", userID)
			continue
		}

		topics, err := s.topicRepo.GetTopicsPendingFacts(userID)
		if err != nil {
			s.logger.Error("failed to get pending topics", "error", err)
			s.finishProcessingUser(userID)
			continue
		}

		for _, topic := range topics {
			if ctx.Err() != nil {
				s.finishProcessingUser(userID)
				return
			}

			// Enforce chronological order: stop if we hit a topic that hasn't been checked for consolidation yet.
			// Exception: merged topics (is_consolidated=true) can proceed immediately since they're already consolidated.
			if !topic.ConsolidationChecked && !topic.IsConsolidated {
				// We stop processing for this user to ensure we don't skip ahead.
				// The consolidation loop will eventually mark this topic as checked (or merge it).
				break
			}

			// Try to acquire processing lock for this topic (prevents race with ForceProcess)
			if !s.tryStartProcessingTopic(topic.ID) {
				s.logger.Debug("topic already being processed, skipping", "topic_id", topic.ID)
				continue
			}

			// Get messages for this topic
			msgs, err := s.msgRepo.GetMessagesByTopicID(ctx, topic.ID)
			if err != nil {
				s.logger.Error("failed to get messages for topic", "topic_id", topic.ID, "error", err)
				s.finishProcessingTopic(topic.ID)
				continue
			}

			if len(msgs) == 0 {
				// Empty topic? Mark processed.
				_ = s.topicRepo.SetTopicFactsExtracted(topic.ID, true)
				s.finishProcessingTopic(topic.ID)
				continue
			}

			// Process facts
			// Use a detached context to ensure completion even if shutdown is triggered
			runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			err = s.memoryService.ProcessSession(runCtx, userID, msgs, topic.CreatedAt, topic.ID)
			cancel()

			if err != nil {
				s.logger.Error("failed to process facts for topic", "topic_id", topic.ID, "error", err)
				// Don't mark processed so we retry? Or mark processed to avoid stuck?
				// Let's retry later.
				s.finishProcessingTopic(topic.ID)
				continue
			}

			// Mark processed
			if err := s.topicRepo.SetTopicFactsExtracted(topic.ID, true); err != nil {
				s.logger.Error("failed to mark topic processed", "topic_id", topic.ID, "error", err)
			}
			s.finishProcessingTopic(topic.ID)
		}

		// Release user lock after processing all topics for this user
		s.finishProcessingUser(userID)
	}
}
