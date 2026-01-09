package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// findChunkBounds returns the min and max message IDs in a chunk.
func findChunkBounds(chunk []storage.Message) (minID, maxID int64) {
	if len(chunk) == 0 {
		return 0, 0
	}
	minID, maxID = chunk[0].ID, chunk[0].ID
	for _, m := range chunk[1:] {
		if m.ID < minID {
			minID = m.ID
		}
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	return minID, maxID
}

// findStragglers returns message IDs from chunk that are not covered by any topic.
func findStragglers(chunk []storage.Message, topics []ExtractedTopic) []int64 {
	coveredIDs := make(map[int64]bool)
	for _, t := range topics {
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				coveredIDs[msg.ID] = true
			}
		}
	}

	var stragglers []int64
	for _, msg := range chunk {
		if !coveredIDs[msg.ID] {
			stragglers = append(stragglers, msg.ID)
		}
	}
	return stragglers
}

func (s *Service) processChunk(ctx context.Context, userID int64, chunk []storage.Message) error {
	if len(chunk) == 0 {
		return nil
	}
	s.logger.Info("Processing chunk", "user_id", userID, "count", len(chunk), "start", chunk[0].ID, "end", chunk[len(chunk)-1].ID)

	// 1. Extract Topics (Wait for completion)
	topics, _, err := s.extractTopics(ctx, userID, chunk)
	if err != nil {
		return fmt.Errorf("extract topics: %w", err)
	}

	// 2. Validate Coverage
	chunkStartID, chunkEndID := findChunkBounds(chunk)
	referenceDate := chunk[len(chunk)-1].CreatedAt

	var validTopics []ExtractedTopic

	for _, t := range topics {
		// Clamp IDs to chunk range
		if t.StartMsgID < chunkStartID {
			t.StartMsgID = chunkStartID
		}
		if t.EndMsgID > chunkEndID {
			t.EndMsgID = chunkEndID
		}

		if t.StartMsgID > t.EndMsgID {
			continue
		}

		// Collect actual message content for this topic
		var foundMessages []storage.Message
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				foundMessages = append(foundMessages, msg)
			}
		}

		if len(foundMessages) == 0 {
			s.logger.Warn("skipping topic with no messages", "summary", t.Summary, "start", t.StartMsgID, "end", t.EndMsgID)
			continue
		}

		// Update IDs to match actual messages found to ensure AddTopic updates them
		t.StartMsgID, t.EndMsgID = findChunkBounds(foundMessages)

		validTopics = append(validTopics, t)
	}

	// Check for stragglers (messages not covered by any topic)
	stragglerIDs := findStragglers(chunk, validTopics)
	if len(stragglerIDs) > 0 {
		s.logger.Warn("Topic extraction incomplete: stragglers detected", "count", len(stragglerIDs), "ids", stragglerIDs)
		// Log received topics for debug
		for i, t := range validTopics {
			s.logger.Debug("Received topic", "index", i, "start", t.StartMsgID, "end", t.EndMsgID, "summary", t.Summary)
		}
		// Log straggler content for debug
		for _, msgID := range stragglerIDs {
			for _, m := range chunk {
				if m.ID == msgID {
					s.logger.Debug("Straggler content", "id", m.ID, "content", m.Content)
					break
				}
			}
		}
		return fmt.Errorf("topic extraction incomplete: %d messages not covered", len(stragglerIDs))
	}

	// 3. Vectorize and Save Topics
	var embeddingInputs []string
	for _, t := range validTopics {
		var contentBuilder strings.Builder
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	if len(embeddingInputs) > 0 {
		embeddingStart := time.Now()
		resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
			Model: s.cfg.Embedding.Model,
			Input: embeddingInputs,
		})
		embeddingDuration := time.Since(embeddingStart).Seconds()
		if err != nil {
			RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, false, 0, nil)
			s.logger.Error("failed to create embeddings for topics", "error", err)
			return fmt.Errorf("create embeddings: %w", err)
		}
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)

		if len(resp.Data) != len(validTopics) {
			s.logger.Error("embedding count mismatch", "expected", len(validTopics), "got", len(resp.Data))
			return fmt.Errorf("embedding count mismatch")
		}

		// Ensure response data is sorted by index to match input order
		sort.Slice(resp.Data, func(i, j int) bool {
			return resp.Data[i].Index < resp.Data[j].Index
		})

		for i, t := range validTopics {
			// Calculate total size of message content in this topic
			var sizeChars int
			for _, msg := range chunk {
				if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
					sizeChars += len(msg.Content)
				}
			}

			topic := storage.Topic{
				UserID:         userID,
				Summary:        t.Summary,
				StartMsgID:     t.StartMsgID,
				EndMsgID:       t.EndMsgID,
				SizeChars:      sizeChars,
				Embedding:      resp.Data[i].Embedding,
				CreatedAt:      referenceDate,
				FactsExtracted: false, // Explicitly false, will be processed by factExtractionLoop
			}

			// AddTopic also updates messages in range with topic_id
			if _, err := s.topicRepo.AddTopic(topic); err != nil {
				s.logger.Error("failed to save topic", "error", err)
				continue
			}
		}
	}

	// Load new vectors (incremental)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.LoadNewVectors(); err != nil {
			s.logger.Error("failed to load new vectors", "error", err)
		}
	}()

	// Trigger consolidation
	s.TriggerConsolidation()

	return nil
}

// processChunkWithStats processes a chunk and returns the created topic IDs.
func (s *Service) processChunkWithStats(ctx context.Context, userID int64, chunk []storage.Message, stats *ProcessingStats) ([]int64, error) {
	if len(chunk) == 0 {
		return nil, nil
	}
	s.logger.Info("Processing chunk with stats", "user_id", userID, "count", len(chunk))

	// 1. Extract Topics
	topics, topicUsage, err := s.extractTopics(ctx, userID, chunk)
	if err != nil {
		return nil, fmt.Errorf("extract topics: %w", err)
	}
	stats.AddChatUsage(topicUsage.PromptTokens, topicUsage.CompletionTokens, topicUsage.Cost)

	// 2. Validate Coverage
	chunkStartID, chunkEndID := findChunkBounds(chunk)
	referenceDate := chunk[len(chunk)-1].CreatedAt

	var validTopics []ExtractedTopic
	for _, t := range topics {
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
		for _, msg := range chunk {
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

	stragglerIDs := findStragglers(chunk, validTopics)
	if len(stragglerIDs) > 0 {
		return nil, fmt.Errorf("topic extraction incomplete: %d messages not covered", len(stragglerIDs))
	}

	// 3. Vectorize and Save Topics
	var topicIDs []int64
	var embeddingInputs []string

	for _, t := range validTopics {
		var contentBuilder strings.Builder
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	if len(embeddingInputs) > 0 {
		embeddingStart := time.Now()
		resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
			Model: s.cfg.Embedding.Model,
			Input: embeddingInputs,
		})
		embeddingDuration := time.Since(embeddingStart).Seconds()
		if err != nil {
			RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, false, 0, nil)
			return nil, fmt.Errorf("create embeddings: %w", err)
		}
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
		stats.AddEmbeddingUsage(resp.Usage.TotalTokens, resp.Usage.Cost)

		if len(resp.Data) != len(validTopics) {
			return nil, fmt.Errorf("embedding count mismatch")
		}

		sort.Slice(resp.Data, func(i, j int) bool {
			return resp.Data[i].Index < resp.Data[j].Index
		})

		for i, t := range validTopics {
			// Calculate total size of message content in this topic
			var sizeChars int
			for _, msg := range chunk {
				if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
					sizeChars += len(msg.Content)
				}
			}

			topic := storage.Topic{
				UserID:         userID,
				Summary:        t.Summary,
				StartMsgID:     t.StartMsgID,
				EndMsgID:       t.EndMsgID,
				SizeChars:      sizeChars,
				Embedding:      resp.Data[i].Embedding,
				CreatedAt:      referenceDate,
				FactsExtracted: false,
			}

			id, err := s.topicRepo.AddTopic(topic)
			if err != nil {
				s.logger.Error("failed to save topic", "error", err)
				continue
			}
			topicIDs = append(topicIDs, id)
		}
	}

	// Load new vectors synchronously
	if err := s.LoadNewVectors(); err != nil {
		s.logger.Error("failed to load new vectors", "error", err)
	}

	return topicIDs, nil
}

// runConsolidationSync runs consolidation for specific topics and returns merge count.
func (s *Service) runConsolidationSync(ctx context.Context, userID int64, topicIDs []int64, stats *ProcessingStats) int {
	mergeCount := 0

	for {
		candidates, err := s.findMergeCandidates(userID)
		if err != nil {
			s.logger.Error("failed to find merge candidates", "error", err)
			break
		}

		// Filter to candidates involving our topics
		var relevantCandidates []storage.MergeCandidate
		topicIDSet := make(map[int64]bool)
		for _, id := range topicIDs {
			topicIDSet[id] = true
		}

		for _, c := range candidates {
			if topicIDSet[c.Topic1.ID] || topicIDSet[c.Topic2.ID] {
				relevantCandidates = append(relevantCandidates, c)
			}
		}

		if len(relevantCandidates) == 0 {
			break
		}

		merged := false
		for _, candidate := range relevantCandidates {
			if ctx.Err() != nil {
				return mergeCount
			}

			shouldMerge, newSummary, verifyUsage, err := s.verifyMerge(ctx, candidate)
			stats.AddChatUsage(verifyUsage.PromptTokens, verifyUsage.CompletionTokens, verifyUsage.Cost)
			if err != nil {
				s.logger.Error("failed to verify merge", "error", err)
				continue
			}

			if shouldMerge {
				mergeUsage, err := s.mergeTopics(ctx, candidate, newSummary)
				stats.AddEmbeddingUsage(mergeUsage.TotalTokens, mergeUsage.Cost)
				if err != nil {
					s.logger.Error("failed to merge topics", "error", err)
				} else {
					mergeCount++
					merged = true
					s.logger.Info("Merged topics (sync)", "t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID)
					// Reload vectors after merge
					if err := s.ReloadVectors(); err != nil {
						s.logger.Error("failed to reload vectors", "error", err)
					}
					break // Refresh candidates
				}
			} else {
				if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic1.ID, true); err != nil {
					s.logger.Error("failed to mark topic checked", "id", candidate.Topic1.ID, "error", err)
				}
			}
		}

		if !merged {
			break
		}
	}

	// Mark remaining topics as consolidation checked
	for _, id := range topicIDs {
		_ = s.topicRepo.SetTopicConsolidationChecked(id, true)
	}

	return mergeCount
}
