package markdown

import (
	"testing"
)

func TestGeometryFormulas(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "окружность (L = 2πR)",
			input:    "$L = 2 \\times \\pi \\times R \\approx 42 \\text{ см}$",
			expected: "L = 2 × π × R ≈ 42 см",
		},
		{
			name:     "температура (37°C)",
			input:    "$T_{water} = 37^\\circ C$",
			expected: "T_water = 37° C",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLatexToUnicode(tt.input)
			t.Logf("Input:    %s", tt.input)
			t.Logf("Expected: %s", tt.expected)
			t.Logf("Got:      %s", result)

			if result != tt.expected {
				t.Errorf("Mismatch!")
			}
		})
	}
}
