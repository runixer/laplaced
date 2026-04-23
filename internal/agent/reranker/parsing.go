// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// parseResponse parses the JSON response from Flash.
// Supports multiple formats for backward compatibility:
// - Bare array: [42, 18, 5]
// - Object: {"topics": [...], "people": [...]}
// - Wrapped array: {"topics": [42, 18, 5]}
// - New format with reasons: {"topics": [{"id": "Topic:42", "reason": "..."}]}
func parseResponse(content string, logger *slog.Logger) (*Result, error) {
	content = extractJSONFromResponse(content)

	// Try bare array format (only topics, no people)
	var bareArray []TopicSelection
	if err := json.Unmarshal([]byte(content), &bareArray); err == nil && len(bareArray) > 0 && bareArray[0].ID != "" {
		return &Result{Topics: bareArray}, nil
	}

	// Try object format
	var resp response
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		var wrappedArray []response
		if arrErr := json.Unmarshal([]byte(content), &wrappedArray); arrErr == nil && len(wrappedArray) > 0 {
			resp = wrappedArray[0]
		} else {
			return nil, err
		}
	}

	var topics []TopicSelection

	if len(resp.TopicIDs) > 0 {
		topics = parseTopicArray(resp.TopicIDs, logger)
	}

	if len(topics) == 0 && len(resp.Topics) > 0 {
		topics = parseTopicArray(resp.Topics, logger)
	}

	if len(topics) == 0 && len(resp.IDs) > 0 {
		for _, id := range resp.IDs {
			topics = append(topics, TopicSelection{ID: fmt.Sprintf("Topic:%d", id)})
		}
	}

	// v0.5.1: Parse people
	var people []PersonSelection

	if len(resp.PeopleIDs) > 0 {
		people = parsePeopleArray(resp.PeopleIDs, logger)
	}

	if len(people) == 0 && len(resp.People) > 0 {
		people = parsePeopleArray(resp.People, logger)
	}

	// v0.6.0: Parse artifacts
	var artifacts []ArtifactSelection

	if len(resp.ArtifactIDs) > 0 {
		artifacts = parseArtifactArray(resp.ArtifactIDs, logger)
	}

	if len(artifacts) == 0 && len(resp.Artifacts) > 0 {
		artifacts = parseArtifactArray(resp.Artifacts, logger)
	}

	return &Result{
		Topics:    topics,
		People:    people,
		Artifacts: artifacts,
	}, nil
}

// parseTopicArray parses topics from JSON with format detection.
// Handles both object format [{"id": "Topic:42", "reason": "..."}] and bare integer array [42, 18].
func parseTopicArray(data json.RawMessage, logger *slog.Logger) []TopicSelection {
	var objFormat []TopicSelection
	if err := json.Unmarshal(data, &objFormat); err == nil && len(objFormat) > 0 && objFormat[0].ID != "" {
		return objFormat
	}

	var intFormat []int64
	if err := json.Unmarshal(data, &intFormat); err == nil && len(intFormat) > 0 {
		// Fallback: LLM used numeric format instead of "Topic:N"
		logger.Warn("reranker returned numeric topic IDs instead of prefixed format (Topic:N)",
			"fallback_count", len(intFormat))
		topics := make([]TopicSelection, len(intFormat))
		for i, id := range intFormat {
			topics[i] = TopicSelection{ID: fmt.Sprintf("Topic:%d", id)}
		}
		return topics
	}

	return nil
}

// parsePeopleArray parses people from JSON with format detection (v0.5.1).
// Handles both object format [{"id": "Person:42", "reason": "..."}] and bare integer array [42, 18].
func parsePeopleArray(data json.RawMessage, logger *slog.Logger) []PersonSelection {
	var objFormat []PersonSelection
	if err := json.Unmarshal(data, &objFormat); err == nil && len(objFormat) > 0 && objFormat[0].ID != "" {
		return objFormat
	}

	var intFormat []int64
	if err := json.Unmarshal(data, &intFormat); err == nil && len(intFormat) > 0 {
		// Fallback: LLM used numeric format instead of "Person:N"
		logger.Warn("reranker returned numeric person IDs instead of prefixed format (Person:N)",
			"fallback_count", len(intFormat))
		people := make([]PersonSelection, len(intFormat))
		for i, id := range intFormat {
			people[i] = PersonSelection{ID: fmt.Sprintf("Person:%d", id)}
		}
		return people
	}

	return nil
}

// parseArtifactArray parses artifacts from JSON with format detection (v0.6.0).
// Handles both object format [{"id": "Artifact:42", "reason": "..."}] and bare integer array [42, 18].
func parseArtifactArray(data json.RawMessage, logger *slog.Logger) []ArtifactSelection {
	var objFormat []ArtifactSelection
	if err := json.Unmarshal(data, &objFormat); err == nil && len(objFormat) > 0 && objFormat[0].ID != "" {
		return objFormat
	}

	var intFormat []int64
	if err := json.Unmarshal(data, &intFormat); err == nil && len(intFormat) > 0 {
		// Fallback: LLM used numeric format instead of "Artifact:N"
		logger.Warn("reranker returned numeric artifact IDs instead of prefixed format (Artifact:N)",
			"fallback_count", len(intFormat))
		artifacts := make([]ArtifactSelection, len(intFormat))
		for i, id := range intFormat {
			artifacts[i] = ArtifactSelection{ID: fmt.Sprintf("Artifact:%d", id)}
		}
		return artifacts
	}

	return nil
}

// extractJSONFromResponse finds JSON object/array in LLM response.
// Handles: markdown blocks, preamble text, escaped strings.
func extractJSONFromResponse(content string) string {
	startArray := stringsIndex(content, "[")
	startObj := stringsIndex(content, "{")

	var startIdx int
	var openBrace, closeBrace byte
	switch {
	case startArray >= 0 && (startObj < 0 || startArray < startObj):
		startIdx = startArray
		openBrace = '['
		closeBrace = ']'
	case startObj >= 0:
		startIdx = startObj
		openBrace = '{'
		closeBrace = '}'
	default:
		return content
	}

	depth := 0
	inString := false
	escaped := false
	for i := startIdx; i < len(content); i++ {
		c := content[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case openBrace:
			depth++
		case closeBrace:
			depth--
			if depth == 0 {
				return content[startIdx : i+1]
			}
		}
	}

	return content
}

// stringsIndex is a wrapper around strings.Index to avoid import conflict.
func stringsIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
