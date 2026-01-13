package markdown

import (
	"strings"
	"testing"
)

func TestOpenRouterResponseFormulas(t *testing.T) {
	// Test formulas from real OpenRouter response
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "kitchen formula",
			input:    "$2 \\text{ eggs} \\times 3 \\text{ people} = 6 \\text{ eggs}$",
			expected: "2 eggs × 3 people = 6 eggs",
		},
		{
			name:     "fraction formula",
			input:    "$Formula: \\frac{150 \\text{ г}}{300 \\text{ мл}} \\times 100\\% = 50\\% \\text{ концентрации}$",
			expected: "Formula: 150 г/300 мл × 100% = 50% концентрации",
		},
		{
			name:     "TV screen with inches",
			input:    "$Screen = 65\" \\text{ OLED Panel}$.",
			expected: "Screen = 65″ OLED Panel.",
		},
		{
			name:     "area formula",
			input:    "$S = 3.5 \\text{ м} \\times 4.2 \\text{ м} = 14.7 \\text{ м}^2$.",
			expected: "S = 3.5 м × 4.2 м = 14.7 м².",
		},
		{
			name:     "budget with approx",
			input:    "$Cost \\approx 5000 \\text{ rub} \\pm 10\\%$.",
			expected: "Cost ≈ 5000 rub ± 10%.",
		},
		{
			name:     "logic with escaped dollar - THE BUG!",
			input:    "$If: Balance \\ge 1000\\$ \\rightarrow \\text{Buy Nvidia}$.",
			expected: "If: Balance ≥ 1000$ → Buy Nvidia.",
		},
		{
			name:     "panic condition",
			input:    "$If: Balance < 0 \\rightarrow \\text{Panic}$.",
			expected: "If: Balance < 0 → Panic.",
		},
		{
			name:     "geometry with pi",
			input:    "$L = 2 \\times \\pi \\times R \\approx 42 \\text{ см}$.",
			expected: "L = 2 × π × R ≈ 42 см.",
		},
		{
			name:     "temperature with degrees",
			input:    "$T_{water} = 37^\\circ C$.",
			expected: "T_water = 37° C.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLatexToUnicode(tt.input)
			if result != tt.expected {
				t.Errorf("Input:    %q\nExpected: %q\nGot:      %q", tt.input, tt.expected, result)
			}
			// Also check no raw LaTeX remains
			if strings.Contains(result, "\\times") && !strings.Contains(tt.input, "Тест") {
				t.Errorf("Result still contains \\times: %q", result)
			}
			if strings.Contains(result, "\\text") && !strings.Contains(tt.input, "Тест") {
				t.Errorf("Result still contains \\text: %q", result)
			}
			if strings.Contains(result, "\\$") {
				t.Errorf("Result still contains \\$: %q", result)
			}
		})
	}
}
