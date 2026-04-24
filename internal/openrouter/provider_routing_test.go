package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProviderRoutingSerialization verifies the JSON shape matches what
// OpenRouter's provider-routing API expects. Guards against silent drift
// if the struct tags change.
func TestProviderRoutingSerialization(t *testing.T) {
	falseVal := false
	trueVal := true

	tests := []struct {
		name     string
		routing  ProviderRouting
		wantJSON string
	}{
		{
			name:     "order only",
			routing:  ProviderRouting{Order: []string{"Google", "Google AI Studio"}},
			wantJSON: `{"order":["Google","Google AI Studio"]}`,
		},
		{
			name:     "order with strict fallback off",
			routing:  ProviderRouting{Order: []string{"Google"}, AllowFallbacks: &falseVal},
			wantJSON: `{"order":["Google"],"allow_fallbacks":false}`,
		},
		{
			name:     "order with explicit fallback on",
			routing:  ProviderRouting{Order: []string{"Google"}, AllowFallbacks: &trueVal},
			wantJSON: `{"order":["Google"],"allow_fallbacks":true}`,
		},
		{
			name:     "empty routing omits fields",
			routing:  ProviderRouting{},
			wantJSON: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.routing)
			assert.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(got))
		})
	}
}

// TestChatCompletionRequestProviderInjection verifies the client-level
// default is injected when the request has no Provider set, and that a
// request-level Provider takes precedence over the default.
func TestChatCompletionRequestProviderInjection(t *testing.T) {
	tests := []struct {
		name            string
		defaultProvider *ProviderRouting
		reqProvider     *ProviderRouting
		wantOrder       []string
	}{
		{
			name:            "default applied when request has none",
			defaultProvider: &ProviderRouting{Order: []string{"Google", "Google AI Studio"}},
			reqProvider:     nil,
			wantOrder:       []string{"Google", "Google AI Studio"},
		},
		{
			name:            "request-level wins over default",
			defaultProvider: &ProviderRouting{Order: []string{"Google"}},
			reqProvider:     &ProviderRouting{Order: []string{"Anthropic"}},
			wantOrder:       []string{"Anthropic"},
		},
		{
			name:            "no default, no request — provider field absent",
			defaultProvider: nil,
			reqProvider:     nil,
			wantOrder:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&capturedBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"1","model":"m","provider":"Google","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer server.Close()

			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			client, err := NewClientWithBaseURL(logger, "k", "", server.URL+"/api/v1", tt.defaultProvider)
			assert.NoError(t, err)

			_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
				Model:    "m",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Provider: tt.reqProvider,
			})
			assert.NoError(t, err)

			if tt.wantOrder == nil {
				_, hasProvider := capturedBody["provider"]
				assert.False(t, hasProvider, "provider field should be absent from serialized body")
				return
			}

			provider, ok := capturedBody["provider"].(map[string]any)
			assert.True(t, ok, "provider field should be present as an object")
			orderRaw, ok := provider["order"].([]any)
			assert.True(t, ok, "provider.order should be a list")
			got := make([]string, len(orderRaw))
			for i, v := range orderRaw {
				got[i] = v.(string)
			}
			assert.Equal(t, tt.wantOrder, got)
		})
	}
}

// TestChatCompletionResponseExposesProvider verifies the actual provider
// name in the response body is parsed into ChatCompletionResponse.Provider
// so fallbacks are visible in logs.
func TestChatCompletionResponseExposesProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","model":"m","provider":"Google AI Studio","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "k", "", server.URL+"/api/v1", nil)
	assert.NoError(t, err)

	resp, err := client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	assert.NoError(t, err)
	assert.Equal(t, "Google AI Studio", resp.Provider)
}

// TestEmbeddingRequestProviderInjection verifies the default provider is
// also injected for embedding requests.
func TestEmbeddingRequestProviderInjection(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"m"}`))
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	def := &ProviderRouting{Order: []string{"Google"}}
	client, err := NewClientWithBaseURL(logger, "k", "", server.URL+"/api/v1", def)
	assert.NoError(t, err)

	_, err = client.CreateEmbeddings(context.Background(), EmbeddingRequest{
		Model: "m",
		Input: []string{"hello"},
	})
	assert.NoError(t, err)

	provider, ok := capturedBody["provider"].(map[string]any)
	assert.True(t, ok, "provider should be present in embedding request")
	order := provider["order"].([]any)
	assert.Equal(t, "Google", order[0])
}
