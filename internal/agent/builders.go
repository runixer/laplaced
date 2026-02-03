package agent

import (
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

// RequestBuilder builds Request objects for testing.
type RequestBuilder struct {
	req *Request
}

// NewRequestBuilder creates a new RequestBuilder with defaults.
func NewRequestBuilder() *RequestBuilder {
	return &RequestBuilder{
		req: &Request{
			Params: make(map[string]any),
		},
	}
}

// WithShared sets the SharedContext.
func (b *RequestBuilder) WithShared(shared *SharedContext) *RequestBuilder {
	b.req.Shared = shared
	return b
}

// WithQuery sets the query string.
func (b *RequestBuilder) WithQuery(query string) *RequestBuilder {
	b.req.Query = query
	return b
}

// WithMessages sets the conversation messages.
func (b *RequestBuilder) WithMessages(messages []Message) *RequestBuilder {
	b.req.Messages = messages
	return b
}

// WithMedia sets the multimodal media parts.
func (b *RequestBuilder) WithMedia(media []MediaPart) *RequestBuilder {
	b.req.Media = media
	return b
}

// WithParam sets a single parameter.
func (b *RequestBuilder) WithParam(key string, value any) *RequestBuilder {
	b.req.Params[key] = value
	return b
}

// WithParams sets all parameters.
func (b *RequestBuilder) WithParams(params map[string]any) *RequestBuilder {
	b.req.Params = params
	return b
}

// Build returns the constructed Request.
func (b *RequestBuilder) Build() *Request {
	return b.req
}

// ResponseBuilder builds Response objects for testing.
type ResponseBuilder struct {
	resp *Response
}

// NewResponseBuilder creates a new ResponseBuilder with defaults.
func NewResponseBuilder() *ResponseBuilder {
	return &ResponseBuilder{
		resp: &Response{
			Metadata: make(map[string]any),
		},
	}
}

// WithContent sets the text content.
func (b *ResponseBuilder) WithContent(content string) *ResponseBuilder {
	b.resp.Content = content
	return b
}

// WithStructured sets the structured data result.
func (b *ResponseBuilder) WithStructured(data any) *ResponseBuilder {
	b.resp.Structured = data
	return b
}

// WithTokens sets token usage.
func (b *ResponseBuilder) WithTokens(prompt, completion, total int) *ResponseBuilder {
	b.resp.Tokens = TokenUsage{
		Prompt:     prompt,
		Completion: completion,
		Total:      total,
	}
	return b
}

// WithDuration sets the execution duration.
func (b *ResponseBuilder) WithDuration(d time.Duration) *ResponseBuilder {
	b.resp.Duration = d
	return b
}

// WithReasoning sets the reasoning text.
func (b *ResponseBuilder) WithReasoning(reasoning string) *ResponseBuilder {
	b.resp.Reasoning = reasoning
	return b
}

// WithMetadata sets a metadata key-value pair.
func (b *ResponseBuilder) WithMetadata(key string, value any) *ResponseBuilder {
	if b.resp.Metadata == nil {
		b.resp.Metadata = make(map[string]any)
	}
	b.resp.Metadata[key] = value
	return b
}

// Build returns the constructed Response.
func (b *ResponseBuilder) Build() *Response {
	return b.resp
}

// SharedContextBuilder builds SharedContext objects for testing.
type SharedContextBuilder struct {
	shared *SharedContext
}

// NewSharedContextBuilder creates a new SharedContextBuilder with defaults.
func NewSharedContextBuilder() *SharedContextBuilder {
	return &SharedContextBuilder{
		shared: &SharedContext{
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
			InnerCircle:  "<inner_circle>\n</inner_circle>",
			Language:     "en",
			LoadedAt:     time.Now(),
		},
	}
}

// WithUserID sets the user ID.
func (b *SharedContextBuilder) WithUserID(userID int64) *SharedContextBuilder {
	b.shared.UserID = userID
	return b
}

// WithProfile sets the profile string.
func (b *SharedContextBuilder) WithProfile(profile string) *SharedContextBuilder {
	b.shared.Profile = profile
	return b
}

// WithProfileFacts sets the profile facts.
func (b *SharedContextBuilder) WithProfileFacts(facts []storage.Fact) *SharedContextBuilder {
	b.shared.ProfileFacts = facts
	return b
}

// WithRecentTopics sets the recent topics string.
func (b *SharedContextBuilder) WithRecentTopics(topics string) *SharedContextBuilder {
	b.shared.RecentTopics = topics
	return b
}

// WithInnerCircle sets the inner circle string.
func (b *SharedContextBuilder) WithInnerCircle(circle string) *SharedContextBuilder {
	b.shared.InnerCircle = circle
	return b
}

// WithLanguage sets the language code.
func (b *SharedContextBuilder) WithLanguage(lang string) *SharedContextBuilder {
	b.shared.Language = lang
	return b
}

// Build returns the constructed SharedContext.
func (b *SharedContextBuilder) Build() *SharedContext {
	return b.shared
}
