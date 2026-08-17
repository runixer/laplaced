package extractor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/runixer/laplaced/internal/agent"
)

// tolerantRAGHints accepts the small set of shapes the extractor model has
// emitted in practice while keeping the stored representation flat.
type tolerantRAGHints []string

func (h *tolerantRAGHints) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}

	flattened := make([]string, 0)
	var flatten func(any) error
	flatten = func(value any) error {
		switch value := value.(type) {
		case nil:
			return nil
		case string:
			flattened = append(flattened, value)
			return nil
		case []any:
			for _, item := range value {
				if err := flatten(item); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("rag_hints must be a string or an array of strings, got %T", value)
		}
	}

	if err := flatten(value); err != nil {
		return err
	}
	*h = flattened
	return nil
}

type extractionPayload struct {
	Summary  string           `json:"summary"`
	Keywords []string         `json:"keywords"`
	Entities []string         `json:"entities"`
	RAGHints tolerantRAGHints `json:"rag_hints"`
}

// parseExtractionResult performs extractor-local recovery. It deliberately
// does not broaden agent.UnmarshalLenient: decorations are repaired only in
// the top-level rag_hints value, so unrelated agents and valid values such as
// negative numbers cannot be silently rewritten.
func parseExtractionResult(content string, allowRAGHintsDrop bool) (
	result ExtractionResult,
	jsonRepaired bool,
	ragHintsDropped bool,
	err error,
) {
	var payload extractionPayload
	jsonRepaired, err = agent.UnmarshalLenient(content, &payload)
	if err == nil {
		return payload.result(), jsonRepaired, false, nil
	}

	extracted := agent.ExtractJSON(strings.TrimSpace(content))
	repairedContent, decorationRepaired := repairRAGHintDecorations(extracted)
	if decorationRepaired {
		jsonRepaired = true
		payload = extractionPayload{}
		if repairErr := json.Unmarshal([]byte(repairedContent), &payload); repairErr == nil {
			return payload.result(), true, false, nil
		} else {
			err = repairErr
		}
	}

	if !allowRAGHintsDrop {
		return ExtractionResult{}, jsonRepaired, false, err
	}

	// On the terminal attempt, preserve otherwise valid metadata by replacing
	// only the exact top-level rag_hints value. If any other field or the outer
	// JSON is malformed, unmarshalling still fails and the artifact follows the
	// normal failed/retry path.
	withoutHints, replaced := replaceRAGHintsWithEmptyArray(repairedContent)
	if !replaced {
		return ExtractionResult{}, jsonRepaired, false, err
	}
	payload = extractionPayload{}
	if dropErr := json.Unmarshal([]byte(withoutHints), &payload); dropErr != nil {
		return ExtractionResult{}, jsonRepaired, false, dropErr
	}

	return payload.result(), true, true, nil
}

func (p extractionPayload) result() ExtractionResult {
	ragHints := make([]string, len(p.RAGHints))
	copy(ragHints, p.RAGHints)
	return ExtractionResult{
		Summary:  p.Summary,
		Keywords: p.Keywords,
		Entities: p.Entities,
		RAGHints: ragHints,
	}
}

// A decoration must be followed by a quoted string. Requiring that quote is
// what keeps JSON numbers (including negative numbers) untouched.
var ragHintDecoration = regexp.MustCompile(
	`^(?:(?:[-*][ \t]+)|(?:[0-9]+[.)][ \t]+)|(?:[_\p{L}][_\p{L}\p{N}]*[ \t]*:[ \t]*))"`,
)

func repairRAGHintDecorations(content string) (string, bool) {
	valueStart, ok := findTopLevelFieldValue(content, "rag_hints")
	if !ok || valueStart >= len(content) || content[valueStart] != '[' {
		return content, false
	}
	valueEnd, ok := findJSONValueEnd(content, valueStart)
	if !ok {
		return content, false
	}

	array := content[valueStart:valueEnd]
	var repaired strings.Builder
	repaired.Grow(len(array))
	changed := false
	inString := false
	escaped := false
	containers := make([]byte, 0, 2)

	for i := 0; i < len(array); {
		c := array[i]
		if inString {
			repaired.WriteByte(c)
			i++
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		if c == '"' {
			inString = true
			repaired.WriteByte(c)
			i++
			continue
		}

		repaired.WriteByte(c)
		i++
		valueExpected := false
		switch c {
		case '[', '{':
			containers = append(containers, c)
			valueExpected = c == '['
		case ']', '}':
			if len(containers) > 0 && matchingJSONDelimiters(containers[len(containers)-1], c) {
				containers = containers[:len(containers)-1]
			}
		case ',':
			valueExpected = len(containers) > 0 && containers[len(containers)-1] == '['
		}
		if !valueExpected {
			continue
		}

		for i < len(array) && isJSONWhitespace(array[i]) {
			repaired.WriteByte(array[i])
			i++
		}
		match := ragHintDecoration.FindStringIndex(array[i:])
		if len(match) != 2 || match[0] != 0 {
			continue
		}

		// Keep the opening quote (the last byte in the match); discard only
		// the bullet, number, or pseudo-key before it.
		i += match[1] - 1
		changed = true
	}

	if !changed {
		return content, false
	}
	return content[:valueStart] + repaired.String() + content[valueEnd:], true
}

func replaceRAGHintsWithEmptyArray(content string) (string, bool) {
	valueStart, ok := findTopLevelFieldValue(content, "rag_hints")
	if !ok {
		return content, false
	}
	valueEnd, ok := findJSONValueEnd(content, valueStart)
	if !ok {
		return content, false
	}
	return content[:valueStart] + "[]" + content[valueEnd:], true
}

// findTopLevelFieldValue returns the start of a value for an exact key in the
// outer JSON object. String contents and nested objects are never considered.
func findTopLevelFieldValue(content, field string) (int, bool) {
	i := 0
	for i < len(content) && isJSONWhitespace(content[i]) {
		i++
	}
	if i >= len(content) || content[i] != '{' {
		return 0, false
	}

	objectDepth := 1
	arrayDepth := 0
	expectingTopLevelKey := true
	for i++; i < len(content); {
		switch content[i] {
		case '"':
			end, ok := scanJSONString(content, i)
			if !ok {
				return 0, false
			}
			if objectDepth == 1 && arrayDepth == 0 && expectingTopLevelKey {
				expectingTopLevelKey = false
				var key string
				if json.Unmarshal([]byte(content[i:end]), &key) == nil && key == field {
					j := end
					for j < len(content) && isJSONWhitespace(content[j]) {
						j++
					}
					if j < len(content) && content[j] == ':' {
						j++
						for j < len(content) && isJSONWhitespace(content[j]) {
							j++
						}
						if j < len(content) {
							return j, true
						}
					}
				}
			}
			i = end
		case '{':
			objectDepth++
			i++
		case '}':
			objectDepth--
			if objectDepth == 0 {
				return 0, false
			}
			i++
		case '[':
			arrayDepth++
			i++
		case ']':
			arrayDepth--
			if arrayDepth < 0 {
				return 0, false
			}
			i++
		case ',':
			if objectDepth == 1 && arrayDepth == 0 {
				expectingTopLevelKey = true
			}
			i++
		default:
			i++
		}
	}
	return 0, false
}

// findJSONValueEnd returns the exclusive end of a balanced object, array,
// string, or primitive. It intentionally refuses unbalanced values so a
// truncated model response cannot be misclassified as safe degradation.
func findJSONValueEnd(content string, start int) (int, bool) {
	if start < 0 || start >= len(content) {
		return 0, false
	}

	switch content[start] {
	case '"':
		return scanJSONString(content, start)
	case '[', '{':
		stack := []byte{content[start]}
		inString := false
		escaped := false
		for i := start + 1; i < len(content); i++ {
			c := content[i]
			if inString {
				switch {
				case escaped:
					escaped = false
				case c == '\\':
					escaped = true
				case c == '"':
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '[', '{':
				stack = append(stack, c)
			case ']', '}':
				if len(stack) == 0 || !matchingJSONDelimiters(stack[len(stack)-1], c) {
					return 0, false
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	default:
		i := start
		for i < len(content) && content[i] != ',' && content[i] != '}' {
			i++
		}
		end := i
		for end > start && isJSONWhitespace(content[end-1]) {
			end--
		}
		return end, end > start
	}
}

func scanJSONString(content string, start int) (int, bool) {
	escaped := false
	for i := start + 1; i < len(content); i++ {
		if escaped {
			escaped = false
			continue
		}
		if content[i] == '\\' {
			escaped = true
			continue
		}
		if content[i] == '"' {
			return i + 1, true
		}
	}
	return 0, false
}

func matchingJSONDelimiters(open, close byte) bool {
	return open == '[' && close == ']' || open == '{' && close == '}'
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
