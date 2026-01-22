package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AddPerson creates a new person record. Returns the new person ID.
func (s *SQLiteStore) AddPerson(person Person) (int64, error) {
	// Ensure aliases is never nil for proper JSON serialization
	if person.Aliases == nil {
		person.Aliases = []string{}
	}

	// Serialize aliases to JSON
	aliasesJSON, err := json.Marshal(person.Aliases)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal aliases: %w", err)
	}

	// Serialize embedding to JSON
	var embBytes []byte
	if len(person.Embedding) > 0 {
		embBytes, err = json.Marshal(person.Embedding)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal embedding: %w", err)
		}
	}

	// Set defaults
	if person.FirstSeen.IsZero() {
		person.FirstSeen = time.Now()
	}
	if person.LastSeen.IsZero() {
		person.LastSeen = time.Now()
	}
	if person.Circle == "" {
		person.Circle = "Other"
	}
	if person.MentionCount == 0 {
		person.MentionCount = 1
	}

	query := `
		INSERT INTO people (user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.Exec(query,
		person.UserID,
		person.DisplayName,
		string(aliasesJSON),
		person.TelegramID,
		person.Username,
		person.Circle,
		person.Bio,
		embBytes,
		person.FirstSeen.UTC().Format("2006-01-02 15:04:05.999"),
		person.LastSeen.UTC().Format("2006-01-02 15:04:05.999"),
		person.MentionCount,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert person: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert id: %w", err)
	}
	return id, nil
}

// UpdatePerson updates an existing person record.
func (s *SQLiteStore) UpdatePerson(person Person) error {
	aliasesJSON, err := json.Marshal(person.Aliases)
	if err != nil {
		return fmt.Errorf("failed to marshal aliases: %w", err)
	}

	var embBytes []byte
	if len(person.Embedding) > 0 {
		embBytes, err = json.Marshal(person.Embedding)
		if err != nil {
			return fmt.Errorf("failed to marshal embedding: %w", err)
		}
	}

	if person.LastSeen.IsZero() {
		person.LastSeen = time.Now()
	}

	query := `
		UPDATE people
		SET display_name = ?, aliases = ?, telegram_id = ?, username = ?, circle = ?, bio = ?, embedding = ?, last_seen = ?, mention_count = ?
		WHERE id = ? AND user_id = ?
	`
	_, err = s.db.Exec(query,
		person.DisplayName,
		string(aliasesJSON),
		person.TelegramID,
		person.Username,
		person.Circle,
		person.Bio,
		embBytes,
		person.LastSeen.UTC().Format("2006-01-02 15:04:05.999"),
		person.MentionCount,
		person.ID,
		person.UserID,
	)
	if err != nil {
		return fmt.Errorf("failed to update person: %w", err)
	}
	return nil
}

// DeletePerson removes a person record.
func (s *SQLiteStore) DeletePerson(userID, personID int64) error {
	query := "DELETE FROM people WHERE id = ? AND user_id = ?"
	_, err := s.db.Exec(query, personID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete person: %w", err)
	}
	return nil
}

// DeleteAllPeople removes all people for a user in a single query.
func (s *SQLiteStore) DeleteAllPeople(userID int64) error {
	query := "DELETE FROM people WHERE user_id = ?"
	_, err := s.db.Exec(query, userID)
	if err != nil {
		return fmt.Errorf("failed to delete all people: %w", err)
	}
	return nil
}

// GetPerson retrieves a single person by ID.
func (s *SQLiteStore) GetPerson(userID, personID int64) (*Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE id = ? AND user_id = ?
	`
	row := s.db.QueryRow(query, personID, userID)
	return s.scanPerson(row)
}

// GetPeople retrieves all people for a user.
func (s *SQLiteStore) GetPeople(userID int64) ([]Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ?
		ORDER BY display_name ASC
	`
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query people: %w", err)
	}
	defer rows.Close()
	return s.scanPeople(rows)
}

// GetPeopleByIDs retrieves people by their IDs.
func (s *SQLiteStore) GetPeopleByIDs(userID int64, ids []int64) ([]Person, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat(",?", len(ids)-1)
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ? AND id IN (?` + placeholders + `)
	`

	args := make([]interface{}, len(ids)+1)
	args[0] = userID
	for i, id := range ids {
		args[i+1] = id
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query people by IDs: %w", err)
	}
	defer rows.Close()
	return s.scanPeople(rows)
}

// GetAllPeople retrieves all people across all users.
// WARNING: Cross-user access - used for vector index loading only.
func (s *SQLiteStore) GetAllPeople() ([]Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		ORDER BY id ASC
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all people: %w", err)
	}
	defer rows.Close()
	return s.scanPeople(rows)
}

// GetPeopleAfterID retrieves people created after a given ID across all users.
// WARNING: Cross-user access - used for incremental vector index updates.
func (s *SQLiteStore) GetPeopleAfterID(minID int64) ([]Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE id > ?
		ORDER BY id ASC
	`
	rows, err := s.db.Query(query, minID)
	if err != nil {
		return nil, fmt.Errorf("failed to query people after ID: %w", err)
	}
	defer rows.Close()
	return s.scanPeople(rows)
}

// FindPersonByTelegramID finds a person by their Telegram ID.
func (s *SQLiteStore) FindPersonByTelegramID(userID, telegramID int64) (*Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ? AND telegram_id = ?
	`
	row := s.db.QueryRow(query, userID, telegramID)
	person, err := s.scanPerson(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return person, err
}

// FindPersonByUsername finds a person by their @username (without @).
func (s *SQLiteStore) FindPersonByUsername(userID int64, username string) (*Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ? AND LOWER(username) = LOWER(?)
	`
	row := s.db.QueryRow(query, userID, username)
	person, err := s.scanPerson(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return person, err
}

// FindPersonByName finds a person by their display name (exact match).
func (s *SQLiteStore) FindPersonByName(userID int64, name string) (*Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ? AND LOWER(display_name) = LOWER(?)
	`
	row := s.db.QueryRow(query, userID, name)
	person, err := s.scanPerson(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return person, err
}

// FindPersonByAlias finds people whose aliases contain the given string.
// Returns multiple matches since aliases might overlap.
func (s *SQLiteStore) FindPersonByAlias(userID int64, alias string) ([]Person, error) {
	// SQLite JSON search: check if alias is in the aliases array
	// We search case-insensitively by checking if the lowercase alias appears in aliases
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ? AND LOWER(aliases) LIKE LOWER(?)
	`
	// Use %"alias"% pattern to match JSON array elements
	pattern := fmt.Sprintf(`%%%q%%`, strings.ToLower(alias))
	rows, err := s.db.Query(query, userID, pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to find person by alias: %w", err)
	}
	defer rows.Close()
	return s.scanPeople(rows)
}

// MergePeople merges source person into target person, then deletes source.
// newUsername and newTelegramID are the values to set (only if non-nil/non-zero).
// If target already has username/telegram_id, those are preserved (callers should decide which to keep).
func (s *SQLiteStore) MergePeople(userID, targetID, sourceID int64, newBio string, newAliases []string, newUsername *string, newTelegramID *int64) error {
	// Start transaction
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Update target with new bio, aliases, username, and telegram_id
	aliasesJSON, err := json.Marshal(newAliases)
	if err != nil {
		return fmt.Errorf("failed to marshal aliases: %w", err)
	}

	updateQuery := `
		UPDATE people
		SET bio = ?, aliases = ?, username = ?, telegram_id = ?, last_seen = ?, mention_count = mention_count + (SELECT COALESCE(mention_count, 0) FROM people WHERE id = ?)
		WHERE id = ? AND user_id = ?
	`

	// Convert newUsername and newTelegramID to nullable types for SQL
	var usernameStr sql.NullString
	if newUsername != nil && *newUsername != "" {
		usernameStr.String = *newUsername
		usernameStr.Valid = true
	}

	var telegramID sql.NullInt64
	if newTelegramID != nil && *newTelegramID != 0 {
		telegramID.Int64 = *newTelegramID
		telegramID.Valid = true
	}

	_, err = tx.Exec(updateQuery, newBio, string(aliasesJSON), usernameStr, telegramID, time.Now().UTC().Format("2006-01-02 15:04:05.999"), sourceID, targetID, userID)
	if err != nil {
		return fmt.Errorf("failed to update target person: %w", err)
	}

	// Delete source
	deleteQuery := "DELETE FROM people WHERE id = ? AND user_id = ?"
	_, err = tx.Exec(deleteQuery, sourceID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete source person: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// GetPeopleExtended retrieves people with filtering and pagination.
func (s *SQLiteStore) GetPeopleExtended(filter PersonFilter, limit, offset int, sortBy, sortDir string) (PersonResult, error) {
	var result PersonResult

	// Build WHERE clause
	var conditions []string
	var args []interface{}

	if filter.UserID != 0 {
		conditions = append(conditions, "user_id = ?")
		args = append(args, filter.UserID)
	}
	if filter.Circle != "" {
		conditions = append(conditions, "circle = ?")
		args = append(args, filter.Circle)
	}
	if filter.Search != "" {
		conditions = append(conditions, "(LOWER(display_name) LIKE LOWER(?) OR LOWER(bio) LIKE LOWER(?) OR LOWER(aliases) LIKE LOWER(?))")
		searchPattern := "%" + filter.Search + "%"
		args = append(args, searchPattern, searchPattern, searchPattern)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total
	countQuery := "SELECT COUNT(*) FROM people " + whereClause
	if err := s.db.QueryRow(countQuery, args...).Scan(&result.TotalCount); err != nil {
		return result, fmt.Errorf("failed to count people: %w", err)
	}

	// Validate sort column
	validSortColumns := map[string]bool{
		"display_name":  true,
		"circle":        true,
		"first_seen":    true,
		"last_seen":     true,
		"mention_count": true,
	}
	if !validSortColumns[sortBy] {
		sortBy = "display_name"
	}
	if sortDir != "ASC" && sortDir != "DESC" {
		sortDir = "ASC"
	}

	// Build query with pagination
	query := fmt.Sprintf(`
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		%s
		ORDER BY %s %s
		LIMIT ? OFFSET ?
	`, whereClause, sortBy, sortDir)

	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return result, fmt.Errorf("failed to query people: %w", err)
	}
	defer rows.Close()

	result.Data, err = s.scanPeople(rows)
	if err != nil {
		return result, err
	}
	return result, nil
}

// CountPeopleWithoutEmbedding returns count of people missing embeddings.
func (s *SQLiteStore) CountPeopleWithoutEmbedding(userID int64) (int, error) {
	var count int
	query := "SELECT COUNT(*) FROM people WHERE user_id = ? AND (embedding IS NULL OR embedding = '' OR embedding = '[]')"
	if err := s.db.QueryRow(query, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count people without embedding: %w", err)
	}
	return count, nil
}

// GetPeopleWithoutEmbedding returns people missing embeddings.
func (s *SQLiteStore) GetPeopleWithoutEmbedding(userID int64) ([]Person, error) {
	query := `
		SELECT id, user_id, display_name, aliases, telegram_id, username, circle, bio, embedding, first_seen, last_seen, mention_count
		FROM people
		WHERE user_id = ? AND (embedding IS NULL OR embedding = '' OR embedding = '[]')
	`
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query people without embedding: %w", err)
	}
	defer rows.Close()
	return s.scanPeople(rows)
}

// Helper: scan a single person row.
func (s *SQLiteStore) scanPerson(row *sql.Row) (*Person, error) {
	var p Person
	var aliasesJSON string
	var embBytes []byte
	var telegramID sql.NullInt64
	var username sql.NullString

	err := row.Scan(
		&p.ID,
		&p.UserID,
		&p.DisplayName,
		&aliasesJSON,
		&telegramID,
		&username,
		&p.Circle,
		&p.Bio,
		&embBytes,
		&p.FirstSeen,
		&p.LastSeen,
		&p.MentionCount,
	)
	if err != nil {
		return nil, err
	}

	// Parse nullable fields
	if telegramID.Valid {
		p.TelegramID = &telegramID.Int64
	}
	if username.Valid {
		p.Username = &username.String
	}

	// Parse aliases JSON
	if aliasesJSON != "" && aliasesJSON != "[]" {
		if err := json.Unmarshal([]byte(aliasesJSON), &p.Aliases); err != nil {
			s.logger.Warn("failed to unmarshal person aliases", "id", p.ID, "error", err)
			p.Aliases = []string{}
		}
	} else {
		p.Aliases = []string{}
	}

	// Parse embedding JSON
	if len(embBytes) > 0 {
		if err := json.Unmarshal(embBytes, &p.Embedding); err != nil {
			s.logger.Warn("failed to unmarshal person embedding", "id", p.ID, "error", err)
		}
	}

	return &p, nil
}

// Helper: scan multiple person rows.
func (s *SQLiteStore) scanPeople(rows *sql.Rows) ([]Person, error) {
	var people []Person
	for rows.Next() {
		var p Person
		var aliasesJSON string
		var embBytes []byte
		var telegramID sql.NullInt64
		var username sql.NullString

		err := rows.Scan(
			&p.ID,
			&p.UserID,
			&p.DisplayName,
			&aliasesJSON,
			&telegramID,
			&username,
			&p.Circle,
			&p.Bio,
			&embBytes,
			&p.FirstSeen,
			&p.LastSeen,
			&p.MentionCount,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan person: %w", err)
		}

		// Parse nullable fields
		if telegramID.Valid {
			p.TelegramID = &telegramID.Int64
		}
		if username.Valid {
			p.Username = &username.String
		}

		// Parse aliases JSON
		if aliasesJSON != "" && aliasesJSON != "[]" {
			if err := json.Unmarshal([]byte(aliasesJSON), &p.Aliases); err != nil {
				s.logger.Warn("failed to unmarshal person aliases", "id", p.ID, "error", err)
				p.Aliases = []string{}
			}
		} else {
			p.Aliases = []string{}
		}

		// Parse embedding JSON
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &p.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal person embedding", "id", p.ID, "error", err)
			}
		}

		people = append(people, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating people rows: %w", err)
	}
	return people, nil
}
