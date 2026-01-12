package markdown

import (
	"fmt"
	"regexp"
	"strings"
)

// latexSymbols maps LaTeX commands to Unicode equivalents
var latexSymbols = map[string]string{
	`\times`:          `×`,
	`\cdot`:           `·`,
	`\cdots`:          `⋯`,
	`\ldots`:          `…`,
	`\dots`:           `…`,
	`\leq`:            `≤`,
	`\geq`:            `≥`,
	`\le`:             `≤`,
	`\ge`:             `≥`,
	`\pm`:             `±`,
	`\neq`:            `≠`,
	`\ne`:             `≠`,
	`\approx`:         `≈`,
	`\infty`:          `∞`,
	`\in`:             `∈`,
	`\notin`:          `∉`,
	`\emptyset`:       `∅`,
	`\varnothing`:     `∅`,
	`\alpha`:          `α`,
	`\beta`:           `β`,
	`\gamma`:          `γ`,
	`\delta`:          `δ`,
	`\epsilon`:        `ε`,
	`\theta`:          `θ`,
	`\lambda`:         `λ`,
	`\rho`:            `ρ`,
	`\mu`:             `μ`,
	`\pi`:             `π`,
	`\sigma`:          `σ`,
	`\phi`:            `φ`,
	`\omega`:          `ω`,
	`\Gamma`:          `Γ`,
	`\Delta`:          `Δ`,
	`\Theta`:          `Θ`,
	`\Lambda`:         `Λ`,
	`\Xi`:             `Ξ`,
	`\Pi`:             `Π`,
	`\Sigma`:          `Σ`,
	`\Phi`:            `Φ`,
	`\Psi`:            `Ψ`,
	`\Omega`:          `Ω`,
	`\sin`:            `sin`,
	`\cos`:            `cos`,
	`\tan`:            `tg`,
	`\cot`:            `ctg`,
	`\sec`:            `sec`,
	`\csc`:            `csc`,
	`\arcsin`:         `arcsin`,
	`\arccos`:         `arccos`,
	`\arctan`:         `arctg`,
	`\log`:            `log`,
	`\ln`:             `ln`,
	`\lg`:             `lg`,
	`\sum`:            `Σ`,
	`\prod`:           `Π`,
	`\int`:            `∫`,
	`\partial`:        `∂`,
	`\nabla`:          `∇`,
	`\to`:             `→`,
	`\rightarrow`:     `→`,
	`\leftarrow`:      `←`,
	`\Rightarrow`:     `⇒`,
	`\Leftarrow`:      `⇐`,
	`\Leftrightarrow`: `⇔`,
	`\iff`:            `⇔`,
	`\therefore`:      `∴`,
	`\because`:        `∵`,
	`\implies`:        `⟹`,
	`\%`:              `%`,
}

// codeBlockPlaceholder is used to temporarily replace code blocks during LaTeX processing
const codeBlockPlaceholder = `__CODE_BLOCK_%d__`

// hideCodeBlocks finds all code blocks (```...``` and `...`) and replaces them with placeholders
// Returns: (map of placeholder -> original content, text with placeholders)
func hideCodeBlocks(text string) (map[string]string, string) {
	placeholders := make(map[string]string)
	result := text
	blockID := 0

	// First, handle multi-line code blocks: ```...```
	// Use (?s) flag for DOTALL mode to match across newlines
	// Build pattern with string concatenation to avoid raw string issues
	codePattern := "(?s)" + "```" + `[\s\S]*?` + "```"
	codeRe := regexp.MustCompile(codePattern)
	result = codeRe.ReplaceAllStringFunc(result, func(match string) string {
		placeholder := fmt.Sprintf(codeBlockPlaceholder, blockID)
		placeholders[placeholder] = match
		blockID++
		return placeholder
	})

	// Then, handle inline code blocks: `...`
	// Be careful not to match backticks inside multi-line blocks (already removed)
	inlineRe := regexp.MustCompile("`[^`\n]+`")
	result = inlineRe.ReplaceAllStringFunc(result, func(match string) string {
		placeholder := fmt.Sprintf(codeBlockPlaceholder, blockID)
		placeholders[placeholder] = match
		blockID++
		return placeholder
	})

	return placeholders, result
}

// restoreCodeBlocks restores original code blocks from placeholders
func restoreCodeBlocks(text string, placeholders map[string]string) string {
	result := text
	for placeholder, original := range placeholders {
		result = strings.ReplaceAll(result, placeholder, original)
	}
	return result
}

// convertLatexToUnicode converts LaTeX math expressions to Unicode text
// Supports both inline ($...$) and display ($$...$$, \[...\]) math
func convertLatexToUnicode(text string) string {
	// Step 1: Hide code blocks to prevent processing LaTeX inside them
	placeholders, maskedText := hideCodeBlocks(text)

	// Step 2: Pre-process: convert typographic quotes (inches/feet) before LaTeX parsing
	maskedText = convertTypographicQuotes(maskedText)

	// Step 3: Process LaTeX math expressions
	// First, handle display math: $$...$$ (with newlines)
	maskedText = convertDisplayMath(maskedText, `$$`, `$$`)

	// Then handle display math: \[...\] (with newlines)
	maskedText = convertDisplayMath(maskedText, `\[`, `\]`)

	// Handle display math with single $ on separate lines
	maskedText = convertDisplayMathSingleDollar(maskedText)

	// Finally, handle inline math: $...$ (but not $$)
	maskedText = convertInlineMath(maskedText)

	// Step 4: Restore original code blocks
	return restoreCodeBlocks(maskedText, placeholders)
}

// convertDisplayMath finds and converts display math expressions
func convertDisplayMath(text, startDelim, endDelim string) string {
	// Build regex pattern for display math with (?s) flag for DOTALL mode
	// This allows .+? to match across newlines
	pattern := `(?s)` + regexp.QuoteMeta(startDelim) + `(.+?)` + regexp.QuoteMeta(endDelim)
	re := regexp.MustCompile(pattern)

	return re.ReplaceAllStringFunc(text, func(match string) string {
		// Extract content between delimiters
		content := re.FindStringSubmatch(match)[1]
		return processLatexContent(content)
	})
}

// convertDisplayMathSingleDollar handles display math with single $ on separate lines
// Pattern: $ at start of line, content, $ at end of line (possibly multi-line)
func convertDisplayMathSingleDollar(text string) string {
	// Match $ at start of line (with optional leading whitespace),
	// content across multiple lines, then $ at end of line (with optional trailing whitespace)
	// Use (?s) for DOTALL mode so .+? matches newlines, and [\s\S]+? to match any char including newlines
	re := regexp.MustCompile(`(?ms)^\s*\$\n([\s\S]+?)\n\$\s*$`)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		// Extract content between the newlines
		submatches := re.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			result := processLatexContent(submatches[1])
			// Trim surrounding whitespace from the result
			return strings.TrimSpace(result)
		}
		return match
	})
}

// convertInlineMath finds and converts inline math expressions
// Handles $...$ but avoids matching $$...$$ (already processed)
// Also avoids matching currency patterns like $3.50
func convertInlineMath(text string) string {
	// Match $...$
	re := regexp.MustCompile(`\$([^$]+?)\$`)

	return re.ReplaceAllStringFunc(text, func(match string) string {
		// Extract content between delimiters (remove the $ signs)
		content := match[1 : len(match)-1]

		// Filter out non-LaTeX content (plain text with currency, etc.)
		// Valid LaTeX content should contain at least one of:
		// 1. A backslash (LaTeX command)
		// 2. An operator (=, +, -, *, /, <, >, _, ^)
		// 3. Just letters (single variable like $P$, $x$, etc.)
		// Otherwise, it's likely just currency or plain text
		hasLaTeX := strings.Contains(content, `\`)
		hasOperator := strings.ContainsAny(content, `=+-*/<>^_`)
		isJustLetters := isAlphaOnly(content)
		looksLikeMath := looksLikeFormulaContent(content)

		if !hasLaTeX && !hasOperator && !isJustLetters && !looksLikeMath {
			// No LaTeX commands or operators - likely currency, return unchanged
			return match
		}

		// Process normal LaTeX content
		return processLatexContent(content)
	})
}

// looksLikeFormulaContent checks if content looks like a math expression
// Returns true for: digits, Unicode primes (′ ″), letters, subscripts, superscripts
func looksLikeFormulaContent(s string) bool {
	if len(s) == 0 {
		return false
	}

	for _, c := range s {
		// Allow: digits, letters, Unicode primes, subscripts, superscripts
		isDigit := c >= '0' && c <= '9'
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isPrime := c == '′' || c == '″' // U+2032, U+2033
		// Common math symbols that might appear
		isMathSymbol := c == '°' || c == '√' || c == '×' || c == '÷' || c == 'π' ||
			c == 'α' || c == 'β' || c == 'γ' || c == 'δ' || c == 'ε' || c == 'θ' ||
			c == 'λ' || c == 'μ' || c == 'ρ' || c == 'σ' || c == 'φ' || c == 'ω' ||
			c == '≤' || c == '≥' || c == '≈' || c == '≠' || c == '±' || c == '∞' ||
			c == '∈' || c == '∉' || c == '∅' || c == '∴' || c == '∵' ||
			(c >= '₀' && c <= '₉') || // subscripts 0-9
			(c >= '⁰' && c <= '⁹') || // superscripts 0-9
			c == '⁺' || c == '⁻' || c == '⁼' || c == '⁽' || c == '⁾' // superscript operators

		if !isDigit && !isLetter && !isPrime && !isMathSymbol {
			return false
		}
	}
	return true
}

// isAlphaOnly checks if a string contains only letters (a-z, A-Z)
func isAlphaOnly(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// looksLikeCurrency checks if a string looks like a currency amount (e.g., "3.50", "100")
func looksLikeCurrency(s string) bool {
	// Trim leading whitespace
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}
	// Check if starts with digit
	if s[0] < '0' || s[0] > '9' {
		return false
	}
	// Check if the remaining part is all digits or decimal point
	for _, c := range s {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// removeSpacingCommands removes LaTeX spacing commands
// Removes \, (thin space), \! (negative thin space), \: (medium space), \; (thick space), \quad, \qquad
func removeSpacingCommands(content string) string {
	// Remove common spacing commands
	// \, is often used as thousands separator in European notation, replace with space
	result := strings.ReplaceAll(content, `\,`, ` `)   // thin space
	result = strings.ReplaceAll(result, `\!`, ``)      // negative thin space
	result = strings.ReplaceAll(result, `\:`, ``)      // medium space
	result = strings.ReplaceAll(result, `\;`, ``)      // thick space
	result = strings.ReplaceAll(result, `\quad`, ` `)  // quad space (1em, replace with single space)
	result = strings.ReplaceAll(result, `\qquad`, ` `) // double quad (2em, replace with single space)
	return result
}

// convertTypographicQuotes converts straight quotes to typographic primes
// Converts 1" -> 1″ (inches), 6' -> 6′ (feet/minutes)
// Only converts when quote follows a digit to avoid breaking text quotes
func convertTypographicQuotes(content string) string {
	runes := []rune(content)
	result := []rune{}

	for i := 0; i < len(runes); i++ {
		// Check for " (inch/second) after digit
		if runes[i] == '"' && i > 0 {
			prev := runes[i-1]
			// Convert ONLY if preceded by digit (for inches)
			if prev >= '0' && prev <= '9' {
				result = append(result, '″') // U+2033 double prime
				continue
			}
		}

		// Check for ' (foot/minute) after digit
		if runes[i] == '\'' && i > 0 {
			prev := runes[i-1]
			// Convert ONLY if preceded by digit (for feet/minutes)
			if prev >= '0' && prev <= '9' {
				result = append(result, '′') // U+2032 single prime
				continue
			}
		}

		result = append(result, runes[i])
	}

	return string(result)
}

// flattenBraces flattens 2D LaTeX constructions to 1D text
// \underbrace{FORMULA}_{TEXT} -> FORMULA (TEXT)
// \overbrace{FORMULA}^{TEXT} -> FORMULA (TEXT)
func flattenBraces(content string) string {
	// Handle \underbrace{...}_{...}
	underRe := regexp.MustCompile(`\\underbrace\{(.+?)\}_\{(.+?)\}`)
	content = underRe.ReplaceAllString(content, `$1 ($2)`)

	// Handle \overbrace{...}^{...}
	overRe := regexp.MustCompile(`\\overbrace\{(.+?)\}\^\{(.+?)\}`)
	content = overRe.ReplaceAllString(content, `$1 ($2)`)

	return content
}

// processLatexContent processes LaTeX content and converts it to Unicode
func processLatexContent(content string) string {
	// Apply conversions in order

	// 0. Remove LaTeX spacing commands (\,, \!, \:, \;, \quad, \qquad)
	content = removeSpacingCommands(content)

	// 0.5. Flatten 2D constructions to 1D text
	// \underbrace{FORMULA}_{TEXT} -> FORMULA (TEXT)
	// \overbrace{FORMULA}^{TEXT} -> FORMULA (TEXT)
	content = flattenBraces(content)

	// 1. Remove \text{...} wrappers
	content = removeTextWrappers(content)

	// 2. Handle fractions \frac{a}{b} -> a/b
	content = convertFractions(content)

	// 3. Handle square roots \sqrt{x} -> √x
	content = convertSquareRoots(content)

	// 4. Replace known LaTeX symbols with Unicode
	content = replaceLatexSymbols(content)

	// 5. Handle simple superscripts x^2 -> x²
	content = convertSuperscripts(content)

	// 6. Handle simple subscripts H_2 -> H₂
	content = convertSubscripts(content)

	return content
}

// removeTextWrappers removes \text{...} wrappers, keeping the content
func removeTextWrappers(content string) string {
	re := regexp.MustCompile(`\\text\{(.*?)\}`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		// Extract content and trim spaces
		submatches := re.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			return strings.TrimSpace(submatches[1])
		}
		return match
	})
}

// findMatchingBrace finds the position of the closing brace that matches the opening brace at startPos
// Returns -1 if not found or braces are unbalanced
// Works with rune positions for proper UTF-8 support
func findMatchingBrace(s string, startPos int) int {
	runes := []rune(s)
	if startPos >= len(runes) || runes[startPos] != '{' {
		return -1
	}

	depth := 0
	for i := startPos; i < len(runes); i++ {
		if runes[i] == '{' {
			depth++
		} else if runes[i] == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1 // Unmatched braces
}

// convertFractions converts \frac{a}{b} to a/b with proper brace matching
func convertFractions(content string) string {
	runes := []rune(content)
	result := []rune{}
	i := 0

	for i < len(runes) {
		// Look for \frac{ (6 runes: \ f r a c {)
		if i+5 < len(runes) &&
			runes[i] == '\\' && runes[i+1] == 'f' && runes[i+2] == 'r' &&
			runes[i+3] == 'a' && runes[i+4] == 'c' && runes[i+5] == '{' {

			// Find matching brace for numerator (start at position of {)
			openBracePos := i + 5
			numEnd := findMatchingBrace(content, openBracePos)

			if numEnd == -1 {
				// Unmatched braces, skip this \frac
				result = append(result, runes[i])
				i++
				continue
			}

			// Look for second { for denominator
			denStart := numEnd + 1
			if denStart >= len(runes) || runes[denStart] != '{' {
				// No denominator brace found, skip
				result = append(result, runes[i])
				i++
				continue
			}

			denEnd := findMatchingBrace(content, denStart)

			if denEnd == -1 {
				// Unmatched braces, skip this \frac
				result = append(result, runes[i])
				i++
				continue
			}

			// Extract numerator and denominator (without the braces)
			numerator := string(runes[openBracePos+1 : numEnd])
			denominator := string(runes[denStart+1 : denEnd])

			// Recursively process the contents
			numerator = processLatexContent(numerator)
			denominator = processLatexContent(denominator)

			// Build result
			result = append(result, []rune(numerator)...)
			result = append(result, '/')
			result = append(result, []rune(denominator)...)

			// Move past the entire \frac{...}{...}
			i = denEnd + 1
		} else {
			result = append(result, runes[i])
			i++
		}
	}

	return string(result)
}

// convertSquareRoots converts \sqrt{x} to √x with proper brace matching
func convertSquareRoots(content string) string {
	runes := []rune(content)
	result := []rune{}
	i := 0

	for i < len(runes) {
		// Look for \sqrt{ (6 runes: \ s q r t {)
		if i+5 < len(runes) &&
			runes[i] == '\\' && runes[i+1] == 's' && runes[i+2] == 'q' &&
			runes[i+3] == 'r' && runes[i+4] == 't' && runes[i+5] == '{' {

			// Find matching brace (start at position of {)
			openBracePos := i + 5
			argEnd := findMatchingBrace(content, openBracePos)

			if argEnd == -1 {
				// Unmatched braces, skip this \sqrt
				result = append(result, runes[i])
				i++
				continue
			}

			// Extract argument (without the braces)
			arg := string(runes[openBracePos+1 : argEnd])

			// Recursively process the contents
			arg = processLatexContent(arg)

			// Determine if we need parentheses around the argument
			// Add parens if arg contains: spaces, operators (+, -, *, /, =, <, >), or is complex
			needsParens := strings.ContainsAny(arg, " +-=<>") || strings.Contains(arg, "/")

			// Build result
			result = append(result, '√')
			if needsParens {
				result = append(result, '(')
			}
			result = append(result, []rune(arg)...)
			if needsParens {
				result = append(result, ')')
			}

			// Move past the entire \sqrt{...}
			i = argEnd + 1
		} else {
			result = append(result, runes[i])
			i++
		}
	}

	return string(result)
}

// replaceLatexSymbols replaces LaTeX commands with Unicode equivalents
func replaceLatexSymbols(content string) string {
	result := content

	// Sort keys by length (descending) to match longer commands first
	// e.g., \leq before \le
	keys := make([]string, 0, len(latexSymbols))
	for k := range latexSymbols {
		keys = append(keys, k)
	}

	// Simple bubble sort by length (descending)
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if len(keys[j]) > len(keys[i]) {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	// Replace each symbol
	for _, key := range keys {
		result = strings.ReplaceAll(result, key, latexSymbols[key])
	}

	return result
}

// convertSuperscripts converts simple superscripts like x^2 to x²
// Handles both x^2 and x^{2} patterns, including x^{-1}
// Also handles text superscripts like x^{text} → x^text (removes braces)
func convertSuperscripts(content string) string {
	superscriptMap := map[rune]rune{
		'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
		'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
		'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
		'n': 'ⁿ',
	}

	// First handle ^\circ pattern (degree symbol)
	// Must come before other patterns to avoid matching as just ^
	circRe := regexp.MustCompile(`\^\\circ`)
	content = circRe.ReplaceAllString(content, `°`)

	// Handle ^{\circ} pattern as well
	circBracedRe := regexp.MustCompile(`\^\{\\circ\}`)
	content = circBracedRe.ReplaceAllString(content, `°`)

	// Then handle ^{-n} and ^{+n} patterns (sign + digit)
	bracedRe := regexp.MustCompile(`\^\{([+-])([0-9n])\}`)
	content = bracedRe.ReplaceAllStringFunc(content, func(match string) string {
		submatches := bracedRe.FindStringSubmatch(match)
		if len(submatches) >= 3 {
			sign := rune(submatches[1][0])
			digit := rune(submatches[2][0])
			if signReplacement, ok1 := superscriptMap[sign]; ok1 {
				if digitReplacement, ok2 := superscriptMap[digit]; ok2 {
					return string(signReplacement) + string(digitReplacement)
				}
			}
		}
		return match
	})

	// Then handle ^{text} pattern (remove braces, keep text as regular superscript)
	// This handles cases like x^{sleep} → x^sleep (no Unicode for text superscripts)
	bracedRe2 := regexp.MustCompile(`\^\{([^}]+)\}`)
	content = bracedRe2.ReplaceAllStringFunc(content, func(match string) string {
		submatches := bracedRe2.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			text := submatches[1]
			// Check if it's a single known character
			if len(text) == 1 {
				char := rune(text[0])
				if replacement, ok := superscriptMap[char]; ok {
					return string(replacement)
				}
			}
			// For multi-character text, just remove braces: x^{sleep} → x^sleep
			return "^" + text
		}
		return match
	})

	// Then handle ^{n} pattern (single character with braces) - redundant but kept for safety
	bracedRe3 := regexp.MustCompile(`\^\{([0-9+\-=()n])\}`)
	content = bracedRe3.ReplaceAllStringFunc(content, func(match string) string {
		submatches := bracedRe3.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			char := rune(submatches[1][0])
			if replacement, ok := superscriptMap[char]; ok {
				return string(replacement)
			}
		}
		return match
	})

	// Then handle ^n pattern (without braces)
	re := regexp.MustCompile(`\^(\d|[+\-=()n])`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		if len(match) == 2 {
			char := rune(match[1])
			if replacement, ok := superscriptMap[char]; ok {
				return string(replacement)
			}
		}
		return match
	})
}

// convertSubscripts converts simple subscripts like H_2 to H₂
// Also handles text subscripts like T_{sleep} → T_sleep
// Preserves braces for expressions like T_{i=1} (contains operators)
func convertSubscripts(content string) string {
	subscriptMap := map[rune]rune{
		'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
		'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
		'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	}

	// First handle _{text} pattern (remove braces, keep text)
	// Only remove braces if content is purely alphabetic (letters only)
	// For expressions like {i=1}, keep the braces
	bracedRe := regexp.MustCompile(`_\{([^}]+)\}`)
	content = bracedRe.ReplaceAllStringFunc(content, func(match string) string {
		submatches := bracedRe.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			text := submatches[1]
			// Check if text contains only letters (a-z, A-Z)
			isAlphaOnly := true
			for _, c := range text {
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
					isAlphaOnly = false
					break
				}
			}
			// Only remove braces for pure text like "sleep", "total"
			if isAlphaOnly {
				return "_" + text
			}
			// For expressions like "i=1", keep the braces
		}
		return match
	})

	// Then handle _{digits} pattern (multi-digit numbers in braces)
	// e.g., log_{10} → log₁₀
	digitsBracedRe := regexp.MustCompile(`_\{(\d+)\}`)
	content = digitsBracedRe.ReplaceAllStringFunc(content, func(match string) string {
		submatches := digitsBracedRe.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			digits := submatches[1]
			result := ""
			for _, c := range digits {
				if replacement, ok := subscriptMap[c]; ok {
					result += string(replacement)
				} else {
					result += string(c)
				}
			}
			return result
		}
		return match
	})

	// Then handle _digits pattern (multi-digit numbers without braces)
	// e.g., log_10 → log₁₀ (but must come before single-digit to avoid matching _1 in _10)
	digitsRe := regexp.MustCompile(`_(\d{2,})`)
	content = digitsRe.ReplaceAllStringFunc(content, func(match string) string {
		submatches := digitsRe.FindStringSubmatch(match)
		if len(submatches) >= 2 {
			digits := submatches[1]
			result := ""
			for _, c := range digits {
				if replacement, ok := subscriptMap[c]; ok {
					result += string(replacement)
				} else {
					result += string(c)
				}
			}
			return result
		}
		return match
	})

	// Then handle _n pattern (single character without braces)
	re := regexp.MustCompile(`_(\d|[+\-=()])`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		if len(match) == 2 {
			char := rune(match[1])
			if replacement, ok := subscriptMap[char]; ok {
				return string(replacement)
			}
		}
		return match
	})
}
