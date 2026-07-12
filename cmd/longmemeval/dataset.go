package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type evalCase struct {
	QuestionID       string          `json:"question_id"`
	QuestionType     string          `json:"question_type"`
	Question         string          `json:"question"`
	Answer           scalarString    `json:"answer"`
	QuestionDate     string          `json:"question_date"`
	SessionIDs       []string        `json:"haystack_session_ids"`
	SessionDates     []string        `json:"haystack_dates"`
	Sessions         [][]evalMessage `json:"haystack_sessions"`
	AnswerSessionIDs []string        `json:"answer_session_ids"`
}

type scalarString string

func (s *scalarString) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch typed := value.(type) {
	case nil:
		*s = ""
	case string:
		*s = scalarString(typed)
	case float64:
		*s = scalarString(fmt.Sprintf("%v", typed))
	case bool:
		*s = scalarString(fmt.Sprintf("%t", typed))
	default:
		return fmt.Errorf("expected scalar value")
	}
	return nil
}

type evalMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer,omitempty"`
}

type datedSession struct {
	ID       string
	Date     time.Time
	Messages []evalMessage
}

func loadDataset(path string) ([]evalCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}
	var cases []evalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("decode dataset: %w", err)
	}
	for i := range cases {
		if err := validateCase(&cases[i]); err != nil {
			return nil, fmt.Errorf("case %d: %w", i, err)
		}
	}
	return cases, nil
}

func validateCase(c *evalCase) error {
	if strings.TrimSpace(c.QuestionID) == "" {
		return fmt.Errorf("question_id is required")
	}
	if strings.TrimSpace(c.Question) == "" {
		return fmt.Errorf("question is required for %s", c.QuestionID)
	}
	if len(c.Sessions) != len(c.SessionIDs) || len(c.Sessions) != len(c.SessionDates) {
		return fmt.Errorf("session arrays have different lengths for %s", c.QuestionID)
	}
	for i, session := range c.Sessions {
		if _, err := parseDatasetTime(c.SessionDates[i]); err != nil {
			return fmt.Errorf("session %s date: %w", c.SessionIDs[i], err)
		}
		for j, message := range session {
			if message.Role != "user" && message.Role != "assistant" {
				return fmt.Errorf("session %s message %d has unsupported role %q", c.SessionIDs[i], j, message.Role)
			}
			if strings.TrimSpace(message.Content) == "" {
				return fmt.Errorf("session %s message %d has empty content", c.SessionIDs[i], j)
			}
		}
	}
	return nil
}

func selectSessions(c evalCase, mode string) ([]datedSession, error) {
	answerIDs := make(map[string]struct{}, len(c.AnswerSessionIDs))
	for _, id := range c.AnswerSessionIDs {
		answerIDs[id] = struct{}{}
	}

	sessions := make([]datedSession, 0, len(c.Sessions))
	for i, messages := range c.Sessions {
		if mode == "oracle" {
			if _, ok := answerIDs[c.SessionIDs[i]]; !ok {
				continue
			}
		}
		date, err := parseDatasetTime(c.SessionDates[i])
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, datedSession{ID: c.SessionIDs[i], Date: date, Messages: messages})
	}
	if mode == "oracle" && len(sessions) == 0 {
		return nil, fmt.Errorf("case %s has no answer sessions", c.QuestionID)
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Date.Before(sessions[j].Date) })
	return sessions, nil
}

func parseDatasetTime(value string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006/01/02 (Mon) 15:04",
		"2006/01/02 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}
