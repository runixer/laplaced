package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandIn_HappyPaths(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		args      []any
		wantQuery string
		wantArgs  []any
	}{
		{
			name:      "no slice, passthrough single scalar",
			query:     "SELECT * FROM t WHERE id = ?",
			args:      []any{int64(5)},
			wantQuery: "SELECT * FROM t WHERE id = ?",
			wantArgs:  []any{int64(5)},
		},
		{
			name:      "no slice, passthrough multiple scalars",
			query:     "SELECT * FROM t WHERE a = ? AND b = ?",
			args:      []any{int64(1), "x"},
			wantQuery: "SELECT * FROM t WHERE a = ? AND b = ?",
			wantArgs:  []any{int64(1), "x"},
		},
		{
			name:      "no args, no slice",
			query:     "SELECT 1",
			wantQuery: "SELECT 1",
			wantArgs:  []any{},
		},
		{
			name:      "scalar then slice",
			query:     "SELECT * FROM t WHERE user_id = ? AND id IN (?)",
			args:      []any{int64(7), []int64{1, 2, 3}},
			wantQuery: "SELECT * FROM t WHERE user_id = ? AND id IN (?,?,?)",
			wantArgs:  []any{int64(7), int64(1), int64(2), int64(3)},
		},
		{
			name:      "slice then scalar",
			query:     "SELECT * FROM t WHERE id IN (?) AND user_id = ?",
			args:      []any{[]int64{1, 2}, int64(9)},
			wantQuery: "SELECT * FROM t WHERE id IN (?,?) AND user_id = ?",
			wantArgs:  []any{int64(1), int64(2), int64(9)},
		},
		{
			name:      "slice sandwiched between scalars",
			query:     "UPDATE t SET x = ? WHERE id IN (?) AND user_id = ?",
			args:      []any{int64(100), []int64{7, 8}, int64(1)},
			wantQuery: "UPDATE t SET x = ? WHERE id IN (?,?) AND user_id = ?",
			wantArgs:  []any{int64(100), int64(7), int64(8), int64(1)},
		},
		{
			name:      "single-element slice still expands to one '?'",
			query:     "SELECT * FROM t WHERE id IN (?)",
			args:      []any{[]int64{42}},
			wantQuery: "SELECT * FROM t WHERE id IN (?)",
			wantArgs:  []any{int64(42)},
		},
		{
			name:      "string slice",
			query:     "SELECT * FROM t WHERE name IN (?)",
			args:      []any{[]string{"alice", "bob", "carol"}},
			wantQuery: "SELECT * FROM t WHERE name IN (?,?,?)",
			wantArgs:  []any{"alice", "bob", "carol"},
		},
		{
			name:      "any slice with mixed element types",
			query:     "SELECT * FROM t WHERE v IN (?)",
			args:      []any{[]any{int64(1), "two", 3.0}},
			wantQuery: "SELECT * FROM t WHERE v IN (?,?,?)",
			wantArgs:  []any{int64(1), "two", 3.0},
		},
		{
			name:      "nil scalar arg is preserved",
			query:     "SELECT * FROM t WHERE a = ? AND id IN (?)",
			args:      []any{nil, []int64{1}},
			wantQuery: "SELECT * FROM t WHERE a = ? AND id IN (?)",
			wantArgs:  []any{nil, int64(1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQuery, gotArgs, err := ExpandIn(tt.query, tt.args...)
			require.NoError(t, err)
			assert.Equal(t, tt.wantQuery, gotQuery)
			assert.Equal(t, tt.wantArgs, gotArgs)
		})
	}
}

func TestExpandIn_Errors(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		args    []any
		errText string
	}{
		{
			name:    "empty slice",
			query:   "SELECT * FROM t WHERE id IN (?)",
			args:    []any{[]int64{}},
			errText: "empty",
		},
		{
			name:    "typed nil slice",
			query:   "SELECT * FROM t WHERE id IN (?)",
			args:    []any{[]int64(nil)},
			errText: "empty",
		},
		{
			name:    "two slice args",
			query:   "SELECT * FROM t WHERE a IN (?) AND b IN (?)",
			args:    []any{[]int64{1}, []int64{2}},
			errText: "multiple slice",
		},
		{
			name:    "slice position has no placeholder",
			query:   "SELECT * FROM t",
			args:    []any{[]int64{1}},
			errText: "not reached",
		},
		{
			name:    "slice at index 1 but query only has one '?'",
			query:   "SELECT * FROM t WHERE a = ?",
			args:    []any{int64(10), []int64{1, 2}},
			errText: "not reached",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ExpandIn(tt.query, tt.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errText)
		})
	}
}

// TestExpandIn_ArgsNotMutated verifies the caller's args slice is not aliased —
// mutating the returned flat args must not affect the caller's input.
func TestExpandIn_ArgsNotMutated(t *testing.T) {
	original := []any{int64(1), int64(2)}
	_, out, err := ExpandIn("SELECT * FROM t WHERE a = ? AND b = ?", original...)
	require.NoError(t, err)
	out[0] = int64(999)
	assert.Equal(t, int64(1), original[0], "original args must not be mutated")
}

// TestExpandIn_EndToEnd gives confidence that the expanded query is actual
// valid SQL that SQLite accepts — catching e.g. off-by-one placeholder errors
// that unit tests on the strings alone can't see.
func TestExpandIn_EndToEnd(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := store.db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, user_id INTEGER)`)
	require.NoError(t, err)
	for i := 1; i <= 5; i++ {
		_, err = store.db.Exec(`INSERT INTO t(id, user_id) VALUES (?, ?)`, i, 42)
		require.NoError(t, err)
	}

	q, args, err := ExpandIn(
		`SELECT id FROM t WHERE user_id = ? AND id IN (?) ORDER BY id`,
		int64(42), []int64{2, 4, 5},
	)
	require.NoError(t, err)

	rows, err := store.db.Query(q, args...)
	require.NoError(t, err)
	defer rows.Close()

	var got []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		got = append(got, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []int64{2, 4, 5}, got)
}
