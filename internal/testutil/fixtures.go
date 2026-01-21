package testutil

import (
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// TestUserID is the default user ID for tests.
const TestUserID int64 = 123

// TestUser returns a standard test user.
func TestUser() storage.User {
	return storage.User{
		ID:        TestUserID,
		Username:  "testuser",
		FirstName: "Test",
		LastName:  "User",
	}
}

// TestUsers returns multiple test users.
func TestUsers() []storage.User {
	return []storage.User{
		TestUser(),
		{
			ID:        456,
			Username:  "anotheruser",
			FirstName: "Another",
			LastName:  "User",
		},
	}
}

// TestFacts returns sample facts for testing.
func TestFacts() []storage.Fact {
	return []storage.Fact{
		{
			ID:        1,
			UserID:    TestUserID,
			Category:  "bio",
			Type:      "identity",
			Content:   "Software engineer",
			TopicID:   nil,
			CreatedAt: time.Now().Add(-24 * time.Hour),
		},
		{
			ID:        2,
			UserID:    TestUserID,
			Category:  "interests",
			Type:      "preference",
			Content:   "Prefers Go programming language",
			TopicID:   nil,
			CreatedAt: time.Now().Add(-12 * time.Hour),
		},
		{
			ID:        3,
			UserID:    TestUserID,
			Category:  "interests",
			Type:      "interest",
			Content:   "Interested in AI and machine learning",
			TopicID:   nil,
			CreatedAt: time.Now().Add(-6 * time.Hour),
		},
	}
}

// TestProfileFacts returns facts that would be included in a user profile.
func TestProfileFacts() []storage.Fact {
	facts := TestFacts()
	// Return first two as profile facts
	return facts[:2]
}

// TestTopic returns a sample topic for testing.
func TestTopic() storage.Topic {
	now := time.Now()
	return storage.Topic{
		ID:             1,
		UserID:         TestUserID,
		Summary:        "Discussion about Go programming",
		StartMsgID:     1,
		EndMsgID:       10,
		SizeChars:      1500,
		FactsExtracted: true,
		CreatedAt:      now.Add(-48 * time.Hour),
	}
}

// TestTopics returns multiple sample topics for testing.
func TestTopics() []storage.Topic {
	now := time.Now()
	return []storage.Topic{
		TestTopic(),
		{
			ID:             2,
			UserID:         TestUserID,
			Summary:        "Planning a machine learning project",
			StartMsgID:     11,
			EndMsgID:       25,
			SizeChars:      2000,
			FactsExtracted: true,
			CreatedAt:      now.Add(-36 * time.Hour),
		},
		{
			ID:             3,
			UserID:         TestUserID,
			Summary:        "Debugging a production issue",
			StartMsgID:     26,
			EndMsgID:       40,
			SizeChars:      1800,
			FactsExtracted: false,
			CreatedAt:      now.Add(-6 * time.Hour),
		},
	}
}

// TestMessage returns a sample message for testing.
func TestMessage() storage.Message {
	return storage.Message{
		ID:        1,
		UserID:    TestUserID,
		Role:      "user",
		Content:   "Hello, how are you?",
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
}

// TestMessages returns a conversation history for testing.
func TestMessages() []storage.Message {
	now := time.Now()
	return []storage.Message{
		{
			ID:        1,
			UserID:    TestUserID,
			Role:      "user",
			Content:   "Hello, how are you?",
			CreatedAt: now.Add(-10 * time.Minute),
		},
		{
			ID:        2,
			UserID:    TestUserID,
			Role:      "assistant",
			Content:   "Hello! I'm doing well, thank you for asking. How can I help you today?",
			CreatedAt: now.Add(-9 * time.Minute),
		},
		{
			ID:        3,
			UserID:    TestUserID,
			Role:      "user",
			Content:   "I'm working on a Go project and need some advice.",
			CreatedAt: now.Add(-8 * time.Minute),
		},
		{
			ID:        4,
			UserID:    TestUserID,
			Role:      "assistant",
			Content:   "I'd be happy to help with your Go project! What specific aspect would you like advice on?",
			CreatedAt: now.Add(-7 * time.Minute),
		},
	}
}

// TestDashboardStats returns sample dashboard stats for testing.
func TestDashboardStats() *storage.DashboardStats {
	return &storage.DashboardStats{
		TotalMessages: 100,
		TotalTopics:   10,
		TotalFacts:    25,
	}
}

// TestStat returns a sample stat for testing.
func TestStat() storage.Stat {
	return storage.Stat{
		UserID:     TestUserID,
		TokensUsed: 1500,
		CostUSD:    0.05,
	}
}

// TestAgentLog returns a sample agent log for testing.
func TestAgentLog() storage.AgentLog {
	return storage.AgentLog{
		ID:               1,
		UserID:           TestUserID,
		AgentType:        "laplace",
		Model:            "google/gemini-2.0-flash-exp",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalCost:        nil,
		InputContext:     "test input request",
		OutputContext:    "test output response",
		Success:          true,
		DurationMs:       1500,
		CreatedAt:        time.Now(),
	}
}

// TestEmbedding returns a sample embedding vector for testing.
func TestEmbedding() []float32 {
	// A simple 1536-dimensional vector (OpenAI embedding size)
	embedding := make([]float32, 1536)
	for i := range embedding {
		embedding[i] = float32(i) / 1536.0
	}
	return embedding
}

// TestPeople returns sample people for testing.
func TestPeople() []storage.Person {
	now := time.Now()
	telegramID := int64(987654321)
	return []storage.Person{
		{
			ID:          1,
			UserID:      TestUserID,
			DisplayName: "Alice Smith",
			Username:    Ptr("alice"),
			Aliases:     []string{"ally", "als"},
			Circle:      "Friends",
			Bio:         "Software engineer at TechCorp, loves hiking",
			TelegramID:  &telegramID,
			FirstSeen:   now.Add(-72 * time.Hour),
			LastSeen:    now.Add(-1 * time.Hour),
		},
		{
			ID:          2,
			UserID:      TestUserID,
			DisplayName: "Bob Johnson",
			Username:    nil, // no username
			Aliases:     []string{"bobby"},
			Circle:      "Family",
			Bio:         "College friend, studying computer science",
			TelegramID:  nil,
			FirstSeen:   now.Add(-48 * time.Hour),
			LastSeen:    now.Add(-2 * time.Hour),
		},
		{
			ID:          3,
			UserID:      TestUserID,
			DisplayName: "Carol Williams",
			Username:    Ptr("carol"),
			Aliases:     []string{}, // no aliases
			Circle:      "Work_Inner",
			Bio:         "Colleague from previous job, now at startup",
			TelegramID:  nil,
			FirstSeen:   now.Add(-24 * time.Hour),
			LastSeen:    now,
		},
	}
}

// MockChatResponse creates a mock ChatCompletionResponse with the given content.
// Token counts are set to reasonable defaults (150/30/180).
func MockChatResponse(content string) openrouter.ChatCompletionResponse {
	return MockChatResponseWithTokens(content, 150, 30)
}

// MockChatResponseWithTokens creates a mock ChatCompletionResponse with the given content
// and token counts. TotalTokens is calculated automatically.
func MockChatResponseWithTokens(content string, promptTokens, completionTokens int) openrouter.ChatCompletionResponse {
	var resp openrouter.ChatCompletionResponse
	resp.Choices = append(resp.Choices, struct {
		Message struct {
			Role             string                `json:"role"`
			Content          string                `json:"content"`
			ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
			Reasoning        string                `json:"reasoning,omitempty"`
			ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason,omitempty"`
		Index        int    `json:"index"`
	}{
		Message: struct {
			Role             string                `json:"role"`
			Content          string                `json:"content"`
			ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
			Reasoning        string                `json:"reasoning,omitempty"`
			ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
		}{
			Role:    "assistant",
			Content: content,
		},
	})
	resp.Usage.PromptTokens = promptTokens
	resp.Usage.CompletionTokens = completionTokens
	resp.Usage.TotalTokens = promptTokens + completionTokens
	return resp
}
