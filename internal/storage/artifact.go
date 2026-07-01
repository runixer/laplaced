package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// scannable wraps the Scan method for both *sql.Row and *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}

// scanArtifactRow scans a single artifact row from a scannable (sql.Row or sql.Rows).
// Handles nullable fields and JSON unmarshaling.
func scanArtifactRow(s scannable) (*Artifact, error) {
	var artifact Artifact
	var processedAt, lastFailedAt sql.NullTime
	var errorMessage, summary, keywords, entities, ragHints sql.NullString
	var retryCount sql.NullInt64
	var lastLoadedAt sql.NullTime
	var contextLoadCount sql.NullInt64
	var userContext sql.NullString // v0.6.0
	var embeddingJSON []byte

	err := s.Scan(
		&artifact.ID,
		&artifact.UserID,
		&artifact.MessageID,
		&artifact.FileType,
		&artifact.FilePath,
		&artifact.FileSize,
		&artifact.MimeType,
		&artifact.OriginalName,
		&artifact.ContentHash,
		&artifact.State,
		&errorMessage,
		&retryCount,
		&lastFailedAt,
		&summary,
		&keywords,
		&entities,
		&ragHints,
		&embeddingJSON,
		&artifact.CreatedAt,
		&processedAt,
		&contextLoadCount,
		&lastLoadedAt,
		&userContext, // v0.6.0
	)
	if err != nil {
		return nil, err
	}

	// Convert nullable fields
	if errorMessage.Valid {
		artifact.ErrorMessage = &errorMessage.String
	}
	if retryCount.Valid {
		artifact.RetryCount = int(retryCount.Int64)
	}
	if lastFailedAt.Valid {
		artifact.LastFailedAt = &lastFailedAt.Time
	}
	if summary.Valid {
		artifact.Summary = &summary.String
	}
	if keywords.Valid {
		artifact.Keywords = &keywords.String
	}
	if entities.Valid {
		artifact.Entities = &entities.String
	}
	if ragHints.Valid {
		artifact.RAGHints = &ragHints.String
	}
	// Embedding is optional - ignore unmarshal errors
	_ = json.Unmarshal(embeddingJSON, &artifact.Embedding)
	if processedAt.Valid {
		artifact.ProcessedAt = &processedAt.Time
	}
	if contextLoadCount.Valid {
		artifact.ContextLoadCount = int(contextLoadCount.Int64)
	}
	if lastLoadedAt.Valid {
		artifact.LastLoadedAt = &lastLoadedAt.Time
	}
	// User context (v0.6.0)
	if userContext.Valid {
		artifact.UserContext = &userContext.String
	}

	return &artifact, nil
}

// AddArtifact saves a new artifact to the database.
// Returns the ID of the inserted artifact.
// If an artifact with the same content_hash exists for the user, returns existing artifact ID.
func (s *Store) AddArtifact(artifact Artifact) (int64, error) {
	// Check for duplicate by hash
	existing, err := s.GetByHash(artifact.UserID, artifact.ContentHash)
	if err == nil && existing != nil {
		s.logger.Info("artifact already exists (deduplication)",
			"user_id", artifact.UserID,
			"existing_id", existing.ID,
			"content_hash", artifact.ContentHash,
		)
		return existing.ID, nil
	}

	query := `
		INSERT INTO artifacts (
			user_id, message_id, file_type, file_path, file_size,
			mime_type, original_name, content_hash, state, user_context
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Convert UserContext to interface{} for NULL handling (v0.6.0)
	var userContextIface interface{}
	if artifact.UserContext != nil {
		userContextIface = *artifact.UserContext
	}

	id, err := s.insertReturningID(query, "id",
		artifact.UserID,
		artifact.MessageID,
		artifact.FileType,
		artifact.FilePath,
		artifact.FileSize,
		artifact.MimeType,
		artifact.OriginalName,
		artifact.ContentHash,
		artifact.State,
		userContextIface,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert artifact: %w", err)
	}

	s.logger.Info("artifact created",
		"id", id,
		"user_id", artifact.UserID,
		"message_id", artifact.MessageID,
		"file_type", artifact.FileType,
		"file_size", artifact.FileSize,
	)

	return id, nil
}

// GetArtifact retrieves an artifact by ID and user ID.
func (s *Store) GetArtifact(userID ScopeID, artifactID int64) (*Artifact, error) {
	query := `
		SELECT id, user_id, message_id, file_type, file_path, file_size,
			mime_type, original_name, content_hash, state, error_message,
			retry_count, last_failed_at,
			summary, keywords, entities, rag_hints, embedding,
			created_at, processed_at,
			context_load_count, last_loaded_at, user_context
		FROM artifacts
		WHERE user_id = ? AND id = ?
	`

	row := s.queryRow(query, userID, artifactID)
	artifact, err := scanArtifactRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact: %w", err)
	}

	return artifact, nil
}

// GetByHash retrieves an artifact by content hash and user ID.
// Used for deduplication checks.
func (s *Store) GetByHash(userID ScopeID, contentHash string) (*Artifact, error) {
	query := `
		SELECT id, user_id, message_id, file_type, file_path, file_size,
			mime_type, original_name, content_hash, state, error_message,
			retry_count, last_failed_at,
			summary, keywords, entities, rag_hints, embedding,
			created_at, processed_at,
			context_load_count, last_loaded_at, user_context
		FROM artifacts
		WHERE user_id = ? AND content_hash = ?
	`

	row := s.queryRow(query, userID, contentHash)
	artifact, err := scanArtifactRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact by hash: %w", err)
	}

	return artifact, nil
}

// GetPendingArtifacts retrieves artifacts ready for processing.
// Includes:
// - state='pending' (new artifacts)
// - state='failed' with retry_count < maxRetries and sufficient backoff elapsed (v0.6.0 - CRIT-3)
// Backoff schedule: 1 min (retry 0), 5 min (retry 1), 30 min (retry 2+)
func (s *Store) GetPendingArtifacts(userID ScopeID, maxRetries int) ([]Artifact, error) {
	query := fmt.Sprintf(`
		SELECT id, user_id, message_id, file_type, file_path, file_size,
			mime_type, original_name, content_hash, state, error_message,
			retry_count, last_failed_at,
			summary, keywords, entities, rag_hints, embedding,
			created_at, processed_at,
			context_load_count, last_loaded_at, user_context
		FROM artifacts
		WHERE user_id = ?
		  AND (
			state = 'pending'
			OR (
				state = 'failed'
				AND retry_count < ?
				AND (
					last_failed_at IS NULL
					OR (retry_count = 0 AND last_failed_at < %s)
					OR (retry_count = 1 AND last_failed_at < %s)
					OR (retry_count >= 2 AND last_failed_at < %s)
				)
			)
		  )
		ORDER BY created_at ASC
	`, s.dialect.MinutesAgoExpr(1), s.dialect.MinutesAgoExpr(5), s.dialect.MinutesAgoExpr(30))

	rows, err := s.query(query, userID, maxRetries)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		artifact, err := scanArtifactRow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan artifact: %w", err)
		}
		artifacts = append(artifacts, *artifact)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating artifacts: %w", err)
	}

	return artifacts, nil
}

// UpdateArtifact updates an artifact's metadata.
func (s *Store) UpdateArtifact(artifact Artifact) error {
	query := `
		UPDATE artifacts
		SET state = ?,
			error_message = ?,
			retry_count = ?,
			last_failed_at = ?,
			summary = ?,
			keywords = ?,
			entities = ?,
			rag_hints = ?,
			embedding = ?,
			processed_at = ?
		WHERE user_id = ? AND id = ?
	`

	var processedAt *time.Time
	if artifact.ProcessedAt != nil {
		processedAt = artifact.ProcessedAt
	} else if artifact.State == "ready" || artifact.State == "failed" {
		now := time.Now()
		processedAt = &now
	}

	// Convert *string to interface{} for NULL handling
	var errorMessageIface, summaryIface, keywordsIface, entitiesIface, ragHintsIface interface{}
	var lastFailedAtIface interface{}
	var embeddingJSON []byte

	if artifact.ErrorMessage != nil {
		errorMessageIface = *artifact.ErrorMessage
	}
	if artifact.LastFailedAt != nil {
		lastFailedAtIface = *artifact.LastFailedAt
	}
	if artifact.Summary != nil {
		summaryIface = *artifact.Summary
	}
	if artifact.Keywords != nil {
		keywordsIface = *artifact.Keywords
	}
	if artifact.Entities != nil {
		entitiesIface = *artifact.Entities
	}
	if artifact.RAGHints != nil {
		ragHintsIface = *artifact.RAGHints
	}
	if artifact.Embedding != nil {
		var err error
		embeddingJSON, err = json.Marshal(artifact.Embedding)
		if err != nil {
			return fmt.Errorf("failed to marshal embedding: %w", err)
		}
	}

	result, err := s.exec(query,
		artifact.State,
		errorMessageIface,
		artifact.RetryCount,
		lastFailedAtIface,
		summaryIface,
		keywordsIface,
		entitiesIface,
		ragHintsIface,
		embeddingJSON,
		processedAt,
		artifact.UserID,
		artifact.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update artifact: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("artifact not found (user_id=%s, id=%d)", artifact.UserID, artifact.ID)
	}

	return nil
}

// RecoverArtifactStates resets zombie 'processing' states to 'pending'.
// Called on startup to recover from crashes or interruptions.
// Only recovers artifacts that have been in 'processing' state for longer than threshold
// to avoid re-processing actively processing artifacts.
func (s *Store) RecoverArtifactStates(threshold time.Duration) error {
	query := fmt.Sprintf(`
		UPDATE artifacts
		SET state = 'pending', error_message = NULL
		WHERE state = 'processing'
		  AND %s
	`, s.dialect.SecondsAgoExpr("created_at"))

	result, err := s.exec(query, int64(threshold.Seconds()))
	if err != nil {
		return fmt.Errorf("failed to recover artifact states: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		s.logger.Info("recovered zombie artifact states",
			"count", rows,
			"threshold_seconds", int64(threshold.Seconds()),
		)
	}

	return nil
}

// GetArtifactsByIDs retrieves artifacts by their IDs (batch load).
func (s *Store) GetArtifactsByIDs(userID ScopeID, artifactIDs []int64) ([]Artifact, error) {
	if len(artifactIDs) == 0 {
		return nil, nil
	}

	query, args, err := ExpandIn(
		`SELECT id, user_id, message_id, file_type, file_path, file_size,
			mime_type, original_name, content_hash, state, error_message,
			retry_count, last_failed_at,
			summary, keywords, entities, rag_hints, embedding,
			created_at, processed_at,
			context_load_count, last_loaded_at, user_context
		FROM artifacts
		WHERE user_id = ? AND id IN (?)
		ORDER BY id ASC`,
		userID, artifactIDs,
	)
	if err != nil {
		return nil, err
	}

	rows, err := s.query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifacts by IDs: %w", err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		artifact, err := scanArtifactRow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan artifact: %w", err)
		}
		artifacts = append(artifacts, *artifact)
	}

	return artifacts, rows.Err()
}

// GetArtifacts retrieves artifacts for a user with optional filters and pagination.
// UserID is REQUIRED for data isolation.
func (s *Store) GetArtifacts(filter ArtifactFilter, limit, offset int) ([]Artifact, int64, error) {
	// Enforce user data isolation - UserID is required
	if filter.UserID == "" {
		return nil, 0, fmt.Errorf("UserID required for GetArtifacts")
	}

	// Build WHERE clause
	whereClause := "user_id = ?"
	countArgs := []interface{}{filter.UserID}
	args := []interface{}{filter.UserID}

	if filter.State != "" {
		whereClause += " AND state = ?"
		countArgs = append(countArgs, filter.State)
		args = append(args, filter.State)
	}
	if filter.FileType != "" {
		whereClause += " AND file_type = ?"
		countArgs = append(countArgs, filter.FileType)
		args = append(args, filter.FileType)
	}

	// Count query first
	countQuery := `SELECT COUNT(*) FROM artifacts WHERE ` + whereClause
	var total int64
	err := s.queryRow(countQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count artifacts: %w", err)
	}

	// Main query
	query := `
		SELECT id, user_id, message_id, file_type, file_path, file_size,
			mime_type, original_name, content_hash, state, error_message,
			retry_count, last_failed_at,
			summary, keywords, entities, rag_hints, embedding,
			created_at, processed_at,
			context_load_count, last_loaded_at, user_context
		FROM artifacts
		WHERE ` + whereClause + `
		ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		artifact, err := scanArtifactRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan artifact: %w", err)
		}
		artifacts = append(artifacts, *artifact)
	}

	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating artifacts: %w", err)
	}

	return artifacts, total, nil
}

// IncrementContextLoadCount increments the load counter for artifacts
// and updates last_loaded_at timestamp. Called asynchronously after
// artifacts are successfully loaded into LLM context (v0.6.0).
func (s *Store) IncrementContextLoadCount(userID ScopeID, artifactIDs []int64) error {
	if len(artifactIDs) == 0 {
		return nil
	}

	query, args, err := ExpandIn(
		`UPDATE artifacts
		SET context_load_count = context_load_count + 1,
		    last_loaded_at = ?
		WHERE user_id = ? AND id IN (?)`,
		s.dialect.BindTime(time.Now()), userID, artifactIDs,
	)
	if err != nil {
		return err
	}

	result, err := s.exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to increment context load count: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		s.logger.Debug("incremented artifact load counts",
			"user_id", userID,
			"count", rows,
		)
	}

	return nil
}

// GetSessionArtifacts returns artifacts attached to messages still in the active session
// (history rows with topic_id IS NULL). Used to ensure freshly-created files are exposed
// to the reranker even when their summary embedding doesn't match the next user query.
//
// Filters:
//   - state IN ('ready', 'pending', 'processing') — a just-sent file is 'pending' until the
//     Extractor picks it up, and that window is exactly when the user asks about it; the
//     file itself is loadable regardless, only the summary is missing. 'failed' stays
//     excluded so poisoned artifacts (e.g. safety-blocked ones) are never re-surfaced.
//   - message_id > 0 (skip in-flight rows where assistant-side message_id assignment hasn't completed)
//   - created_at within maxAge window (safety cap for stalled sessions)
//
// Double user_id filter (a.user_id AND h.user_id) is intentional defense-in-depth per the
// project's user-isolation invariants — session-aware queries with JOIN must enforce isolation
// on every joined table.
func (s *Store) GetSessionArtifacts(ctx context.Context, userID ScopeID, limit int, maxAge time.Duration) ([]Artifact, error) {
	if userID == "" {
		return nil, fmt.Errorf("UserID required for GetSessionArtifacts")
	}
	if limit <= 0 {
		return nil, nil
	}

	cutoff := s.dialect.BindTime(time.Now().Add(-maxAge))

	query := `
		SELECT a.id, a.user_id, a.message_id, a.file_type, a.file_path, a.file_size,
			a.mime_type, a.original_name, a.content_hash, a.state, a.error_message,
			a.retry_count, a.last_failed_at,
			a.summary, a.keywords, a.entities, a.rag_hints, a.embedding,
			a.created_at, a.processed_at,
			a.context_load_count, a.last_loaded_at, a.user_context
		FROM artifacts a
		JOIN history h ON a.message_id = h.id
		WHERE a.user_id = ?
		  AND h.user_id = ?
		  AND h.topic_id IS NULL
		  AND a.message_id > 0
		  AND a.state IN ('ready', 'pending', 'processing')
		  AND a.created_at > ?
		ORDER BY a.created_at DESC
		LIMIT ?
	`

	rows, err := s.queryContext(ctx, query, userID, userID, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get session artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		artifact, err := scanArtifactRow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session artifact: %w", err)
		}
		artifacts = append(artifacts, *artifact)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating session artifacts: %w", err)
	}

	return artifacts, nil
}

// UpdateMessageID links an artifact to a history message.
// Called after message is saved to history (message_id is not known during file processing).
// Requires userID for proper data isolation (CRIT-2 security fix).
func (s *Store) UpdateMessageID(userID ScopeID, artifactID, messageID int64) error {
	query := `UPDATE artifacts SET message_id = ? WHERE user_id = ? AND id = ?`

	result, err := s.exec(query, messageID, userID, artifactID)
	if err != nil {
		return fmt.Errorf("failed to update artifact message_id: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("artifact not found (user_id=%s, id=%d)", userID, artifactID)
	}

	s.logger.Debug("linked artifact to message",
		"user_id", userID,
		"artifact_id", artifactID,
		"message_id", messageID,
	)

	return nil
}
