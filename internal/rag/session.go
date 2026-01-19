package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/storage"
)

// ActiveSessionInfo contains information about unprocessed messages for a user.
type ActiveSessionInfo struct {
	UserID           int64
	MessageCount     int
	FirstMessageTime time.Time
	LastMessageTime  time.Time
	ContextSize      int // Total characters in session messages
}

// GetActiveSessions returns information about unprocessed messages (active sessions) for all users.
func (s *Service) GetActiveSessions() ([]ActiveSessionInfo, error) {
	users := s.cfg.Bot.AllowedUserIDs
	var sessions []ActiveSessionInfo

	for _, userID := range users {
		messages, err := s.msgRepo.GetUnprocessedMessages(userID)
		if err != nil {
			s.logger.Error("failed to fetch unprocessed messages for user", "user_id", userID, "error", err)
			continue
		}

		if len(messages) == 0 {
			continue
		}

		// Calculate total context size
		contextSize := 0
		for _, msg := range messages {
			contextSize += len(msg.Content)
		}

		sessions = append(sessions, ActiveSessionInfo{
			UserID:           userID,
			MessageCount:     len(messages),
			FirstMessageTime: messages[0].CreatedAt,
			LastMessageTime:  messages[len(messages)-1].CreatedAt,
			ContextSize:      contextSize,
		})
	}

	return sessions, nil
}

func (s *Service) ForceProcessUser(ctx context.Context, userID int64) (int, error) {
	// Mark as background job for metrics (force processing is a maintenance task)
	ctx = jobtype.WithJobType(ctx, jobtype.Background)

	// 1. Fetch unprocessed messages
	messages, err := s.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch unprocessed messages: %w", err)
	}

	if len(messages) == 0 {
		return 0, nil
	}

	// 2. Process all messages in chunks
	maxChunkSize := s.cfg.RAG.MaxChunkSize
	if maxChunkSize == 0 {
		maxChunkSize = 400
	}

	processedCount := 0
	var currentChunk []storage.Message

	for _, msg := range messages {
		currentChunk = append(currentChunk, msg)
		if len(currentChunk) >= maxChunkSize {
			if err := s.processChunk(ctx, userID, currentChunk); err != nil {
				return processedCount, fmt.Errorf("failed to process chunk: %w", err)
			}
			processedCount += len(currentChunk)
			currentChunk = []storage.Message{}
		}
	}

	// Process remaining messages
	if len(currentChunk) > 0 {
		if err := s.processChunk(ctx, userID, currentChunk); err != nil {
			return processedCount, fmt.Errorf("failed to process final chunk: %w", err)
		}
		processedCount += len(currentChunk)
	}

	return processedCount, nil
}

// ForceProcessUserWithProgress processes all unprocessed messages for a user with progress reporting.
// Unlike ForceProcessUser, this runs consolidation and fact extraction synchronously.
func (s *Service) ForceProcessUserWithProgress(ctx context.Context, userID int64, onProgress ProgressCallback) (*ProcessingStats, error) {
	// Mark as background job for metrics (force processing is a maintenance task)
	ctx = jobtype.WithJobType(ctx, jobtype.Background)

	stats := &ProcessingStats{}

	// 1. Fetch unprocessed messages
	messages, err := s.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch unprocessed messages: %w", err)
	}

	if len(messages) == 0 {
		onProgress(ProgressEvent{
			Stage:    "complete",
			Complete: true,
			Message:  "No messages to process",
			Stats:    stats,
		})
		return stats, nil
	}

	stats.MessagesProcessed = len(messages)

	// 2. Extract topics
	onProgress(ProgressEvent{
		Stage:   "topics",
		Current: 0,
		Total:   1,
		Message: "Extracting topics from messages...",
	})

	topicIDs, err := s.processChunkWithStats(ctx, userID, messages, stats)
	if err != nil {
		return stats, fmt.Errorf("failed to extract topics: %w", err)
	}
	stats.TopicsExtracted = len(topicIDs)

	onProgress(ProgressEvent{
		Stage:   "topics",
		Current: 1,
		Total:   1,
		Message: fmt.Sprintf("Extracted %d topics", len(topicIDs)),
	})

	// Build topic ID set for tracking (needed for consolidation and fact extraction)
	topicIDSet := make(map[int64]bool)
	for _, id := range topicIDs {
		topicIDSet[id] = true
	}

	// 3. Run consolidation synchronously
	onProgress(ProgressEvent{
		Stage:   "consolidation",
		Current: 0,
		Total:   1,
		Message: "Checking for similar topics to merge...",
	})

	mergedTopicIDs := s.runConsolidationSync(ctx, userID, topicIDs, stats)

	// Add merged topic IDs to the set for fact extraction
	for _, mergedID := range mergedTopicIDs {
		topicIDSet[mergedID] = true
	}

	stats.TopicsMerged = len(mergedTopicIDs)

	onProgress(ProgressEvent{
		Stage:   "consolidation",
		Current: 1,
		Total:   1,
		Message: fmt.Sprintf("Merged %d topics", len(mergedTopicIDs)),
	})

	// 4. Process facts for new topics
	// Acquire user lock to prevent concurrent people merges (BUG-013)
	if !s.tryStartProcessingUser(userID) {
		s.logger.Debug("user already being processed for facts, waiting...", "user_id", userID)
		// Wait and retry - another goroutine is processing this user
		for i := 0; i < 60; i++ { // Wait up to 60 seconds
			time.Sleep(1 * time.Second)
			if s.tryStartProcessingUser(userID) {
				break
			}
			if i == 59 {
				return stats, fmt.Errorf("timeout waiting for user processing lock")
			}
		}
	}
	defer s.finishProcessingUser(userID)

	// Re-fetch topics that weren't merged and need fact extraction
	pendingTopics, err := s.topicRepo.GetTopicsPendingFacts(userID)
	if err != nil {
		s.logger.Error("failed to get pending topics", "error", err)
	}

	// Filter to only include topics we just created (including merged topics)
	var toProcess []storage.Topic
	for _, t := range pendingTopics {
		// Process if: (created/merged in this session AND checked) OR (is consolidated)
		if (topicIDSet[t.ID] && t.ConsolidationChecked) || t.IsConsolidated {
			toProcess = append(toProcess, t)
		}
	}

	if len(toProcess) > 0 {
		onProgress(ProgressEvent{
			Stage:   "facts",
			Current: 0,
			Total:   len(toProcess),
			Message: fmt.Sprintf("Extracting facts from %d topics...", len(toProcess)),
		})

		for i, topic := range toProcess {
			onProgress(ProgressEvent{
				Stage:   "facts",
				Current: i,
				Total:   len(toProcess),
				Message: fmt.Sprintf("Processing topic %d/%d...", i+1, len(toProcess)),
			})

			// Try to acquire processing lock for this topic (prevents race with background loop)
			if !s.tryStartProcessingTopic(topic.ID) {
				s.logger.Debug("topic already being processed, skipping", "topic_id", topic.ID)
				continue
			}

			msgs, err := s.msgRepo.GetMessagesByTopicID(ctx, topic.ID)
			if err != nil {
				s.logger.Error("failed to get messages for topic", "topic_id", topic.ID, "error", err)
				s.finishProcessingTopic(topic.ID)
				continue
			}

			if len(msgs) == 0 {
				_ = s.topicRepo.SetTopicFactsExtracted(topic.ID, true)
				s.finishProcessingTopic(topic.ID)
				continue
			}

			factStats, err := s.memoryService.ProcessSessionWithStats(ctx, userID, msgs, topic.CreatedAt, topic.ID)
			if err != nil {
				s.logger.Error("failed to process facts", "topic_id", topic.ID, "error", err)
				s.finishProcessingTopic(topic.ID)
				continue
			}

			stats.FactsCreated += factStats.Created
			stats.FactsUpdated += factStats.Updated
			stats.FactsDeleted += factStats.Deleted
			// Aggregate people stats (v0.5.1)
			stats.PeopleAdded += factStats.PeopleAdded
			stats.PeopleUpdated += factStats.PeopleUpdated
			stats.PeopleMerged += factStats.PeopleMerged
			// Aggregate usage from fact extraction (cost is aggregated, tokens counted separately)
			stats.PromptTokens += factStats.PromptTokens
			stats.CompletionTokens += factStats.CompletionTokens
			stats.EmbeddingTokens += factStats.EmbeddingTokens
			if factStats.Cost != nil {
				if stats.TotalCost == nil {
					stats.TotalCost = new(float64)
				}
				*stats.TotalCost += *factStats.Cost
			}

			_ = s.topicRepo.SetTopicFactsExtracted(topic.ID, true)
			s.finishProcessingTopic(topic.ID)
		}

		// Build progress message
		progressMsg := fmt.Sprintf("Processed facts: %d created, %d updated, %d deleted",
			stats.FactsCreated, stats.FactsUpdated, stats.FactsDeleted)
		if stats.PeopleAdded > 0 || stats.PeopleUpdated > 0 || stats.PeopleMerged > 0 {
			progressMsg += fmt.Sprintf(" | People: %d added, %d updated, %d merged",
				stats.PeopleAdded, stats.PeopleUpdated, stats.PeopleMerged)
		}

		onProgress(ProgressEvent{
			Stage:   "facts",
			Current: len(toProcess),
			Total:   len(toProcess),
			Message: progressMsg,
		})
	}

	// Final progress
	onProgress(ProgressEvent{
		Stage:    "complete",
		Complete: true,
		Message:  "Processing complete",
		Stats:    stats,
	})

	return stats, nil
}
