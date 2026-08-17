package bot

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Internal artifact identifiers are valid only inside the application-owned
// tool/history protocol. The model may see them so a later tool call can refer
// to an existing file, but they are never part of the user-facing contract.
// Keep the grammar deliberately narrow: numeric reserved forms only, with
// horizontal whitespace, so filenames, input_artifact_ids, artifact_context,
// bare numbers and prose such as "artifact:abc" remain untouched.
const modelArtifactReferenceCore = `artifact(?:[ \t]*[:#][ \t]*[0-9]+|(?:[ \t]+|[_-])id[ \t]*(?:[:#=][ \t]*)?[0-9]+)`

var (
	modelArtifactReferenceXML    = regexp.MustCompile(`(?i)<[ \t]*artifact\b[^>\r\n]*\bid[ \t]*=[ \t]*["']?[0-9]+["']?[^>\r\n]*>`)
	modelArtifactReferenceParen  = regexp.MustCompile(`(?i)\([ \t]*` + modelArtifactReferenceCore + `[ \t]*\)`)
	modelArtifactReferenceSquare = regexp.MustCompile(`(?i)\[[ \t]*` + modelArtifactReferenceCore + `[ \t]*\]`)
	modelArtifactReferenceBare   = regexp.MustCompile(`(?i)\b` + modelArtifactReferenceCore + `\b`)
)

// scrubModelArtifactReferences removes complete internal numeric references
// from model-authored text. It must not be applied to tool results or to the
// trusted artifact markers the application appends to assistant history.
func scrubModelArtifactReferences(text string) string {
	if text == "" {
		return ""
	}
	text = modelArtifactReferenceXML.ReplaceAllString(text, "")
	text = modelArtifactReferenceParen.ReplaceAllString(text, "")
	text = modelArtifactReferenceSquare.ReplaceAllString(text, "")
	return modelArtifactReferenceBare.ReplaceAllString(text, "")
}

// sanitizeModelPresentation additionally withholds an unfinished reserved
// reference at the end of a streaming snapshot. The raw source buffer remains
// unchanged, so tool chaining and persistent final planning still see the
// canonical model output.
func sanitizeModelPresentation(text string) string {
	text = scrubModelArtifactReferences(text)
	if start, ok := trailingArtifactXMLStart(text); ok {
		return text[:start]
	}
	if start, ok := trailingArtifactReferencePrefixStart(text); ok {
		return text[:start]
	}
	return text
}

func trailingArtifactXMLStart(text string) (int, bool) {
	lineStart := strings.LastIndexByte(text, '\n') + 1
	line := text[lineStart:]
	lower := asciiLowerString(line)
	search := len(lower)
	for search > 0 {
		relative := strings.LastIndexByte(lower[:search], '<')
		if relative < 0 {
			return 0, false
		}
		nameStart := relative + 1
		for nameStart < len(line) && (line[nameStart] == ' ' || line[nameStart] == '\t') {
			nameStart++
		}
		if !strings.HasPrefix(lower[nameStart:], "artifact") {
			search = relative
			continue
		}
		after := nameStart + len("artifact")
		if after < len(line) {
			next, _ := utf8.DecodeRuneInString(line[after:])
			if next != ' ' && next != '\t' && next != '>' {
				search = relative
				continue
			}
		}
		if strings.IndexByte(line[relative:], '>') < 0 {
			return lineStart + relative, true
		}
		return 0, false
	}
	return 0, false
}

func trailingArtifactReferencePrefixStart(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	lineStart := strings.LastIndexByte(text, '\n') + 1
	line := text[lineStart:]
	lower := asciiLowerString(line)
	search := len(lower)
	for search > 0 {
		relative := strings.LastIndex(lower[:search], "artifact")
		if relative < 0 {
			return 0, false
		}
		if relative > 0 {
			previous, _ := utf8.DecodeLastRuneInString(line[:relative])
			if isArtifactWordRune(previous) {
				search = relative
				continue
			}
		}
		suffix := lower[relative+len("artifact"):]
		if artifactReferenceSuffixCanContinue(suffix) {
			start := lineStart + relative
			// Remove an unmatched presentation-only wrapper and its horizontal
			// padding as well, so chunking cannot flash "(" or "[" by itself.
			prefix := line[:relative]
			trimmed := strings.TrimRight(prefix, " \t")
			if len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '(' || trimmed[len(trimmed)-1] == '[') {
				start = lineStart + len(trimmed) - 1
			}
			return start, true
		}
		return 0, false
	}
	return 0, false
}

func artifactReferenceSuffixCanContinue(suffix string) bool {
	if suffix == "" || horizontalWhitespaceOnly(suffix) {
		return true
	}
	trimmed := strings.TrimLeft(suffix, " \t")
	if trimmed == "" {
		return true
	}
	if trimmed[0] == ':' || trimmed[0] == '#' {
		rest := strings.TrimLeft(trimmed[1:], " \t")
		return rest == ""
	}
	if trimmed[0] == '_' || trimmed[0] == '-' {
		trimmed = trimmed[1:]
	} else if len(suffix) == len(trimmed) {
		// `artifactid` is not one of the reserved forms.
		return false
	}
	trimmed = strings.TrimLeft(trimmed, " \t")
	if trimmed == "" || trimmed == "i" {
		return true
	}
	if !strings.HasPrefix(trimmed, "id") {
		return false
	}
	rest := strings.TrimLeft(trimmed[len("id"):], " \t")
	if rest == "" {
		return true
	}
	if rest[0] != ':' && rest[0] != '#' && rest[0] != '=' {
		return false
	}
	return horizontalWhitespaceOnly(rest[1:])
}

func horizontalWhitespaceOnly(value string) bool {
	return strings.Trim(value, " \t") == ""
}

func isArtifactWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func asciiLowerString(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		out.WriteByte(b)
	}
	return out.String()
}
