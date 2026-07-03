package rag

import (
	"sort"
	"sync"

	"github.com/runixer/laplaced/internal/storage"
)

// VectorKind identifies which vector index an entry belongs to.
type VectorKind string

// Vector index kinds. Values double as the metric label for vector search.
const (
	VectorKindTopics    VectorKind = "topics"
	VectorKindFacts     VectorKind = "facts"
	VectorKindPeople    VectorKind = "people"
	VectorKindArtifacts VectorKind = "artifacts"
)

// VectorEntry is a single indexed embedding.
type VectorEntry struct {
	ID        int64
	Embedding []float32
}

// VectorSearchResult holds ID and similarity score from vector search.
type VectorSearchResult struct {
	ID    int64
	Score float32
}

// VectorStore is the vector index used for RAG similarity search.
// Implementations must be safe for concurrent use. The only implementation
// today is in-memory; a backend with native similarity search (e.g. pgvector)
// can replace it behind this interface.
type VectorStore interface {
	// ReplaceAll atomically replaces every entry of the kind (full reload).
	ReplaceAll(kind VectorKind, entries map[storage.ScopeID][]VectorEntry)
	// MaxID returns the highest loaded entry ID for the kind — the watermark
	// incremental loads resume from.
	MaxID(kind VectorKind) int64
	// AppendSince appends entries fetched for IDs above sinceMaxID and
	// advances the watermark. If the watermark already moved past sinceMaxID
	// (a concurrent load won the race), nothing is appended and 0 is
	// returned. Returns the number of entries appended.
	AppendSince(kind VectorKind, sinceMaxID int64, entries map[storage.ScopeID][]VectorEntry) int
	// Search returns the user's entries with cosine similarity to query of at
	// least threshold, sorted by score descending, capped at limit
	// (0 = unlimited). scanned is the number of entries compared.
	Search(kind VectorKind, userID storage.ScopeID, query []float32, threshold float32, limit int) (results []VectorSearchResult, scanned int)
	// Count returns the total number of entries of the kind across all users.
	Count(kind VectorKind) int
	// CountByUser returns per-user entry counts for the kind.
	CountByUser(kind VectorKind) map[storage.ScopeID]int
}

// memoryVectorStore keeps all embeddings in process memory and scans them
// linearly on search. Similarity is computed application-side, which is why
// this is the only backend for now (see docs: pgvector is deferred until
// similarity moves into SQL).
type memoryVectorStore struct {
	mu      sync.RWMutex
	entries map[VectorKind]map[storage.ScopeID][]VectorEntry
	maxIDs  map[VectorKind]int64
}

// NewMemoryVectorStore creates an empty in-memory vector store.
func NewMemoryVectorStore() VectorStore {
	return &memoryVectorStore{
		entries: make(map[VectorKind]map[storage.ScopeID][]VectorEntry),
		maxIDs:  make(map[VectorKind]int64),
	}
}

func (m *memoryVectorStore) ReplaceAll(kind VectorKind, entries map[storage.ScopeID][]VectorEntry) {
	// Copy so later mutations of the caller's map don't alias the index.
	fresh := make(map[storage.ScopeID][]VectorEntry, len(entries))
	var maxID int64
	for userID, list := range entries {
		if len(list) == 0 {
			continue
		}
		fresh[userID] = append([]VectorEntry(nil), list...)
		for _, e := range list {
			if e.ID > maxID {
				maxID = e.ID
			}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[kind] = fresh
	m.maxIDs[kind] = maxID
}

func (m *memoryVectorStore) MaxID(kind VectorKind) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxIDs[kind]
}

func (m *memoryVectorStore) AppendSince(kind VectorKind, sinceMaxID int64, entries map[storage.ScopeID][]VectorEntry) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.maxIDs[kind] > sinceMaxID {
		return 0 // a concurrent load already advanced the watermark
	}

	byUser := m.entries[kind]
	if byUser == nil {
		byUser = make(map[storage.ScopeID][]VectorEntry)
		m.entries[kind] = byUser
	}

	count := 0
	maxID := m.maxIDs[kind]
	for userID, list := range entries {
		for _, e := range list {
			byUser[userID] = append(byUser[userID], e)
			if e.ID > maxID {
				maxID = e.ID
			}
			count++
		}
	}
	m.maxIDs[kind] = maxID
	return count
}

func (m *memoryVectorStore) Search(kind VectorKind, userID storage.ScopeID, query []float32, threshold float32, limit int) ([]VectorSearchResult, int) {
	m.mu.RLock()
	userEntries := m.entries[kind][userID]
	scanned := len(userEntries)
	var results []VectorSearchResult
	for _, e := range userEntries {
		score := cosineSimilarity(query, e.Embedding)
		if score >= threshold {
			results = append(results, VectorSearchResult{ID: e.ID, Score: score})
		}
	}
	m.mu.RUnlock()

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, scanned
}

func (m *memoryVectorStore) Count(kind VectorKind) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := 0
	for _, list := range m.entries[kind] {
		total += len(list)
	}
	return total
}

func (m *memoryVectorStore) CountByUser(kind VectorKind) map[storage.ScopeID]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := make(map[storage.ScopeID]int, len(m.entries[kind]))
	for userID, list := range m.entries[kind] {
		counts[userID] = len(list)
	}
	return counts
}

// collectVectorEntries groups embeddings by user, skipping items without one.
// Returns the grouped entries and the number of items that had embeddings.
func collectVectorEntries[T any](items []T, extract func(T) (storage.ScopeID, int64, []float32)) (map[storage.ScopeID][]VectorEntry, int) {
	entries := make(map[storage.ScopeID][]VectorEntry)
	count := 0
	for _, item := range items {
		userID, id, embedding := extract(item)
		if len(embedding) == 0 {
			continue
		}
		entries[userID] = append(entries[userID], VectorEntry{ID: id, Embedding: embedding})
		count++
	}
	return entries, count
}
