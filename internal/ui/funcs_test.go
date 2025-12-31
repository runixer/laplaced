package ui

import (
	"html/template"
	"testing"
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
		"seq", "add", "sub", "add64", "sub64",
		"derefInt64", "derefBool", "isTopicList",
		"renderContent", "prettyJSON",
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
