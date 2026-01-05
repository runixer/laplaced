package storage

import (
	"database/sql"
	"time"
)

func (s *SQLiteStore) AddRAGLog(log RAGLog) error {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}
	query := `
	INSERT INTO rag_logs (
		user_id, original_query, enriched_query, enrichment_prompt, 
		context_used, system_prompt, retrieval_results, llm_response,
		enrichment_tokens, generation_tokens, total_cost_usd, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		log.UserID, log.OriginalQuery, log.EnrichedQuery, log.EnrichmentPrompt,
		log.ContextUsed, log.SystemPrompt, log.RetrievalResults, log.LLMResponse,
		log.EnrichmentTokens, log.GenerationTokens, log.TotalCostUSD, log.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"),
	)
	return err
}

func (s *SQLiteStore) GetRAGLogs(userID int64, limit int) ([]RAGLog, error) {
	var rows *sql.Rows
	var err error

	if userID != 0 {
		query := `
		SELECT
			id, user_id, original_query, enriched_query, enrichment_prompt,
			context_used, system_prompt, retrieval_results, llm_response,
			enrichment_tokens, generation_tokens, total_cost_usd, created_at
		FROM rag_logs
		WHERE user_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
		rows, err = s.db.Query(query, userID, limit)
	} else {
		query := `
		SELECT
			id, user_id, original_query, enriched_query, enrichment_prompt,
			context_used, system_prompt, retrieval_results, llm_response,
			enrichment_tokens, generation_tokens, total_cost_usd, created_at
		FROM rag_logs
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
		rows, err = s.db.Query(query, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []RAGLog
	for rows.Next() {
		var l RAGLog
		err := rows.Scan(
			&l.ID, &l.UserID, &l.OriginalQuery, &l.EnrichedQuery, &l.EnrichmentPrompt,
			&l.ContextUsed, &l.SystemPrompt, &l.RetrievalResults, &l.LLMResponse,
			&l.EnrichmentTokens, &l.GenerationTokens, &l.TotalCostUSD, &l.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

func (s *SQLiteStore) GetTopicExtractionLogs(limit, offset int) ([]RAGLog, int, error) {
	// Count
	var total int
	err := s.db.QueryRow("SELECT COUNT(*) FROM rag_logs WHERE original_query = 'Topic Extraction'").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	query := `
		SELECT
			id, user_id, original_query, enriched_query, enrichment_prompt,
			context_used, system_prompt, retrieval_results, llm_response,
			enrichment_tokens, generation_tokens, total_cost_usd, created_at
		FROM rag_logs
		WHERE original_query = 'Topic Extraction'
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?`

	rows, err := s.db.Query(query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []RAGLog
	for rows.Next() {
		var l RAGLog
		err := rows.Scan(
			&l.ID, &l.UserID, &l.OriginalQuery, &l.EnrichedQuery, &l.EnrichmentPrompt,
			&l.ContextUsed, &l.SystemPrompt, &l.RetrievalResults, &l.LLMResponse,
			&l.EnrichmentTokens, &l.GenerationTokens, &l.TotalCostUSD, &l.CreatedAt,
		)
		if err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, nil
}

// AddRerankerLog saves a reranker debug trace.
func (s *SQLiteStore) AddRerankerLog(log RerankerLog) error {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}
	query := `
	INSERT INTO reranker_logs (
		user_id, original_query, enriched_query, candidates_json,
		tool_calls_json, selected_ids_json, reasoning_json, fallback_reason,
		duration_enrichment_ms, duration_vector_ms, duration_reranker_ms,
		duration_total_ms, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		log.UserID, log.OriginalQuery, log.EnrichedQuery, log.CandidatesJSON,
		log.ToolCallsJSON, log.SelectedIDsJSON, log.ReasoningJSON, log.FallbackReason,
		log.DurationEnrichmentMs, log.DurationVectorMs, log.DurationRerankerMs,
		log.DurationTotalMs, log.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"),
	)
	return err
}

// GetRerankerLogs retrieves recent reranker traces for a user.
func (s *SQLiteStore) GetRerankerLogs(userID int64, limit int) ([]RerankerLog, error) {
	var rows *sql.Rows
	var err error

	if userID != 0 {
		query := `
		SELECT
			id, user_id, original_query, enriched_query, candidates_json,
			tool_calls_json, selected_ids_json, COALESCE(reasoning_json, ''), fallback_reason,
			duration_enrichment_ms, duration_vector_ms, duration_reranker_ms,
			duration_total_ms, created_at
		FROM reranker_logs
		WHERE user_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
		rows, err = s.db.Query(query, userID, limit)
	} else {
		query := `
		SELECT
			id, user_id, original_query, enriched_query, candidates_json,
			tool_calls_json, selected_ids_json, COALESCE(reasoning_json, ''), fallback_reason,
			duration_enrichment_ms, duration_vector_ms, duration_reranker_ms,
			duration_total_ms, created_at
		FROM reranker_logs
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
		rows, err = s.db.Query(query, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []RerankerLog
	for rows.Next() {
		var l RerankerLog
		err := rows.Scan(
			&l.ID, &l.UserID, &l.OriginalQuery, &l.EnrichedQuery, &l.CandidatesJSON,
			&l.ToolCallsJSON, &l.SelectedIDsJSON, &l.ReasoningJSON, &l.FallbackReason,
			&l.DurationEnrichmentMs, &l.DurationVectorMs, &l.DurationRerankerMs,
			&l.DurationTotalMs, &l.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}
