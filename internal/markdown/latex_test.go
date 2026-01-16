package markdown

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConvertLatexToUnicode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple multiplication",
			input:    "$5 \\times 3 = 15$",
			expected: "5 × 3 = 15",
		},
		{
			name:     "with text wrapper",
			input:    "$500 \\text{ г} \\times 4 \\text{ недели} = 2.0 \\text{ кг}$",
			expected: "500 г × 4 недели = 2.0 кг",
		},
		{
			name:     "fraction",
			input:    "$\\frac{1}{2}$",
			expected: "1/2",
		},
		{
			name:     "square root",
			input:    "$\\sqrt{16} = 4$",
			expected: "√16 = 4",
		},
		{
			name:     "comparison symbols",
			input:    "$x \\leq 5$ and $y \\geq 10$",
			expected: "x ≤ 5 and y ≥ 10",
		},
		{
			name:     "short comparison forms",
			input:    "$x \\le 5$ and $y \\ge 10$",
			expected: "x ≤ 5 and y ≥ 10",
		},
		{
			name:     "not equal",
			input:    "$a \\neq b$",
			expected: "a ≠ b",
		},
		{
			name:     "not equal short form",
			input:    "$a \\ne b$",
			expected: "a ≠ b",
		},
		{
			name:     "plus minus",
			input:    "$\\pm 5$",
			expected: "± 5",
		},
		{
			name:     "infinity",
			input:    "$\\infty$",
			expected: "∞",
		},
		{
			name:     "approximation",
			input:    "$\\approx 3.14$",
			expected: "≈ 3.14",
		},
		{
			name:     "greek letters",
			input:    "$\\alpha, \\beta, \\gamma, \\pi$",
			expected: "α, β, γ, π",
		},
		{
			name:     "sum and product",
			input:    "$\\sum$ and $\\prod$",
			expected: "Σ and Π",
		},
		{
			name:     "arrows",
			input:    "$a \\rightarrow b$",
			expected: "a → b",
		},
		{
			name:     "double arrow",
			input:    "$a \\Leftrightarrow b$",
			expected: "a ⇔ b",
		},
		{
			name:     "integral",
			input:    "$\\int f(x) dx$",
			expected: "∫ f(x) dx",
		},
		{
			name:     "display math with double dollars",
			input:    "$$\\sum_{i=1}^{n} i = \\frac{n(n+1)}{2}$$",
			expected: "Σ_{i=1}ⁿ i = n(n+1)/2", // Note: {i=1} preserved because it contains operators
		},
		{
			name:     "display math with brackets",
			input:    "\\[\\int_0^1 x^2 dx\\]",
			expected: "∫₀¹ x² dx",
		},
		{
			name:     "no LaTeX - plain text",
			input:    "Just regular text",
			expected: "Just regular text",
		},
		{
			name:     "mixed content",
			input:    "Formula: $a + b = c$ and more text",
			expected: "Formula: a + b = c and more text",
		},
		{
			name:     "multiple formulas",
			input:    "$x + y = z$ and $a \\times b = c$",
			expected: "x + y = z and a × b = c",
		},
		{
			name:     "complex formula with units",
			input:    "Energy: $E = mc^2$ joules",
			expected: "Energy: E = mc² joules",
		},
		{
			name:     "chemical formula",
			input:    "Water: $H_2O$",
			expected: "Water: H₂O",
		},
		{
			name:     "nested fraction",
			input:    "$\\frac{\\frac{a}{b}}{c}$",
			expected: "a/b/c",
		},
		{
			name:     "dot product",
			input:    "$a \\cdot b$",
			expected: "a · b",
		},
		{
			name:     "set membership",
			input:    "$x \\in S$ and $y \\notin T$",
			expected: "x ∈ S and y ∉ T",
		},
		{
			name:     "therefore and because",
			input:    "$a \\therefore b \\because c$",
			expected: "a ∴ b ∵ c",
		},
		{
			name:     "square root with fraction",
			input:    "$\\sqrt{\\frac{a^2 + b^2}{c}}$",
			expected: "√(a² + b²/c)", // Now with proper parentheses
		},
		{
			name:     "escape dollar signs should not be treated as LaTeX",
			input:    "Price is $5.99",
			expected: "Price is $5.99",
		},
		{
			name:     "number with decimal followed by dollar",
			input:    "It costs $3.50 and $2.00",
			expected: "It costs $3.50 and $2.00",
		},
		{
			name:     "partial derivative",
			input:    "$\\partial f / \\partial x$",
			expected: "∂ f / ∂ x",
		},
		{
			name:     "nabla operator",
			input:    "$\\nabla \\cdot \\vec{F}$",
			expected: "∇ · F⃗",
		},
		{
			name:     "empty text wrapper",
			input:    "$\\text{}$",
			expected: "",
		},
		{
			name:     "multiple superscripts",
			input:    "$x^2 + y^3 = z^4$",
			expected: "x² + y³ = z⁴",
		},
		{
			name:     "negative superscript",
			input:    "$x^{-1}$",
			expected: "x⁻¹",
		},
		{
			name:     "complex subscript",
			input:    "$H_2O$ and $CO_2$",
			expected: "H₂O and CO₂",
		},
		{
			name:     "mixed super and subscript",
			input:    "$x_0^2 + x_1^2$",
			expected: "x₀² + x₁²",
		},
		{
			name:     "triangle symbol",
			input:    "$\\triangle ABC$",
			expected: "△ ABC",
		},
		{
			name:     "angle symbol",
			input:    "$\\angle ABC = 90^\\circ$",
			expected: "∠ ABC = 90°",
		},
		{
			name:     "parallel lines",
			input:    "$AB \\parallel CD$",
			expected: "AB ∥ CD",
		},
		{
			name:     "perpendicular lines",
			input:    "$AB \\perp CD$",
			expected: "AB ⊥ CD",
		},
		{
			name:     "vector notation",
			input:    "$\\vec{v} + \\vec{w}$",
			expected: "v⃗ + w⃗",
		},
		{
			name:     "vector with subscript",
			input:    "$\\vec{v}_1$",
			expected: "v⃗₁",
		},
		{
			name:     "eta efficiency",
			input:    "$\\eta = 0.9$",
			expected: "η = 0.9",
		},
		{
			name:     "omega resistance",
			input:    "$R = 0.3 \\ \\Omega$",
			expected: "R = 0.3  Ω",
		},
		{
			name:     "mu microfarad",
			input:    "$C = 60 \\mu\\text{F}$",
			expected: "C = 60 μF",
		},
		{
			name:     "all greek letters",
			input:    "$\\alpha \\beta \\gamma \\delta \\epsilon \\zeta \\eta \\theta \\iota \\kappa \\lambda \\mu \\nu \\xi \\pi \\rho \\sigma \\tau \\upsilon \\phi \\chi \\psi \\omega$",
			expected: "α β γ δ ε ζ η θ ι κ λ μ ν ξ π ρ σ τ υ φ χ ψ ω",
		},
		{
			name:     "subscript with cyrillic",
			input:    "$\\frac{\\text{Вес}_{груза}}{1000}$",
			expected: "Вес_груза/1000",
		},
		{
			name:     "subscript with greek unicode",
			input:    "$I_{\\Delta}$",
			expected: "I_Δ",
		},
		{
			name:     "mixed subscripts",
			input:    "$T_{i=1} + T_{груз} + M_{Δ}$",
			expected: "T_{i=1} + T_груз + M_Δ",
		},
		{
			name:     "superscript with cyrillic",
			input:    "$x^{показатель}$",
			expected: "x^показатель",
		},
		{
			name:     "complex subscript with unicode and space",
			input:    "$\\text{Масса}_{\\Delta m}$",
			expected: "Масса_{Δ m}",
		},
		{
			name:     "subscript with single unicode letter",
			input:    "$F_{\\mu}$",
			expected: "F_μ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLatexToUnicode(tt.input)
			assert.Equal(t, tt.expected, result, "Conversion should match expected output")
		})
	}
}

func TestRemoveTextWrappers(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text wrapper",
			input:    `\text{hello}`,
			expected: "hello",
		},
		{
			name:     "text with spaces",
			input:    `\text{hello world}`,
			expected: "hello world",
		},
		{
			name:     "text with Cyrillic",
			input:    `\text{г недели}`,
			expected: "г недели",
		},
		{
			name:     "multiple text wrappers",
			input:    `\text{a} + \text{b}`,
			expected: "a + b",
		},
		{
			name:     "no text wrapper",
			input:    `just text`,
			expected: "just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeTextWrappers(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertFractions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple fraction",
			input:    `\frac{1}{2}`,
			expected: "1/2",
		},
		{
			name:     "fraction with expressions",
			input:    `\frac{a+b}{c-d}`,
			expected: "a+b/c-d",
		},
		{
			name:     "nested fraction",
			input:    `\frac{\frac{a}{b}}{c}`,
			expected: "a/b/c",
		},
		{
			name:     "no fraction",
			input:    `just text`,
			expected: "just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertFractions(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertSquareRoots(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple square root",
			input:    `\sqrt{16}`,
			expected: "√16",
		},
		{
			name:     "square root with expression",
			input:    `\sqrt{a^2 + b^2}`,
			expected: "√(a² + b²)",
		},
		{
			name:     "no square root",
			input:    `just text`,
			expected: "just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertSquareRoots(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestReplaceLatexSymbols(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "times symbol",
			input:    `a \times b`,
			expected: "a × b",
		},
		{
			name:     "comparison symbols",
			input:    `\leq and \geq`,
			expected: "≤ and ≥",
		},
		{
			name:     "greek letters",
			input:    `\alpha + \beta`,
			expected: "α + β",
		},
		{
			name:     "arrows",
			input:    `\rightarrow`,
			expected: "→",
		},
		{
			name:     "unknown command stays",
			input:    `\unknowncommand`,
			expected: `\unknowncommand`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := replaceLatexSymbols(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertSuperscripts(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single digit superscript",
			input:    `x^2`,
			expected: "x²",
		},
		{
			name:     "zero superscript",
			input:    `x^0`,
			expected: "x⁰",
		},
		{
			name:     "plus superscript",
			input:    `n^+`,
			expected: "n⁺",
		},
		{
			name:     "minus superscript",
			input:    `x^-`,
			expected: "x⁻",
		},
		{
			name:     "no superscript",
			input:    `just text`,
			expected: "just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertSuperscripts(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertSubscripts(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single digit subscript",
			input:    `H_2`,
			expected: "H₂",
		},
		{
			name:     "zero subscript",
			input:    `x_0`,
			expected: "x₀",
		},
		{
			name:     "plus subscript",
			input:    `n_+`,
			expected: "n₊",
		},
		{
			name:     "minus subscript",
			input:    `x_-`,
			expected: "x₋",
		},
		{
			name:     "no subscript",
			input:    `just text`,
			expected: "just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertSubscripts(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestToHTMLWithLatex(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "inline formula in markdown",
			input:    "The result is $5 \\times 3 = 15$",
			expected: "The result is 5 × 3 = 15",
		},
		{
			name:     "bold text with formula",
			input:    "**Formula:** $E = mc^2$",
			expected: "<b>Formula:</b> E = mc²",
		},
		{
			name:     "mixed formatting",
			input:    "Calculate $\\frac{1}{2}$ * $\\pi$",
			expected: "Calculate 1/2 * π",
		},
		{
			name:     "bug-05 block 1 - code with bitwise shifts",
			input:    "```cpp\nint seed_0 = 0xA5;\nint key = (seed_0 << 2) | (seed_0 >> 1);\n```",
			expected: "<pre><code class=\"language-cpp\">int seed_0 = 0xA5;\nint key = (seed_0 &lt;&lt; 2) | (seed_0 &gt;&gt; 1);\n</code></pre>",
		},
		{
			name:     "bug-05 block 2 - financial with escaped dollar",
			input:    "$C_{init} = 10\\,000\\$",
			expected: "C_init = 10 000$",
		},
		{
			name:     "bug-06 block 1 - Rate with escaped dollar",
			input:    "$Rate_{min} = 50\\$",
			expected: "Rate_min = 50$",
		},
		{
			name:     "bug-06 block 1 - Limit with ge and escaped dollar",
			input:    "$Limit_{max} \\ge 1000\\$",
			expected: "Limit_max ≥ 1000$",
		},
		{
			name:     "bug-06 block 4 - approx with escaped dollar",
			input:    "$\\approx 600\\$",
			expected: "≈ 600$",
		},
		{
			name:     "bug-06 block 2 - display math with escaped dollar",
			input:    "$$P_{init} = 500\\,000\\$$",
			expected: "P_init = 500 000$",
		},
		{
			name:     "bug-06 full - text with display and inline math",
			input:    "Где $P_{init} = 500\\,000\\$.",
			expected: "Где P_init = 500 000$.",
		},
		{
			name:     "bug-05 block 3 - mixed inline math (separate formulas)",
			input:    "Variable $x_0$ in formula, but `var_0` in code",
			expected: "Variable x₀ in formula, but <code>var_0</code> in code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLooksLikeCurrency(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"simple dollar", "3.50", true},
		{"integer dollar", "100", true},
		{"zero", "0", true},
		{"with leading space", " 3.50", true},
		{"not currency - letters", "a3.50", false},
		{"not currency - starts with letter", "price", false},
		{"not currency - starts with symbol", "@price", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := looksLikeCurrency(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDisplayMathMultiline(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single dollar display math",
			input:    "$\nf(x) = x^2\n$",
			expected: "f(x) = x²",
		},
		{
			name:     "multiline formula with single dollar",
			input:    "$\na + b = c\nd + e = f\n$",
			expected: "a + b = c\nd + e = f",
		},
		{
			name:     "double dollar display math",
			input:    "$$f(x) = x^2$$",
			expected: "f(x) = x²",
		},
		{
			name:     "double dollar multiline",
			input:    "$$a + b\nc + d$$",
			expected: "a + b\nc + d",
		},
		{
			name:     "bracket display math",
			input:    "\\[x^2 + y^2\\]",
			expected: "x² + y²",
		},
		{
			name:     "bracket display multiline",
			input:    "\\[a + b\nc + d\\]",
			expected: "a + b\nc + d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLatexToUnicode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormulaExamplesFromLog(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string // check that result contains these strings
	}{
		{
			name:     "dots",
			input:    "$1 + 2 + \\dots + n$",
			contains: []string{"1", "2", "…", "n"},
		},
		{
			name:     "nested roots",
			input:    "$\\sqrt{1 + 2\\sqrt{1 + 3\\sqrt{1 + 4\\sqrt{1 + \\dots}}} = 3$",
			contains: []string{"√", "…", "3"},
		},
		{
			name:     "integral",
			input:    "$\\int_{-\\infty}^{\\infty} e^{-x^2} \\,dx = \\sqrt{\\pi}$",
			contains: []string{"∫", "∞", "√", "π"},
		},
		{
			name:     "infinity limits",
			input:    "$$\\int_{-\\infty}^{\\infty} e^{-x^2} \\,dx = \\sqrt{\\pi}$$",
			contains: []string{"∫", "∞", "√", "π"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			for _, expected := range tt.contains {
				assert.Contains(t, result, expected)
			}
		})
	}
}

func TestThinSpace(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "thin space as thousands separator",
			input:    "$45\\,000 + 15\\,000$",
			expected: "45 000 + 15 000", // thin space becomes regular space for readability
		},
		{
			name:     "thin space in plain text (no dollar delimiters)",
			input:    "Price: 100\\,000 rub",
			expected: "Price: 100\\,000 rub", // unchanged - not inside $...$
		},
		{
			name:     "multiple thin spaces",
			input:    "$1\\,000\\,000$",
			expected: "1 000 000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPlainTextWithLatexCommands(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "thin space in plain text (no dollar signs)",
			input:    "Price: 100\\,000 rub",
			expected: "Price: 100\\,000 rub", // unchanged - no $...$ delimiters
		},
		{
			name:     "thin space with dollar signs",
			input:    "$100\\,000 rub$",
			expected: "100 000 rub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormula1Examples(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "diapers arithmetic",
			input:    "$8 \\text{ шт} \\times 30 \\text{ дней} = 240 \\text{ шт/мес}.$",
			expected: "8 шт × 30 дней = 240 шт/мес.",
		},
		{
			name:     "feeding volume",
			input:    "$120 \\text{ мл} / 2 \\text{ часа} = 60 \\text{ мл/час}.$",
			expected: "120 мл / 2 часа = 60 мл/час.",
		},
		{
			name:     "budget with thin spaces and approx",
			input:    "$45\\,000 \\text{ руб} + 15\\,000 \\text{ руб} \\approx 60 \\text{ к}.$",
			expected: "45 000 руб + 15 000 руб ≈ 60 к.",
		},
		{
			name:     "weight calculation",
			input:    "$3200 \\text{ г} + (30 \\text{ г} \\times 7 \\text{ дней}) = 3410 \\text{ г}.$",
			expected: "3200 г + (30 г × 7 дней) = 3410 г.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormula1SmokeTest(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "medicine dosage (no LaTeX)",
			input:    "2.5мл + 2.5мл = 5мл.",
			expected: "2.5мл + 2.5мл = 5мл.", // unchanged - no $...$
		},
		{
			name:     "budget with dots and leq",
			input:    "$1500 + 3000 + \\dots + 500 \\leq 10000 руб.$",
			expected: "1500 + 3000 + … + 500 ≤ 10000 руб.",
		},
		{
			name:     "sleep with fraction and subscript",
			input:    "$T_{sleep} \\approx 1/3 \\text{ суток} \\approx 8 \\text{ ч}.$",
			expected: "T_sleep ≈ 1/3 суток ≈ 8 ч.",
		},
		{
			name:     "area with pi and squared",
			input:    "$S = \\pi r^2 \\approx 3.14 \\times (1.5)^2 \\text{ м}^2.$",
			expected: "S = π r² ≈ 3.14 × (1.5)² м².",
		},
		{
			name:     "diapers supply combo",
			input:    "$P_{total} = 80 \\text{ шт} - (6 \\text{ шт} \\times 5 \\text{ дней}) = 50 \\text{ шт}.$",
			expected: "P_total = 80 шт - (6 шт × 5 дней) = 50 шт.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormula1FinOps(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "DTI with implies and percent",
			input:    "$DTI = 108\\,335 \\text{ руб} / 520\\,000 \\text{ руб} \\approx 0.21 \\implies 21\\%$",
			expected: "DTI = 108 335 руб / 520 000 руб ≈ 0.21 ⟹ 21%",
		},
		{
			name:     "bonus calculation with text subscript",
			input:    "$Bonus_{net} = (520\\,000 \\times 4) - 13\\% \\approx 1\\,809\\,600 \\text{ руб}$",
			expected: "Bonus_net = (520 000 × 4) - 13% ≈ 1 809 600 руб",
		},
		{
			name:     "real rate calculation",
			input:    "$R_{real} = R_{bank} - I_{offic} = 21\\% - 8.5\\% = 12.5\\% \\text{ годовых}$",
			expected: "R_real = R_bank - I_offic = 21% - 8.5% = 12.5% годовых",
		},
		{
			name:     "burn rate daily",
			input:    "$Burn_{daily} = 2500 \\text{ руб} / 80 \\text{ шт} \\times 8 \\text{ шт} + 300 \\text{ руб} \\approx 550 \\text{ руб/день}$",
			expected: "Burn_daily = 2500 руб / 80 шт × 8 шт + 300 руб ≈ 550 руб/день",
		},
		{
			name:     "runway calculation",
			input:    "$T_{runway} = Cash_{reserve} / Expense_{month} = 2\\,000\\,000 / 150\\,000 \\approx 13.3 \\text{ мес}$",
			expected: "T_runway = Cash_reserve / Expense_month = 2 000 000 / 150 000 ≈ 13.3 мес",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTrigonometricFunctions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "cos in text",
			input:    `$(\cos \phi \approx 1)$`,
			expected: "(cos φ ≈ 1)",
		},
		{
			name:     "sin and tan",
			input:    `$\sin(\alpha) + \tan(\beta)$`,
			expected: `sin(α) + tg(β)`,
		},
		{
			name:     "log functions",
			input:    `$\ln(x) + \log_{10}(x)$`,
			expected: `ln(x) + log₁₀(x)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSingleVariableInMath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single uppercase letter",
			input:    `Мощность ($P$):`,
			expected: "Мощность (P):",
		},
		{
			name:     "single lowercase letter",
			input:    `$x$ - переменная`,
			expected: "x - переменная",
		},
		{
			name:     "currency should not convert",
			input:    `Price: $3.50`,
			expected: "Price: $3.50",
		},
		{
			name:     "currency with decimal",
			input:    `Cost: $100.00`,
			expected: "Cost: $100.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := ToHTML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTypographicQuotes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "inches after digit",
			input:    `1" pipe`,
			expected: `1″ pipe`,
		},
		{
			name:     "feet and inches",
			input:    `6' 4" tall`,
			expected: `6′ 4″ tall`,
		},
		{
			name:     "quotes in formula",
			input:    `$1"$ diameter`,
			expected: `1″ diameter`, // Now processed since 1″ looks like math content
		},
		{
			name:     "text quotes unchanged",
			input:    `he said "hello"`,
			expected: `he said "hello"`,
		},
		{
			name:     "single prime for minutes",
			input:    `$30'$ angle`,
			expected: `30′ angle`, // Now processed since 30′ looks like math content
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test convertLatexToUnicode directly, not ToHTML (which adds HTML entities)
			result := convertLatexToUnicode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCodeBlocksProtected(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "LaTeX in code block unchanged",
			input:    "Text ```$1\" \\to 1\"``` more",
			expected: "Text ```$1\" \\to 1\"``` more",
		},
		{
			name:     "LaTeX outside code block processed",
			input:    "$1\"$ ```code``` $\\to$",
			expected: "1″ ```code``` →",
		},
		{
			name:     "multi-line code block",
			input:    "Formula: $x^2$\n```\n$\\frac{1}{2}$\n```\nDone",
			expected: "Formula: x²\n```\n$\\frac{1}{2}$\n```\nDone",
		},
		{
			name:     "inline code protected",
			input:    "Use `$x$` for variables",
			expected: "Use `$x$` for variables",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test convertLatexToUnicode directly
			result := convertLatexToUnicode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFlattenBraces(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "underbrace with text",
			input:    `$\underbrace{2 \times 25}_{зазоры}$`,
			expected: `2 × 25 (зазоры)`,
		},
		{
			name:     "overbrace with text",
			input:    `$\overbrace{a + b}^{sum}$`,
			expected: `a + b (sum)`,
		},
		{
			name:     "underbrace in complex formula",
			input:    `$L = 1200 + \underbrace{2 \times 25}_{зазоры} = 1250$`,
			expected: `L = 1200 + 2 × 25 (зазоры) = 1250`,
		},
		{
			name:     "nested with fraction",
			input:    `$\underbrace{\frac{1}{2}}_{half}$`,
			expected: `1/2 (half)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLatexToUnicode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEscapedDollarInFormula(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "escaped dollar in formula",
			input:    "$Balance \\ge 1000\\$ \\rightarrow \\text{Buy Nvidia}$",
			expected: "Balance ≥ 1000$ → Buy Nvidia",
		},
		{
			name:     "multiple escaped dollars",
			input:    "$Price_1 = \\$100, Price_2 = \\$200$",
			expected: "Price₁ = $100, Price₂ = $200",
		},
		{
			name:     "currency comparison",
			input:    "$If: cost \\le \\$50 \\rightarrow buy$",
			expected: "If: cost ≤ $50 → buy",
		},
		{
			name:     "finops alert with implies - CRITICAL from bug-02.json",
			input:    "$If \\Delta Balance < 100\\$ \\implies \\text{Critical Alert!}$",
			expected: "If Δ Balance < 100$ ⟹ Critical Alert!",
		},
		{
			name:     "block 1 from bug-02.json - GPU cost limit",
			input:    "$$Cost_{gpu} \\le 1200\\$ \\text{ (RTX 5090)}$$",
			expected: "Cost_gpu ≤ 1200$ (RTX 5090)",
		},
		{
			name:     "block 3 from bug-02.json - SaaS subscription with multiple dollars",
			input:    "$$P_{total} = 5 \\text{ users} \\times 20\\$ = 100\\$ \\text{ / month}$$",
			expected: "P_total = 5 users × 20$ = 100$ / month",
		},
		{
			name:     "bug-03 block 1 - base salary with thin space",
			input:    "$$Base = 5\\,200\\$ \\text{ (Gross)}$$",
			expected: "Base = 5 200$ (Gross)",
		},
		{
			name:     "bug-03 block 2 - Delta with escaped dollar (CRITICAL)",
			input:    "$\\Delta Tax > 50\\$ \\implies \\text{Пересчет!}$",
			expected: "Δ Tax > 50$ ⟹ Пересчет!",
		},
		{
			name:     "bug-03 block 3 - multiple dollars with percent",
			input:    "$$Net = 100\\$ - 13\\% \\approx 87\\$$",
			expected: "Net = 100$ - 13% ≈ 87$",
		},
		{
			name:     "bug-04 block 1 - NAV with escaped dollars and thin spaces",
			input:    "$$NAV = 10\\,000\\$ - 15\\% \\text{ fees} \\approx 8\\,500\\$$",
			expected: "NAV = 10 000$ - 15% fees ≈ 8 500$",
		},
		{
			name:     "bug-04 block 1 - inline NAV comparison",
			input:    "$NAV < 9\\,000\\$$",
			expected: "NAV < 9 000$",
		},
		{
			name:     "bug-04 block 2 - Call option with escaped dollar",
			input:    "$C = \\$5.50$",
			expected: "C = $5.50",
		},
		{
			name:     "bug-04 block 2 - Put option with escaped dollar",
			input:    "$P = \\$4.20$",
			expected: "P = $4.20",
		},
		{
			name:     "bug-04 block 2 - Spread with Delta and escaped dollar",
			input:    "$\\Delta = \\$1.30$",
			expected: "Δ = $1.30",
		},
		{
			name:     "bug-04 block 4 - Sum with multiple escaped dollars",
			input:    "$$Total = \\sum_{i=1}^3 P_i = \\$100 + \\$200 + \\$300 = \\$600$$",
			expected: "Total = Σ_{i=1}³ P_i = $100 + $200 + $300 = $600",
		},
		{
			name:     "bug-05 block 1 - code with bitwise shifts protected",
			input:    "int seed_0 = 0xA5; int key = (seed_0 << 2);",
			expected: "int seed_0 = 0xA5; int key = (seed_0 << 2);",
		},
		{
			name:     "bug-05 block 2 - financial with subscripts",
			input:    "$C_{init} = 10\\,000\\$",
			expected: "C_init = 10 000$",
		},
		{
			name:     "bug-05 block 3 - variable with subscript",
			input:    "$x_0$ in formula",
			expected: "x₀ in formula",
		},
		// Bug-06 tests
		{
			name:     "bug-06 Rate with escaped dollar",
			input:    "$Rate_{min} = 50\\$",
			expected: "Rate_min = 50$",
		},
		{
			name:     "bug-06 Limit with ge and escaped dollar",
			input:    "$Limit_{max} \\ge 1000\\$",
			expected: "Limit_max ≥ 1000$",
		},
		{
			name:     "bug-06 approx with escaped dollar",
			input:    "$\\approx 600\\$",
			expected: "≈ 600$",
		},
		{
			name:     "bug-06 display math with escaped dollar",
			input:    "$$P_{init} = 500\\,000\\$$",
			expected: "P_init = 500 000$",
		},
		// Bug-07 tests
		{
			name:     "bug-07 collision le with left",
			input:    "$\\text{Divergence} = \\left( d \\le 0.5 \\text{ mm} \\right)$",
			expected: "Divergence = ( d ≤ 0.5 mm )",
		},
		{
			name:     "bug-07 arrows xrightarrow",
			input:    "$\\text{Склад} \\xrightarrow{} \\text{Цех} \\rightarrow \\text{Монтаж}$",
			expected: "Склад →{} Цех → Монтаж",
		},
		{
			name:     "bug-07 table geometry left right pm",
			input:    "$L = \\left( 10 \\pm 0.1 \\right) \\text{ m}$",
			expected: "L = ( 10 ± 0.1 ) m",
		},
		{
			name:     "bug-07 table budget to and escaped dollar",
			input:    "$Cost \\to 500\\$$",
			expected: "Cost → 500$",
		},
		{
			name:     "bug-07 left with le no collision",
			input:    "$$\\left( x \\le 5 \\right)$$",
			expected: "( x ≤ 5 )",
		},
		{
			name:     "bug-07 left with ge no collision",
			input:    "$$\\left( y \\ge 10 \\right)$$",
			expected: "( y ≥ 10 )",
		},
		{
			name:     "bug-07 nested left right with le",
			input:    "$$\\left[ d \\le 0.5 \\text{ mm} \\right]$$",
			expected: "[ d ≤ 0.5 mm ]",
		},
		// Bug-08 tests
		{
			name:     "bug-08 formula ending with escaped dollar",
			input:    "$Max \\le 100\\$",
			expected: "Max ≤ 100$",
		},
		{
			name:     "bug-08 formula with escaped dollar followed by period",
			input:    "Cost is $Price = \\$100$.",
			expected: "Cost is Price = $100.",
		},
		{
			name:     "bug-08 formula ending with escaped dollar and punctuation",
			input:    "Limit: $Max \\le 100\\$.",
			expected: "Limit: Max ≤ 100$.",
		},
		{
			name:     "bug-08 multiple escaped dollars in formula",
			input:    "$A = \\$1$ and $B = \\$2$",
			expected: "A = $1 and B = $2",
		},
		{
			name:     "bug-08 mixed escaped and non-escaped dollars",
			input:    "$Total = \\$50 + \\$30 = \\$80$$",
			expected: "Total = $50 + $30 = $80$",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLatexToUnicode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
