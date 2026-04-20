package rag

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

const (
	// reembedBatchSize is the number of inputs per embedding request. Matches
	// the pattern benchmarked in cmd/embed-benchmark and fits comfortably
	// below OpenRouter request-size limits.
	reembedBatchSize = 32

	// reembedParallelism is the number of concurrent embedding requests
	// during migration. Validated by the benchmark: at 8 concurrent requests
	// the full prod DB (~6500 vectors) re-embeds in ~45 seconds.
	reembedParallelism = 8
)

// currentEmbeddingVersion produces the version tag written into
// `embedding_version` columns. Format: "{model}:{dim}". A value of 0 for
// Dimensions means "provider default" — in that case the version is just
// the model string.
func currentEmbeddingVersion(model string, dim int) string {
	if dim <= 0 {
		return model
	}
	return fmt.Sprintf("%s:%d", model, dim)
}

// entityKind is an enum for logging/metrics. Values map 1:1 to tables.
type entityKind string

const (
	entityTopic    entityKind = "topics"
	entityFact     entityKind = "facts"
	entityPerson   entityKind = "people"
	entityArtifact entityKind = "artifacts"
)

// reembedTable abstracts the four storage bindings so ReembedIfNeeded can
// walk them with a single loop.
type reembedTable struct {
	kind   entityKind
	fetch  func(version string, limit int) ([]storage.ReembedCandidate, error)
	update func(id int64, emb []float32, version string) error
}

// ReembedIfNeeded detects embeddings produced under a different model/dim
// than the currently configured one and re-generates them in-place. It is
// intended to run once on startup, synchronously, before vector-index load
// — see Service.Start.
//
// The operation is resumable: each row is persisted as soon as its new
// embedding is available, so a crash or context cancellation leaves the DB
// in a consistent "partially migrated" state. A second run picks up where
// the first left off.
func (s *Service) ReembedIfNeeded(ctx context.Context) error {
	version := currentEmbeddingVersion(s.cfg.Embedding.Model, s.cfg.Embedding.Dimensions)
	tables := []reembedTable{
		{entityTopic, s.topicRepo.GetTopicsNeedingReembed, s.topicRepo.UpdateTopicEmbeddingVersion},
		{entityFact, s.factRepo.GetFactsNeedingReembed, s.factRepo.UpdateFactEmbeddingVersion},
	}
	if s.peopleRepo != nil {
		tables = append(tables, reembedTable{
			entityPerson,
			s.peopleRepo.GetPeopleNeedingReembed,
			s.peopleRepo.UpdatePersonEmbeddingVersion,
		})
	}
	if s.artifactRepo != nil {
		tables = append(tables, reembedTable{
			entityArtifact,
			s.artifactRepo.GetArtifactsNeedingReembed,
			s.artifactRepo.UpdateArtifactEmbeddingVersion,
		})
	}

	var totalDone, totalFailed int64
	for _, t := range tables {
		done, failed, err := s.reembedTable(ctx, t, version)
		totalDone += done
		totalFailed += failed
		if err != nil {
			s.logger.Error("re-embed aborted for table",
				"kind", t.kind, "error", err, "done", done, "failed", failed)
			return fmt.Errorf("re-embed %s: %w", t.kind, err)
		}
	}
	if totalDone > 0 || totalFailed > 0 {
		s.logger.Info("re-embed complete", "version", version, "done", totalDone, "failed", totalFailed)
	}
	return nil
}

// reembedTable processes all rows of one table until no candidates remain.
// Embedding requests run concurrently (reembedParallelism workers, batches
// of reembedBatchSize inputs each).
//
// Returns: done (rows with new embeddings saved), failed (rows that could
// not be re-embedded but did not abort the run — NOT yet implemented; any
// single failure currently returns an error).
func (s *Service) reembedTable(ctx context.Context, t reembedTable, version string) (int64, int64, error) {
	// A first candidates fetch tells us whether there is work to do at all.
	candidates, err := t.fetch(version, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("list candidates: %w", err)
	}
	if len(candidates) == 0 {
		return 0, 0, nil
	}

	s.logger.Info("re-embed starting", "kind", t.kind, "pending", len(candidates), "target_version", version)
	start := time.Now()

	var done atomic.Int64
	var failed atomic.Int64
	var firstErr atomic.Pointer[error]

	sem := make(chan struct{}, reembedParallelism)
	var wg sync.WaitGroup

	batches := batchCandidates(candidates, reembedBatchSize)
	for idx, batch := range batches {
		if ctx.Err() != nil {
			break
		}
		if firstErr.Load() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(batchIdx int, batch []storage.ReembedCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.reembedOneBatch(ctx, t, version, batch); err != nil {
				failed.Add(int64(len(batch)))
				firstErr.CompareAndSwap(nil, &err)
				return
			}
			done.Add(int64(len(batch)))
			if batchIdx%10 == 0 {
				s.logger.Info("re-embed progress",
					"kind", t.kind,
					"done", done.Load(),
					"total", len(candidates),
					"elapsed_s", int(time.Since(start).Seconds()),
				)
			}
		}(idx, batch)
	}
	wg.Wait()

	if errPtr := firstErr.Load(); errPtr != nil {
		return done.Load(), failed.Load(), *errPtr
	}

	s.logger.Info("re-embed table complete",
		"kind", t.kind, "done", done.Load(), "elapsed_s", int(time.Since(start).Seconds()))
	return done.Load(), failed.Load(), nil
}

// reembedOneBatch embeds up to reembedBatchSize inputs in a single API call
// and persists each resulting vector. Batch size is bounded by the caller.
func (s *Service) reembedOneBatch(ctx context.Context, t reembedTable, version string, batch []storage.ReembedCandidate) error {
	inputs := make([]string, len(batch))
	for i, c := range batch {
		inputs[i] = c.Content
	}
	req := openrouter.EmbeddingRequest{
		Model:      s.cfg.Embedding.Model,
		Input:      inputs,
		Dimensions: s.cfg.Embedding.Dimensions,
	}
	resp, err := s.client.CreateEmbeddings(ctx, req)
	if err != nil {
		return fmt.Errorf("embed batch: %w", err)
	}
	if len(resp.Data) != len(batch) {
		return fmt.Errorf("embed batch: expected %d results, got %d", len(batch), len(resp.Data))
	}
	// OpenRouter MAY return out of order; sort by Index.
	byIndex := make([]openrouter.EmbeddingObject, len(batch))
	for _, item := range resp.Data {
		if item.Index < 0 || item.Index >= len(batch) {
			return fmt.Errorf("embed batch: bad index %d", item.Index)
		}
		byIndex[item.Index] = item
	}
	for i, item := range byIndex {
		if err := t.update(batch[i].ID, item.Embedding, version); err != nil {
			return fmt.Errorf("persist %s id=%d: %w", t.kind, batch[i].ID, err)
		}
	}
	return nil
}

func batchCandidates(cands []storage.ReembedCandidate, size int) [][]storage.ReembedCandidate {
	if size <= 0 {
		size = 1
	}
	var out [][]storage.ReembedCandidate
	for i := 0; i < len(cands); i += size {
		end := i + size
		if end > len(cands) {
			end = len(cands)
		}
		out = append(out, cands[i:end])
	}
	return out
}
