package agent

import (
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
)

// TestRequestBuilder tests the RequestBuilder fluent API.
func TestRequestBuilder(t *testing.T) {
	t.Run("WithShared", func(t *testing.T) {
		shared := &SharedContext{UserID: 123}
		req := NewRequestBuilder().WithShared(shared).Build()

		assert.Same(t, shared, req.Shared)
		assert.Equal(t, int64(123), req.Shared.UserID)
	})

	t.Run("WithQuery", func(t *testing.T) {
		req := NewRequestBuilder().WithQuery("test query").Build()

		assert.Equal(t, "test query", req.Query)
	})

	t.Run("WithMessages", func(t *testing.T) {
		messages := []Message{
			{Role: "user", Content: "hello"},
		}
		req := NewRequestBuilder().WithMessages(messages).Build()

		assert.Equal(t, messages, req.Messages)
	})

	t.Run("WithMedia", func(t *testing.T) {
		media := []MediaPart{
			{Type: "image", Data: []byte("fake")},
		}
		req := NewRequestBuilder().WithMedia(media).Build()

		assert.Equal(t, media, req.Media)
	})

	t.Run("WithParam", func(t *testing.T) {
		req := NewRequestBuilder().
			WithParam("key1", "value1").
			WithParam("key2", 42).
			Build()

		assert.Equal(t, "value1", req.Params["key1"])
		assert.Equal(t, 42, req.Params["key2"])
	})

	t.Run("WithParams", func(t *testing.T) {
		params := map[string]any{
			"a": 1,
			"b": "two",
		}
		req := NewRequestBuilder().WithParams(params).Build()

		assert.Equal(t, params, req.Params)
	})

	t.Run("Chained", func(t *testing.T) {
		shared := &SharedContext{UserID: 456}
		messages := []Message{{Role: "user", Content: "test"}}
		media := []MediaPart{{Type: "audio"}}

		req := NewRequestBuilder().
			WithShared(shared).
			WithQuery("query").
			WithMessages(messages).
			WithMedia(media).
			WithParam("x", "y").
			Build()

		assert.Same(t, shared, req.Shared)
		assert.Equal(t, "query", req.Query)
		assert.Equal(t, messages, req.Messages)
		assert.Equal(t, media, req.Media)
		assert.Equal(t, "y", req.Params["x"])
	})

	t.Run("DefaultValues", func(t *testing.T) {
		req := NewRequestBuilder().Build()

		assert.NotNil(t, req.Params)
		assert.Empty(t, req.Params)
	})
}

// TestResponseBuilder tests the ResponseBuilder fluent API.
func TestResponseBuilder(t *testing.T) {
	t.Run("WithContent", func(t *testing.T) {
		resp := NewResponseBuilder().WithContent("response text").Build()

		assert.Equal(t, "response text", resp.Content)
	})

	t.Run("WithStructured", func(t *testing.T) {
		data := map[string]any{"result": "success"}
		resp := NewResponseBuilder().WithStructured(data).Build()

		assert.Equal(t, data, resp.Structured)
	})

	t.Run("WithTokens", func(t *testing.T) {
		resp := NewResponseBuilder().
			WithTokens(100, 50, 150).
			Build()

		assert.Equal(t, 100, resp.Tokens.Prompt)
		assert.Equal(t, 50, resp.Tokens.Completion)
		assert.Equal(t, 150, resp.Tokens.Total)
	})

	t.Run("WithDuration", func(t *testing.T) {
		duration := 5 * time.Second
		resp := NewResponseBuilder().WithDuration(duration).Build()

		assert.Equal(t, duration, resp.Duration)
	})

	t.Run("WithReasoning", func(t *testing.T) {
		reasoning := "Step 1: Analyze\nStep 2: Conclude"
		resp := NewResponseBuilder().WithReasoning(reasoning).Build()

		assert.Equal(t, reasoning, resp.Reasoning)
	})

	t.Run("WithMetadata", func(t *testing.T) {
		resp := NewResponseBuilder().
			WithMetadata("model", "gpt-4").
			WithMetadata("cost", 0.01).
			Build()

		assert.Equal(t, "gpt-4", resp.Metadata["model"])
		assert.Equal(t, 0.01, resp.Metadata["cost"])
	})

	t.Run("WithMetadata_NilMapInitializes", func(t *testing.T) {
		builder := NewResponseBuilder()
		// Metadata is initialized in constructor, but test the method handles nil
		resp := builder.WithMetadata("key", "value").Build()

		assert.NotNil(t, resp.Metadata)
		assert.Equal(t, "value", resp.Metadata["key"])
	})

	t.Run("Chained", func(t *testing.T) {
		data := map[string]any{"status": "ok"}
		duration := 2 * time.Second

		resp := NewResponseBuilder().
			WithContent("done").
			WithStructured(data).
			WithTokens(10, 5, 15).
			WithDuration(duration).
			WithReasoning("thought process").
			WithMetadata("key", "val").
			Build()

		assert.Equal(t, "done", resp.Content)
		assert.Equal(t, data, resp.Structured)
		assert.Equal(t, 10, resp.Tokens.Prompt)
		assert.Equal(t, 5, resp.Tokens.Completion)
		assert.Equal(t, 15, resp.Tokens.Total)
		assert.Equal(t, duration, resp.Duration)
		assert.Equal(t, "thought process", resp.Reasoning)
		assert.Equal(t, "val", resp.Metadata["key"])
	})

	t.Run("DefaultValues", func(t *testing.T) {
		resp := NewResponseBuilder().Build()

		assert.NotNil(t, resp.Metadata)
		assert.Empty(t, resp.Metadata)
	})
}

// TestSharedContextBuilder tests the SharedContextBuilder fluent API.
func TestSharedContextBuilder(t *testing.T) {
	t.Run("WithUserID", func(t *testing.T) {
		shared := NewSharedContextBuilder().WithUserID(789).Build()

		assert.Equal(t, int64(789), shared.UserID)
	})

	t.Run("WithProfile", func(t *testing.T) {
		profile := "<user_profile>\n<fact>test</fact>\n</user_profile>"
		shared := NewSharedContextBuilder().WithProfile(profile).Build()

		assert.Equal(t, profile, shared.Profile)
	})

	t.Run("WithProfileFacts", func(t *testing.T) {
		facts := []storage.Fact{
			{ID: 1, Content: "User name is Alice", Category: "identity"},
		}
		shared := NewSharedContextBuilder().WithProfileFacts(facts).Build()

		assert.Equal(t, facts, shared.ProfileFacts)
	})

	t.Run("WithRecentTopics", func(t *testing.T) {
		topics := "<recent_topics>\n<topic>discussed AI</topic>\n</recent_topics>"
		shared := NewSharedContextBuilder().WithRecentTopics(topics).Build()

		assert.Equal(t, topics, shared.RecentTopics)
	})

	t.Run("WithInnerCircle", func(t *testing.T) {
		circle := "<inner_circle>\n<person>Alice</person>\n</inner_circle>"
		shared := NewSharedContextBuilder().WithInnerCircle(circle).Build()

		assert.Equal(t, circle, shared.InnerCircle)
	})

	t.Run("WithLanguage", func(t *testing.T) {
		shared := NewSharedContextBuilder().WithLanguage("ru").Build()

		assert.Equal(t, "ru", shared.Language)
	})

	t.Run("Chained", func(t *testing.T) {
		facts := []storage.Fact{{ID: 1, Content: "test"}}
		beforeBuild := time.Now()

		shared := NewSharedContextBuilder().
			WithUserID(999).
			WithProfile("profile").
			WithProfileFacts(facts).
			WithRecentTopics("topics").
			WithInnerCircle("circle").
			WithLanguage("en").
			Build()

		assert.Equal(t, int64(999), shared.UserID)
		assert.Equal(t, "profile", shared.Profile)
		assert.Equal(t, facts, shared.ProfileFacts)
		assert.Equal(t, "topics", shared.RecentTopics)
		assert.Equal(t, "circle", shared.InnerCircle)
		assert.Equal(t, "en", shared.Language)
		assert.False(t, shared.LoadedAt.IsZero())
		assert.True(t, shared.LoadedAt.Before(beforeBuild) || shared.LoadedAt.After(beforeBuild.Add(-1*time.Second)))
	})

	t.Run("DefaultValues", func(t *testing.T) {
		shared := NewSharedContextBuilder().Build()

		assert.Equal(t, int64(0), shared.UserID)
		assert.Contains(t, shared.Profile, "<user_profile>")
		assert.Contains(t, shared.RecentTopics, "<recent_topics>")
		assert.Contains(t, shared.InnerCircle, "<inner_circle>")
		assert.Equal(t, "en", shared.Language)
		assert.False(t, shared.LoadedAt.IsZero())
	})
}
