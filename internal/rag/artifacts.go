package rag

import (
	"context"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/extractor"
	"github.com/runixer/laplaced/internal/storage"
)

// artifactExtractionLoop is the background loop for artifact processing (v0.6.0).
// Polls at configured interval for pending artifacts and processes them with rate limiting.
func (s *Service) artifactExtractionLoop(ctx context.Context) {
	defer s.wg.Done()
	interval := s.cfg.Agents.Extractor.GetPollingInterval()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial run
	s.processArtifactExtraction(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.processArtifactExtraction(ctx)
		}
	}
}

// processArtifactExtraction processes all pending artifacts with rate limiting (v0.6.0).
// Max concurrent extractions configured by ExtractorAgentConfig.MaxConcurrent.
// Respects shuttingDown flag to stop accepting new work (v0.6.0 - CRIT-2).
func (s *Service) processArtifactExtraction(ctx context.Context) {
	// Don't start new work if shutting down (v0.6.0 - CRIT-2)
	if s.shuttingDown.Load() {
		s.logger.Debug("shutdown in progress, skipping artifact extraction")
		return
	}

	if s.extractorAgent == nil {
		s.logger.Warn("extractor agent not set, skipping artifact extraction")
		return
	}

	if s.artifactRepo == nil {
		s.logger.Warn("artifact repository not set, skipping artifact extraction")
		return
	}

	// Get all allowed users
	users := s.cfg.Bot.AllowedUserIDs
	if len(users) == 0 {
		return
	}

	// Collect pending artifacts from all users
	var allArtifacts []storage.Artifact
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}

		// Get pending artifacts including retriable failed ones (v0.6.0 - CRIT-3)
		maxRetries := s.cfg.Agents.Extractor.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}
		artifacts, err := s.artifactRepo.GetPendingArtifacts(userID, maxRetries)
		if err != nil {
			s.logger.Error("failed to get pending artifacts", "user_id", userID, "error", err)
			continue
		}

		allArtifacts = append(allArtifacts, artifacts...)
	}

	if len(allArtifacts) == 0 {
		return
	}

	s.logger.Info("processing pending artifacts", "count", len(allArtifacts))

	// Rate limiting: use configured max concurrent extractions
	maxConcurrent := s.cfg.Agents.Extractor.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, artifact := range allArtifacts {
		// Check shutdown signal before starting each artifact (v0.6.0)
		if s.shuttingDown.Load() {
			s.logger.Info("shutdown in progress, not starting new artifacts",
				"pending", len(allArtifacts),
			)
			break
		}

		if ctx.Err() != nil {
			return
		}

		// Check if already being processed
		if !s.tryStartProcessingArtifact(artifact.ID) {
			s.logger.Debug("artifact already being processed, skipping", "artifact_id", artifact.ID)
			continue
		}

		// Acquire semaphore
		sem <- struct{}{}
		wg.Add(1)

		go func(a storage.Artifact) { // #nosec G118 -- detached ctx is intentional (see below): LLM calls must survive parent shutdown
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore
			defer s.finishProcessingArtifact(a.ID)

			// Track in-flight artifact for shutdown visibility (v0.6.0 - CRIT-2)
			s.inFlightArtifacts.Store(a.ID, time.Now())
			defer s.inFlightArtifacts.Delete(a.ID)

			// Use detached context for cancellation safety during shutdown
			// Allows LLM call to complete even if parent context is cancelled
			// Use configurable timeout from config (v0.6.0 - CRIT-2)
			timeout := s.cfg.Agents.Extractor.GetTimeout()
			runCtx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			s.processSingleArtifact(runCtx, a)
		}(artifact)
	}

	// Wait for all extractions to complete
	wg.Wait()
}

// processSingleArtifact processes a single artifact through the extractor agent.
func (s *Service) processSingleArtifact(ctx context.Context, artifact storage.Artifact) {
	startTime := time.Now()
	userID := artifact.UserID
	artifactID := artifact.ID

	s.logger.Info("processing artifact",
		"user_id", userID,
		"artifact_id", artifactID,
		"file_type", artifact.FileType,
	)

	// Load user context for personalized extraction
	var shared *agent.SharedContext
	if s.contextService != nil {
		shared = s.contextService.Load(ctx, userID)
	}

	// Prepare request for extractor agent
	req := &agent.Request{
		Shared: shared,
		Params: map[string]any{
			"artifact": &artifact,
		},
	}

	// Execute extractor agent
	resp, err := s.extractorAgent.Execute(ctx, req)
	duration := time.Since(startTime)

	// Record metrics
	success := err == nil
	RecordArtifactExtraction(userID, duration.Seconds(), success)

	if err != nil {
		s.logger.Error("artifact extraction failed",
			"user_id", userID,
			"artifact_id", artifactID,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
		return
	}

	// Reload artifact vectors incrementally (summary embeddings only)
	if loadErr := s.LoadNewArtifactSummaries(); loadErr != nil {
		s.logger.Error("failed to reload artifact summaries",
			"user_id", userID,
			"artifact_id", artifactID,
			"error", loadErr,
		)
	}

	s.logger.Info("artifact extraction complete",
		"user_id", userID,
		"artifact_id", artifactID,
		"duration_ms", duration.Milliseconds(),
		"tokens", resp.Tokens.TotalTokens(),
	)

	// Log structured result if available
	if resp.Structured != nil {
		if result, ok := resp.Structured.(*extractor.ProcessResult); ok {
			s.logger.Debug("artifact extraction result",
				"user_id", userID,
				"artifact_id", artifactID,
				"summary", result.Summary,
				"keywords", result.Keywords,
				"entities", result.Entities,
			)
		}
	}
}
