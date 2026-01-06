package rag

import (
	"context"
	"fmt"
	"sort"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/storage"
)

// DatabaseHealth contains diagnostic information about the database.
type DatabaseHealth struct {
	TotalTopics      int         `json:"total_topics"`
	OrphanedTopics   int         `json:"orphaned_topics"`
	ZeroSizeTopics   int         `json:"zero_size_topics"`
	LargeTopics      int         `json:"large_topics"`
	OverlappingPairs int         `json:"overlapping_pairs"`
	FactsOnOrphaned  int         `json:"facts_on_orphaned"`
	AvgTopicSize     int         `json:"avg_topic_size"`
	LargeTopicsList  []TopicInfo `json:"large_topics_list,omitempty"`
}

// TopicInfo contains brief topic information for display.
type TopicInfo struct {
	ID        int64  `json:"id"`
	SizeChars int    `json:"size_chars"`
	Summary   string `json:"summary"`
}

// RepairStats contains statistics about repair operations.
type RepairStats struct {
	OrphanedTopicsDeleted int `json:"orphaned_topics_deleted"`
	FactsRelinked         int `json:"facts_relinked"`
	SizesRecalculated     int `json:"sizes_recalculated"`
}

// GetDatabaseHealth returns diagnostic information about database health.
// If userID is 0, returns stats for all users.
func (s *Service) GetDatabaseHealth(ctx context.Context, userID int64, largeThreshold int) (*DatabaseHealth, error) {
	if largeThreshold == 0 {
		largeThreshold = 25000
	}

	health := &DatabaseHealth{}

	// Get topics (all or for specific user)
	var topics []storage.Topic
	var err error
	if userID == 0 {
		topics, err = s.topicRepo.GetAllTopics()
	} else {
		topics, err = s.topicRepo.GetTopics(userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get topics: %w", err)
	}
	health.TotalTopics = len(topics)

	// Calculate stats
	var totalSize int
	for _, t := range topics {
		totalSize += t.SizeChars

		if t.SizeChars == 0 {
			health.ZeroSizeTopics++
		}
		if t.SizeChars > largeThreshold {
			health.LargeTopics++
			health.LargeTopicsList = append(health.LargeTopicsList, TopicInfo{
				ID:        t.ID,
				SizeChars: t.SizeChars,
				Summary:   t.Summary,
			})
		}
	}

	// Sort large topics by size descending
	sort.Slice(health.LargeTopicsList, func(i, j int) bool {
		return health.LargeTopicsList[i].SizeChars > health.LargeTopicsList[j].SizeChars
	})

	if len(topics) > 0 {
		health.AvgTopicSize = totalSize / len(topics)
	}

	// Count orphaned topics (topics with no messages linked)
	orphaned, err := s.maintenanceRepo.CountOrphanedTopics(userID)
	if err != nil {
		s.logger.Warn("failed to count orphaned topics", "error", err)
	} else {
		health.OrphanedTopics = orphaned
	}

	// Count overlapping topic pairs
	overlaps, err := s.maintenanceRepo.CountOverlappingTopics(userID)
	if err != nil {
		s.logger.Warn("failed to count overlapping topics", "error", err)
	} else {
		health.OverlappingPairs = overlaps
	}

	// Count facts on orphaned topics
	factsOnOrphaned, err := s.maintenanceRepo.CountFactsOnOrphanedTopics(userID)
	if err != nil {
		s.logger.Warn("failed to count facts on orphaned topics", "error", err)
	} else {
		health.FactsOnOrphaned = factsOnOrphaned
	}

	return health, nil
}

// RepairDatabase fixes database integrity issues.
func (s *Service) RepairDatabase(ctx context.Context, userID int64, dryRun bool) (*RepairStats, error) {
	ctx = jobtype.WithJobType(ctx, jobtype.Background)

	stats := &RepairStats{}

	// 1. Get orphaned topic IDs
	orphanedIDs, err := s.maintenanceRepo.GetOrphanedTopicIDs(userID)
	if err != nil {
		return stats, fmt.Errorf("failed to get orphaned topics: %w", err)
	}
	stats.OrphanedTopicsDeleted = len(orphanedIDs)

	if !dryRun && len(orphanedIDs) > 0 {
		// Relink facts from orphaned topics before deleting
		relinked, err := s.relinkFactsFromOrphanedTopics(ctx, userID, orphanedIDs)
		if err != nil {
			s.logger.Error("failed to relink facts", "error", err)
		} else {
			stats.FactsRelinked = relinked
		}

		// Delete orphaned topics
		for _, id := range orphanedIDs {
			if err := s.topicRepo.DeleteTopicCascade(id); err != nil {
				s.logger.Error("failed to delete orphaned topic", "id", id, "error", err)
			}
		}
		s.logger.Info("Deleted orphaned topics", "count", len(orphanedIDs))
	}

	// 2. Recalculate size_chars for all topics
	if !dryRun {
		recalced, err := s.maintenanceRepo.RecalculateTopicSizes(userID)
		if err != nil {
			s.logger.Error("failed to recalculate sizes", "error", err)
		} else {
			stats.SizesRecalculated = recalced
		}
	} else {
		// For dry run, just count how many would be updated
		topics, _ := s.topicRepo.GetTopics(userID)
		stats.SizesRecalculated = len(topics)
	}

	// 3. Reload vectors after repair
	if !dryRun && (stats.OrphanedTopicsDeleted > 0 || stats.SizesRecalculated > 0) {
		if err := s.ReloadVectors(); err != nil {
			s.logger.Error("failed to reload vectors after repair", "error", err)
		}
	}

	return stats, nil
}

// relinkFactsFromOrphanedTopics moves facts from orphaned topics to valid ones.
func (s *Service) relinkFactsFromOrphanedTopics(ctx context.Context, userID int64, orphanedIDs []int64) (int, error) {
	if len(orphanedIDs) == 0 {
		return 0, nil
	}

	// Find a valid topic to relink to (most recent one)
	topics, err := s.topicRepo.GetTopics(userID)
	if err != nil {
		return 0, err
	}

	var validTopicID int64
	for _, t := range topics {
		isOrphaned := false
		for _, oid := range orphanedIDs {
			if t.ID == oid {
				isOrphaned = true
				break
			}
		}
		if !isOrphaned {
			validTopicID = t.ID
			break
		}
	}

	if validTopicID == 0 {
		// No valid topic found, facts will be deleted with cascade
		return 0, nil
	}

	// Update facts to point to valid topic
	var count int
	for _, orphanedID := range orphanedIDs {
		facts, err := s.factRepo.GetFactsByTopicID(orphanedID)
		if err != nil {
			continue
		}
		if len(facts) > 0 {
			if err := s.factRepo.UpdateFactTopic(orphanedID, validTopicID); err != nil {
				s.logger.Error("failed to update fact topic", "from", orphanedID, "to", validTopicID, "error", err)
				continue
			}
			count += len(facts)
		}
	}

	return count, nil
}

// ContaminationInfo contains information about cross-user data contamination.
type ContaminationInfo struct {
	TotalContaminated  int                         `json:"total_contaminated"`
	ContaminatedTopics []storage.ContaminatedTopic `json:"contaminated_topics"`
}

// ContaminationFixStats contains statistics about contamination repair.
type ContaminationFixStats struct {
	MessagesUnlinked int64 `json:"messages_unlinked"`
}

// GetContaminationInfo returns information about cross-user data contamination.
// If userID is 0, checks all users.
func (s *Service) GetContaminationInfo(ctx context.Context, userID int64) (*ContaminationInfo, error) {
	info := &ContaminationInfo{}

	// Count contaminated topics
	count, err := s.maintenanceRepo.CountContaminatedTopics(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to count contaminated topics: %w", err)
	}
	info.TotalContaminated = count

	// Get detailed info
	topics, err := s.maintenanceRepo.GetContaminatedTopics(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get contaminated topics: %w", err)
	}
	info.ContaminatedTopics = topics

	return info, nil
}

// FixContamination removes foreign messages from contaminated topics.
// If userID is 0, fixes all users.
func (s *Service) FixContamination(ctx context.Context, userID int64, dryRun bool) (*ContaminationFixStats, error) {
	stats := &ContaminationFixStats{}

	if dryRun {
		// For dry run, count what would be fixed
		topics, err := s.maintenanceRepo.GetContaminatedTopics(userID)
		if err != nil {
			return stats, err
		}
		var total int64
		for _, t := range topics {
			total += int64(t.ForeignMsgCnt)
		}
		stats.MessagesUnlinked = total
		return stats, nil
	}

	// Log contamination count BEFORE fix
	beforeCount, _ := s.maintenanceRepo.CountContaminatedTopics(userID)
	s.logger.Info("Contamination fix starting", "before_count", beforeCount, "user_id", userID)

	// Actually fix
	unlinked, err := s.maintenanceRepo.FixContaminatedTopics(userID)
	if err != nil {
		return stats, fmt.Errorf("failed to fix contaminated topics: %w", err)
	}
	stats.MessagesUnlinked = unlinked

	// Log contamination count AFTER fix SQL
	afterFixCount, _ := s.maintenanceRepo.CountContaminatedTopics(userID)
	s.logger.Info("Contamination fix SQL completed", "messages_unlinked", unlinked, "after_count", afterFixCount, "user_id", userID)

	if unlinked > 0 {
		// Recalculate topic sizes after fixing
		if _, err := s.maintenanceRepo.RecalculateTopicSizes(userID); err != nil {
			s.logger.Error("failed to recalculate sizes after contamination fix", "error", err)
		}

		// Force WAL checkpoint to ensure data is flushed to main database file
		if err := s.maintenanceRepo.Checkpoint(); err != nil {
			s.logger.Error("failed to checkpoint after contamination fix", "error", err)
		}

		// Reload vectors
		if err := s.ReloadVectors(); err != nil {
			s.logger.Error("failed to reload vectors after contamination fix", "error", err)
		}
	}

	return stats, nil
}
