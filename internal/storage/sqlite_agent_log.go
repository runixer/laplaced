package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// AddAgentLog inserts a new agent log entry.
func (s *SQLiteStore) AddAgentLog(log AgentLog) error {
	query := `
		INSERT INTO agent_logs (
			user_id, agent_type, input_prompt, input_context, output_response,
			output_parsed, output_context, model, prompt_tokens, completion_tokens,
			total_cost, duration_ms, metadata, success, error_message, conversation_turns
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.Exec(query,
		log.UserID,
		log.AgentType,
		log.InputPrompt,
		log.InputContext,
		log.OutputResponse,
		log.OutputParsed,
		log.OutputContext,
		log.Model,
		log.PromptTokens,
		log.CompletionTokens,
		log.TotalCost,
		log.DurationMs,
		log.Metadata,
		log.Success,
		log.ErrorMessage,
		log.ConversationTurns,
	)
	if err != nil {
		return fmt.Errorf("failed to add agent log: %w", err)
	}
	return nil
}

// GetAgentLogs returns the most recent agent logs for a specific agent type.
// If userID is 0, returns logs for all users.
func (s *SQLiteStore) GetAgentLogs(agentType string, userID int64, limit int) ([]AgentLog, error) {
	var query string
	var args []interface{}

	if userID > 0 {
		query = `
			SELECT id, user_id, agent_type, input_prompt, input_context, output_response,
				   output_parsed, output_context, model, prompt_tokens, completion_tokens,
				   total_cost, duration_ms, metadata, success, error_message, conversation_turns, created_at
			FROM agent_logs
			WHERE agent_type = ? AND user_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		`
		args = []interface{}{agentType, userID, limit}
	} else {
		query = `
			SELECT id, user_id, agent_type, input_prompt, input_context, output_response,
				   output_parsed, output_context, model, prompt_tokens, completion_tokens,
				   total_cost, duration_ms, metadata, success, error_message, conversation_turns, created_at
			FROM agent_logs
			WHERE agent_type = ?
			ORDER BY created_at DESC
			LIMIT ?
		`
		args = []interface{}{agentType, limit}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query agent logs: %w", err)
	}
	defer rows.Close()

	return s.scanAgentLogs(rows)
}

// GetAgentLogsExtended returns agent logs with filtering and pagination.
func (s *SQLiteStore) GetAgentLogsExtended(filter AgentLogFilter, limit, offset int) (AgentLogResult, error) {
	var result AgentLogResult

	// Build WHERE clause
	var conditions []string
	var args []interface{}

	if filter.AgentType != "" {
		conditions = append(conditions, "agent_type = ?")
		args = append(args, filter.AgentType)
	}

	if filter.UserID > 0 {
		conditions = append(conditions, "user_id = ?")
		args = append(args, filter.UserID)
	}

	if filter.Success != nil {
		conditions = append(conditions, "success = ?")
		args = append(args, *filter.Success)
	}

	if filter.Search != "" {
		conditions = append(conditions, "(input_prompt LIKE ? OR output_response LIKE ? OR metadata LIKE ?)")
		searchPattern := "%" + filter.Search + "%"
		args = append(args, searchPattern, searchPattern, searchPattern)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total
	countQuery := "SELECT COUNT(*) FROM agent_logs " + whereClause
	if err := s.db.QueryRow(countQuery, args...).Scan(&result.TotalCount); err != nil {
		return result, fmt.Errorf("failed to count agent logs: %w", err)
	}

	// Fetch data
	dataQuery := fmt.Sprintf(`
		SELECT id, user_id, agent_type, input_prompt, input_context, output_response,
			   output_parsed, output_context, model, prompt_tokens, completion_tokens,
			   total_cost, duration_ms, metadata, success, error_message, conversation_turns, created_at
		FROM agent_logs
		%s
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, whereClause)

	args = append(args, limit, offset)
	rows, err := s.db.Query(dataQuery, args...)
	if err != nil {
		return result, fmt.Errorf("failed to query agent logs: %w", err)
	}
	defer rows.Close()

	result.Data, err = s.scanAgentLogs(rows)
	if err != nil {
		return result, err
	}

	return result, nil
}

// scanAgentLogs scans rows into AgentLog slice.
func (s *SQLiteStore) scanAgentLogs(rows *sql.Rows) ([]AgentLog, error) {
	var logs []AgentLog

	for rows.Next() {
		var log AgentLog
		var conversationTurns sql.NullString
		err := rows.Scan(
			&log.ID,
			&log.UserID,
			&log.AgentType,
			&log.InputPrompt,
			&log.InputContext,
			&log.OutputResponse,
			&log.OutputParsed,
			&log.OutputContext,
			&log.Model,
			&log.PromptTokens,
			&log.CompletionTokens,
			&log.TotalCost,
			&log.DurationMs,
			&log.Metadata,
			&log.Success,
			&log.ErrorMessage,
			&conversationTurns,
			&log.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan agent log: %w", err)
		}
		if conversationTurns.Valid {
			log.ConversationTurns = conversationTurns.String
		}
		logs = append(logs, log)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return logs, nil
}
