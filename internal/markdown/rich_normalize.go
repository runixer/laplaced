package markdown

import (
	"strconv"
	"strings"
)

// normalizeRichListBoundaries repairs one narrow CommonMark ambiguity in
// model-produced Markdown. An ordered list whose first marker is not 1 cannot
// interrupt a paragraph, so a label immediately followed by "3. ..." remains
// part of that paragraph unless there is a blank line between them.
//
// The repair is intentionally conservative and rich-only: a root-level label
// ending in a colon must be followed by at least two sequential root-level
// markers, and the first marker must be in the ordinary list range 2..99. The
// original source kept by the delivery path is not modified, so legacy fallback
// behavior remains unchanged.
func normalizeRichListBoundaries(input string) string {
	newline := richMarkdownNewline(input)
	lines := strings.Split(input, newline)
	if len(lines) < 3 {
		return input
	}

	out := make([]string, 0, len(lines)+1)
	inFence := false
	var fenceChar byte
	var fenceLength int
	changed := false

	for i, line := range lines {
		if inFence {
			out = append(out, line)
			if isClosingRichFence(line, fenceChar, fenceLength) {
				inFence = false
			}
			continue
		}

		if char, length, ok := openingRichFence(line); ok {
			out = append(out, line)
			inFence = true
			fenceChar = char
			fenceLength = length
			continue
		}

		out = append(out, line)
		if i+2 >= len(lines) || !isRootRichListPrelude(line) {
			continue
		}

		first, firstOK := rootRichOrderedMarker(lines[i+1])
		second, secondOK := rootRichOrderedMarker(lines[i+2])
		if firstOK && secondOK && first >= 2 && first <= 99 && second == first+1 {
			out = append(out, "")
			changed = true
		}
	}

	if !changed {
		return input
	}
	return strings.Join(out, newline)
}

func richMarkdownNewline(input string) string {
	withoutCRLF := strings.ReplaceAll(input, "\r\n", "")
	if strings.Contains(input, "\r\n") && !strings.Contains(withoutCRLF, "\n") {
		return "\r\n"
	}
	return "\n"
}

func isRootRichListPrelude(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '>' {
		return false
	}
	if _, ok := rootRichOrderedMarker(line); ok {
		return false
	}
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "* ") {
		return false
	}

	trimmed := strings.TrimSpace(line)
	for _, closer := range []string{"**", "__", "~~", "||"} {
		if strings.HasSuffix(trimmed, closer) {
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, closer))
			break
		}
	}
	return strings.HasSuffix(trimmed, ":")
}

func rootRichOrderedMarker(line string) (int, bool) {
	if line == "" || line[0] < '0' || line[0] > '9' {
		return 0, false
	}

	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits+1 >= len(line) || line[digits] != '.' {
		return 0, false
	}
	if line[digits+1] != ' ' && line[digits+1] != '\t' {
		return 0, false
	}
	if strings.TrimSpace(line[digits+2:]) == "" {
		return 0, false
	}

	number, err := strconv.Atoi(line[:digits])
	if err != nil {
		return 0, false
	}
	return number, true
}

func openingRichFence(line string) (byte, int, bool) {
	trimmed := strings.TrimSuffix(line, "\r")
	indent := 0
	for indent < len(trimmed) && trimmed[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(trimmed) {
		return 0, 0, false
	}

	char := trimmed[indent]
	if char != '`' && char != '~' {
		return 0, 0, false
	}
	length := markerRunLength(trimmed[indent:], char)
	if length < 3 {
		return 0, 0, false
	}
	return char, length, true
}

func isClosingRichFence(line string, fenceChar byte, fenceLength int) bool {
	trimmed := strings.TrimSuffix(line, "\r")
	indent := 0
	for indent < len(trimmed) && trimmed[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(trimmed) || trimmed[indent] != fenceChar {
		return false
	}

	length := markerRunLength(trimmed[indent:], fenceChar)
	return length >= fenceLength && strings.TrimSpace(trimmed[indent+length:]) == ""
}

func markerRunLength(input string, marker byte) int {
	length := 0
	for length < len(input) && input[length] == marker {
		length++
	}
	return length
}
