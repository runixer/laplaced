package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractJSON returns the first balanced JSON object or array found in an LLM
// response, stripping markdown fences, preamble text and trailing garbage.
// Brace tracking is string-aware (quotes and escapes inside JSON strings are
// handled). When no balanced JSON value is found, the content is returned
// unchanged so the caller's unmarshal error stays informative.
func ExtractJSON(content string) string {
	startArray := strings.Index(content, "[")
	startObj := strings.Index(content, "{")

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

// UnmarshalLenient extracts the first balanced JSON value from content and
// unmarshals it into v. The repaired result reports whether extraction had to
// change the content (fences, preamble or trailing garbage were stripped) —
// callers surface it as a span attribute so repair frequency is observable.
func UnmarshalLenient(content string, v any) (repaired bool, err error) {
	trimmed := strings.TrimSpace(content)
	extracted := ExtractJSON(trimmed)
	repaired = extracted != trimmed
	if err := json.Unmarshal([]byte(extracted), v); err != nil {
		return repaired, err
	}
	return repaired, nil
}

// FlexID is a string-valued ID that tolerates the LLM emitting it as a JSON
// number ("fact_id": 5215) instead of a string ("fact_id": "5215" or
// "fact_id": "Fact:5215"). It unmarshals from string, number or null and
// marshals back as a plain string.
type FlexID string

// UnmarshalJSON accepts a JSON string, number or null.
func (f *FlexID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = FlexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("id must be a string or number, got %s", trimmed)
	}
	*f = FlexID(n.String())
	return nil
}
