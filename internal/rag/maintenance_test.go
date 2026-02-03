package rag

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// TestServiceGetDatabaseHealth_Success tests successful health check.
func TestServiceGetDatabaseHealth_Success(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topics := []storage.Topic{
		{ID: 1, UserID: userID, Summary: "Small topic", SizeChars: 1000},
		{ID: 2, UserID: userID, Summary: "Large topic", SizeChars: 30000},
		{ID: 3, UserID: userID, Summary: "Zero topic", SizeChars: 0},
	}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()
	mockStore.On("CountOrphanedTopics", userID).Return(2, nil).Once()
	mockStore.On("CountOverlappingTopics", userID).Return(1, nil).Once()
	mockStore.On("GetOverlappingTopics", userID).Return([]storage.OverlappingPair{
		{Topic1ID: 1, Topic1Summary: "Topic 1", Topic2ID: 2, Topic2Summary: "Topic 2"},
	}, nil).Once()
	mockStore.On("CountFactsOnOrphanedTopics", userID).Return(5, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	health, err := svc.GetDatabaseHealth(context.Background(), userID, 25000)
	assert.NoError(t, err)

	assert.Equal(t, 3, health.TotalTopics)
	assert.Equal(t, 1, health.ZeroSizeTopics)
	assert.Equal(t, 1, health.LargeTopics)
	assert.Equal(t, 2, health.OrphanedTopics)
	assert.Equal(t, 1, health.OverlappingPairs)
	assert.Equal(t, 5, health.FactsOnOrphaned)
	assert.Equal(t, 10333, health.AvgTopicSize) // (1000 + 30000 + 0) / 3
	assert.Len(t, health.LargeTopicsList, 1)
	assert.Equal(t, int64(2), health.LargeTopicsList[0].ID)
	assert.Len(t, health.OverlappingPairsList, 1)

	mockStore.AssertExpectations(t)
}

// TestServiceGetDatabaseHealth_AllUsers tests health check for all users.
func TestServiceGetDatabaseHealth_AllUsers(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	topics := []storage.Topic{
		{ID: 1, UserID: 123, Summary: "Topic 1", SizeChars: 5000},
		{ID: 2, UserID: 456, Summary: "Topic 2", SizeChars: 10000},
	}

	mockStore.On("GetAllTopics").Return(topics, nil).Once()
	mockStore.On("CountOrphanedTopics", int64(0)).Return(0, nil).Once()
	mockStore.On("CountOverlappingTopics", int64(0)).Return(0, nil).Once()
	mockStore.On("CountFactsOnOrphanedTopics", int64(0)).Return(0, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	health, err := svc.GetDatabaseHealth(context.Background(), 0, 25000)
	assert.NoError(t, err)

	assert.Equal(t, 2, health.TotalTopics)
	assert.Equal(t, 7500, health.AvgTopicSize)

	mockStore.AssertExpectations(t)
}

// TestServiceGetDatabaseHealth_DefaultThreshold tests default threshold is used.
func TestServiceGetDatabaseHealth_DefaultThreshold(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topics := []storage.Topic{
		{ID: 1, UserID: userID, SizeChars: 26000}, // Above default 25000
	}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()
	mockStore.On("CountOrphanedTopics", userID).Return(0, nil).Once()
	mockStore.On("CountOverlappingTopics", userID).Return(0, nil).Once()
	mockStore.On("CountFactsOnOrphanedTopics", userID).Return(0, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	health, err := svc.GetDatabaseHealth(context.Background(), userID, 0)
	assert.NoError(t, err)

	assert.Equal(t, 1, health.LargeTopics) // Should use default 25000

	mockStore.AssertExpectations(t)
}

// TestServiceGetDatabaseHealth_Errors tests error handling in health check.
func TestServiceGetDatabaseHealth_Errors(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*testutil.MockStorage)
		wantErr     bool
		checkHealth bool
		expectedOr  int
	}{
		{
			name: "GetTopics fails",
			setupMocks: func(s *testutil.MockStorage) {
				s.On("GetTopics", int64(123)).Return(nil, errors.New("db error"))
			},
			wantErr: true,
		},
		{
			name: "CountOrphanedTopics fails - continues",
			setupMocks: func(s *testutil.MockStorage) {
				s.On("GetTopics", int64(123)).Return([]storage.Topic{}, nil)
				s.On("CountOrphanedTopics", int64(123)).Return(0, errors.New("count error"))
				s.On("CountOverlappingTopics", int64(123)).Return(0, nil)
				s.On("CountFactsOnOrphanedTopics", int64(123)).Return(0, nil)
			},
			wantErr:     false,
			checkHealth: true,
			expectedOr:  0,
		},
		{
			name: "CountOverlappingTopics fails - continues",
			setupMocks: func(s *testutil.MockStorage) {
				s.On("GetTopics", int64(123)).Return([]storage.Topic{}, nil)
				s.On("CountOrphanedTopics", int64(123)).Return(0, nil)
				s.On("CountOverlappingTopics", int64(123)).Return(0, errors.New("overlap error"))
				s.On("CountFactsOnOrphanedTopics", int64(123)).Return(0, nil)
			},
			wantErr:     false,
			checkHealth: true,
		},
		{
			name: "CountFactsOnOrphanedTopics fails - continues",
			setupMocks: func(s *testutil.MockStorage) {
				s.On("GetTopics", int64(123)).Return([]storage.Topic{}, nil)
				s.On("CountOrphanedTopics", int64(123)).Return(0, nil)
				s.On("CountOverlappingTopics", int64(123)).Return(0, nil)
				s.On("CountFactsOnOrphanedTopics", int64(123)).Return(0, errors.New("facts error"))
			},
			wantErr:     false,
			checkHealth: true,
		},
		{
			name: "No topics",
			setupMocks: func(s *testutil.MockStorage) {
				s.On("GetTopics", int64(123)).Return([]storage.Topic{}, nil)
				s.On("CountOrphanedTopics", int64(123)).Return(0, nil)
				s.On("CountOverlappingTopics", int64(123)).Return(0, nil)
				s.On("CountFactsOnOrphanedTopics", int64(123)).Return(0, nil)
			},
			wantErr:     false,
			checkHealth: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			mockStore := new(testutil.MockStorage)
			mockClient := new(testutil.MockOpenRouterClient)
			translator := testutil.TestTranslator(t)

			tt.setupMocks(mockStore)

			memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
			svc, err := NewServiceBuilder().
				WithLogger(testutil.TestLogger()).
				WithConfig(cfg).
				WithOpenRouterClient(mockClient).
				WithTopicRepository(mockStore).
				WithFactRepository(mockStore).
				WithFactHistoryRepository(mockStore).
				WithMessageRepository(mockStore).
				WithMaintenanceRepository(mockStore).
				WithMemoryService(memSvc).
				WithTranslator(translator).
				Build()
			assert.NoError(t, err)

			health, err := svc.GetDatabaseHealth(context.Background(), 123, 25000)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkHealth {
					assert.NotNil(t, health)
				}
			}

			mockStore.AssertExpectations(t)
		})
	}
}

// TestServiceRepairDatabase_DryRun tests dry run mode.
func TestServiceRepairDatabase_DryRun(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	orphanedIDs := []int64{10, 20}

	mockStore.On("GetOrphanedTopicIDs", userID).Return(orphanedIDs, nil).Once()
	mockStore.On("GetTopics", userID).Return([]storage.Topic{
		{ID: 1, UserID: userID},
		{ID: 2, UserID: userID},
	}, nil).Once()
	mockStore.On("CountFactsOnOrphanedTopics", userID).Return(3, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.RepairDatabase(context.Background(), userID, true)
	assert.NoError(t, err)

	assert.Equal(t, 2, stats.OrphanedTopicsDeleted)
	assert.Equal(t, 3, stats.FactsRelinked)
	assert.Equal(t, 2, stats.RangesRecalculated)
	assert.Equal(t, 2, stats.SizesRecalculated)

	mockStore.AssertExpectations(t)
}

// TestServiceRepairDatabase_RealRun tests real repair mode.
func TestServiceRepairDatabase_RealRun(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	orphanedIDs := []int64{10, 20}
	topics := []storage.Topic{
		{ID: 10, UserID: userID},
		{ID: 20, UserID: userID},
		{ID: 1, UserID: userID}, // Valid topic
	}
	topicID10 := int64(10)
	facts := []storage.Fact{
		{ID: 1, UserID: userID, TopicID: &topicID10},
		{ID: 2, UserID: userID, TopicID: &topicID10},
	}

	mockStore.On("GetOrphanedTopicIDs", userID).Return(orphanedIDs, nil).Once()
	// GetTopics is called twice: once in relinkFactsFromOrphanedTopics, once in RepairDatabase
	mockStore.On("GetTopics", userID).Return(topics, nil).Times(2)
	// Topic 10 has facts to relink
	mockStore.On("GetFactsByTopicID", userID, int64(10)).Return(facts, nil).Once()
	mockStore.On("UpdateFactsTopic", userID, int64(10), int64(1)).Return(nil).Once()
	mockStore.On("DeleteTopicCascade", userID, int64(10)).Return(nil).Once()
	// Topic 20 has no facts
	mockStore.On("GetFactsByTopicID", userID, int64(20)).Return([]storage.Fact{}, nil).Once()
	mockStore.On("DeleteTopicCascade", userID, int64(20)).Return(nil).Once()
	mockStore.On("RecalculateTopicRanges", userID).Return(2, nil).Once()
	mockStore.On("RecalculateTopicSizes", userID).Return(2, nil).Once()
	// ReloadVectors is called after repair
	mockStore.On("GetAllTopics").Return(topics, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetFactHistoryByTopicID", int64(10)).Return(nil, nil).Maybe()
	mockStore.On("UpdateFactHistoryTopic", int64(10), int64(1)).Return(nil).Maybe()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.RepairDatabase(context.Background(), userID, false)
	assert.NoError(t, err)

	assert.Equal(t, 2, stats.OrphanedTopicsDeleted)
	assert.Equal(t, 2, stats.FactsRelinked)
	assert.Equal(t, 2, stats.RangesRecalculated)
	assert.Equal(t, 2, stats.SizesRecalculated)

	mockStore.AssertExpectations(t)
}

// TestServiceRepairDatabase_NoOrphans tests repair with no orphans.
func TestServiceRepairDatabase_NoOrphans(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	mockStore.On("GetOrphanedTopicIDs", userID).Return([]int64{}, nil).Once()
	topics := []storage.Topic{
		{ID: 1, UserID: userID},
	}
	mockStore.On("GetTopics", userID).Return(topics, nil).Once()
	mockStore.On("RecalculateTopicRanges", userID).Return(1, nil).Once()
	mockStore.On("RecalculateTopicSizes", userID).Return(1, nil).Once()
	// ReloadVectors is called after repair (SizesRecalculated > 0)
	mockStore.On("GetAllTopics").Return(topics, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.RepairDatabase(context.Background(), userID, false)
	assert.NoError(t, err)

	assert.Equal(t, 0, stats.OrphanedTopicsDeleted)
	assert.Equal(t, 1, stats.RangesRecalculated)
	assert.Equal(t, 1, stats.SizesRecalculated)

	mockStore.AssertExpectations(t)
}

// TestServiceRepairDatabase_AllUsers tests repair for all users.
func TestServiceRepairDatabase_AllUsers(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	orphanedIDs := []int64{10, 20}
	topics := []storage.Topic{
		{ID: 10, UserID: 123},
		{ID: 20, UserID: 456},
		{ID: 1, UserID: 123},
		{ID: 2, UserID: 456},
	}

	mockStore.On("GetOrphanedTopicIDs", int64(0)).Return(orphanedIDs, nil).Once()
	// GetAllTopics is called multiple times during repair
	mockStore.On("GetAllTopics").Return(topics, nil).Maybe()
	mockStore.On("GetFactsByTopicID", int64(123), int64(10)).Return([]storage.Fact{}, nil).Once()
	mockStore.On("GetFactsByTopicID", int64(456), int64(20)).Return([]storage.Fact{}, nil).Once()
	mockStore.On("DeleteTopicCascade", int64(123), int64(10)).Return(nil).Once()
	mockStore.On("DeleteTopicCascade", int64(456), int64(20)).Return(nil).Once()
	mockStore.On("RecalculateTopicRanges", int64(0)).Return(0, nil).Once()
	mockStore.On("RecalculateTopicSizes", int64(0)).Return(0, nil).Once()
	// ReloadVectors calls GetAllFacts
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.RepairDatabase(context.Background(), 0, false)
	assert.NoError(t, err)

	assert.Equal(t, 2, stats.OrphanedTopicsDeleted)

	mockStore.AssertExpectations(t)
}

// TestServiceGetContaminationInfo_Success tests successful contamination check.
func TestServiceGetContaminationInfo_Success(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	contaminated := []storage.ContaminatedTopic{
		{TopicID: 1, TopicOwner: 123, ForeignUsers: []int64{456, 789}, ForeignMsgCnt: 3},
		{TopicID: 2, TopicOwner: 123, ForeignUsers: []int64{456}, ForeignMsgCnt: 1},
	}

	mockStore.On("CountContaminatedTopics", userID).Return(2, nil).Once()
	mockStore.On("GetContaminatedTopics", userID).Return(contaminated, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	info, err := svc.GetContaminationInfo(context.Background(), userID)
	assert.NoError(t, err)

	assert.Equal(t, 2, info.TotalContaminated)
	assert.Len(t, info.ContaminatedTopics, 2)
	assert.Equal(t, int64(1), info.ContaminatedTopics[0].TopicID)

	mockStore.AssertExpectations(t)
}

// TestServiceGetContaminationInfo_AllUsers tests contamination check for all users.
func TestServiceGetContaminationInfo_AllUsers(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	contaminated := []storage.ContaminatedTopic{
		{TopicID: 1, TopicOwner: 123, ForeignUsers: []int64{456}, ForeignMsgCnt: 2},
	}

	mockStore.On("CountContaminatedTopics", int64(0)).Return(1, nil).Once()
	mockStore.On("GetContaminatedTopics", int64(0)).Return(contaminated, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	info, err := svc.GetContaminationInfo(context.Background(), 0)
	assert.NoError(t, err)

	assert.Equal(t, 1, info.TotalContaminated)

	mockStore.AssertExpectations(t)
}

// TestServiceFixContamination_DryRun tests dry run mode for contamination fix.
func TestServiceFixContamination_DryRun(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	contaminated := []storage.ContaminatedTopic{
		{TopicID: 1, TopicOwner: 123, ForeignUsers: []int64{456}, ForeignMsgCnt: 3},
		{TopicID: 2, TopicOwner: 123, ForeignUsers: []int64{789}, ForeignMsgCnt: 2},
	}

	mockStore.On("GetContaminatedTopics", userID).Return(contaminated, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.FixContamination(context.Background(), userID, true)
	assert.NoError(t, err)

	assert.Equal(t, int64(5), stats.MessagesUnlinked)
	assert.Equal(t, 0, stats.OrphanedTopicsDeleted)

	mockStore.AssertExpectations(t)
}

// TestServiceFixContamination_RealRun tests real contamination fix.
func TestServiceFixContamination_RealRun(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	orphanedIDs := []int64{1}
	topics := []storage.Topic{
		{ID: 1, UserID: 123},
		{ID: 2, UserID: 123},
	}

	mockStore.On("CountContaminatedTopics", userID).Return(1, nil).Once()
	mockStore.On("FixContaminatedTopics", userID).Return(int64(3), nil).Once()
	mockStore.On("CountContaminatedTopics", userID).Return(0, nil).Once() // After fix
	mockStore.On("RecalculateTopicSizes", userID).Return(2, nil).Once()
	mockStore.On("GetOrphanedTopicIDs", userID).Return(orphanedIDs, nil).Once()
	mockStore.On("GetAllTopics").Return(topics, nil).Once()
	mockStore.On("DeleteTopicCascade", userID, int64(1)).Return(nil).Once()
	mockStore.On("Checkpoint").Return(nil).Once()
	// ReloadVectors is called after fix
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.FixContamination(context.Background(), userID, false)
	assert.NoError(t, err)

	assert.Equal(t, int64(3), stats.MessagesUnlinked)
	assert.Equal(t, 1, stats.OrphanedTopicsDeleted)

	mockStore.AssertExpectations(t)
}

// TestServiceFixContamination_NoContamination tests when there's no contamination.
func TestServiceFixContamination_NoContamination(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// CountContaminatedTopics is called twice (before and after fix)
	mockStore.On("CountContaminatedTopics", userID).Return(0, nil).Times(2)
	mockStore.On("FixContaminatedTopics", userID).Return(int64(0), nil).Once() // No messages to unlink

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	stats, err := svc.FixContamination(context.Background(), userID, false)
	assert.NoError(t, err)

	assert.Equal(t, int64(0), stats.MessagesUnlinked)
	assert.Equal(t, 0, stats.OrphanedTopicsDeleted)

	mockStore.AssertExpectations(t)
}

// TestServiceRelinkFactsFromOrphanedTopics_Success tests successful fact relinking.
func TestServiceRelinkFactsFromOrphanedTopics_Success(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	orphanedIDs := []int64{10, 20}
	topics := []storage.Topic{
		{ID: 1, UserID: userID},  // Valid topic for user 123
		{ID: 10, UserID: userID}, // Orphaned
		{ID: 20, UserID: userID}, // Orphaned
	}
	topicID10 := int64(10)
	topicID20 := int64(20)
	facts1 := []storage.Fact{{ID: 1, UserID: userID, TopicID: &topicID10}}
	facts2 := []storage.Fact{{ID: 2, UserID: userID, TopicID: &topicID20}, {ID: 3, UserID: userID, TopicID: &topicID20}}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()
	mockStore.On("GetFactsByTopicID", userID, int64(10)).Return(facts1, nil).Once()
	mockStore.On("GetFactsByTopicID", userID, int64(20)).Return(facts2, nil).Once()
	mockStore.On("UpdateFactsTopic", userID, int64(10), int64(1)).Return(nil).Once()
	mockStore.On("UpdateFactsTopic", userID, int64(20), int64(1)).Return(nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	count, err := svc.relinkFactsFromOrphanedTopics(context.Background(), userID, orphanedIDs)
	assert.NoError(t, err)
	assert.Equal(t, 3, count)

	mockStore.AssertExpectations(t)
}

// TestServiceRelinkFactsFromOrphanedTopics_NoValidTopic tests when no valid topic exists.
func TestServiceRelinkFactsFromOrphanedTopics_NoValidTopic(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	orphanedIDs := []int64{10, 20}
	topics := []storage.Topic{
		{ID: 10, UserID: userID}, // Only orphaned topics, no valid ones
		{ID: 20, UserID: userID},
	}

	mockStore.On("GetTopics", userID).Return(topics, nil).Once()

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	count, err := svc.relinkFactsFromOrphanedTopics(context.Background(), userID, orphanedIDs)
	assert.NoError(t, err)
	assert.Equal(t, 0, count)

	mockStore.AssertExpectations(t)
}

// TestServiceRelinkFactsFromOrphanedTopics_EmptyList tests empty orphaned list.
func TestServiceRelinkFactsFromOrphanedTopics_EmptyList(t *testing.T) {
	cfg := testutil.TestConfig()
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(testutil.TestLogger(), cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(testutil.TestLogger()).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	assert.NoError(t, err)

	count, err := svc.relinkFactsFromOrphanedTopics(context.Background(), 123, []int64{})
	assert.NoError(t, err)
	assert.Equal(t, 0, count)
}
