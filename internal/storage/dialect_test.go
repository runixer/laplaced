package storage

import (
	"strings"
	"testing"
	"time"
)

func TestRebind(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"no placeholders", "SELECT 1", "SELECT 1"},
		{"single", "WHERE user_id = ?", "WHERE user_id = $1"},
		{"multiple", "VALUES (?, ?, ?)", "VALUES ($1, $2, $3)"},
		{"expanded IN + trailing", "WHERE id IN (?,?,?) LIMIT ? OFFSET ?", "WHERE id IN ($1,$2,$3) LIMIT $4 OFFSET $5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// SQLite is identity.
			if got := (sqliteDialect{}).Rebind(tt.query); got != tt.query {
				t.Errorf("sqlite Rebind(%q) = %q, want identity", tt.query, got)
			}
			// Postgres numbers left-to-right.
			if got := (postgresDialect{}).Rebind(tt.query); got != tt.want {
				t.Errorf("postgres Rebind(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestRebindPlaceholderCount guards risk #5: the count of $N must equal the
// count of ? for every query the codebase emits (left-to-right ordering).
func TestRebindPlaceholderCount(t *testing.T) {
	queries := []string{
		"INSERT INTO topics (a, b, c) VALUES (?, ?, ?)",
		"SELECT * FROM t WHERE user_id = ? AND id IN (?,?,?) ORDER BY id LIMIT ? OFFSET ?",
		"UPDATE x SET y = ? WHERE user_id = ? AND id >= ? AND id <= ?",
	}
	for _, q := range queries {
		got := (postgresDialect{}).Rebind(q)
		if want, have := strings.Count(q, "?"), strings.Count(got, "$"); want != have {
			t.Errorf("Rebind(%q): %d placeholders -> %d $N", q, want, have)
		}
	}
}

func TestBindTime(t *testing.T) {
	ts := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	// SQLite formats to the canonical string (byte-identical to pre-dialect).
	if got := (sqliteDialect{}).BindTime(ts); got != "2026-01-02 15:04:05" {
		t.Errorf("sqlite BindTime = %v, want canonical string", got)
	}
	// Postgres binds a time.Time.
	if _, ok := (postgresDialect{}).BindTime(ts).(time.Time); !ok {
		t.Errorf("postgres BindTime should return time.Time, got %T", (postgresDialect{}).BindTime(ts))
	}
}

func TestBoolLit(t *testing.T) {
	if (sqliteDialect{}).BoolLit(true) != "1" || (sqliteDialect{}).BoolLit(false) != "0" {
		t.Error("sqlite BoolLit should be 1/0")
	}
	if (postgresDialect{}).BoolLit(true) != "TRUE" || (postgresDialect{}).BoolLit(false) != "FALSE" {
		t.Error("postgres BoolLit should be TRUE/FALSE")
	}
}

func TestDialectExpressionsRender(t *testing.T) {
	// Smoke: every dialect-divergent expression renders the expected backend form.
	if got := (sqliteDialect{}).SinceDaysPredicate("created_at", 14); !strings.Contains(got, "date('now'") {
		t.Errorf("sqlite SinceDaysPredicate = %q", got)
	}
	if got := (postgresDialect{}).SinceDaysPredicate("created_at", 14); !strings.Contains(got, "interval") {
		t.Errorf("postgres SinceDaysPredicate = %q", got)
	}
	if got := (postgresDialect{}).DateExpr("created_at"); !strings.Contains(got, "to_char") {
		t.Errorf("postgres DateExpr = %q", got)
	}
}
