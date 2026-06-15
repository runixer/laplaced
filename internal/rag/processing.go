package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
)

// maxStragglerIDsInTrace caps the straggler ID slice and content event so a
// pathological chunk can't blow up the trace size.
const maxStragglerIDsInTrace = 50

// minCoverageForSalvage is the fraction of a chunk that must land in some topic
// for us to salvage the rest (see salvageStragglers). Below it, the splitter's
// output is too far off to paper over and we fail the chunk so it is retried.
const minCoverageForSalvage = 0.5

// recordCoverage attaches splitter coverage diagnostics to the active span
// (rag.processChunk). Shared by both processChunk variants so the
// `{ span.splitter.coverage_ok = false }` TraceQL query catches either path.
//
// When stragglers > 0 and trace_content is on, also emits a content event with
// the actual message bodies so a debugger can see which texts the LLM dropped
// without bumping log level or hitting the DB.
func recordCoverage(ctx context.Context, chunk []storage.Message, stragglerIDs []int64) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}
	total := len(chunk)
	stragglers := len(stragglerIDs)
	covered := total - stragglers
	coveragePct := 1.0
	if total > 0 {
		coveragePct = float64(covered) / float64(total)
	}
	attrs := []attribute.KeyValue{
		attribute.Bool("splitter.coverage_ok", stragglers == 0),
		attribute.Int("splitter.straggler_count", stragglers),
		attribute.Float64("splitter.coverage_pct", coveragePct),
	}
	if stragglers > 0 {
		capped := stragglerIDs
		if len(capped) > maxStragglerIDsInTrace {
			capped = capped[:maxStragglerIDsInTrace]
		}
		attrs = append(attrs, attribute.Int64Slice("splitter.straggler_ids", capped))
	}
	span.SetAttributes(attrs...)

	if stragglers == 0 || !obs.ContentEnabled() {
		return
	}
	// Build a compact JSON of straggler bodies. Preview-trim each to keep the
	// event bounded; the chunk-context retrievable from the DB if more detail
	// is ever needed.
	type stragglerItem struct {
		ID      int64  `json:"id"`
		Role    string `json:"role"`
		Preview string `json:"preview"`
	}
	items := make([]stragglerItem, 0, len(stragglerIDs))
	for _, id := range stragglerIDs {
		if len(items) >= maxStragglerIDsInTrace {
			break
		}
		for _, m := range chunk {
			if m.ID != id {
				continue
			}
			preview, _ := obs.TextPreview(m.Content, obs.DefaultPreviewLen)
			items = append(items, stragglerItem{ID: m.ID, Role: m.Role, Preview: preview})
			break
		}
	}
	if body, err := json.Marshal(items); err == nil {
		obs.RecordContent(span, "splitter.stragglers", string(body))
	}
}

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

// salvageStragglers folds messages the splitter left out of every topic
// ("stragglers") into the nearest existing topic, widening that topic's
// [StartMsgID, EndMsgID] range to include them.
//
// Why this is needed: topic segmentation is done by an LLM. It occasionally
// ends one topic and begins the next with a short exchange in between that it
// assigns to neither — e.g. topics [661-670] and [674-679] skip 671-673. We
// require complete coverage (every message must belong to a topic, otherwise it
// is dropped from long-term memory and the messages stay forever "unprocessed"),
// but failing the whole session over a few orphaned messages is too brittle —
// the session would never archive, and a forced run surfaces an error to the
// user. Salvaging keeps the session intact: each orphan is attached to its
// closest topic by message-ID distance. The trade-off is that the topic's
// summary won't mention the salvaged messages, but the messages themselves are
// preserved under a topic and remain retrievable.
//
// Distances are measured against the ORIGINAL bounds (snapshotted up front) so
// the assignment doesn't depend on straggler order; the live bounds are widened
// as we go. With ties, the earlier topic wins.
func salvageStragglers(topics []ExtractedTopic, stragglerIDs []int64) []ExtractedTopic {
	if len(topics) == 0 {
		return topics
	}
	type bounds struct{ start, end int64 }
	orig := make([]bounds, len(topics))
	for i, t := range topics {
		orig[i] = bounds{t.StartMsgID, t.EndMsgID}
	}
	dist := func(b bounds, id int64) int64 {
		switch {
		case id < b.start:
			return b.start - id
		case id > b.end:
			return id - b.end
		default:
			return 0
		}
	}
	for _, id := range stragglerIDs {
		best, bestDist := 0, dist(orig[0], id)
		for i := 1; i < len(orig); i++ {
			if d := dist(orig[i], id); d < bestDist {
				best, bestDist = i, d
			}
		}
		if id < topics[best].StartMsgID {
			topics[best].StartMsgID = id
		}
		if id > topics[best].EndMsgID {
			topics[best].EndMsgID = id
		}
	}
	return topics
}

func (s *Service) processChunk(ctx context.Context, userID storage.ScopeID, chunk []storage.Message) error {
	if len(chunk) == 0 {
		return nil
	}
	s.logger.Info("Processing chunk", "user_id", userID, "count", len(chunk), "start", chunk[0].ID, "end", chunk[len(chunk)-1].ID)

	var topicsSaved int

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

	// Check for stragglers — messages the splitter left out of every topic.
	stragglerIDs := findStragglers(chunk, validTopics)
	recordCoverage(ctx, chunk, stragglerIDs)
	if len(stragglerIDs) > 0 {
		coverage := float64(len(chunk)-len(stragglerIDs)) / float64(len(chunk))
		// Too few messages landed in a topic to trust the segmentation. Salvaging
		// would bury a badly-wrong extraction under topics whose summaries cover
		// almost nothing, so fail and let the session be retried instead.
		if len(validTopics) == 0 || coverage < minCoverageForSalvage {
			s.logger.Warn("Topic extraction coverage too low to salvage; failing for retry",
				"covered_pct", coverage, "stragglers", len(stragglerIDs), "topics", len(validTopics), "ids", stragglerIDs)
			return fmt.Errorf("topic extraction incomplete: %d of %d messages not covered", len(stragglerIDs), len(chunk))
		}
		// Fold the orphaned messages into their nearest topic so the session still
		// archives and nothing is lost from long-term memory (see salvageStragglers).
		validTopics = salvageStragglers(validTopics, stragglerIDs)
		s.logger.Info("Salvaged uncovered messages into nearest topics",
			"salvaged", len(stragglerIDs), "ids", stragglerIDs, "covered_pct_before", coverage)
	}

	// 3. Vectorize and Save Topics
	var embeddingInputs []string
	for _, t := range validTopics {
		var contentBuilder strings.Builder
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				fmt.Fprintf(&contentBuilder, "[%s]: %s\n", msg.Role, msg.Content)
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	if len(embeddingInputs) > 0 {
		embeddingStart := time.Now()
		resp, err := s.client.CreateEmbeddings(ctx, llm.EmbeddingRequest{
			Model:      s.cfg.Embedding.Model,
			Dimensions: s.cfg.Embedding.Dimensions,
			Input:      embeddingInputs,
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
			topicsSaved++
		}
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		span.SetAttributes(attribute.Int("chunk.topics_saved", topicsSaved))
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
func (s *Service) processChunkWithStats(ctx context.Context, userID storage.ScopeID, chunk []storage.Message, stats *ProcessingStats) ([]int64, error) {
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
	recordCoverage(ctx, chunk, stragglerIDs)
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
				fmt.Fprintf(&contentBuilder, "[%s]: %s\n", msg.Role, msg.Content)
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	if len(embeddingInputs) > 0 {
		embeddingStart := time.Now()
		resp, err := s.client.CreateEmbeddings(ctx, llm.EmbeddingRequest{
			Model:      s.cfg.Embedding.Model,
			Dimensions: s.cfg.Embedding.Dimensions,
			Input:      embeddingInputs,
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

// runConsolidationSync runs consolidation for specific topics and returns merged topic IDs.
func (s *Service) runConsolidationSync(ctx context.Context, userID storage.ScopeID, topicIDs []int64, stats *ProcessingStats) []int64 {
	mergedTopicIDs := []int64{}

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
				return mergedTopicIDs
			}

			shouldMerge, newSummary, verifyUsage, err := s.verifyMerge(ctx, candidate)
			stats.AddChatUsage(verifyUsage.PromptTokens, verifyUsage.CompletionTokens, verifyUsage.Cost)
			if err != nil {
				s.logger.Error("failed to verify merge", "error", err)
				continue
			}

			if shouldMerge {
				newTopicID, mergeUsage, err := s.mergeTopics(ctx, candidate, newSummary)
				stats.AddEmbeddingUsage(mergeUsage.TotalTokens, mergeUsage.Cost)
				if err != nil {
					s.logger.Error("failed to merge topics", "error", err)
				} else {
					mergedTopicIDs = append(mergedTopicIDs, newTopicID)
					merged = true
					s.logger.Info("Merged topics (sync)", "t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID, "new_topic_id", newTopicID)
					// Reload vectors after merge
					if err := s.ReloadVectors(); err != nil {
						s.logger.Error("failed to reload vectors", "error", err)
					}
					break // Refresh candidates
				}
			} else {
				if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic1.UserID, candidate.Topic1.ID, true); err != nil {
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
		// Get userID from the first candidate (all candidates have same user in this loop)
		_ = s.topicRepo.SetTopicConsolidationChecked(userID, id, true)
	}

	return mergedTopicIDs
}
