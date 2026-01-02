package storage

import (
	"os"
)

// TableSize represents the size of a database table.
type TableSize struct {
	Name  string
	Bytes int64
}

// GetDBSize returns the size of the database file in bytes.
func (s *SQLiteStore) GetDBSize() (int64, error) {
	info, err := os.Stat(s.dbPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// GetTableSizes returns the size of each table in bytes using SQLite's dbstat virtual table.
func (s *SQLiteStore) GetTableSizes() ([]TableSize, error) {
	query := `
		SELECT name, SUM(pgsize) as size_bytes
		FROM dbstat
		WHERE name NOT LIKE 'sqlite_%'
		GROUP BY name
		ORDER BY size_bytes DESC
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sizes []TableSize
	for rows.Next() {
		var ts TableSize
		if err := rows.Scan(&ts.Name, &ts.Bytes); err != nil {
			return nil, err
		}
		sizes = append(sizes, ts)
	}
	return sizes, rows.Err()
}

// CleanupFactHistory removes old fact_history records, keeping only the N most recent per user.
// Returns the number of deleted rows.
func (s *SQLiteStore) CleanupFactHistory(keepPerUser int) (int64, error) {
	// Delete records that are not in the top N per user (by id DESC)
	query := `
		DELETE FROM fact_history
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY id DESC) as rn
				FROM fact_history
			) WHERE rn <= ?
		)
	`
	result, err := s.db.Exec(query, keepPerUser)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CleanupRagLogs removes old rag_logs records, keeping only the N most recent per user.
// Returns the number of deleted rows.
func (s *SQLiteStore) CleanupRagLogs(keepPerUser int) (int64, error) {
	// Delete records that are not in the top N per user (by id DESC)
	query := `
		DELETE FROM rag_logs
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY id DESC) as rn
				FROM rag_logs
			) WHERE rn <= ?
		)
	`
	result, err := s.db.Exec(query, keepPerUser)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
