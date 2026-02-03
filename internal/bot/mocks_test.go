// Package bot contains test mocks shared across all test files in this package.
package bot

import (
	"context"
	"time"

	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
)

// mockMemoryService is a no-op implementation of rag.MemoryService for testing.
// Defined here to avoid redeclaration across multiple test files.
// Not moved to testutil due to import cycle: testutil → rag → testutil (in tests).
type mockMemoryService struct{}

func (m *mockMemoryService) ProcessSession(ctx context.Context, userID int64, messages []storage.Message, topicDate time.Time, topicID int64) error {
	return nil
}

func (m *mockMemoryService) ProcessSessionWithStats(ctx context.Context, userID int64, messages []storage.Message, topicDate time.Time, topicID int64) (memory.FactStats, error) {
	return memory.FactStats{}, nil
}
