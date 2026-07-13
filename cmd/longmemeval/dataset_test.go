package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadDataset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dataset.json")
	data := `[{"question_id":"q1","question_type":"single-session-user","question":"Where?","answer":"Paris","question_date":"2024-02-01","haystack_session_ids":["s1"],"haystack_dates":["2024/01/02 (Tue) 10:30"],"haystack_sessions":[[{"role":"user","content":"I moved to Paris."},{"role":"assistant","content":"Noted."}]],"answer_session_ids":["s1"]}]`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	cases, err := loadDataset(path)
	require.NoError(t, err)
	require.Len(t, cases, 1)
	require.Equal(t, "q1", cases[0].QuestionID)
}

func TestLoadDatasetAcceptsNumericAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dataset.json")
	data := `[{"question_id":"q1","question_type":"multi-session","question":"How many?","answer":3,"question_date":"2024-02-01","haystack_session_ids":[],"haystack_dates":[],"haystack_sessions":[],"answer_session_ids":[]}]`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	cases, err := loadDataset(path)
	require.NoError(t, err)
	require.Equal(t, scalarString("3"), cases[0].Answer)
}

func TestSelectSessions(t *testing.T) {
	c := evalCase{
		QuestionID: "q1", Question: "Question",
		SessionIDs:   []string{"later", "evidence"},
		SessionDates: []string{"2024-02-01", "2024-01-01"},
		Sessions: [][]evalMessage{
			{{Role: "user", Content: "distractor"}},
			{{Role: "user", Content: "answer"}},
		},
		AnswerSessionIDs: []string{"evidence"},
	}

	oracle, err := selectSessions(c, "oracle")
	require.NoError(t, err)
	require.Len(t, oracle, 1)
	require.Equal(t, "evidence", oracle[0].ID)

	full, err := selectSessions(c, "full")
	require.NoError(t, err)
	require.Len(t, full, 2)
	require.Equal(t, "evidence", full[0].ID)
	require.True(t, full[0].Date.Before(full[1].Date))
}

func TestParseDatasetTime(t *testing.T) {
	for _, value := range []string{"2024-01-02", "2024-01-02T03:04:05Z", "2024/01/02 (Tue) 03:04"} {
		parsed, err := parseDatasetTime(value)
		require.NoError(t, err)
		require.False(t, parsed.Equal(time.Time{}))
	}
}

func TestValidateCaseRejectsMismatchedSessions(t *testing.T) {
	c := evalCase{QuestionID: "q1", Question: "Question", SessionIDs: []string{"s1"}}
	require.Error(t, validateCase(&c))
}
