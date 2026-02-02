package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseFactID tests ParseFactID with various inputs.
func TestParseFactID(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    int64
		wantErr bool
	}{
		// Valid formats with "Fact:" prefix (preferred)
		{
			name:    "Fact:123 format",
			input:   "Fact:123",
			want:    123,
			wantErr: false,
		},
		{
			name:    "Fact:0",
			input:   "Fact:0",
			want:    0,
			wantErr: false,
		},
		{
			name:    "Fact:456 with large number",
			input:   "Fact:999999",
			want:    999999,
			wantErr: false,
		},

		// Valid formats without prefix (backward compatibility)
		{
			name:    "plain numeric string",
			input:   "123",
			want:    123,
			wantErr: false,
		},
		{
			name:    "plain numeric string 0",
			input:   "0",
			want:    0,
			wantErr: false,
		},

		// Valid numeric types (LLM error fallback)
		{
			name:    "float64 123",
			input:   float64(123),
			want:    123,
			wantErr: false,
		},
		{
			name:    "float64 456.7 (truncated)",
			input:   float64(456.7),
			want:    456,
			wantErr: false,
		},
		{
			name:    "float64 0",
			input:   float64(0),
			want:    0,
			wantErr: false,
		},
		{
			name:    "int 789",
			input:   int(789),
			want:    789,
			wantErr: false,
		},
		{
			name:    "int64 1000",
			input:   int64(1000),
			want:    1000,
			wantErr: false,
		},

		// Invalid formats
		{
			name:    "invalid string",
			input:   "invalid",
			want:    0,
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "Fact: with trailing text",
			input:   "Fact:123abc",
			want:    0,
			wantErr: true,
		},
		{
			name:    "Person:123 (wrong prefix)",
			input:   "Person:123",
			want:    0,
			wantErr: true,
		},
		{
			name:    "nil",
			input:   nil,
			want:    0,
			wantErr: true,
		},
		{
			name:    "boolean true",
			input:   true,
			want:    0,
			wantErr: true,
		},
		{
			name:    "map",
			input:   map[string]interface{}{},
			want:    0,
			wantErr: true,
		},
		{
			name:    "negative number string",
			input:   "-123",
			want:    0,
			wantErr: true,
		},
		{
			name:    "Fact:-1 (negative)",
			input:   "Fact:-1",
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFactID(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, int64(0), got)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestParsePersonID tests ParsePersonID with various inputs.
func TestParsePersonID(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    int64
		wantErr bool
	}{
		// Valid formats with "Person:" prefix (preferred)
		{
			name:    "Person:123 format",
			input:   "Person:123",
			want:    123,
			wantErr: false,
		},
		{
			name:    "Person:0",
			input:   "Person:0",
			want:    0,
			wantErr: false,
		},
		{
			name:    "Person:999999",
			input:   "Person:999999",
			want:    999999,
			wantErr: false,
		},

		// Valid formats without prefix (backward compatibility)
		{
			name:    "plain numeric string",
			input:   "123",
			want:    123,
			wantErr: false,
		},
		{
			name:    "plain numeric string 0",
			input:   "0",
			want:    0,
			wantErr: false,
		},

		// Valid numeric types (LLM error fallback)
		{
			name:    "float64 123",
			input:   float64(123),
			want:    123,
			wantErr: false,
		},
		{
			name:    "float64 456.7 (truncated)",
			input:   float64(456.7),
			want:    456,
			wantErr: false,
		},
		{
			name:    "int 789",
			input:   int(789),
			want:    789,
			wantErr: false,
		},

		// Invalid formats
		{
			name:    "invalid string",
			input:   "invalid",
			want:    0,
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "Person: with trailing text",
			input:   "Person:123abc",
			want:    0,
			wantErr: true,
		},
		{
			name:    "Fact:123 (wrong prefix)",
			input:   "Fact:123",
			want:    0,
			wantErr: true,
		},
		{
			name:    "nil",
			input:   nil,
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePersonID(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, int64(0), got)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestParseMemoryOpParams tests ParseMemoryOpParams with various inputs.
func TestParseMemoryOpParams(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]interface{}
		want    MemoryOpParams
		wantErr bool
		errMsg  string
	}{
		{
			name: "full add operation",
			input: map[string]interface{}{
				"action":     "add",
				"content":    "Test content",
				"category":   "test",
				"type":       "identity",
				"reason":     "Testing",
				"importance": float64(80),
			},
			want: MemoryOpParams{
				Action:     "add",
				Content:    "Test content",
				Category:   "test",
				FactType:   "identity",
				Reason:     "Testing",
				Importance: 80,
			},
			wantErr: false,
		},
		{
			name: "minimal add operation",
			input: map[string]interface{}{
				"action":  "add",
				"content": "Minimal test",
			},
			want: MemoryOpParams{
				Action:   "add",
				Content:  "Minimal test",
				Category: "",
				FactType: "",
				Reason:   "",
			},
			wantErr: false,
		},
		{
			name: "delete operation with Fact:123",
			input: map[string]interface{}{
				"action":  "delete",
				"fact_id": "Fact:123",
				"reason":  "No longer relevant",
			},
			want: MemoryOpParams{
				Action: "delete",
				Reason: "No longer relevant",
				FactID: 123,
			},
			wantErr: false,
		},
		{
			name: "update operation with numeric fact_id",
			input: map[string]interface{}{
				"action":  "update",
				"fact_id": float64(456),
				"content": "Updated content",
			},
			want: MemoryOpParams{
				Action:  "update",
				Content: "Updated content",
				FactID:  456,
			},
			wantErr: false,
		},
		{
			name: "operation with importance 0",
			input: map[string]interface{}{
				"action":     "add",
				"content":    "Test",
				"importance": float64(0),
			},
			want: MemoryOpParams{
				Action:     "add",
				Content:    "Test",
				Importance: 0,
			},
			wantErr: false,
		},
		{
			name: "missing action field",
			input: map[string]interface{}{
				"content": "Test",
			},
			wantErr: true,
			errMsg:  "missing action",
		},
		{
			name: "invalid fact_id format",
			input: map[string]interface{}{
				"action":  "delete",
				"fact_id": "invalid",
			},
			wantErr: true,
			errMsg:  "invalid fact_id",
		},
		{
			name:    "empty params",
			input:   map[string]interface{}{},
			wantErr: true,
			errMsg:  "missing action",
		},
		{
			name: "negative fact_id",
			input: map[string]interface{}{
				"action":  "delete",
				"fact_id": "-123",
			},
			wantErr: true,
			errMsg:  "invalid fact_id",
		},
		{
			name: "fact_id with wrong prefix",
			input: map[string]interface{}{
				"action":  "delete",
				"fact_id": "Person:123",
			},
			wantErr: true,
			errMsg:  "invalid fact_id",
		},
		{
			name: "all optional fields set",
			input: map[string]interface{}{
				"action":     "add",
				"content":    "Full test",
				"category":   "work",
				"type":       "context",
				"reason":     "Work related",
				"importance": float64(75),
			},
			want: MemoryOpParams{
				Action:     "add",
				Content:    "Full test",
				Category:   "work",
				FactType:   "context",
				Reason:     "Work related",
				Importance: 75,
			},
			wantErr: false,
		},
		{
			name: "importance as int (not float64)",
			input: map[string]interface{}{
				"action":     "add",
				"content":    "Test",
				"importance": int(50),
			},
			want: MemoryOpParams{
				Action:     "add",
				Content:    "Test",
				Importance: 0, // int type not handled, falls back to 0
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMemoryOpParams(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want.Action, got.Action)
				assert.Equal(t, tt.want.Content, got.Content)
				assert.Equal(t, tt.want.Category, got.Category)
				assert.Equal(t, tt.want.FactType, got.FactType)
				assert.Equal(t, tt.want.Reason, got.Reason)
				assert.Equal(t, tt.want.Importance, got.Importance)
				assert.Equal(t, tt.want.FactID, got.FactID)
			}
		})
	}
}
