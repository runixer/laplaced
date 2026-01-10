package testutil

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/i18n"
)

// TestLogger returns a discarding logger for tests.
func TestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestConfig returns a config with sensible test defaults.
func TestConfig() *config.Config {
	return &config.Config{
		Bot: config.BotConfig{
			Language: "en",
		},
		Agents: config.AgentsConfig{
			Default: config.AgentConfig{
				Name:  "TestAgent",
				Model: "test-model",
			},
			Chat: config.AgentConfig{
				Name:  "TestBot",
				Model: "test-model",
			},
			Archivist: config.AgentConfig{Name: "Archivist"},
			Enricher:  config.AgentConfig{Name: "Enricher"},
			Reranker: config.RerankerAgentConfig{
				AgentConfig: config.AgentConfig{Name: "Reranker"},
				Enabled:     true,
				MaxTopics:   5,
			},
			Splitter: config.AgentConfig{Name: "Splitter"},
			Merger:   config.AgentConfig{Name: "Merger"},
		},
		Embedding: config.EmbeddingConfig{
			Model: "test-embedding-model",
		},
		RAG: config.RAGConfig{
			Enabled:            true,
			MaxContextMessages: 50,
			MaxProfileFacts:    50,
		},
	}
}

// TestConfigWithRAGDisabled returns a test config with RAG disabled.
func TestConfigWithRAGDisabled() *config.Config {
	cfg := TestConfig()
	cfg.RAG.Enabled = false
	return cfg
}

// TestTranslator creates a translator with minimal translations for tests.
// Use t.TempDir() automatically cleaned up after test.
func TestTranslator(t *testing.T) *i18n.Translator {
	t.Helper()
	tmpDir := t.TempDir()
	content := `
telegram:
  forwarded_from: "[Forwarded from %s by %s at %s]"
bot:
  voice_recognition_prefix: "(Transcribed from audio):"
  voice_message_marker: "[Voice message]"
  voice_instruction: "The user sent a voice message (audio file below). Listen to it and respond in English. Do not describe the listening process — just respond to the content."
  system_prompt: "System {{.BotName}}"
rag:
  no_context: "No relevant context found"
  enrichment_system_prompt: "Enricher System {{.Date}} {{.Profile}} {{.RecentTopics}}"
  enrichment_user_prompt: "History: {{.History}}\nQuery: {{.Query}}"
  topic_extraction_prompt: "Extract topics {{.Profile}} {{.RecentTopics}} {{.Goal}}"
  topic_consolidation_system_prompt: "Consolidation System {{.Profile}} {{.RecentTopics}}"
  topic_consolidation_user_prompt: "Topic1: {{.Topic1Summary}}\nTopic2: {{.Topic2Summary}}"
  reranker_system_prompt: "Reranker System {{.Profile}} {{.RecentTopics}} max={{.MaxTopics}} budget={{.TargetCharsK}}K min={{.MinCandidates}} max={{.MaxCandidates}} large={{.LargeBudgetK}}K"
  reranker_system_prompt_simple: "Reranker Simple {{.Profile}} {{.RecentTopics}} max={{.MaxTopics}} min={{.MinCandidates}} max={{.MaxCandidates}}"
  reranker_user_prompt: "Date: {{.Date}}\nQuery: {{.Query}}\nEnriched: {{.EnrichedQuery}}\nMessages: {{.CurrentMessages}}\nCandidates: {{.Candidates}}"
  reranker_tool_description: "Load topic content"
  reranker_tool_param_description: "Topic IDs"
memory:
  system_prompt: "Archivist {{.Date}} limit={{.UserFactsLimit}} user={{.UserFactsCount}} other={{.OtherFactsCount}}\nUser: {{.UserFacts}}\nOther: {{.OtherFacts}}\nConversation: {{.Conversation}}"
`
	err := os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte(content), 0600)
	if err != nil {
		t.Fatalf("failed to write test translations: %v", err)
	}

	tr, err := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")
	if err != nil {
		t.Fatalf("failed to create test translator: %v", err)
	}
	return tr
}

// TestFileProcessor creates a file processor for tests.
func TestFileProcessor(t *testing.T, downloader *MockFileDownloader, translator *i18n.Translator) *files.Processor {
	t.Helper()
	logger := TestLogger()
	return files.NewProcessor(downloader, translator, "en", logger)
}

// Ptr returns a pointer to the given value. Useful for optional fields.
func Ptr[T any](v T) *T {
	return &v
}
