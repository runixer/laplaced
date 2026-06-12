package telegram

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// CodeBlock represents a protected byte range [Start, End) that must not be
// split (code blocks, tables).
type CodeBlock struct {
	Start int
	End   int
}

// splitPoint is a candidate split position tracked in both byte offset (for
// slicing and protected-block checks) and rune offset (for length budgets),
// with the priority of splitting there (higher = better).
type splitPoint struct {
	byteOff  int
	runeOff  int
	priority int
}

// SplitMessageSmart splits a long message into chunks of at most limit RUNES.
// It preserves markdown structure: code blocks and tables are never cut at
// safe-point selection; oversized tables are split between rows with the
// header repeated in every piece.
func SplitMessageSmart(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	blocks := FindProtectedBlocks(text)
	safeSplitPoints := findSafeSplitPoints(text, blocks)
	chunks := splitBySafePoints(text, safeSplitPoints, limit)

	// If any chunk is still too long, split it by table/prose segments.
	finalChunks := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk) <= limit {
			finalChunks = append(finalChunks, chunk)
		} else {
			finalChunks = append(finalChunks, splitOversizedChunk(chunk, limit)...)
		}
	}

	return finalChunks
}

// FindCodeBlocks finds all code blocks in the text and returns their positions
func FindCodeBlocks(text string) []CodeBlock {
	var blocks []CodeBlock

	// Find HTML <pre> blocks (for tables and code in HTML format)
	preBlockRegex := regexp.MustCompile(`<pre>[\s\S]*?</pre>`)
	preMatches := preBlockRegex.FindAllStringIndex(text, -1)
	for _, match := range preMatches {
		blocks = append(blocks, CodeBlock{Start: match[0], End: match[1]})
	}

	// Find triple-backtick code blocks
	tripleBacktickRegex := regexp.MustCompile("```[\\s\\S]*?```")
	matches := tripleBacktickRegex.FindAllStringIndex(text, -1)
	for _, match := range matches {
		blocks = append(blocks, CodeBlock{Start: match[0], End: match[1]})
	}

	// Find inline code blocks (single backticks)
	// We need to be careful not to match backticks inside triple-backtick blocks
	inlineCodeRegex := regexp.MustCompile("`[^`\n]+`")
	inlineMatches := inlineCodeRegex.FindAllStringIndex(text, -1)

	for _, match := range inlineMatches {
		// Check if this inline code is inside a triple-backtick block or <pre> block
		insideBlock := false
		for _, block := range blocks {
			if match[0] >= block.Start && match[1] <= block.End {
				insideBlock = true
				break
			}
		}

		if !insideBlock {
			blocks = append(blocks, CodeBlock{Start: match[0], End: match[1]})
		}
	}

	return blocks
}

// FindProtectedBlocks returns every byte range that must not be split: code
// blocks plus markdown tables. Table ranges overlapping a code block are
// dropped — pipe-prefixed lines inside a fence are already protected.
func FindProtectedBlocks(text string) []CodeBlock {
	blocks := FindCodeBlocks(text)
	return append(blocks, findTableBlocksOutsideCode(text, blocks)...)
}

// findTableBlocks returns byte ranges of markdown tables: runs of two or more
// consecutive lines whose trimmed form starts with "|".
func findTableBlocks(text string) []CodeBlock {
	var blocks []CodeBlock

	lines := strings.Split(text, "\n")
	bytePos := 0
	runStart := -1
	runEnd := 0
	runLines := 0

	flush := func() {
		if runLines >= 2 {
			blocks = append(blocks, CodeBlock{Start: runStart, End: runEnd})
		}
		runStart = -1
		runLines = 0
	}

	for _, line := range lines {
		lineEnd := bytePos + len(line)
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			if runStart == -1 {
				runStart = bytePos
			}
			runLines++
			runEnd = lineEnd
		} else {
			flush()
		}
		bytePos = lineEnd + 1
	}
	flush()

	return blocks
}

// findTableBlocksOutsideCode returns table blocks that do not overlap any of
// the given code blocks.
func findTableBlocksOutsideCode(text string, codeBlocks []CodeBlock) []CodeBlock {
	var out []CodeBlock
	for _, tb := range findTableBlocks(text) {
		overlaps := false
		for _, cb := range codeBlocks {
			if tb.Start < cb.End && cb.Start < tb.End {
				overlaps = true
				break
			}
		}
		if !overlaps {
			out = append(out, tb)
		}
	}
	return out
}

// findSafeSplitPoints finds positions where it's safe to split the message
func findSafeSplitPoints(text string, blocks []CodeBlock) []splitPoint {
	var splitPoints []splitPoint

	lines := strings.Split(text, "\n")
	bytePos := 0
	runePos := 0

	for i, line := range lines {
		lineEndByte := bytePos + len(line)
		lineEndRune := runePos + utf8.RuneCountInString(line)

		// Check if we're at the end of a line (potential split point)
		if i < len(lines)-1 { // Not the last line
			// Check if this position is safe (not inside a protected block)
			if IsSafeSplitPosition(lineEndByte, blocks) {
				// Prioritize certain types of splits
				priority := getSplitPriority(line, i, lines)
				if priority > 0 {
					splitPoints = append(splitPoints, splitPoint{byteOff: lineEndByte, runeOff: lineEndRune, priority: priority})
				}
			}
		}

		// Move to next line (including the \n character)
		bytePos = lineEndByte + 1
		runePos = lineEndRune + 1
	}

	return splitPoints
}

// IsSafeSplitPosition checks if a byte position is safe for splitting (not
// inside a protected block)
func IsSafeSplitPosition(pos int, blocks []CodeBlock) bool {
	for _, block := range blocks {
		if pos > block.Start && pos < block.End {
			return false
		}
	}
	return true
}

// getSplitPriority returns the priority of splitting at this line (higher = better)
func getSplitPriority(line string, lineIndex int, allLines []string) int {
	// Double empty line (paragraph separator with vertical spacing) - HIGHEST priority
	// This happens when we have \n\n between blocks
	if strings.TrimSpace(line) == "" && lineIndex > 0 && strings.TrimSpace(allLines[lineIndex-1]) == "" {
		return 150
	}

	// Empty line after non-empty line (paragraph break) - very high priority
	if strings.TrimSpace(line) == "" && lineIndex > 0 && strings.TrimSpace(allLines[lineIndex-1]) != "" {
		return 100
	}

	// Line ending with triple backticks (end of code block) - high priority
	if strings.HasSuffix(strings.TrimSpace(line), "```") {
		return 90
	}

	// Line starting with # (heading) - high priority
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return 80
	}

	// Line ending with period, exclamation, or question mark - medium priority
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 0 {
		lastChar := trimmed[len(trimmed)-1]
		if lastChar == '.' || lastChar == '!' || lastChar == '?' || lastChar == ':' {
			return 50
		}
	}

	// Any other line - low priority
	return 10
}

// splitBySafePoints splits text using the provided safe split points; lengths
// are budgeted in runes, slicing happens at byte offsets. Within each budget
// window the highest-priority point wins (paragraph break beats an arbitrary
// line end), with the farthest point winning among equals.
func splitBySafePoints(text string, splitPoints []splitPoint, limit int) []string {
	if len(splitPoints) == 0 {
		return []string{text}
	}

	totalRunes := utf8.RuneCountInString(text)

	var chunks []string
	start := splitPoint{}
	next := 0 // index of the first split point after start

	for start.byteOff < len(text) {
		// Everything left fits — no further splitting needed.
		if totalRunes-start.runeOff <= limit {
			break
		}

		// Pick the best point within the budget: highest priority, then farthest.
		best := -1
		for j := next; j < len(splitPoints); j++ {
			if splitPoints[j].runeOff-start.runeOff > limit {
				break
			}
			if best == -1 || splitPoints[j].priority >= splitPoints[best].priority {
				best = j
			}
		}
		if best == -1 {
			// No safe point fits the budget. Cut at the next available point
			// (the oversized chunk is handled by the force-split pass), or
			// give up and let the tail be force-split.
			if next < len(splitPoints) {
				best = next
			} else {
				break
			}
		}

		if chunk := strings.TrimSpace(text[start.byteOff:splitPoints[best].byteOff]); chunk != "" {
			chunks = append(chunks, chunk)
		}
		// +1 to skip the newline
		start = splitPoint{byteOff: splitPoints[best].byteOff + 1, runeOff: splitPoints[best].runeOff + 1}
		next = best + 1
	}

	// Add the final chunk
	if start.byteOff < len(text) {
		if remaining := strings.TrimSpace(text[start.byteOff:]); remaining != "" {
			chunks = append(chunks, remaining)
		}
	}

	return chunks
}

// splitOversizedChunk splits a chunk that exceeds the rune limit. Markdown
// tables inside the chunk are split between rows (header repeated); prose
// segments fall back to forceSplitChunk.
func splitOversizedChunk(chunk string, limit int) []string {
	tables := findTableBlocksOutsideCode(chunk, FindCodeBlocks(chunk))
	if len(tables) == 0 {
		return forceSplitChunk(chunk, limit)
	}

	var out []string
	emit := func(segment string, isTable bool) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return
		}
		switch {
		case utf8.RuneCountInString(segment) <= limit:
			out = append(out, segment)
		case isTable:
			out = append(out, splitTableBlock(segment, limit)...)
		default:
			out = append(out, forceSplitChunk(segment, limit)...)
		}
	}

	pos := 0
	for _, tb := range tables {
		emit(chunk[pos:tb.Start], false)
		emit(chunk[tb.Start:tb.End], true)
		pos = tb.End
	}
	emit(chunk[pos:], false)

	return out
}

// splitTableBlock splits an oversized markdown table between rows, repeating
// the header and separator lines in every piece so each piece still parses as
// a table on its own.
func splitTableBlock(table string, limit int) []string {
	lines := strings.Split(table, "\n")
	if len(lines) < 3 {
		return forceSplitChunk(table, limit)
	}

	header := lines[0] + "\n" + lines[1]
	headerRunes := utf8.RuneCountInString(header)

	var out []string
	var rows []string
	curRunes := headerRunes

	flush := func() {
		if len(rows) > 0 {
			out = append(out, header+"\n"+strings.Join(rows, "\n"))
			rows = nil
			curRunes = headerRunes
		}
	}

	for _, row := range lines[2:] {
		rowRunes := utf8.RuneCountInString(row) + 1 // +1 for the newline
		if curRunes+rowRunes > limit && len(rows) > 0 {
			flush()
		}
		rows = append(rows, row)
		curRunes += rowRunes
	}
	flush()

	return out
}

// forceSplitChunk splits a chunk that's too large, even if it breaks markdown
func forceSplitChunk(chunk string, limit int) []string {
	// This is a fallback for when we can't split nicely
	// We'll try to split by sentences first, then by words, then by characters

	runes := []rune(chunk)
	if len(runes) <= limit {
		return []string{chunk}
	}

	var chunks []string

	for len(runes) > limit {
		// Try to find a good break point within the limit
		breakPoint := limit

		// Look for sentence endings within the limit
		for i := limit - 1; i >= limit/2; i-- {
			r := runes[i]
			if r == '.' || r == '!' || r == '?' || r == '\n' {
				breakPoint = i + 1
				break
			}
		}

		// If no sentence ending found, look for word boundaries
		if breakPoint == limit {
			for i := limit - 1; i >= limit/2; i-- {
				if runes[i] == ' ' {
					breakPoint = i
					break
				}
			}
		}

		// Extract the chunk
		if piece := strings.TrimSpace(string(runes[:breakPoint])); piece != "" {
			chunks = append(chunks, piece)
		}

		// Move to the next part
		runes = runes[breakPoint:]
		for len(runes) > 0 && runes[0] == ' ' {
			runes = runes[1:]
		}
	}

	// Add the remaining text
	if remaining := strings.TrimSpace(string(runes)); remaining != "" {
		chunks = append(chunks, remaining)
	}

	return chunks
}
