package markdown

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// ... (все переменные и константы без изменений)

var latexSymbols = map[string]string{
	`\left`: ``, `\right`: ``,
	`\times`: `×`, `\cdot`: `·`, `\cdots`: `⋯`, `\ldots`: `…`, `\dots`: `…`,
	`\leq`: `≤`, `\geq`: `≥`, `\le`: `≤`, `\ge`: `≥`,
	`\neq`: `≠`, `\ne`: `≠`, `\approx`: `≈`,
	`\pm`: `±`, `\infty`: `∞`, `\in`: `∈`, `\notin`: `∉`,
	`\emptyset`: `∅`, `\varnothing`: `∅`, `\partial`: `∂`, `\nabla`: `∇`,
	`\alpha`: `α`, `\beta`: `β`, `\gamma`: `γ`, `\delta`: `δ`,
	`\epsilon`: `ε`, `\zeta`: `ζ`, `\eta`: `η`, `\theta`: `θ`,
	`\iota`: `ι`, `\kappa`: `κ`, `\lambda`: `λ`, `\mu`: `μ`,
	`\nu`: `ν`, `\xi`: `ξ`, `\pi`: `π`, `\rho`: `ρ`,
	`\sigma`: `σ`, `\tau`: `τ`, `\upsilon`: `υ`, `\phi`: `φ`,
	`\chi`: `χ`, `\psi`: `ψ`, `\omega`: `ω`,
	`\Gamma`: `Γ`, `\Delta`: `Δ`, `\Theta`: `Θ`, `\Lambda`: `Λ`,
	`\Xi`: `Ξ`, `\Pi`: `Π`, `\Sigma`: `Σ`, `\Phi`: `Φ`,
	`\Psi`: `Ψ`, `\Omega`: `Ω`,
	`\sin`: `sin`, `\cos`: `cos`, `\tan`: `tg`, `\cot`: `ctg`,
	`\sec`: `sec`, `\csc`: `csc`, `\arcsin`: `arcsin`,
	`\arccos`: `arccos`, `\arctan`: `arctg`,
	`\log`: `log`, `\ln`: `ln`, `\lg`: `lg`,
	`\sum`: `Σ`, `\prod`: `Π`, `\int`: `∫`,
	`\lim`: `lim`, `\limsup`: `lim sup`, `\liminf`: `lim inf`,
	`\iint`: `∬`, `\iiint`: `∭`, `\oint`: `∮`,
	`\cup`: `∪`, `\cap`: `∩`,
	`\to`: `→`, `\rightarrow`: `→`, `\xrightarrow`: `→`,
	`\leftarrow`: `←`, `\Rightarrow`: `⇒`, `\Leftarrow`: `⇐`,
	`\Leftrightarrow`: `⇔`, `\iff`: `⇔`, `\implies`: `⟹`,
	`\uparrow`: `↑`, `\downarrow`: `↓`,
	`\therefore`: `∴`, `\because`: `∵`,
	`\triangle`: `△`, `\angle`: `∠`,
	`\parallel`: `∥`, `\perp`: `⊥`,
	`\%`: `%`,
}

var sortedLatexKeys []string

func init() {
	sortedLatexKeys = make([]string, 0, len(latexSymbols))
	for k := range latexSymbols {
		sortedLatexKeys = append(sortedLatexKeys, k)
	}
	sort.Slice(sortedLatexKeys, func(i, j int) bool {
		return len(sortedLatexKeys[i]) > len(sortedLatexKeys[j])
	})
}

var superscriptMap = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾', 'n': 'ⁿ',
}

var subscriptMap = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
}

type tokenType int

const (
	tokenText tokenType = iota
	tokenCodeBlock
	tokenCodeInline
	tokenMathDisplay
	tokenMathInline
)

type token struct {
	typ     tokenType
	content string
	raw     string
}

func convertLatexToUnicode(text string) string {
	tokens := tokenize(text)

	var result strings.Builder
	for _, tok := range tokens {
		switch tok.typ {
		case tokenCodeBlock, tokenCodeInline:
			result.WriteString(tok.raw)
		case tokenMathDisplay:
			processed := processLatexContent(tok.content)
			result.WriteString(strings.TrimSpace(processed))
		case tokenMathInline:
			processed := processLatexContent(tok.content)
			result.WriteString(processed)
		case tokenText:
			result.WriteString(convertTypographicQuotes(tok.content))
		}
	}

	return result.String()
}

func tokenize(text string) []token {
	var tokens []token
	runes := []rune(text)
	i := 0

	for i < len(runes) {
		if raw, end := matchCodeBlock(runes, i); end > i {
			tokens = append(tokens, token{tokenCodeBlock, raw, raw})
			i = end
			continue
		}

		if raw, end := matchInlineCode(runes, i); end > i {
			tokens = append(tokens, token{tokenCodeInline, raw, raw})
			i = end
			continue
		}

		if content, raw, end := matchDisplayMath(runes, i); end > i {
			tokens = append(tokens, token{tokenMathDisplay, content, raw})
			i = end
			continue
		}

		if content, raw, end := matchInlineMath(runes, i); end > i {
			if looksLikeCurrency(content) {
				tokens = append(tokens, token{tokenText, raw, raw})
			} else {
				tokens = append(tokens, token{tokenMathInline, content, raw})
			}
			i = end
			continue
		}

		tokens = append(tokens, token{tokenText, string(runes[i]), string(runes[i])})
		i++
	}

	return mergeTextTokens(tokens)
}

func mergeTextTokens(tokens []token) []token {
	if len(tokens) == 0 {
		return tokens
	}

	merged := make([]token, 0, len(tokens))
	current := tokens[0]

	for i := 1; i < len(tokens); i++ {
		if current.typ == tokenText && tokens[i].typ == tokenText {
			current.content += tokens[i].content
			current.raw += tokens[i].raw
		} else {
			merged = append(merged, current)
			current = tokens[i]
		}
	}
	merged = append(merged, current)
	return merged
}

func matchCodeBlock(runes []rune, start int) (raw string, end int) {
	if start+2 >= len(runes) || string(runes[start:start+3]) != "```" {
		return "", start
	}

	for i := start + 3; i+2 < len(runes); i++ {
		if string(runes[i:i+3]) == "```" {
			return string(runes[start : i+3]), i + 3
		}
	}
	if len(runes) >= start+6 && string(runes[len(runes)-3:]) == "```" {
		return string(runes[start:]), len(runes)
	}
	return "", start
}

func matchInlineCode(runes []rune, start int) (raw string, end int) {
	if runes[start] != '`' {
		return "", start
	}
	if start+2 < len(runes) && runes[start+1] == '`' && runes[start+2] == '`' {
		return "", start
	}

	for i := start + 1; i < len(runes); i++ {
		if runes[i] == '`' {
			return string(runes[start : i+1]), i + 1
		}
		if runes[i] == '\n' {
			return "", start
		}
	}
	return "", start
}

func matchDisplayMath(runes []rune, start int) (content, raw string, end int) {
	// Try $$...$$
	if start+1 < len(runes) && runes[start] == '$' && runes[start+1] == '$' {
		contentStart := start + 2
		for i := contentStart; i < len(runes); i++ {
			// Check for \$$ (escaped dollar + closing $)
			if i+2 < len(runes) && runes[i] == '\\' && runes[i+1] == '$' && runes[i+2] == '$' {
				return string(runes[contentStart : i+2]), string(runes[start : i+3]), i + 3
			}
			// Regular $$ closing (not preceded by \)
			if runes[i] == '$' && i+1 < len(runes) && runes[i+1] == '$' {
				if i > 0 && runes[i-1] == '\\' {
					continue
				}
				return string(runes[contentStart:i]), string(runes[start : i+2]), i + 2
			}
		}
		// Check end with \$$
		if len(runes) >= 3 && string(runes[len(runes)-3:]) == `\$$` && contentStart <= len(runes)-3 {
			return string(runes[contentStart : len(runes)-1]), string(runes[start:]), len(runes)
		}
	}

	// Try \[...\]
	if start+1 < len(runes) && runes[start] == '\\' && runes[start+1] == '[' {
		contentStart := start + 2
		for i := contentStart; i+1 < len(runes); i++ {
			if runes[i] == '\\' && runes[i+1] == ']' {
				return string(runes[contentStart:i]), string(runes[start : i+2]), i + 2
			}
		}
	}

	// Try $\n...\n$ (single dollar display math)
	if runes[start] == '$' && start+1 < len(runes) && runes[start+1] == '\n' {
		if start+1 < len(runes) && runes[start+1] == '$' {
			return "", "", start
		}
		contentStart := start + 2
		for i := contentStart; i < len(runes); i++ {
			if runes[i] == '\n' && i+1 < len(runes) && runes[i+1] == '$' {
				if i+2 >= len(runes) || runes[i+2] != '$' {
					return string(runes[contentStart:i]), string(runes[start : i+2]), i + 2
				}
			}
		}
	}

	return "", "", start
}

func matchInlineMath(runes []rune, start int) (content, raw string, end int) {
	if runes[start] != '$' {
		return "", "", start
	}
	if start+1 < len(runes) && runes[start+1] == '$' {
		return "", "", start
	}
	if start+1 < len(runes) && runes[start+1] == '\n' {
		return "", "", start
	}

	contentStart := start + 1
	i := contentStart

	// Check for simple currency: $digits followed by punctuation or space+conjunction
	if i < len(runes) && unicode.IsDigit(runes[i]) {
		j := i
		for j < len(runes) {
			r := runes[j]
			// Stop at non-digit/dot/comma
			if !unicode.IsDigit(r) && r != '.' && r != ',' {
				break
			}
			j++
		}

		// j now points to first non-digit char after the number
		if j > i { // We have some digits
			if j < len(runes) {
				nextChar := runes[j]
				// Currency patterns:
				// $100? $200! $50. $30,
				// $100 или
				// $100$ или (closed but followed by conjunction)
				if nextChar == '?' || nextChar == '!' || nextChar == '.' ||
					nextChar == ',' || nextChar == ';' || nextChar == ':' {
					// Currency followed by punctuation - don't parse as math
					return "", "", start
				}
				if nextChar == ' ' {
					remaining := string(runes[j:])
					lowerRemaining := strings.ToLower(remaining)
					if strings.HasPrefix(lowerRemaining, " или") ||
						strings.HasPrefix(lowerRemaining, " and ") ||
						strings.HasPrefix(lowerRemaining, " or ") ||
						strings.HasPrefix(lowerRemaining, " и ") {
						return "", "", start
					}
				}
				if nextChar == '$' {
					// $100$ - check what's after
					if j+1 < len(runes) {
						afterClose := runes[j+1]
						if afterClose == '?' || afterClose == '!' || afterClose == ' ' {
							// Likely currency
							return "", "", start
						}
					}
				}
			}
		}
	}

	// Normal parsing continues...
	for i < len(runes) {
		if runes[i] == '\\' && i+1 < len(runes) && runes[i+1] == '$' {
			if i+2 >= len(runes) {
				return string(runes[contentStart : i+2]), string(runes[start : i+2]), i + 2
			}
			afterEscaped := runes[i+2]
			if isFormulaTerminator(afterEscaped) {
				return string(runes[contentStart : i+2]), string(runes[start : i+2]), i + 2
			}
			if afterEscaped == ' ' {
				k := i + 3
				for k < len(runes) && runes[k] == ' ' {
					k++
				}
				if k >= len(runes) || !isMathContinuation(runes[k]) {
					return string(runes[contentStart : i+2]), string(runes[start : i+2]), i + 2
				}
			}
			i += 2
			continue
		}

		if runes[i] == '$' {
			content := string(runes[contentStart:i])
			if looksLikeCurrency(content) {
				return "", "", start
			}
			return content, string(runes[start : i+1]), i + 1
		}

		if runes[i] == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
			return "", "", start
		}

		i++
	}

	return "", "", start
}

func isFormulaTerminator(r rune) bool {
	return r == '.' || r == ',' || r == '!' || r == '?' ||
		r == ':' || r == ';' || r == ')' || r == ']' || r == '\n'
}

func isMathContinuation(r rune) bool {
	return r == '\\' || r == '+' || r == '-' || r == '*' || r == '/' ||
		r == '=' || r == '<' || r == '>' || r == '≈' || r == '≤' ||
		r == '≥' || r == '×' || r == '·' || (r >= '0' && r <= '9')
}

func processLatexContent(content string) string {
	content = convertTypographicQuotes(content)
	content = strings.ReplaceAll(content, `\$`, `$`)
	content = removeSpacingCommands(content)
	content = flattenBraces(content)
	content = removeTextWrappers(content)
	content = removeFontWrappers(content)
	content = convertFractions(content)
	content = convertSquareRoots(content)
	content = convertVectors(content)
	content = replaceLatexSymbols(content)
	content = convertSuperscripts(content)
	content = convertSubscripts(content)
	return content
}

func removeSpacingCommands(content string) string {
	content = strings.ReplaceAll(content, `\ `, ` `) // backslash-space
	content = strings.ReplaceAll(content, `\,`, ` `)
	content = strings.ReplaceAll(content, `\!`, ``)
	content = strings.ReplaceAll(content, `\:`, ``)
	content = strings.ReplaceAll(content, `\;`, ``)
	content = strings.ReplaceAll(content, `\quad`, ` `)
	content = strings.ReplaceAll(content, `\qquad`, ` `)
	return content
}

func convertTypographicQuotes(content string) string {
	var result strings.Builder
	runes := []rune(content)

	for i, r := range runes {
		if i > 0 && unicode.IsDigit(runes[i-1]) {
			switch r {
			case '"':
				result.WriteRune('″')
				continue
			case '\'':
				result.WriteRune('′')
				continue
			}
		}
		result.WriteRune(r)
	}
	return result.String()
}

func flattenBraces(content string) string {
	content = flattenConstruct(content, `\underbrace`, "_")
	content = flattenConstruct(content, `\overbrace`, "^")
	return content
}

func flattenConstruct(content, command, separator string) string {
	runes := []rune(content)
	cmdRunes := []rune(command)
	var result strings.Builder
	i := 0

	for i < len(runes) {
		if i+len(cmdRunes) < len(runes) && string(runes[i:i+len(cmdRunes)]) == command {
			cmdEnd := i + len(cmdRunes)
			if cmdEnd < len(runes) && runes[cmdEnd] == '{' {
				firstEnd := findMatchingBrace(runes, cmdEnd)
				if firstEnd != -1 {
					sepIdx := firstEnd + 1
					if sepIdx < len(runes) && string(runes[sepIdx:sepIdx+1]) == separator {
						if sepIdx+1 < len(runes) && runes[sepIdx+1] == '{' {
							secondEnd := findMatchingBrace(runes, sepIdx+1)
							if secondEnd != -1 {
								firstArg := string(runes[cmdEnd+1 : firstEnd])
								secondArg := string(runes[sepIdx+2 : secondEnd])
								firstArg = processLatexContent(firstArg)
								result.WriteString(firstArg)
								result.WriteString(" (")
								result.WriteString(secondArg)
								result.WriteString(")")
								i = secondEnd + 1
								continue
							}
						}
					}
				}
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func removeTextWrappers(content string) string {
	re := regexp.MustCompile(`\\text\{([^}]*)\}`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		inner := re.FindStringSubmatch(match)
		if len(inner) >= 2 {
			return strings.TrimSpace(inner[1])
		}
		return match
	})
}

func removeFontWrappers(content string) string {
	// Font modifiers: \mathbf{}, \mathit{}, \mathrm{}, \mathsf{}, \mathtt{}, \mathcal{}
	// These are LaTeX font style commands that we want to strip, keeping only the content
	fontCmds := []string{`mathbf`, `mathit`, `mathrm`, `mathsf`, `mathtt`, `mathcal`}

	for _, cmd := range fontCmds {
		pattern := regexp.MustCompile(`\\` + cmd + `\{([^}]*)\}`)
		content = pattern.ReplaceAllStringFunc(content, func(match string) string {
			inner := pattern.FindStringSubmatch(match)
			if len(inner) >= 2 {
				return strings.TrimSpace(inner[1])
			}
			return match
		})
	}
	return content
}

func convertFractions(content string) string {
	runes := []rune(content)
	var result strings.Builder
	i := 0

	for i < len(runes) {
		if i+5 < len(runes) && string(runes[i:i+6]) == `\frac{` {
			numEnd := findMatchingBrace(runes, i+5)
			if numEnd == -1 || numEnd+1 >= len(runes) || runes[numEnd+1] != '{' {
				result.WriteRune(runes[i])
				i++
				continue
			}

			denEnd := findMatchingBrace(runes, numEnd+1)
			if denEnd == -1 {
				result.WriteRune(runes[i])
				i++
				continue
			}

			num := processLatexContent(string(runes[i+6 : numEnd]))
			den := processLatexContent(string(runes[numEnd+2 : denEnd]))

			result.WriteString(num)
			result.WriteRune('/')
			result.WriteString(den)
			i = denEnd + 1
		} else {
			result.WriteRune(runes[i])
			i++
		}
	}
	return result.String()
}

func convertSquareRoots(content string) string {
	runes := []rune(content)
	var result strings.Builder
	i := 0

	for i < len(runes) {
		if i+5 < len(runes) && string(runes[i:i+6]) == `\sqrt{` {
			argEnd := findMatchingBrace(runes, i+5)
			if argEnd == -1 {
				result.WriteRune(runes[i])
				i++
				continue
			}

			arg := processLatexContent(string(runes[i+6 : argEnd]))
			needsParens := strings.ContainsAny(arg, " +-=<>/")

			result.WriteRune('√')
			if needsParens {
				result.WriteRune('(')
			}
			result.WriteString(arg)
			if needsParens {
				result.WriteRune(')')
			}
			i = argEnd + 1
		} else {
			result.WriteRune(runes[i])
			i++
		}
	}
	return result.String()
}

func convertVectors(content string) string {
	runes := []rune(content)
	var result strings.Builder
	i := 0

	for i < len(runes) {
		if i+4 < len(runes) && string(runes[i:i+5]) == `\vec{` {
			argEnd := findMatchingBrace(runes, i+4)
			if argEnd == -1 {
				result.WriteRune(runes[i])
				i++
				continue
			}

			arg := string(runes[i+5 : argEnd])
			// Process the argument first (might contain nested LaTeX)
			arg = processLatexContent(arg)
			// Add combining right arrow above (U+20D7)
			result.WriteString(arg)
			result.WriteRune('\u20D7') // combining character: ⃗
			i = argEnd + 1
		} else {
			result.WriteRune(runes[i])
			i++
		}
	}
	return result.String()
}

func findMatchingBrace(runes []rune, start int) int {
	if start >= len(runes) || runes[start] != '{' {
		return -1
	}
	depth := 0
	for i := start; i < len(runes); i++ {
		switch runes[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func replaceLatexSymbols(content string) string {
	for _, key := range sortedLatexKeys {
		content = strings.ReplaceAll(content, key, latexSymbols[key])
	}
	return content
}

func convertSuperscripts(content string) string {
	content = strings.ReplaceAll(content, `^\circ`, `°`)
	content = strings.ReplaceAll(content, `^{\circ}`, `°`)

	// Remove braces around single Unicode letters/words: ^{text} -> ^text
	// Using \pL for Unicode letter matching (Go's syntax, not \p{L})
	textBracedRe := regexp.MustCompile(`\^\{(\pL+)\}`)
	content = textBracedRe.ReplaceAllString(content, `^$1`)

	bracedRe := regexp.MustCompile(`\^\{([^}]+)\}`)
	content = bracedRe.ReplaceAllStringFunc(content, func(match string) string {
		inner := bracedRe.FindStringSubmatch(match)[1]
		return convertToSuperscript(inner)
	})

	singleRe := regexp.MustCompile(`\^([0-9+\-=()n])`)
	content = singleRe.ReplaceAllStringFunc(content, func(match string) string {
		char := rune(match[1])
		if rep, ok := superscriptMap[char]; ok {
			return string(rep)
		}
		return match
	})

	return content
}

func convertToSuperscript(s string) string {
	var result strings.Builder
	allConverted := true

	for _, r := range s {
		if rep, ok := superscriptMap[r]; ok {
			result.WriteRune(rep)
		} else {
			allConverted = false
			result.WriteRune(r)
		}
	}

	if allConverted {
		return result.String()
	}
	return "^" + s
}

func convertSubscripts(content string) string {
	// Remove braces around single Unicode letters/words: _{text} -> _text
	// This handles: _{cargo}, _{Δ}, _{греческое_слово}
	// Using \pL for Unicode letter matching (Go's syntax, not \p{L})
	textBracedRe := regexp.MustCompile(`_\{(\pL+)\}`)
	content = textBracedRe.ReplaceAllString(content, `_$1`)

	digitsBracedRe := regexp.MustCompile(`_\{(\d+)\}`)
	content = digitsBracedRe.ReplaceAllStringFunc(content, func(match string) string {
		inner := digitsBracedRe.FindStringSubmatch(match)[1]
		return convertToSubscript(inner)
	})

	singleRe := regexp.MustCompile(`_([0-9+\-=()])`)
	content = singleRe.ReplaceAllStringFunc(content, func(match string) string {
		char := rune(match[1])
		if rep, ok := subscriptMap[char]; ok {
			return string(rep)
		}
		return match
	})

	return content
}

func convertToSubscript(s string) string {
	var result strings.Builder
	for _, r := range s {
		if rep, ok := subscriptMap[r]; ok {
			result.WriteRune(rep)
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func looksLikeCurrency(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}

	r := []rune(s)
	if !unicode.IsDigit(r[0]) {
		return false
	}

	for _, c := range r {
		if unicode.IsDigit(c) || c == '.' || c == ',' {
			continue
		}
		return false
	}

	return true
}
