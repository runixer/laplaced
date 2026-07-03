package bot

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestPrepareErrorText(t *testing.T) {
	b := &Bot{cfg: testutil.TestConfig(), translator: testutil.TestTranslator(t)}

	tests := []struct {
		name        string
		err         error
		contains    string
		notContains string
	}{
		{
			name:        "unsupported format with extension",
			err:         &files.UnsupportedFormatError{FileName: "archive.rar", MimeType: "application/x-rar"},
			contains:    ".rar",
			notContains: "{ext}",
		},
		{
			name:        "unsupported format without extension falls back to mime",
			err:         &files.UnsupportedFormatError{FileName: "noext", MimeType: "application/x-rar"},
			contains:    "(application/x-rar)",
			notContains: "{ext}",
		},
		{
			name:        "file too large formats size in MB",
			err:         &files.FileTooLargeError{FileName: "big.pdf", Size: 25 * 1024 * 1024},
			contains:    "25.0",
			notContains: "{size}",
		},
		{
			name:        "wrapped errors unwrap through errors.As",
			err:         fmt.Errorf("processing: %w", &files.FileTooLargeError{Size: 1024 * 1024}),
			contains:    "1.0",
			notContains: "{size}",
		},
		{
			name:     "unknown error falls back to generic api_error",
			err:      errors.New("boom"),
			contains: "",
		},
	}
	apiError := b.translator.Get(b.cfg.Bot.Language, "bot.api_error")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := b.prepareErrorText(tt.err)
			assert.NotEmpty(t, got)
			if tt.contains != "" {
				assert.Contains(t, got, tt.contains)
				assert.NotEqual(t, apiError, got)
			} else {
				assert.Equal(t, apiError, got)
			}
			if tt.notContains != "" {
				assert.NotContains(t, got, tt.notContains)
			}
		})
	}
}

func TestCountToolCalls(t *testing.T) {
	toolCall := func(name string) llm.ToolCall {
		tc := llm.ToolCall{}
		tc.Function.Name = name
		return tc
	}

	tests := []struct {
		name      string
		messages  []llm.Message
		wantTotal int
		wantNames []string
	}{
		{
			name:      "no messages",
			messages:  nil,
			wantTotal: 0,
			wantNames: nil,
		},
		{
			name: "assistant without tool calls",
			messages: []llm.Message{
				{Role: "assistant", Content: "hi"},
			},
			wantTotal: 0,
			wantNames: nil,
		},
		{
			name: "counts repeats, names deduped in first-use order",
			messages: []llm.Message{
				{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("search_history"), toolCall("manage_memory")}},
				{Role: "tool", Content: "result"},
				{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("search_history")}},
			},
			wantTotal: 3,
			wantNames: []string{"search_history", "manage_memory"},
		},
		{
			name: "non-assistant tool calls ignored",
			messages: []llm.Message{
				{Role: "user", ToolCalls: []llm.ToolCall{toolCall("bogus")}},
			},
			wantTotal: 0,
			wantNames: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, names := countToolCalls(tt.messages)
			assert.Equal(t, tt.wantTotal, total)
			assert.Equal(t, tt.wantNames, names)
		})
	}
}
