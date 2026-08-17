package bot

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/runixer/laplaced/internal/markdown"
)

const generatedMediaDirectiveStem = "###MEDIA"

type generatedMediaLayoutMode string

const (
	generatedMediaLayoutAuto     generatedMediaLayoutMode = "auto"
	generatedMediaLayoutDirected generatedMediaLayoutMode = "directed"
	generatedMediaLayoutInvalid  generatedMediaLayoutMode = "invalid"
)

type generatedMediaLayoutReason string

const (
	generatedMediaLayoutReasonNone              generatedMediaLayoutReason = ""
	generatedMediaLayoutReasonSyntax            generatedMediaLayoutReason = "syntax"
	generatedMediaLayoutReasonDuplicate         generatedMediaLayoutReason = "duplicate"
	generatedMediaLayoutReasonUnavailable       generatedMediaLayoutReason = "unavailable"
	generatedMediaLayoutReasonUnreferenced      generatedMediaLayoutReason = "unreferenced"
	generatedMediaLayoutReasonTooMany           generatedMediaLayoutReason = "too_many"
	generatedMediaLayoutReasonAvailableOrdinals generatedMediaLayoutReason = "available_ordinals"
)

type generatedMediaProtocolLineKind string

const (
	generatedMediaProtocolSplit generatedMediaProtocolLineKind = "split"
	generatedMediaProtocolMedia generatedMediaProtocolLineKind = "media"
)

// generatedMediaProtocolLine identifies one application-owned physical line.
// SourceRange includes its indentation, trailing whitespace and LF/CRLF when
// present, so removing it preserves every non-protocol source byte exactly.
type generatedMediaProtocolLine struct {
	Kind        generatedMediaProtocolLineKind
	SourceRange markdown.RichSourceRange
}

// generatedMediaPlacement contains only trusted indexes into the loaded media
// slice. Model-authored ordinals are resolved before this value is returned;
// they never become Telegram ids, URLs or attachment names.
type generatedMediaPlacement struct {
	SourceRange markdown.RichSourceRange
	ItemIndexes []int
}

// generatedMediaLayout is an all-or-nothing interpretation of turn-local
// MEDIA directives. ProtocolLines always records eligible SPLIT lines and
// reserved MEDIA candidates in source order. Placements is populated only for
// a fully valid directed layout. MarkerFreeSource removes both protocol kinds
// and is suitable for assistant history.
type generatedMediaLayout struct {
	Mode             generatedMediaLayoutMode
	Reason           generatedMediaLayoutReason
	ProtocolLines    []generatedMediaProtocolLine
	Placements       []generatedMediaPlacement
	MarkerFreeSource string
}

type standaloneRichProtocolLine struct {
	kind        generatedMediaProtocolLineKind
	sourceRange markdown.RichSourceRange
	payload     string
}

// parseGeneratedMediaLayout resolves model-visible, one-based artifact slots
// against the actually available artifacts. availableOrdinals follows the
// loaded item slice: availableOrdinals[i] is the original slot for item i, so
// gaps caused by an unavailable artifact never shift later MEDIA references.
func parseGeneratedMediaLayout(source string, availableOrdinals []int) (generatedMediaLayout, error) {
	if err := validateRichSourceBounds(source); err != nil {
		return generatedMediaLayout{}, err
	}
	document, err := markdown.ParseRichFragments(source)
	if err != nil {
		return generatedMediaLayout{}, err
	}
	protocol, err := scanStandaloneRichProtocolLines(document)
	if err != nil {
		return generatedMediaLayout{}, err
	}

	layout := generatedMediaLayout{Mode: generatedMediaLayoutAuto}
	layout.ProtocolLines = make([]generatedMediaProtocolLine, 0, len(protocol))
	ranges := make([]markdown.RichSourceRange, 0, len(protocol))
	mediaLines := make([]standaloneRichProtocolLine, 0, len(protocol))
	for _, line := range protocol {
		layout.ProtocolLines = append(layout.ProtocolLines, generatedMediaProtocolLine{
			Kind:        line.kind,
			SourceRange: line.sourceRange,
		})
		ranges = append(ranges, line.sourceRange)
		if line.kind == generatedMediaProtocolMedia {
			mediaLines = append(mediaLines, line)
		}
	}
	layout.MarkerFreeSource, err = stripRichSourceRanges(source, ranges)
	if err != nil {
		return generatedMediaLayout{}, fmt.Errorf("strip generated-media protocol lines: %w", err)
	}
	if len(mediaLines) == 0 {
		return layout, nil
	}

	ordinalToItem := make(map[int]int, len(availableOrdinals))
	availableValid := len(availableOrdinals) > 0 && len(availableOrdinals) <= generatedRichGalleryMax
	for itemIndex, ordinal := range availableOrdinals {
		if ordinal <= 0 {
			availableValid = false
			continue
		}
		if _, duplicate := ordinalToItem[ordinal]; duplicate {
			availableValid = false
			continue
		}
		ordinalToItem[ordinal] = itemIndex
	}
	if !availableValid {
		return invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonAvailableOrdinals), nil
	}
	if len(mediaLines) > generatedRichGalleryMax {
		return invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonTooMany), nil
	}

	placements := make([]generatedMediaPlacement, 0, len(mediaLines))
	seen := make(map[int]struct{}, len(availableOrdinals))
	referenceCount := 0
	for _, line := range mediaLines {
		ordinals, reason := parseGeneratedMediaDirective(line.payload)
		if reason != generatedMediaLayoutReasonNone {
			return invalidGeneratedMediaLayout(layout, reason), nil
		}
		if referenceCount > generatedRichGalleryMax-len(ordinals) {
			return invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonTooMany), nil
		}
		referenceCount += len(ordinals)

		itemIndexes := make([]int, 0, len(ordinals))
		for _, ordinal := range ordinals {
			if _, duplicate := seen[ordinal]; duplicate {
				return invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonDuplicate), nil
			}
			itemIndex, available := ordinalToItem[ordinal]
			if !available {
				return invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonUnavailable), nil
			}
			seen[ordinal] = struct{}{}
			itemIndexes = append(itemIndexes, itemIndex)
		}
		placements = append(placements, generatedMediaPlacement{
			SourceRange: line.sourceRange,
			ItemIndexes: itemIndexes,
		})
	}
	if len(seen) != len(ordinalToItem) {
		return invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonUnreferenced), nil
	}

	layout.Mode = generatedMediaLayoutDirected
	layout.Placements = placements
	return layout, nil
}

func invalidGeneratedMediaLayout(layout generatedMediaLayout, reason generatedMediaLayoutReason) generatedMediaLayout {
	layout.Mode = generatedMediaLayoutInvalid
	layout.Reason = reason
	layout.Placements = nil
	return layout
}

func parseGeneratedMediaDirective(payload string) ([]int, generatedMediaLayoutReason) {
	const prefix = generatedMediaDirectiveStem + ":"
	const suffix = "###"
	if !strings.HasPrefix(payload, prefix) || !strings.HasSuffix(payload, suffix) {
		return nil, generatedMediaLayoutReasonSyntax
	}
	body := payload[len(prefix) : len(payload)-len(suffix)]
	if body == "" {
		return nil, generatedMediaLayoutReasonSyntax
	}

	values := make([]int, 0, min(generatedRichGalleryMax, strings.Count(body, ",")+1))
	for cursor := 0; cursor < len(body); {
		if len(values) >= generatedRichGalleryMax {
			return nil, generatedMediaLayoutReasonTooMany
		}
		if body[cursor] < '1' || body[cursor] > '9' {
			return nil, generatedMediaLayoutReasonSyntax
		}
		value := 0
		for cursor < len(body) && body[cursor] >= '0' && body[cursor] <= '9' {
			digit := int(body[cursor] - '0')
			if value > (math.MaxInt-digit)/10 {
				return nil, generatedMediaLayoutReasonSyntax
			}
			value = value*10 + digit
			cursor++
		}
		values = append(values, value)
		if cursor == len(body) {
			break
		}
		if body[cursor] != ',' {
			return nil, generatedMediaLayoutReasonSyntax
		}
		cursor++
		if cursor == len(body) {
			return nil, generatedMediaLayoutReasonSyntax
		}
	}
	return values, generatedMediaLayoutReasonNone
}

// scanStandaloneRichProtocolLines is the shared positive allowlist for SPLIT
// and MEDIA. A token must occupy a complete trimmed physical line and every
// non-whitespace payload byte must belong to direct Text children of one
// top-level paragraph. Goldmark may split plain text at internal whitespace;
// syntax owned by an inline container remains outside the positive allowlist.
func scanStandaloneRichProtocolLines(document markdown.RichDocument) ([]standaloneRichProtocolLine, error) {
	var lines []standaloneRichProtocolLine
	for fragmentIndex, fragment := range document.Fragments {
		if fragment.SourceStart < 0 || fragment.SourceEnd < fragment.SourceStart ||
			fragment.SourceEnd > len(document.LegacySource) {
			return nil, fmt.Errorf("rich fragment %d has invalid source range [%d,%d)",
				fragmentIndex, fragment.SourceStart, fragment.SourceEnd)
		}
		if fragment.LegacySource != document.LegacySource[fragment.SourceStart:fragment.SourceEnd] {
			return nil, fmt.Errorf("rich fragment %d legacy source does not match document", fragmentIndex)
		}
		if fragment.Kind != markdown.RichFragmentParagraph {
			continue
		}

		local := fragment.LegacySource
		for lineStart := 0; lineStart < len(local); {
			lineEnd := strings.IndexByte(local[lineStart:], '\n')
			fullLineEnd := len(local)
			if lineEnd >= 0 {
				lineEnd += lineStart
				fullLineEnd = lineEnd + 1
			} else {
				lineEnd = len(local)
			}
			rawLine := local[lineStart:lineEnd]
			trimmedLeft := strings.TrimLeftFunc(rawLine, unicode.IsSpace)
			payload := strings.TrimRightFunc(trimmedLeft, unicode.IsSpace)
			payloadOffset := len(rawLine) - len(trimmedLeft)

			kind, candidate := classifyStandaloneRichProtocolPayload(payload)
			if candidate {
				payloadRange := markdown.RichSourceRange{
					Start: fragment.SourceStart + lineStart + payloadOffset,
					End:   fragment.SourceStart + lineStart + payloadOffset + len(payload),
				}
				if richRangeCoveredByBoundaryText(payloadRange, fragment.BoundaryTextRanges, document.LegacySource) {
					lineRange := markdown.RichSourceRange{
						Start: fragment.SourceStart + lineStart,
						End:   fragment.SourceStart + fullLineEnd,
					}
					if len(lines) > 0 && lineRange.Start < lines[len(lines)-1].sourceRange.End {
						return nil, fmt.Errorf("rich protocol line [%d,%d) overlaps previous line",
							lineRange.Start, lineRange.End)
					}
					lines = append(lines, standaloneRichProtocolLine{
						kind:        kind,
						sourceRange: lineRange,
						payload:     payload,
					})
				}
			}

			lineStart = fullLineEnd
		}
	}
	return lines, nil
}

func classifyStandaloneRichProtocolPayload(payload string) (generatedMediaProtocolLineKind, bool) {
	if payload == richSplitDelimiter {
		return generatedMediaProtocolSplit, true
	}
	if reservedGeneratedMediaPayload(payload) {
		return generatedMediaProtocolMedia, true
	}
	return "", false
}

func reservedGeneratedMediaPayload(payload string) bool {
	if payload == generatedMediaDirectiveStem {
		return true
	}
	if !strings.HasPrefix(payload, generatedMediaDirectiveStem) || len(payload) == len(generatedMediaDirectiveStem) {
		return false
	}
	next := payload[len(generatedMediaDirectiveStem)]
	return next == ':' || next == '#'
}

func richRangeCoveredByBoundaryText(
	candidate markdown.RichSourceRange,
	eligible []markdown.RichSourceRange,
	source string,
) bool {
	if candidate.Start < 0 || candidate.End < candidate.Start || candidate.End > len(source) {
		return false
	}
	cursor := candidate.Start
	for _, sourceRange := range eligible {
		if sourceRange.End <= cursor || sourceRange.Start >= candidate.End {
			continue
		}
		if sourceRange.Start > cursor {
			gapEnd := min(sourceRange.Start, candidate.End)
			if strings.TrimSpace(source[cursor:gapEnd]) != "" {
				return false
			}
			cursor = gapEnd
		}
		if sourceRange.Start <= cursor && sourceRange.End > cursor {
			cursor = min(sourceRange.End, candidate.End)
			if cursor == candidate.End {
				return true
			}
		}
	}
	return cursor == candidate.End || strings.TrimSpace(source[cursor:candidate.End]) == ""
}

func stripRichSourceRanges(source string, ranges []markdown.RichSourceRange) (string, error) {
	if len(ranges) == 0 {
		return source, nil
	}
	var out strings.Builder
	out.Grow(len(source))
	previousEnd := 0
	for i, sourceRange := range ranges {
		if sourceRange.Start < previousEnd || sourceRange.End < sourceRange.Start || sourceRange.End > len(source) {
			return "", fmt.Errorf("source range %d [%d,%d) is invalid after %d",
				i, sourceRange.Start, sourceRange.End, previousEnd)
		}
		out.WriteString(source[previousEnd:sourceRange.Start])
		previousEnd = sourceRange.End
	}
	out.WriteString(source[previousEnd:])
	return out.String(), nil
}

// stripPersistentModelProtocolSource removes application-owned MEDIA/SPLIT
// lines before model-authored text is written to any persistent preview or
// intermediate message. The AST-aware path preserves identical-looking text
// inside code, lists, quotes, links, and other protected containers. If rich
// parsing itself fails, the conservative physical-line fallback prefers
// removing a reserved-looking line over leaking the internal protocol.
func stripPersistentModelProtocolSource(source string) (string, error) {
	layout, err := parseGeneratedMediaLayout(source, nil)
	if err == nil {
		return layout.MarkerFreeSource, nil
	}
	return stripReservedProtocolPhysicalLines(source), err
}

func stripReservedProtocolPhysicalLines(source string) string {
	var out strings.Builder
	out.Grow(len(source))
	for lineStart := 0; lineStart < len(source); {
		lineEnd := len(source)
		fullLineEnd := len(source)
		if relativeEnd := strings.IndexByte(source[lineStart:], '\n'); relativeEnd >= 0 {
			lineEnd = lineStart + relativeEnd
			fullLineEnd = lineEnd + 1
		}
		payload := strings.TrimSpace(source[lineStart:lineEnd])
		if payload != richSplitDelimiter && !reservedGeneratedMediaPayload(payload) {
			out.WriteString(source[lineStart:fullLineEnd])
		}
		lineStart = fullLineEnd
	}
	return out.String()
}

// suppressGeneratedMediaDraftSource removes complete MEDIA/SPLIT protocol
// lines and withholds an ambiguous terminal prefix. The persistent final still
// resolves the untouched source against the available artifact ordinals.
func suppressGeneratedMediaDraftSource(source string) (string, error) {
	if source == "" {
		return "", nil
	}
	document, err := markdown.ParseRichFragments(source)
	if err != nil {
		return "", err
	}
	protocol, err := scanStandaloneRichProtocolLines(document)
	if err != nil {
		return "", err
	}
	ranges := make([]markdown.RichSourceRange, 0, len(protocol)+1)
	for _, line := range protocol {
		ranges = append(ranges, line.sourceRange)
	}
	if pending, ok := trailingGeneratedMediaDraftRange(document, source); ok {
		if len(ranges) == 0 || ranges[len(ranges)-1].End <= pending.Start {
			ranges = append(ranges, pending)
		}
	}
	return stripRichSourceRanges(source, ranges)
}

func trailingGeneratedMediaDraftRange(document markdown.RichDocument, source string) (markdown.RichSourceRange, bool) {
	if strings.HasSuffix(source, "\n") {
		return markdown.RichSourceRange{}, false
	}
	lineStart := strings.LastIndexByte(source, '\n') + 1
	rawLine := source[lineStart:]
	trimmedLeft := strings.TrimLeftFunc(rawLine, unicode.IsSpace)
	payload := strings.TrimRightFunc(trimmedLeft, unicode.IsSpace)
	if !ambiguousRichProtocolDraftPayload(payload) {
		return markdown.RichSourceRange{}, false
	}
	payloadOffset := len(rawLine) - len(trimmedLeft)
	payloadRange := markdown.RichSourceRange{
		Start: lineStart + payloadOffset,
		End:   lineStart + payloadOffset + len(payload),
	}
	// Goldmark treats the earliest prefixes (`#`, `##`, `###`) as empty
	// headings rather than Paragraph text. They are still ambiguous while the
	// model is streaming the MEDIA directive, so withhold this terminal line
	// until one more byte disambiguates it. Persistent parsing never uses this
	// best-effort preview rule.
	if len(payload) < len("###M") {
		return markdown.RichSourceRange{Start: lineStart, End: len(source)}, true
	}
	for _, fragment := range document.Fragments {
		if fragment.Kind != markdown.RichFragmentParagraph ||
			payloadRange.Start < fragment.SourceStart || payloadRange.End > fragment.SourceEnd {
			continue
		}
		if richRangeCoveredByBoundaryText(payloadRange, fragment.BoundaryTextRanges, source) {
			return markdown.RichSourceRange{Start: lineStart, End: len(source)}, true
		}
	}
	return markdown.RichSourceRange{}, false
}

func ambiguousRichProtocolDraftPayload(payload string) bool {
	const directivePrefix = generatedMediaDirectiveStem + ":"
	if payload != "" && strings.HasPrefix(directivePrefix, payload) {
		return true
	}
	if payload != "" && strings.HasPrefix(richSplitDelimiter, payload) {
		return true
	}
	return reservedGeneratedMediaPayload(payload)
}
