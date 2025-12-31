package ui

import (
	"encoding/json"
	"testing"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

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
