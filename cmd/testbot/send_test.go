package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/rag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputText(t *testing.T) {
	tests := []struct {
		name     string
		result   *rag.TestMessageResult
		failures []string
		contains []string
	}{
		{
			name: "successful result",
			result: &rag.TestMessageResult{
				Response:         "Test response",
				TimingTotal:      100 * time.Millisecond,
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalCost:        0.001,
				FactsInjected:    2,
				TopicsMatched:    1,
			},
			failures: []string{},
			contains: []string{
				"Status: PASS",
				"Test response",
				"Duration:",
				"Tokens: 30",
				"Cost: $",
				"Facts injected: 2",
			},
		},
		{
			name: "result with failures",
			result: &rag.TestMessageResult{
				Response:         "Test response",
				TimingTotal:      100 * time.Millisecond,
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalCost:        0.001,
			},
			failures: []string{"check failed 1", "check failed 2"},
			contains: []string{
				"Status: FAIL",
				"Failures:",
				"check failed 1",
				"check failed 2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			outputErr := outputText(tt.result, tt.failures)

			// Restore stdout
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err := buf.ReadFrom(r)
			require.NoError(t, err)

			require.NoError(t, outputErr)
			output := buf.String()

			for _, expected := range tt.contains {
				assert.Contains(t, output, expected)
			}
		})
	}
}

func TestOutputJSON(t *testing.T) {
	tests := []struct {
		name     string
		result   *rag.TestMessageResult
		failures []string
		check    func(*testing.T, map[string]interface{})
	}{
		{
			name: "successful result",
			result: &rag.TestMessageResult{
				Response:         "Test response",
				TimingTotal:      100 * time.Millisecond,
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalCost:        0.001,
				FactsInjected:    2,
				TopicsMatched:    1,
			},
			failures: []string{},
			check: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "PASS", data["status"])
				assert.Contains(t, data, "response_raw")
				assert.Contains(t, data, "response_html")
				assert.Contains(t, data, "metrics")
				assert.Contains(t, data, "duration_ms")

				metrics := data["metrics"].(map[string]interface{})
				assert.Equal(t, float64(30), metrics["tokens"])
			},
		},
		{
			name: "result with failures",
			result: &rag.TestMessageResult{
				Response:         "Test response",
				TimingTotal:      100 * time.Millisecond,
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalCost:        0.001,
			},
			failures: []string{"check failed"},
			check: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "FAIL", data["status"])
				failures := data["failures"].([]interface{})
				assert.Len(t, failures, 1)
				assert.Equal(t, "check failed", failures[0])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			outputErr := outputJSON(tt.result, tt.failures)

			// Restore stdout
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err := buf.ReadFrom(r)
			require.NoError(t, err)

			require.NoError(t, outputErr)

			var data map[string]interface{}
			err = json.Unmarshal(buf.Bytes(), &data)
			require.NoError(t, err)

			tt.check(t, data)
		})
	}
}

func TestSendCommandErrors(t *testing.T) {
	t.Run("testbot not initialized", func(t *testing.T) {
		cmd := sendCmd
		ctx := context.Background()
		cmd.SetContext(ctx)

		err := cmd.RunE(cmd, []string{"test"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "testbot not initialized")
	})

	t.Run("no args", func(t *testing.T) {
		cmd := sendCmd
		ctx := context.Background()
		cmd.SetContext(ctx)

		err := cmd.RunE(cmd, []string{})
		assert.Error(t, err)
	})
}

func TestSendResponseCheck(t *testing.T) {
	tests := []struct {
		name          string
		response      string
		checkResponse string
		shouldPass    bool
	}{
		{
			name:          "exact match",
			response:      "The answer is 4",
			checkResponse: "4",
			shouldPass:    true,
		},
		{
			name:          "case insensitive match",
			response:      "The answer is FOUR",
			checkResponse: "four",
			shouldPass:    true,
		},
		{
			name:          "no match",
			response:      "The answer is 4",
			checkResponse: "5",
			shouldPass:    false,
		},
		{
			name:          "empty check response",
			response:      "Any response",
			checkResponse: "",
			shouldPass:    true,
		},
		{
			name:          "substring match",
			response:      "Hello world, this is a test",
			checkResponse: "world",
			shouldPass:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.checkResponse == "" {
				tt.shouldPass = true
			} else {
				passed := strings.Contains(strings.ToLower(tt.response), strings.ToLower(tt.checkResponse))
				assert.Equal(t, tt.shouldPass, passed)
			}
		})
	}
}
