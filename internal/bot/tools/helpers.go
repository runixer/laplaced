// Package tools provides tool execution for the laplaced bot.
// It handles fact management, people management, and search tools.
package tools

import (
	"fmt"
	"regexp"
	"strconv"
)

// MemoryOpParams holds parsed parameters for a memory operation.
type MemoryOpParams struct {
	Action     string
	Content    string
	Category   string
	FactType   string
	Reason     string
	Importance int
	FactID     int64
}

// ParseFactID extracts fact ID from string "Fact:123" format or int.
// Prefers "Fact:123" format, falls back to numeric for backward compatibility.
func ParseFactID(v interface{}) (int64, error) {
	// String with prefix (new format)
	if s, ok := v.(string); ok {
		re := regexp.MustCompile(`^(?:Fact:)?(\d+)$`)
		matches := re.FindStringSubmatch(s)
		if len(matches) == 2 {
			return strconv.ParseInt(matches[1], 10, 64)
		}
		return 0, fmt.Errorf("invalid fact ID format: %s", s)
	}
	// Numeric fallback (LLM error) - handle float64, int, int64
	switch num := v.(type) {
	case float64:
		return int64(num), nil
	case int:
		return int64(num), nil
	case int64:
		return num, nil
	}
	return 0, fmt.Errorf("fact_id must be string or number")
}

// ParsePersonID extracts person ID from string "Person:123" format or int.
// Prefers "Person:123" format, falls back to numeric for backward compatibility.
func ParsePersonID(v interface{}) (int64, error) {
	if s, ok := v.(string); ok {
		re := regexp.MustCompile(`^(?:Person:)?(\d+)$`)
		matches := re.FindStringSubmatch(s)
		if len(matches) == 2 {
			return strconv.ParseInt(matches[1], 10, 64)
		}
		return 0, fmt.Errorf("invalid person ID format: %s", s)
	}
	// Numeric fallback (LLM error) - handle float64, int, int64
	switch num := v.(type) {
	case float64:
		return int64(num), nil
	case int:
		return int64(num), nil
	case int64:
		return num, nil
	}
	return 0, fmt.Errorf("person_id must be string or number")
}

// ParseMemoryOpParams extracts operation parameters from a map.
// Returns error if fact_id is provided but invalid.
func ParseMemoryOpParams(params map[string]interface{}) (MemoryOpParams, error) {
	// Check for required action field
	actionVal, hasAction := params["action"]
	if !hasAction {
		return MemoryOpParams{}, fmt.Errorf("missing action field")
	}
	action, ok := actionVal.(string)
	if !ok {
		return MemoryOpParams{}, fmt.Errorf("action must be a string")
	}

	p := MemoryOpParams{
		Action:   action,
		Content:  "",
		Category: "",
		FactType: "",
		Reason:   "",
	}
	if v, ok := params["content"].(string); ok {
		p.Content = v
	}
	if v, ok := params["category"].(string); ok {
		p.Category = v
	}
	if v, ok := params["type"].(string); ok {
		p.FactType = v
	}
	if v, ok := params["reason"].(string); ok {
		p.Reason = v
	}
	if v, ok := params["importance"].(float64); ok {
		p.Importance = int(v)
	}
	// Handle fact_id (for update/delete operations) - accept both "Fact:123" and numeric
	if v, ok := params["fact_id"]; ok {
		id, err := ParseFactID(v)
		if err != nil {
			// Return error - fact_id is required for update/delete and must be valid
			return p, fmt.Errorf("invalid fact_id: %w", err)
		}
		p.FactID = id
	}
	return p, nil
}
