package storage

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect abstracts the handful of SQL constructs that genuinely differ between
// the SQLite and PostgreSQL backends. Everything else — table
// shapes, ON CONFLICT upserts, CURRENT_TIMESTAMP defaults — is written once and
// shared. The SQLite dialect is a behavioral no-op: its generated SQL stays
// byte-identical to the pre-dialect code, so the existing SQLite deployment is
// unaffected.
//
// Ordering rule: ExpandIn (sqlin.go) expands `(?)` → `(?,?,?)` FIRST; only then
// does Rebind number the placeholders. Never Rebind before ExpandIn.
type Dialect interface {
	// Name reports the backend ("sqlite" | "postgres").
	Name() string

	// Rebind converts `?` placeholders into the dialect's parameter syntax.
	// SQLite keeps `?`; Postgres numbers them left-to-right ($1, $2, ...).
	Rebind(query string) string

	// BindTime returns the value to bind for a timestamp column. SQLite stores
	// the canonical "2006-01-02 15:04:05.999" UTC string (preserving the exact
	// original SQLite representation); Postgres binds time.Time for native timestamptz.
	BindTime(t time.Time) any

	// SinceDaysPredicate renders a "<col> >= <now − days>" predicate.
	SinceDaysPredicate(col string, days int) string

	// DateExpr renders day-truncation of a timestamp column (for GROUP BY day).
	DateExpr(col string) string

	// AvgAgeDaysExpr renders AVG age-in-days of <col> relative to now.
	AvgAgeDaysExpr(col string) string

	// MinutesAgoExpr renders the timestamp "<now − minutes>" as a SQL expression
	// (no bound parameter) for inline retry-backoff predicates.
	MinutesAgoExpr(minutes int) string

	// SecondsAgoExpr renders "<col> < <now − ? seconds>", where the seconds
	// count is supplied by a single bound `?` parameter at the call site.
	SecondsAgoExpr(col string) string

	// BoolLit renders a boolean literal for WHERE comparisons (SQLite: 0/1,
	// Postgres: FALSE/TRUE).
	BoolLit(b bool) string
}

// sqliteDialect is the SQLite backend. Every method reproduces exactly the SQL
// that the codebase emitted before the Dialect abstraction existed.
type sqliteDialect struct{}

func (sqliteDialect) Name() string           { return "sqlite" }
func (sqliteDialect) Rebind(q string) string { return q }

func (sqliteDialect) BindTime(t time.Time) any {
	return t.UTC().Format("2006-01-02 15:04:05.999")
}

func (sqliteDialect) SinceDaysPredicate(col string, days int) string {
	return fmt.Sprintf("%s >= date('now', '-%d days')", col, days)
}

func (sqliteDialect) DateExpr(col string) string { return fmt.Sprintf("date(%s)", col) }

func (sqliteDialect) AvgAgeDaysExpr(col string) string {
	return fmt.Sprintf("AVG(julianday('now') - julianday(%s))", col)
}

func (sqliteDialect) MinutesAgoExpr(minutes int) string {
	return fmt.Sprintf("datetime('now', '-%d minutes')", minutes)
}

func (sqliteDialect) SecondsAgoExpr(col string) string {
	return fmt.Sprintf("%s < datetime('now', '-' || ? || ' seconds')", col)
}

func (sqliteDialect) BoolLit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// postgresDialect is the PostgreSQL backend (pgx via database/sql).
type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }

// Rebind numbers placeholders left-to-right. Naive (matches ExpandIn): a literal
// `?` inside a string constant is not skipped — the codebase has none.
func (postgresDialect) Rebind(query string) string {
	var sb strings.Builder
	sb.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(n))
			continue
		}
		sb.WriteByte(query[i])
	}
	return sb.String()
}

func (postgresDialect) BindTime(t time.Time) any { return t.UTC() }

func (postgresDialect) SinceDaysPredicate(col string, days int) string {
	return fmt.Sprintf("%s >= now() - interval '%d days'", col, days)
}

// DateExpr renders a TEXT 'YYYY-MM-DD' day key (not a date type) so it scans
// into a Go string on both backends — pgx returns time.Time for a bare ::date.
func (postgresDialect) DateExpr(col string) string {
	return fmt.Sprintf("to_char(%s, 'YYYY-MM-DD')", col)
}

func (postgresDialect) AvgAgeDaysExpr(col string) string {
	return fmt.Sprintf("AVG(EXTRACT(EPOCH FROM (now() - %s)) / 86400.0)", col)
}

func (postgresDialect) MinutesAgoExpr(minutes int) string {
	return fmt.Sprintf("now() - interval '%d minutes'", minutes)
}

func (postgresDialect) SecondsAgoExpr(col string) string {
	return fmt.Sprintf("%s < now() - (? * interval '1 second')", col)
}

func (postgresDialect) BoolLit(b bool) string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}
