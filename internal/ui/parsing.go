package ui

import (
	"encoding/json"
	"strings"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// extractMessageContent extracts text content from openrouter.Message.Content,
// handling both string and []interface{} (multipart) formats.
func extractMessageContent(content interface{}) string {
	if str, ok := content.(string); ok {
		return str
	}
	if parts, ok := content.([]interface{}); ok {
		var result string
		for _, p := range parts {
			if pm, ok := p.(map[string]interface{}); ok {
				if txt, ok := pm["text"].(string); ok {
					result += txt
				}
			}
		}
		return result
	}
	return ""
}

func ParseRAGLog(l storage.RAGLog) RAGLogView {
	view := RAGLogView{RAGLog: l}

	// Parse ContextUsed
	if l.ContextUsed != "" {
		_ = json.Unmarshal([]byte(l.ContextUsed), &view.ParsedContext)
	}

	// Parse RetrievalResults - handle both new (TopicSearchResult) and old (SearchResult) formats
	if l.RetrievalResults != "" {
		// Try new format
		var topics []rag.TopicSearchResult
		if err := json.Unmarshal([]byte(l.RetrievalResults), &topics); err == nil {
			// Check if it's really the new format (has Messages)
			isNew := false
			for _, t := range topics {
				if len(t.Messages) > 0 {
					isNew = true
					break
				}
			}
			if isNew {
				view.ParsedResults = topics
			}
		}

		// Fallback to old format
		if view.ParsedResults == nil {
			var old []rag.SearchResult
			if err := json.Unmarshal([]byte(l.RetrievalResults), &old); err == nil {
				view.ParsedResults = old
			}
		}
	}

	// --- Parsing Logic ---

	// 1. System Prompt Breakdown
	sysText := l.SystemPrompt

	// Headers to look for (Russian and English)
	userFactsHeaders := []string{"=== ФАКТЫ О ПОЛЬЗОВАТЕЛЕ ===", "=== USER FACTS ==="}
	envFactsHeaders := []string{"=== ФАКТЫ ОБ ОКРУЖЕНИИ ===", "=== ENVIRONMENT FACTS ==="}

	var userFactsIdx, envFactsIdx int = -1, -1

	for _, h := range userFactsHeaders {
		if idx := strings.Index(sysText, h); idx != -1 {
			userFactsIdx = idx
			break
		}
	}

	for _, h := range envFactsHeaders {
		if idx := strings.Index(sysText, h); idx != -1 {
			envFactsIdx = idx
			break
		}
	}

	// Split
	if userFactsIdx != -1 {
		view.SystemPromptPart = strings.TrimSpace(sysText[:userFactsIdx])
		if envFactsIdx != -1 && envFactsIdx > userFactsIdx {
			view.UserFactsPart = strings.TrimSpace(sysText[userFactsIdx:envFactsIdx])
			view.EnvFactsPart = strings.TrimSpace(sysText[envFactsIdx:])
		} else {
			view.UserFactsPart = strings.TrimSpace(sysText[userFactsIdx:])
		}
	} else {
		view.SystemPromptPart = sysText
	}

	// 2. RAG Context & History
	ragHeaders := []string{"# КОНТЕКСТ (RAG)", "# CONTEXT (RAG)"}

	var history []openrouter.Message

	for _, msg := range view.ParsedContext {
		if msg.Role == "system" {
			continue // Already handled via SystemPrompt string
		}

		isRAG := false
		if msg.Role == "user" {
			contentStr := extractMessageContent(msg.Content)
			for _, h := range ragHeaders {
				if strings.Contains(contentStr, h) {
					isRAG = true
					view.RAGContextPart = contentStr
					break
				}
			}
		}

		if !isRAG {
			history = append(history, msg)
		}

		// Tool Calls
		if len(msg.ToolCalls) > 0 {
			view.ToolCalls = append(view.ToolCalls, msg)
		}
	}

	// Last 4 messages
	if len(history) > 4 {
		view.LastMessages = history[len(history)-4:]
	} else {
		view.LastMessages = history
	}

	// 3. Stats
	sPromptLen := len(view.SystemPromptPart)
	uFactsLen := len(view.UserFactsPart)
	eFactsLen := len(view.EnvFactsPart)
	ragLen := len(view.RAGContextPart)

	histLen := 0
	for _, m := range history {
		histLen += len(extractMessageContent(m.Content))
	}

	total := sPromptLen + uFactsLen + eFactsLen + ragLen + histLen
	if total > 0 {
		view.Stats.TotalSize = total
		view.Stats.SystemPromptPct = float64(sPromptLen) / float64(total) * 100
		view.Stats.UserFactsPct = float64(uFactsLen) / float64(total) * 100
		view.Stats.EnvFactsPct = float64(eFactsLen) / float64(total) * 100
		view.Stats.RAGContextPct = float64(ragLen) / float64(total) * 100
		view.Stats.HistoryPct = float64(histLen) / float64(total) * 100
	}

	return view
}

func ParseTopicLog(l storage.RAGLog) TopicLogView {
	view := TopicLogView{RAGLog: l}

	// Parse ContextUsed (Input Messages)
	type MsgItem struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
	}
	var items []MsgItem
	if err := json.Unmarshal([]byte(l.ContextUsed), &items); err == nil && len(items) > 0 {
		view.InputMsgCount = len(items)
		view.InputStartID = items[0].ID
		view.InputEndID = items[len(items)-1].ID
	}

	// Parse LLMResponse (Topics)
	var result struct {
		Topics []rag.ExtractedTopic `json:"topics"`
	}
	// Try to parse JSON
	// Clean up potential markdown code blocks if present (sometimes LLMs wrap JSON in ```json ... ```)
	cleanResp := l.LLMResponse
	cleanResp = strings.TrimPrefix(cleanResp, "```json")
	cleanResp = strings.TrimPrefix(cleanResp, "```")
	cleanResp = strings.TrimSuffix(cleanResp, "```")
	cleanResp = strings.TrimSpace(cleanResp)

	if err := json.Unmarshal([]byte(cleanResp), &result); err != nil {
		view.ParseError = err.Error()
		// If it's just "ext" or similar garbage, the error will reflect that
	} else {
		view.ParsedTopics = result.Topics
	}

	return view
}
