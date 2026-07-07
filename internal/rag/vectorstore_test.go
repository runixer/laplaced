package rag

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/storage"
)

const (
	vsUser1 = storage.ScopeID("user-1")
	vsUser2 = storage.ScopeID("user-2")
)

func TestMemoryVectorStore_ReplaceAll(t *testing.T) {
	tests := []struct {
		name        string
		entries     map[storage.ScopeID][]VectorEntry
		wantCount   int
		wantMaxID   int64
		wantByUser1 int
	}{
		{
			name: "two users",
			entries: map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: 1, Embedding: []float32{1, 0}}, {ID: 7, Embedding: []float32{0, 1}}},
				vsUser2: {{ID: 3, Embedding: []float32{1, 1}}},
			},
			wantCount:   3,
			wantMaxID:   7,
			wantByUser1: 2,
		},
		{
			name:        "nil entries reset the index",
			entries:     nil,
			wantCount:   0,
			wantMaxID:   0,
			wantByUser1: 0,
		},
		{
			name: "user with empty list is dropped",
			entries: map[storage.ScopeID][]VectorEntry{
				vsUser1: {},
			},
			wantCount:   0,
			wantMaxID:   0,
			wantByUser1: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryVectorStore()
			// Pre-fill so we verify ReplaceAll discards old state
			store.ReplaceAll(VectorKindTopics, map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: 99, Embedding: []float32{0.5, 0.5}}},
			})

			store.ReplaceAll(VectorKindTopics, tt.entries)

			assert.Equal(t, tt.wantCount, store.Count(VectorKindTopics))
			assert.Equal(t, tt.wantMaxID, store.MaxID(VectorKindTopics))
			assert.Equal(t, tt.wantByUser1, store.CountByUser(VectorKindTopics)[vsUser1])
		})
	}
}

func TestMemoryVectorStore_KindsAreIndependent(t *testing.T) {
	store := NewMemoryVectorStore()
	store.ReplaceAll(VectorKindTopics, map[storage.ScopeID][]VectorEntry{
		vsUser1: {{ID: 10, Embedding: []float32{1, 0}}},
	})

	assert.Equal(t, 1, store.Count(VectorKindTopics))
	assert.Equal(t, 0, store.Count(VectorKindFacts))
	assert.Equal(t, int64(0), store.MaxID(VectorKindFacts))
}

func TestMemoryVectorStore_AppendSince(t *testing.T) {
	tests := []struct {
		name       string
		sinceMaxID int64
		entries    map[storage.ScopeID][]VectorEntry
		wantAdded  int
		wantCount  int
		wantMaxID  int64
	}{
		{
			name:       "appends and advances watermark",
			sinceMaxID: 5,
			entries: map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: 6, Embedding: []float32{1, 0}}},
				vsUser2: {{ID: 8, Embedding: []float32{0, 1}}},
			},
			wantAdded: 2,
			wantCount: 3,
			wantMaxID: 8,
		},
		{
			name:       "skips when watermark already advanced",
			sinceMaxID: 3, // store watermark is 5 — another load won the race
			entries: map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: 4, Embedding: []float32{1, 0}}},
			},
			wantAdded: 0,
			wantCount: 1,
			wantMaxID: 5,
		},
		{
			name:       "nothing to append",
			sinceMaxID: 5,
			entries:    nil,
			wantAdded:  0,
			wantCount:  1,
			wantMaxID:  5,
		},
		{
			// Regression: a ReplaceAll reset the watermark BELOW the value this
			// batch was fetched against. Appending would jump the watermark past
			// entries the full reload has yet to fetch, hiding them from search.
			name:       "skips when watermark was reset below sinceMaxID",
			sinceMaxID: 500, // store watermark is 5 — a full reload reset it
			entries: map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: 502, Embedding: []float32{1, 0}}},
			},
			wantAdded: 0,
			wantCount: 1,
			wantMaxID: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryVectorStore()
			store.ReplaceAll(VectorKindFacts, map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: 5, Embedding: []float32{0.5, 0.5}}},
			})

			added := store.AppendSince(VectorKindFacts, tt.sinceMaxID, tt.entries)

			assert.Equal(t, tt.wantAdded, added)
			assert.Equal(t, tt.wantCount, store.Count(VectorKindFacts))
			assert.Equal(t, tt.wantMaxID, store.MaxID(VectorKindFacts))
		})
	}
}

func TestMemoryVectorStore_AppendSince_EmptyStore(t *testing.T) {
	store := NewMemoryVectorStore()

	added := store.AppendSince(VectorKindArtifacts, 0, map[storage.ScopeID][]VectorEntry{
		vsUser1: {{ID: 2, Embedding: []float32{1, 0}}},
	})

	assert.Equal(t, 1, added)
	assert.Equal(t, int64(2), store.MaxID(VectorKindArtifacts))
}

func TestMemoryVectorStore_Search(t *testing.T) {
	store := NewMemoryVectorStore()
	store.ReplaceAll(VectorKindTopics, map[storage.ScopeID][]VectorEntry{
		vsUser1: {
			{ID: 1, Embedding: []float32{1, 0}},     // cos = 1.0
			{ID: 2, Embedding: []float32{0.9, 0.1}}, // cos ≈ 0.994
			{ID: 3, Embedding: []float32{0, 1}},     // cos = 0
		},
		vsUser2: {
			{ID: 4, Embedding: []float32{1, 0}},
		},
	})
	query := []float32{1, 0}

	tests := []struct {
		name        string
		userID      storage.ScopeID
		threshold   float32
		limit       int
		wantIDs     []int64
		wantScanned int
	}{
		{
			name:        "sorted by score, threshold filters",
			userID:      vsUser1,
			threshold:   0.5,
			limit:       0,
			wantIDs:     []int64{1, 2},
			wantScanned: 3,
		},
		{
			name:        "limit caps results",
			userID:      vsUser1,
			threshold:   0.5,
			limit:       1,
			wantIDs:     []int64{1},
			wantScanned: 3,
		},
		{
			name:        "threshold is inclusive",
			userID:      vsUser1,
			threshold:   1.0,
			limit:       0,
			wantIDs:     []int64{1},
			wantScanned: 3,
		},
		{
			name:        "user isolation - only own vectors scanned",
			userID:      vsUser2,
			threshold:   0.5,
			limit:       0,
			wantIDs:     []int64{4},
			wantScanned: 1,
		},
		{
			name:        "unknown user",
			userID:      storage.ScopeID("nobody"),
			threshold:   0,
			limit:       0,
			wantIDs:     nil,
			wantScanned: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, scanned := store.Search(VectorKindTopics, tt.userID, query, tt.threshold, tt.limit)

			assert.Equal(t, tt.wantScanned, scanned)
			var ids []int64
			for _, r := range results {
				ids = append(ids, r.ID)
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestMemoryVectorStore_ReplaceAll_CopiesInput(t *testing.T) {
	store := NewMemoryVectorStore()
	entries := map[storage.ScopeID][]VectorEntry{
		vsUser1: {{ID: 1, Embedding: []float32{1, 0}}},
	}
	store.ReplaceAll(VectorKindTopics, entries)

	// Mutating the caller's map must not affect the index
	entries[vsUser1] = append(entries[vsUser1], VectorEntry{ID: 2, Embedding: []float32{0, 1}})
	delete(entries, vsUser1)

	assert.Equal(t, 1, store.Count(VectorKindTopics))
}

func TestMemoryVectorStore_ConcurrentAccess(t *testing.T) {
	store := NewMemoryVectorStore()
	store.ReplaceAll(VectorKindTopics, map[storage.ScopeID][]VectorEntry{
		vsUser1: {{ID: 1, Embedding: []float32{1, 0}}},
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			sinceMaxID := store.MaxID(VectorKindTopics)
			store.AppendSince(VectorKindTopics, sinceMaxID, map[storage.ScopeID][]VectorEntry{
				vsUser1: {{ID: sinceMaxID + 1, Embedding: []float32{1, 0}}},
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = store.Search(VectorKindTopics, vsUser1, []float32{1, 0}, 0.5, 0)
			_ = store.Count(VectorKindTopics)
			_ = store.CountByUser(VectorKindTopics)
		}()
	}
	wg.Wait()

	// Every successful append added exactly the entries above the watermark —
	// no duplicates regardless of interleaving.
	results, _ := store.Search(VectorKindTopics, vsUser1, []float32{1, 0}, 0.5, 0)
	seen := make(map[int64]bool)
	for _, r := range results {
		assert.False(t, seen[r.ID], "duplicate ID %d in index", r.ID)
		seen[r.ID] = true
	}
}
