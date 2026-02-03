package ui

import (
	"html/template"
	"testing"

	"github.com/runixer/laplaced/internal/testutil"
)

func TestPrettyJSON(t *testing.T) {
	funcMap := GetFuncMap()
	prettyJSONFunc, ok := funcMap["prettyJSON"].(func(string) string)
	if !ok {
		t.Fatal("prettyJSON function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:  "valid JSON object",
			input: `{"name":"test","value":123}`,
			expected: `{
  "name": "test",
  "value": 123
}`,
		},
		{
			name:  "valid JSON array",
			input: `[{"id":1},{"id":2}]`,
			expected: `[
  {
    "id": 1
  },
  {
    "id": 2
  }
]`,
		},
		{
			name:     "invalid JSON",
			input:    `{invalid json}`,
			expected: `{invalid json}`,
		},
		{
			name:     "empty string",
			input:    ``,
			expected: ``,
		},
		{
			name:  "nested JSON",
			input: `{"outer":{"inner":"value"}}`,
			expected: `{
  "outer": {
    "inner": "value"
  }
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := prettyJSONFunc(tt.input)
			if result != tt.expected {
				t.Errorf("prettyJSON() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestRenderContent(t *testing.T) {
	funcMap := GetFuncMap()
	renderContentFunc, ok := funcMap["renderContent"].(func(interface{}) string)
	if !ok {
		t.Fatal("renderContent function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "string content",
			input:    "Hello, World!",
			expected: "Hello, World!",
		},
		{
			name: "text parts",
			input: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "First part",
				},
				map[string]interface{}{
					"type": "text",
					"text": " Second part",
				},
			},
			expected: "First part Second part",
		},
		{
			name: "image part",
			input: []interface{}{
				map[string]interface{}{
					"type": "image_url",
				},
			},
			expected: "[Image]",
		},
		{
			name: "mixed parts",
			input: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Text before image ",
				},
				map[string]interface{}{
					"type": "image_url",
				},
			},
			expected: "Text before image [Image]",
		},
		{
			name:     "number",
			input:    42,
			expected: "42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := renderContentFunc(tt.input)
			if result != tt.expected {
				t.Errorf("renderContent() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestIsTopicList(t *testing.T) {
	funcMap := GetFuncMap()
	isTopicListFunc, ok := funcMap["isTopicList"].(func(interface{}) bool)
	if !ok {
		t.Fatal("isTopicList function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    interface{}
		expected bool
	}{
		{
			name:     "not a topic list",
			input:    "string",
			expected: false,
		},
		{
			name:     "nil",
			input:    nil,
			expected: false,
		},
		{
			name:     "empty slice",
			input:    []interface{}{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTopicListFunc(tt.input)
			if result != tt.expected {
				t.Errorf("isTopicList() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMathFunctions(t *testing.T) {
	funcMap := GetFuncMap()

	t.Run("add", func(t *testing.T) {
		addFunc, ok := funcMap["add"].(func(int, int) int)
		if !ok {
			t.Fatal("add function not found in FuncMap")
		}
		if result := addFunc(2, 3); result != 5 {
			t.Errorf("add(2, 3) = %d, want 5", result)
		}
	})

	t.Run("sub", func(t *testing.T) {
		subFunc, ok := funcMap["sub"].(func(int, int) int)
		if !ok {
			t.Fatal("sub function not found in FuncMap")
		}
		if result := subFunc(5, 3); result != 2 {
			t.Errorf("sub(5, 3) = %d, want 2", result)
		}
	})

	t.Run("add64", func(t *testing.T) {
		add64Func, ok := funcMap["add64"].(func(int64, int64) int64)
		if !ok {
			t.Fatal("add64 function not found in FuncMap")
		}
		if result := add64Func(2, 3); result != 5 {
			t.Errorf("add64(2, 3) = %d, want 5", result)
		}
	})

	t.Run("sub64", func(t *testing.T) {
		sub64Func, ok := funcMap["sub64"].(func(int64, int64) int64)
		if !ok {
			t.Fatal("sub64 function not found in FuncMap")
		}
		if result := sub64Func(5, 3); result != 2 {
			t.Errorf("sub64(5, 3) = %d, want 2", result)
		}
	})
}

func TestDerefFunctions(t *testing.T) {
	funcMap := GetFuncMap()

	t.Run("derefInt64", func(t *testing.T) {
		derefInt64Func, ok := funcMap["derefInt64"].(func(*int64) int64)
		if !ok {
			t.Fatal("derefInt64 function not found in FuncMap")
		}

		val := int64(42)
		if result := derefInt64Func(&val); result != 42 {
			t.Errorf("derefInt64(&42) = %d, want 42", result)
		}

		if result := derefInt64Func(nil); result != 0 {
			t.Errorf("derefInt64(nil) = %d, want 0", result)
		}
	})

	t.Run("derefBool", func(t *testing.T) {
		derefBoolFunc, ok := funcMap["derefBool"].(func(*bool) bool)
		if !ok {
			t.Fatal("derefBool function not found in FuncMap")
		}

		val := true
		if result := derefBoolFunc(&val); result != true {
			t.Errorf("derefBool(&true) = %v, want true", result)
		}

		if result := derefBoolFunc(nil); result != false {
			t.Errorf("derefBool(nil) = %v, want false", result)
		}
	})
}

func TestSeq(t *testing.T) {
	funcMap := GetFuncMap()
	seqFunc, ok := funcMap["seq"].(func(int, int) []int)
	if !ok {
		t.Fatal("seq function not found in FuncMap")
	}

	tests := []struct {
		name     string
		start    int
		end      int
		expected []int
	}{
		{
			name:     "simple sequence",
			start:    1,
			end:      5,
			expected: []int{1, 2, 3, 4, 5},
		},
		{
			name:     "single element",
			start:    3,
			end:      3,
			expected: []int{3},
		},
		{
			name:     "empty sequence",
			start:    5,
			end:      3,
			expected: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := seqFunc(tt.start, tt.end)
			if len(result) != len(tt.expected) {
				t.Errorf("seq(%d, %d) length = %d, want %d", tt.start, tt.end, len(result), len(tt.expected))
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("seq(%d, %d)[%d] = %d, want %d", tt.start, tt.end, i, result[i], tt.expected[i])
				}
			}
		})
	}
}

func TestFuncMapIntegration(t *testing.T) {
	funcMap := GetFuncMap()

	// Test that all expected functions are present
	expectedFuncs := []string{
		"seq", "add", "sub", "div", "add64", "sub64",
		"derefInt64", "derefString", "derefBool", "deref",
		"lower", "isTopicList", "renderContent", "prettyJSON",
		"jsString", "formatBytes",
	}

	for _, funcName := range expectedFuncs {
		if _, ok := funcMap[funcName]; !ok {
			t.Errorf("Expected function %q not found in FuncMap", funcName)
		}
	}

	// Test that FuncMap can be used with template
	tmpl, err := template.New("test").Funcs(funcMap).Parse(`{{prettyJSON .}}`)
	if err != nil {
		t.Fatalf("Failed to parse template: %v", err)
	}

	if tmpl == nil {
		t.Error("Template should not be nil")
	}
}

func TestDiv(t *testing.T) {
	funcMap := GetFuncMap()
	divFunc, ok := funcMap["div"].(func(int, int) int)
	if !ok {
		t.Fatal("div function not found in FuncMap")
	}

	tests := []struct {
		name     string
		a        int
		b        int
		expected int
	}{
		{"normal division", 10, 2, 5},
		{"rounds down", 7, 3, 2},
		{"negative result", 5, 10, 0},
		{"division by zero", 10, 0, 0},
		{"both zero", 0, 0, 0},
		{"negative numbers", -10, 3, -3}, // Go rounds toward zero
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := divFunc(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("div(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestDerefString(t *testing.T) {
	funcMap := GetFuncMap()
	derefStringFunc, ok := funcMap["derefString"].(func(*string) string)
	if !ok {
		t.Fatal("derefString function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    *string
		expected string
	}{
		{
			name:     "non-nil string",
			input:    testutil.Ptr("hello"),
			expected: "hello",
		},
		{
			name:     "nil pointer",
			input:    nil,
			expected: "",
		},
		{
			name:     "empty string",
			input:    testutil.Ptr(""),
			expected: "",
		},
		{
			name:     "string with spaces",
			input:    testutil.Ptr("hello world"),
			expected: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := derefStringFunc(tt.input)
			if result != tt.expected {
				t.Errorf("derefString() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestLower(t *testing.T) {
	funcMap := GetFuncMap()
	lowerFunc, ok := funcMap["lower"].(func(string) string)
	if !ok {
		t.Fatal("lower function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"uppercase", "HELLO", "hello"},
		{"mixed case", "HeLLo WoRLd", "hello world"},
		{"already lowercase", "hello", "hello"},
		{"with numbers", "Test123", "test123"},
		{"empty string", "", ""},
		{"with special chars", "Hello@World!", "hello@world!"},
		{"unicode", "Привет", "привет"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := lowerFunc(tt.input)
			if result != tt.expected {
				t.Errorf("lower() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDerefFloat64(t *testing.T) {
	funcMap := GetFuncMap()
	derefFunc, ok := funcMap["deref"].(func(*float64) float64)
	if !ok {
		t.Fatal("deref function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    *float64
		expected float64
	}{
		{
			name:     "non-nil float",
			input:    testutil.Ptr(3.14159),
			expected: 3.14159,
		},
		{
			name:     "nil pointer",
			input:    nil,
			expected: 0,
		},
		{
			name:     "zero value",
			input:    testutil.Ptr(0.0),
			expected: 0,
		},
		{
			name:     "negative float",
			input:    testutil.Ptr(-2.5),
			expected: -2.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := derefFunc(tt.input)
			if result != tt.expected {
				t.Errorf("deref() = %f, want %f", result, tt.expected)
			}
		})
	}
}

func TestJsString(t *testing.T) {
	funcMap := GetFuncMap()
	jsStringFunc, ok := funcMap["jsString"].(func(string) template.JS)
	if !ok {
		t.Fatal("jsString function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    string
		contains string // Check that output contains this (for escaped content)
	}{
		{
			name:     "simple string",
			input:    "hello",
			contains: "hello",
		},
		{
			name:     "string with quotes",
			input:    `say "hello"`,
			contains: "hello",
		},
		{
			name:     "string with newlines",
			input:    "line1\nline2",
			contains: "line1",
		},
		{
			name:     "string with special characters",
			input:    "test's \"quoted\"",
			contains: "test",
		},
		{
			name:     "empty string",
			input:    "",
			contains: "",
		},
		{
			name:     "unicode string",
			input:    "Привет мир",
			contains: "Привет",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := jsStringFunc(tt.input)
			resultStr := string(result)
			if tt.contains != "" && !contains(resultStr, tt.contains) {
				t.Errorf("jsString() = %q, want to contain %q", resultStr, tt.contains)
			}
			// All results should be wrapped in quotes (JSON encoding)
			if len(resultStr) > 0 && resultStr[0] != '"' && resultStr[0] != '\x00' {
				t.Errorf("jsString() result should be JSON-encoded (start with quote), got %q", resultStr)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && findInString(s, substr)))
}

func findInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestFormatBytes(t *testing.T) {
	funcMap := GetFuncMap()
	formatBytesFunc, ok := funcMap["formatBytes"].(func(int64) string)
	if !ok {
		t.Fatal("formatBytes function not found in FuncMap")
	}

	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{"bytes", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"just under KB", 1023, "1023 B"},
		{"kilobytes", 1024, "1.0 KiB"},
		{"kilobytes", 1536, "1.5 KiB"},
		{"megabytes", 1048576, "1.0 MiB"},
		{"megabytes", 2097152, "2.0 MiB"},
		{"gigabytes", 1073741824, "1.0 GiB"},
		{"gigabytes", 2147483648, "2.0 GiB"},
		{"terabytes", 1099511627776, "1.0 TiB"},
		{"petabytes", 1125899906842624, "1.0 PiB"},
		{"exabytes", 1152921504606846976, "1.0 EiB"},
		{"mixed value", 3670016, "3.5 MiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBytesFunc(tt.input)
			if result != tt.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
