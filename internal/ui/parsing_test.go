package ui

import (
	"encoding/json"
	"testing"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

func TestExtractMessageContent(t *testing.T) {
	tests := []struct {
		name    string
		content interface{}
		want    string
	}{
		{
			name:    "nil content",
			content: nil,
			want:    "",
		},
		{
			name:    "string content",
			content: "Hello world",
			want:    "Hello world",
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
		},
		{
			name: "multipart with single text",
			content: []interface{}{
				map[string]interface{}{"type": "text", "text": "Hello"},
			},
			want: "Hello",
		},
		{
			name: "multipart with multiple texts",
			content: []interface{}{
				map[string]interface{}{"type": "text", "text": "Hello "},
				map[string]interface{}{"type": "text", "text": "world"},
			},
			want: "Hello world",
		},
		{
			name: "multipart with mixed types",
			content: []interface{}{
				map[string]interface{}{"type": "text", "text": "Check this: "},
				map[string]interface{}{"type": "image_url", "url": "http://example.com/img.png"},
				map[string]interface{}{"type": "text", "text": "cool image"},
			},
			want: "Check this: cool image",
		},
		{
			name:    "unsupported type",
			content: 12345,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMessageContent(tt.content)
			if got != tt.want {
				t.Errorf("extractMessageContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRAGLog(t *testing.T) {
	// Prepare test data
	systemPrompt := `Some system instructions.
=== ФАКТЫ О ПОЛЬЗОВАТЕЛЕ ===
- User likes cats.
=== ФАКТЫ ОБ ОКРУЖЕНИИ ===
- It is sunny.`

	contextUsed := []openrouter.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "user", Content: "# КОНТЕКСТ (RAG)\n- Fact 1"},
	}
	contextBytes, _ := json.Marshal(contextUsed)

	log := storage.RAGLog{
		SystemPrompt: systemPrompt,
		ContextUsed:  string(contextBytes),
	}

	// Execute
	view := ParseRAGLog(log)

	// Verify System Prompt Splitting
	if view.SystemPromptPart != "Some system instructions." {
		t.Errorf("Expected SystemPromptPart 'Some system instructions.', got '%s'", view.SystemPromptPart)
	}
	expectedUserFacts := "=== ФАКТЫ О ПОЛЬЗОВАТЕЛЕ ===\n- User likes cats."
	if view.UserFactsPart != expectedUserFacts {
		t.Errorf("Expected UserFactsPart '%s', got '%s'", expectedUserFacts, view.UserFactsPart)
	}
	expectedEnvFacts := "=== ФАКТЫ ОБ ОКРУЖЕНИИ ===\n- It is sunny."
	if view.EnvFactsPart != expectedEnvFacts {
		t.Errorf("Expected EnvFactsPart '%s', got '%s'", expectedEnvFacts, view.EnvFactsPart)
	}

	// Verify Context Parsing
	if len(view.LastMessages) != 2 {
		t.Errorf("Expected 2 LastMessages (excluding RAG), got %d", len(view.LastMessages))
	}
	if view.RAGContextPart == "" {
		t.Error("Expected RAGContextPart to be populated")
	}

	// Verify Stats
	if view.Stats.TotalSize == 0 {
		t.Error("Expected TotalSize > 0")
	}
}

func TestParseTopicLog(t *testing.T) {
	// Prepare test data
	llmResponse := "```json\n{\"topics\": [{\"summary\": \"Cats\", \"start_msg_id\": 100, \"end_msg_id\": 101}]}\n```"

	contextUsed := []struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
	}{
		{ID: 100, Content: "I love cats"},
		{ID: 101, Content: "Dogs are okay too"},
	}
	contextBytes, _ := json.Marshal(contextUsed)

	log := storage.RAGLog{
		LLMResponse: llmResponse,
		ContextUsed: string(contextBytes),
	}

	// Execute
	view := ParseTopicLog(log)

	// Verify Topic Parsing
	if len(view.ParsedTopics) != 1 {
		t.Errorf("Expected 1 parsed topic, got %d", len(view.ParsedTopics))
	}
	if view.ParsedTopics[0].Summary != "Cats" {
		t.Errorf("Expected topic summary 'Cats', got '%s'", view.ParsedTopics[0].Summary)
	}

	// Verify Input IDs
	if view.InputStartID != 100 {
		t.Errorf("Expected InputStartID 100, got %d", view.InputStartID)
	}
	if view.InputEndID != 101 {
		t.Errorf("Expected InputEndID 101, got %d", view.InputEndID)
	}
	if view.InputMsgCount != 2 {
		t.Errorf("Expected InputMsgCount 2, got %d", view.InputMsgCount)
	}
}

func TestParseRerankerLog_NewFormat(t *testing.T) {
	// New format with objects containing reason and excerpt
	log := storage.RerankerLog{
		SelectedIDsJSON: `[{"id": 42, "reason": "test reason", "excerpt": "some text"}, {"id": 18, "reason": "another reason"}]`,
		CandidatesJSON:  `[{"topic_id": 42, "summary": "Topic 42", "score": 0.9}, {"topic_id": 18, "summary": "Topic 18", "score": 0.8}]`,
	}

	view := ParseRerankerLog(log)

	// Check SelectedTopics
	if len(view.SelectedTopics) != 2 {
		t.Fatalf("Expected 2 SelectedTopics, got %d", len(view.SelectedTopics))
	}

	// First topic with excerpt
	if view.SelectedTopics[0].ID != 42 {
		t.Errorf("Expected first topic ID 42, got %d", view.SelectedTopics[0].ID)
	}
	if view.SelectedTopics[0].Reason != "test reason" {
		t.Errorf("Expected reason 'test reason', got '%s'", view.SelectedTopics[0].Reason)
	}
	if view.SelectedTopics[0].Excerpt != "some text" {
		t.Errorf("Expected excerpt 'some text', got '%s'", view.SelectedTopics[0].Excerpt)
	}

	// Second topic without excerpt
	if view.SelectedTopics[1].ID != 18 {
		t.Errorf("Expected second topic ID 18, got %d", view.SelectedTopics[1].ID)
	}
	if view.SelectedTopics[1].Reason != "another reason" {
		t.Errorf("Expected reason 'another reason', got '%s'", view.SelectedTopics[1].Reason)
	}
	if view.SelectedTopics[1].Excerpt != "" {
		t.Errorf("Expected empty excerpt, got '%s'", view.SelectedTopics[1].Excerpt)
	}

	// Check SelectedIDs map
	if !view.SelectedIDs[42] {
		t.Error("Expected SelectedIDs[42] to be true")
	}
	if !view.SelectedIDs[18] {
		t.Error("Expected SelectedIDs[18] to be true")
	}

	// Check SelectedIDList
	if len(view.SelectedIDList) != 2 {
		t.Errorf("Expected 2 SelectedIDList, got %d", len(view.SelectedIDList))
	}

	// Check candidates have reasons assigned
	if len(view.Candidates) != 2 {
		t.Fatalf("Expected 2 candidates, got %d", len(view.Candidates))
	}
	if view.Candidates[0].Reason != "test reason" {
		t.Errorf("Expected candidate reason 'test reason', got '%s'", view.Candidates[0].Reason)
	}
}

func TestParseRerankerLog_OldFormat(t *testing.T) {
	// Old format with plain array of IDs
	log := storage.RerankerLog{
		SelectedIDsJSON: `[42, 18, 5]`,
		CandidatesJSON:  `[{"topic_id": 42, "summary": "Topic 42"}, {"topic_id": 18, "summary": "Topic 18"}]`,
	}

	view := ParseRerankerLog(log)

	// Check SelectedTopics (should be created from IDs without reason)
	if len(view.SelectedTopics) != 3 {
		t.Fatalf("Expected 3 SelectedTopics, got %d", len(view.SelectedTopics))
	}
	if view.SelectedTopics[0].ID != 42 {
		t.Errorf("Expected first topic ID 42, got %d", view.SelectedTopics[0].ID)
	}
	if view.SelectedTopics[0].Reason != "" {
		t.Errorf("Expected empty reason in old format, got '%s'", view.SelectedTopics[0].Reason)
	}

	// Check SelectedIDs map
	if !view.SelectedIDs[42] {
		t.Error("Expected SelectedIDs[42] to be true")
	}
	if !view.SelectedIDs[18] {
		t.Error("Expected SelectedIDs[18] to be true")
	}
	if !view.SelectedIDs[5] {
		t.Error("Expected SelectedIDs[5] to be true")
	}

	// Check SelectedIDList
	if len(view.SelectedIDList) != 3 {
		t.Errorf("Expected 3 SelectedIDList, got %d", len(view.SelectedIDList))
	}
}

func TestParseRerankerLog_ToolCalls(t *testing.T) {
	log := storage.RerankerLog{
		SelectedIDsJSON: `[{"id": 42, "reason": "found in content"}]`,
		ToolCallsJSON:   `[{"iteration": 1, "topics": [{"id": 42, "summary": "Topic summary"}]}]`,
	}

	view := ParseRerankerLog(log)

	if len(view.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(view.ToolCalls))
	}
	if view.ToolCalls[0].Iteration != 1 {
		t.Errorf("Expected iteration 1, got %d", view.ToolCalls[0].Iteration)
	}
	if len(view.ToolCalls[0].Topics) != 1 {
		t.Fatalf("Expected 1 topic in tool call, got %d", len(view.ToolCalls[0].Topics))
	}
	if view.ToolCalls[0].Topics[0].ID != 42 {
		t.Errorf("Expected topic ID 42, got %d", view.ToolCalls[0].Topics[0].ID)
	}
}
