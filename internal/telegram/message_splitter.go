package telegram

import (
	"regexp"
	"strings"
)

// CodeBlock represents a code block with its start and end positions
type CodeBlock struct {
	Start int
	End   int
}

// SplitMessageSmart splits a long message into chunks that are safe for Telegram's message length limit.
// It preserves markdown structure by avoiding splits inside code blocks and other markdown constructs.
func SplitMessageSmart(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}

	// Find all code blocks (both ``` and single `)
	codeBlocks := FindCodeBlocks(text)

	// Find safe split points (paragraph breaks, after code blocks, etc.)
	safeSplitPoints := findSafeSplitPoints(text, codeBlocks)

	// Split the message using safe points
	chunks := splitBySafePoints(text, safeSplitPoints, limit)

	// If any chunk is still too long, force split it (but log a warning)
	finalChunks := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if len(chunk) <= limit {
			finalChunks = append(finalChunks, chunk)
		} else {
			// Force split oversized chunks
			forceSplitChunks := forceSplitChunk(chunk, limit)
			finalChunks = append(finalChunks, forceSplitChunks...)
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

// findSafeSplitPoints finds positions where it's safe to split the message
func findSafeSplitPoints(text string, codeBlocks []CodeBlock) []int {
	var splitPoints []int

	lines := strings.Split(text, "\n")
	currentPos := 0

	for i, line := range lines {
		lineEnd := currentPos + len(line)

		// Check if we're at the end of a line (potential split point)
		if i < len(lines)-1 { // Not the last line
			splitPos := lineEnd

			// Check if this position is safe (not inside a code block)
			if IsSafeSplitPosition(splitPos, codeBlocks) {
				// Prioritize certain types of splits
				priority := getSplitPriority(line, i, lines)
				if priority > 0 {
					splitPoints = append(splitPoints, splitPos)
				}
			}
		}

		// Move to next line (including the \n character)
		currentPos = lineEnd + 1
	}

	return splitPoints
}

// IsSafeSplitPosition checks if a position is safe for splitting (not inside code blocks)
func IsSafeSplitPosition(pos int, codeBlocks []CodeBlock) bool {
	for _, block := range codeBlocks {
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

// splitBySafePoints splits text using the provided safe split points
func splitBySafePoints(text string, splitPoints []int, limit int) []string {
	if len(splitPoints) == 0 {
		return []string{text}
	}

	var chunks []string
	start := 0
	currentChunkEnd := start

	for _, splitPoint := range splitPoints {
		// Check if we can extend the current chunk to this split point
		if splitPoint-start <= limit {
			// Yes, we can extend to this split point
			currentChunkEnd = splitPoint
		} else {
			// No, this would make the chunk too long
			// Finalize the current chunk at the last good split point
			if currentChunkEnd > start {
				chunk := text[start:currentChunkEnd]
				chunks = append(chunks, strings.TrimSpace(chunk))
				start = currentChunkEnd + 1 // +1 to skip the newline

				// Check if we can start a new chunk with this split point
				if splitPoint-start <= limit {
					currentChunkEnd = splitPoint
				} else {
					// This split point is still too far, we'll handle it in force split
					currentChunkEnd = start
				}
			} else {
				// No good split point found yet, this chunk will be force split later
				currentChunkEnd = start
			}
		}
	}

	// Add the final chunk
	if start < len(text) {
		remaining := text[start:]
		if strings.TrimSpace(remaining) != "" {
			chunks = append(chunks, strings.TrimSpace(remaining))
		}
	}

	return chunks
}

// forceSplitChunk splits a chunk that's too large, even if it breaks markdown
func forceSplitChunk(chunk string, limit int) []string {
	// This is a fallback for when we can't split nicely
	// We'll try to split by sentences first, then by words, then by characters

	if len(chunk) <= limit {
		return []string{chunk}
	}

	var chunks []string
	remaining := chunk

	for len(remaining) > limit {
		// Try to find a good break point within the limit
		breakPoint := limit

		// Look for sentence endings within the limit
		for i := limit - 1; i >= limit/2 && i < len(remaining); i-- {
			char := remaining[i]
			if char == '.' || char == '!' || char == '?' || char == '\n' {
				breakPoint = i + 1
				break
			}
		}

		// If no sentence ending found, look for word boundaries
		if breakPoint == limit {
			for i := limit - 1; i >= limit/2 && i < len(remaining); i-- {
				if remaining[i] == ' ' {
					breakPoint = i
					break
				}
			}
		}

		// Extract the chunk
		chunkText := remaining[:breakPoint]
		chunks = append(chunks, strings.TrimSpace(chunkText))

		// Move to the next part
		remaining = remaining[breakPoint:]
		remaining = strings.TrimPrefix(remaining, " ")
	}

	// Add the remaining text
	if strings.TrimSpace(remaining) != "" {
		chunks = append(chunks, strings.TrimSpace(remaining))
	}

	return chunks
}
