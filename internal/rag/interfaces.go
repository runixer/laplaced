package rag

import (
	"context"

	"github.com/runixer/laplaced/internal/storage"
)

// MaintenanceService provides database maintenance and diagnostic operations.
// This interface allows web handlers to be tested without the full RAG service.
type MaintenanceService interface {
	// Database health and repair
	GetDatabaseHealth(ctx context.Context, userID storage.ScopeID, largeThreshold int) (*DatabaseHealth, error)
	RepairDatabase(ctx context.Context, userID storage.ScopeID, dryRun bool) (*RepairStats, error)

	// Contamination management
	GetContaminationInfo(ctx context.Context, userID storage.ScopeID) (*ContaminationInfo, error)
	FixContamination(ctx context.Context, userID storage.ScopeID, dryRun bool) (*ContaminationFixStats, error)

	// Topic management
	SplitLargeTopics(ctx context.Context, userID storage.ScopeID, thresholdChars int) (*SplitStats, error)
}

// Verify Service implements MaintenanceService (compile-time check)
var _ MaintenanceService = (*Service)(nil)
