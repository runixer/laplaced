package storage

import (
	"fmt"
)

func (s *SQLiteStore) AddStat(stat Stat) error {
	query := "INSERT INTO stats (user_id, tokens_used, cost_usd) VALUES (?, ?, ?)"
	_, err := s.db.Exec(query, stat.UserID, stat.TokensUsed, stat.CostUSD)
	return err
}

func (s *SQLiteStore) GetStats() (map[int64]Stat, error) {
	query := "SELECT user_id, SUM(tokens_used), SUM(cost_usd) FROM stats GROUP BY user_id"
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[int64]Stat)
	for rows.Next() {
		var stat Stat
		if err := rows.Scan(&stat.UserID, &stat.TokensUsed, &stat.CostUSD); err != nil {
			return nil, err
		}
		stats[stat.UserID] = stat
	}
	return stats, nil
}

func (s *SQLiteStore) GetDashboardStats(userID int64) (*DashboardStats, error) {
	stats := &DashboardStats{
		FactsByCategory: make(map[string]int),
		FactsByType:     make(map[string]int),
		MessagesPerDay:  make(map[string]int),
		FactsGrowth:     make(map[string]int),
	}

	// Helper for WHERE clause
	whereUser := ""
	var args []interface{}
	if userID != 0 {
		whereUser = " WHERE user_id = ?"
		args = append(args, userID)
	}

	// 1. Topics Stats
	// Total Topics
	err := s.db.QueryRow("SELECT COUNT(*) FROM topics"+whereUser, args...).Scan(&stats.TotalTopics)
	if err != nil {
		return nil, fmt.Errorf("failed to count topics: %w", err)
	}

	if stats.TotalTopics > 0 {
		// Avg Size
		err = s.db.QueryRow("SELECT AVG(end_msg_id - start_msg_id + 1) FROM topics"+whereUser, args...).Scan(&stats.AvgTopicSize)
		if err != nil {
			return nil, fmt.Errorf("failed to calc avg topic size: %w", err)
		}

		// Processed Count
		var processedCount int
		whereProcessed := " WHERE facts_extracted = 1"
		if userID != 0 {
			whereProcessed += " AND user_id = ?"
		}
		err = s.db.QueryRow("SELECT COUNT(*) FROM topics"+whereProcessed, args...).Scan(&processedCount)
		if err != nil {
			return nil, fmt.Errorf("failed to count processed topics: %w", err)
		}
		stats.ProcessedTopicsPct = float64(processedCount) / float64(stats.TotalTopics) * 100

		// Consolidated Count
		var consolidatedCount int
		whereConsolidated := " WHERE is_consolidated = 1"
		if userID != 0 {
			whereConsolidated += " AND user_id = ?"
		}
		err = s.db.QueryRow("SELECT COUNT(*) FROM topics"+whereConsolidated, args...).Scan(&consolidatedCount)
		if err != nil {
			return nil, fmt.Errorf("failed to count consolidated topics: %w", err)
		}
		stats.ConsolidatedTopics = consolidatedCount
	}

	// 2. Facts Stats
	err = s.db.QueryRow("SELECT COUNT(*) FROM structured_facts"+whereUser, args...).Scan(&stats.TotalFacts)
	if err != nil {
		return nil, fmt.Errorf("failed to count facts: %w", err)
	}

	// Facts by Category
	rows, err := s.db.Query("SELECT category, COUNT(*) FROM structured_facts"+whereUser+" GROUP BY category", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to group facts by category: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		stats.FactsByCategory[cat] = count
	}

	// Facts by Type
	rowsType, err := s.db.Query("SELECT type, COUNT(*) FROM structured_facts"+whereUser+" GROUP BY type", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to group facts by type: %w", err)
	}
	defer rowsType.Close()
	for rowsType.Next() {
		var t string
		var count int
		if err := rowsType.Scan(&t, &count); err != nil {
			return nil, err
		}
		stats.FactsByType[t] = count
	}

	// 3. Messages Stats
	err = s.db.QueryRow("SELECT COUNT(*) FROM history"+whereUser, args...).Scan(&stats.TotalMessages)
	if err != nil {
		return nil, fmt.Errorf("failed to count messages: %w", err)
	}

	whereUnprocessed := " WHERE topic_id IS NULL"
	if userID != 0 {
		whereUnprocessed += " AND user_id = ?"
	}
	err = s.db.QueryRow("SELECT COUNT(*) FROM history"+whereUnprocessed, args...).Scan(&stats.UnprocessedMessages)
	if err != nil {
		return nil, fmt.Errorf("failed to count unprocessed messages: %w", err)
	}

	// 4. RAG Stats
	err = s.db.QueryRow("SELECT COUNT(*), COALESCE(AVG(total_cost_usd), 0) FROM rag_logs"+whereUser, args...).Scan(&stats.TotalRAGQueries, &stats.AvgRAGCost)
	if err != nil {
		return nil, fmt.Errorf("failed to get rag stats: %w", err)
	}

	// 5. Activity (Messages per Day) - Last 14 days
	dateFilter := "created_at >= date('now', '-14 days')"
	whereActivity := " WHERE " + dateFilter
	if userID != 0 {
		whereActivity += " AND user_id = ?"
	}

	queryActivity := "SELECT date(created_at), COUNT(*) FROM history" + whereActivity + " GROUP BY date(created_at) ORDER BY date(created_at)"
	rowsActivity, err := s.db.Query(queryActivity, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get activity stats: %w", err)
	}
	defer rowsActivity.Close()
	for rowsActivity.Next() {
		var date string
		var count int
		if err := rowsActivity.Scan(&date, &count); err != nil {
			return nil, err
		}
		stats.MessagesPerDay[date] = count
	}

	// 6. Facts Growth (Facts Created Over Time) - Last 30 days
	dateFilterFacts := "created_at >= date('now', '-30 days')"
	whereFactsGrowth := " WHERE " + dateFilterFacts
	if userID != 0 {
		whereFactsGrowth += " AND user_id = ?"
	}

	queryFactsGrowth := "SELECT date(created_at), COUNT(*) FROM structured_facts" + whereFactsGrowth + " GROUP BY date(created_at) ORDER BY date(created_at)"
	rowsFactsGrowth, err := s.db.Query(queryFactsGrowth, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get facts growth stats: %w", err)
	}
	defer rowsFactsGrowth.Close()
	for rowsFactsGrowth.Next() {
		var date string
		var count int
		if err := rowsFactsGrowth.Scan(&date, &count); err != nil {
			return nil, err
		}
		stats.FactsGrowth[date] = count
	}

	return stats, nil
}
