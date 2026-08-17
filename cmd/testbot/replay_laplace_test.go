package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrepareBody_StrictEvalOverridesPreserveFrozenConversation(t *testing.T) {
	t.Parallel()

	original := `{
  "model":"google/gemini-3.6-flash",
  "user":"production-user-id",
  "trace":{"trace_id":"production-trace"},
  "stream":false,
  "reasoning":{"effort":"medium"},
  "provider":{"order":["Google"]},
  "tools":[{"type":"function","function":{"name":"search"}}],
  "messages":[
    {"role":"system","content":"policy"},
    {"role":"user","content":"question"},
    {"role":"assistant","content":null,"tool_calls":[{"id":"c1"}],"reasoning_details":[{"type":"reasoning.encrypted","data":"opaque"}]},
    {"role":"tool","tool_call_id":"c1","content":"frozen result"}
  ]
}`
	noFallback := false
	requireParams := true
	zdr := true
	body, err := prepareBody(original, variantSpec{
		Model:               "openai/gpt-5.6-luna",
		ReasoningEffort:     "high",
		ProviderOnly:        []string{"OpenAI"},
		AllowFallbacks:      &noFallback,
		DataCollection:      "deny",
		ZeroDataRetention:   &zdr,
		RequireParameters:   &requireParams,
		ForceToolChoiceNone: true,
		MaxTokens:           8192,
	}, t.TempDir(), "", false)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, "openai/gpt-5.6-luna", got["model"])
	require.NotContains(t, got, "user")
	require.NotContains(t, got, "trace")
	require.Equal(t, false, got["stream"])
	require.Equal(t, "none", got["tool_choice"])
	require.Contains(t, got, "tools")
	require.Equal(t, float64(8192), got["max_tokens"])
	require.Equal(t, map[string]any{"effort": "high"}, got["reasoning"])
	require.Equal(t, map[string]any{
		"only":               []any{"OpenAI"},
		"allow_fallbacks":    false,
		"data_collection":    "deny",
		"zdr":                true,
		"require_parameters": true,
	}, got["provider"])

	messages := got["messages"].([]any)
	assistant := messages[2].(map[string]any)
	require.Equal(t, []any{map[string]any{
		"type": "reasoning.encrypted",
		"data": "opaque",
	}}, assistant["reasoning_details"])
	require.Equal(t, "frozen result", messages[3].(map[string]any)["content"])
}

func TestPrepareBody_AppliesTraceDerivedToolChoiceTransforms(t *testing.T) {
	t.Parallel()

	appendix := "<artifact_delivery>Call send_artifacts only for an explicit delivery request.</artifact_delivery>"
	current := "Send the second image as an original file."
	original := `{
      "model":"google/gemini-3.7-flash",
      "messages":[
        {"role":"system","content":[{"type":"text","text":"base policy"}]},
        {"role":"user","content":[{"type":"text","text":"retrieved context"}]},
        {"role":"assistant","content":"earlier answer"},
        {"role":"user","content":[
          {"type":"text","text":"captured current query"},
          {"type":"text","text":"📷 current-media marker"},
          {"type":"file","file":{"filename":"photo.jpg","file_data":"data:image/jpeg;base64,AA=="}}
        ]}
      ],
      "tools":[{"type":"function","function":{"name":"search","description":"old search","parameters":{"type":"object"}}}]
    }`
	searchReplacement := replayVariantTool{
		Type: "function",
		Function: replayVariantToolFunction{
			Name: "search", Description: "new search", Parameters: map[string]any{"type": "object"},
		},
	}
	sendArtifacts := replayVariantTool{
		Type: "function",
		Function: replayVariantToolFunction{
			Name: "send_artifacts", Description: "Deliver available artifacts.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"items": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"artifact_id": map[string]any{"type": "integer"},
								"mode":        map[string]any{"type": "string", "enum": []any{"preview", "original", "preview_and_original"}},
							},
							"required":             []any{"artifact_id", "mode"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []any{"items"},
				"additionalProperties": false,
			},
		},
	}

	body, err := prepareBody(original, variantSpec{
		AppendSystem:       &appendix,
		SetCurrentUserText: &current,
		UpsertTools:        []replayVariantTool{searchReplacement, sendArtifacts},
	}, t.TempDir(), "", false)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	messages := got["messages"].([]any)
	systemParts := messages[0].(map[string]any)["content"].([]any)
	require.Equal(t, "base policy", systemParts[0].(map[string]any)["text"])
	require.Equal(t, appendix, systemParts[1].(map[string]any)["text"])
	require.Equal(t, "retrieved context", messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
	currentParts := messages[3].(map[string]any)["content"].([]any)
	require.Equal(t, current, currentParts[0].(map[string]any)["text"])
	require.Equal(t, "📷 current-media marker", currentParts[1].(map[string]any)["text"])
	require.Equal(t, "photo.jpg", currentParts[2].(map[string]any)["file"].(map[string]any)["filename"])

	tools := got["tools"].([]any)
	require.Len(t, tools, 2)
	require.Equal(t, "search", tools[0].(map[string]any)["function"].(map[string]any)["name"])
	require.Equal(t, "new search", tools[0].(map[string]any)["function"].(map[string]any)["description"])
	require.Equal(t, "send_artifacts", tools[1].(map[string]any)["function"].(map[string]any)["name"])

	assessments, err := validateToolCalls(body, []replayToolCall{{
		ID: "call-1", Type: "function", Name: "send_artifacts", EnvelopeValid: true,
		Arguments: `{"items":[{"artifact_id":42,"mode":"original"}]}`,
	}})
	require.NoError(t, err)
	require.Len(t, assessments, 1)
	require.True(t, assessments[0].ArgumentsSchemaValid)
}

func TestPrepareBody_SetCurrentUserTextPreservesStringContextEnvelope(t *testing.T) {
	t.Parallel()

	current := "Пришли второй снимок в оригинале."
	original := `{
      "messages":[
        {"role":"system","content":"policy"},
        {"role":"user","content":"Текущая дата: 2026-08-18\n\n<current_messages>\n[Assistant]: earlier answer\n</current_messages>\n\n<artifact_candidates>\n[Artifact:41] first.jpg\n[Artifact:42] second.jpg\n</artifact_candidates>\n\n<user_query>\nстарый запрос\n</user_query>"}
      ]
    }`

	body, err := prepareBody(original, variantSpec{SetCurrentUserText: &current}, t.TempDir(), "", false)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	messages := got["messages"].([]any)
	content := messages[1].(map[string]any)["content"].(string)
	require.Contains(t, content, "<current_messages>\n[Assistant]: earlier answer\n</current_messages>")
	require.Contains(t, content, "<artifact_candidates>\n[Artifact:41] first.jpg\n[Artifact:42] second.jpg\n</artifact_candidates>")
	require.Contains(t, content, "<user_query>\n"+current+"\n</user_query>")
	require.NotContains(t, content, "старый запрос")
}

func TestPrepareBody_RejectsInvalidTraceDerivedTransforms(t *testing.T) {
	t.Parallel()

	text := "x"
	blank := " \n\t"
	validTool := replayVariantTool{
		Type: "function",
		Function: replayVariantToolFunction{
			Name: "send_artifacts", Description: "Deliver artifacts.", Parameters: map[string]any{"type": "object"},
		},
	}
	invalidType := validTool
	invalidType.Type = "custom"
	invalidName := validTool
	invalidName.Function.Name = "send artifacts"
	missingDescription := validTool
	missingDescription.Function.Description = ""
	missingParameters := validTool
	missingParameters.Function.Parameters = nil
	nonObjectParameters := validTool
	nonObjectParameters.Function.Parameters = map[string]any{"type": "array"}
	base := `{"messages":[{"role":"system","content":"policy"},{"role":"user","content":"question"}],"tools":[]}`
	tests := []struct {
		name      string
		body      string
		spec      variantSpec
		errSubstr string
	}{
		{name: "ambiguous user transform", body: base, spec: variantSpec{SetUserText: &text, SetCurrentUserText: &text}, errSubstr: "mutually exclusive"},
		{name: "blank system appendix", body: base, spec: variantSpec{AppendSystem: &blank}, errSubstr: "must not be empty"},
		{name: "disabled and upserted tools", body: base, spec: variantSpec{DisableTools: true, UpsertTools: []replayVariantTool{validTool}}, errSubstr: "mutually exclusive"},
		{name: "invalid tool type", body: base, spec: variantSpec{UpsertTools: []replayVariantTool{invalidType}}, errSubstr: "type must be function"},
		{name: "invalid tool name", body: base, spec: variantSpec{UpsertTools: []replayVariantTool{invalidName}}, errSubstr: "function.name"},
		{name: "missing description", body: base, spec: variantSpec{UpsertTools: []replayVariantTool{missingDescription}}, errSubstr: "description"},
		{name: "missing parameter schema", body: base, spec: variantSpec{UpsertTools: []replayVariantTool{missingParameters}}, errSubstr: "parameters"},
		{name: "non-object parameter schema", body: base, spec: variantSpec{UpsertTools: []replayVariantTool{nonObjectParameters}}, errSubstr: "parameters.type"},
		{name: "duplicate upsert", body: base, spec: variantSpec{UpsertTools: []replayVariantTool{validTool, validTool}}, errSubstr: "duplicate tool name"},
		{name: "captured tools not array", body: `{"messages":[],"tools":{}}`, spec: variantSpec{UpsertTools: []replayVariantTool{validTool}}, errSubstr: "captured tools must be an array"},
		{name: "malformed captured tool", body: `{"messages":[],"tools":[null]}`, spec: variantSpec{UpsertTools: []replayVariantTool{validTool}}, errSubstr: "tool must be an object"},
		{name: "duplicate captured tool", body: `{"messages":[],"tools":[{"type":"function","function":{"name":"search"}},{"type":"function","function":{"name":"search"}}]}`, spec: variantSpec{UpsertTools: []replayVariantTool{validTool}}, errSubstr: "duplicate name"},
		{name: "no system", body: `{"messages":[{"role":"user","content":"q"}]}`, spec: variantSpec{AppendSystem: &text}, errSubstr: "exactly one"},
		{name: "multiple systems", body: `{"messages":[{"role":"system","content":"a"},{"role":"system","content":"b"}]}`, spec: variantSpec{AppendSystem: &text}, errSubstr: "exactly one"},
		{name: "no current user", body: `{"messages":[{"role":"system","content":"a"}]}`, spec: variantSpec{SetCurrentUserText: &text}, errSubstr: "requires a captured user"},
		{name: "current user without text", body: `{"messages":[{"role":"user","content":[{"type":"file","file":{}}]}]}`, spec: variantSpec{SetCurrentUserText: &text}, errSubstr: "requires a text part"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := prepareBody(tt.body, tt.spec, t.TempDir(), "", false)
			require.ErrorContains(t, err, tt.errSubstr)
		})
	}
}

func TestLoadVariantSpecs_RejectsUnknownUpsertToolFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "variants.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
      "candidate": {
        "upsert_tools": [{
          "type": "function",
          "function": {
            "name": "send_artifacts",
            "description": "Deliver artifacts.",
            "parameters": {"type": "object"},
            "unexpected": true
          }
        }]
      }
    }`), 0o600))

	_, err := loadVariantSpecs(path)
	require.ErrorContains(t, err, "unknown field")
}

func TestValidateReplayVariantNamesRejectsOutputCollisions(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateReplayVariantNames([]string{"baseline", "candidate"}))
	require.ErrorContains(t, validateReplayVariantNames([]string{"a/b", "a_b"}), "collide")
	require.ErrorContains(t, validateReplayVariantNames([]string{"baseline", "baseline"}), "collide")
}

func TestReadBoundedReplayResponse(t *testing.T) {
	t.Parallel()
	got, err := readBoundedReplayResponse(strings.NewReader("12345"), 5)
	require.NoError(t, err)
	require.Equal(t, []byte("12345"), got)

	_, err = readBoundedReplayResponse(strings.NewReader("123456"), 5)
	require.ErrorContains(t, err, "exceeds 5-byte limit")
}

func TestPrepareBody_TopLevelPrivacyIdentifiersRequireExplicitOptIn(t *testing.T) {
	t.Parallel()
	original := `{
	  "model":"m","stream":false,
	  "user":"production-user","session_id":"production-session",
	  "safety_identifier":"production-safety","metadata":{"tenant":"private"},
	  "messages":[]
	}`
	want := map[string]any{
		"user":              "production-user",
		"session_id":        "production-session",
		"safety_identifier": "production-safety",
		"metadata":          map[string]any{"tenant": "private"},
	}
	for _, tt := range []struct {
		name string
		keep bool
	}{
		{name: "strip by default", keep: false},
		{name: "explicitly keep", keep: true},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body, err := prepareBody(original, variantSpec{}, t.TempDir(), "", tt.keep)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(body, &got))
			for key, value := range want {
				if tt.keep {
					require.Equal(t, value, got[key])
				} else {
					require.NotContains(t, got, key)
				}
			}
		})
	}
}

func TestPrepareBody_CanStripNonPortableMessageReasoningDetails(t *testing.T) {
	t.Parallel()
	body, err := prepareBody(`{
      "model":"m","messages":[
        {"role":"user","content":"question"},
        {"role":"assistant","content":null,"reasoning_details":[{"type":"reasoning.encrypted","data":"opaque"}],"tool_calls":[{"id":"c1"}]},
        {"role":"tool","tool_call_id":"c1","content":"frozen result"}
      ]
    }`, variantSpec{StripMessageReasoning: true}, t.TempDir(), "", false)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	messages := got["messages"].([]any)
	require.NotContains(t, messages[1].(map[string]any), "reasoning_details")
	require.Contains(t, messages[1].(map[string]any), "tool_calls")
	require.Equal(t, "frozen result", messages[2].(map[string]any)["content"])
}

func TestPrepareBody_ForceNoneRequiresFrozenToolResult(t *testing.T) {
	t.Parallel()
	_, err := prepareBody(`{"model":"m","messages":[{"role":"user","content":"q"}]}`,
		variantSpec{ForceToolChoiceNone: true}, t.TempDir(), "", false)
	require.ErrorContains(t, err, "requires at least one frozen tool result")
}

func TestPrepareBody_RejectsStreamingCapture(t *testing.T) {
	t.Parallel()
	_, err := prepareBody(`{"model":"m","stream":true,"messages":[]}`,
		variantSpec{}, t.TempDir(), "", false)
	require.ErrorContains(t, err, "streaming request")
}

func TestPrepareBody_RemovesCapturedFallbackRoute(t *testing.T) {
	t.Parallel()
	original := `{
      "model":"captured/model","models":["captured/fallback"],"route":"fallback",
      "messages":[{"role":"user","content":"question"}]
    }`
	body, err := prepareBody(original, variantSpec{Model: "pinned/model"}, "", "", false)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, "pinned/model", got["model"])
	require.NotContains(t, got, "models")
	require.NotContains(t, got, "route")
}

func TestPrepareBody_RehydratesAndVerifiesNestedMedia(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := []byte("real media bytes")
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	require.NoError(t, os.WriteFile(filepath.Join(dir, hash+".bin"), raw, 0o600))
	placeholder := fmt.Sprintf("redacted:sha256:%s:image/png:%d", hash, len(raw))
	original := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, placeholder)

	body, err := prepareBody(original, variantSpec{}, dir, "", false)
	require.NoError(t, err)
	require.NotContains(t, string(body), "redacted:sha256:")
	require.Contains(t, string(body), "data:image/png;base64,")
}

func TestPrepareBody_ConvertsImageFileToOpenAIImageURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := []byte("image bytes")
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	require.NoError(t, os.WriteFile(filepath.Join(dir, hash+".jpg"), raw, 0o600))
	placeholder := fmt.Sprintf("redacted:sha256:%s:image/jpeg:%d", hash, len(raw))
	original := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"x.jpg","file_data":%q}}]}]}`, placeholder)

	body, err := prepareBody(original, variantSpec{ImageInputFormat: "openai"}, dir, "", false)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	messages := parsed["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	part := content[0].(map[string]any)
	require.Equal(t, "image_url", part["type"])
	require.NotContains(t, part, "file")
	url := part["image_url"].(map[string]any)["url"].(string)
	require.Contains(t, url, "data:image/jpeg;base64,")
}

func TestPostOnce_RecordsOpenRouterUsageAndProtocolFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "false", r.Header.Get("X-OpenRouter-Cache"))
		w.Header().Set("X-OpenRouter-Cache-Status", "MISS")
		_, _ = w.Write([]byte(`{
          "id":"gen-1","model":"openai/gpt-5.6-luna","provider":"OpenAI",
          "choices":[{"finish_reason":"tool_calls","native_finish_reason":"tool_use","message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"search","arguments":"{}"}}]}}],
          "usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"cost":0.000123,"is_byok":false,
            "prompt_tokens_details":{"cached_tokens":10,"cache_write_tokens":2,"audio_tokens":3,"video_tokens":4},
            "completion_tokens_details":{"reasoning_tokens":15,"image_tokens":5,"audio_tokens":1},
            "cost_details":{"upstream_inference_cost":0.00009}}
        }`))
	}))
	defer server.Close()

	outDir := t.TempDir()
	body := []byte(`{"model":"openai/gpt-5.6-luna","tool_choice":"none","provider":{"only":["OpenAI"]}}`)
	got := postOnce(context.Background(), server.URL, "secret", body, "../unsafe label", 1, outDir, time.Second)
	require.Empty(t, got.Err)
	require.Equal(t, "OpenAI", got.Provider)
	require.Equal(t, 100, got.PromptTokens)
	require.Equal(t, 10, got.CachedTokens)
	require.Equal(t, 2, got.CacheWriteTokens)
	require.Equal(t, 15, got.ReasoningTokens)
	require.Equal(t, 1, got.ToolCalls)
	require.True(t, got.ProtocolFailure)
	require.False(t, got.RouteMismatch)
	require.Equal(t, "MISS", got.CacheStatus)
	require.NotNil(t, got.CostUSD)
	require.InDelta(t, 0.000123, *got.CostUSD, 1e-12)
	require.NotNil(t, got.UpstreamCostUSD)
	require.InDelta(t, 0.00009, *got.UpstreamCostUSD, 1e-12)
	require.Equal(t, filepath.Join(outDir, "unsafe_label_run1.md"), got.ContentFile)
}

func TestPostOnce_TreatsChoiceFinishErrorAsGenerationFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "id":"gen-1","model":"m","provider":"Provider",
          "choices":[{"finish_reason":"error","native_finish_reason":"SAFETY","message":{"content":null}}],
          "usage":{}
        }`))
	}))
	defer server.Close()

	got := postOnce(context.Background(), server.URL, "secret", []byte(`{"model":"m"}`), "candidate", 1, t.TempDir(), time.Second)
	require.Equal(t, "error", got.FinishReason)
	require.Equal(t, "SAFETY", got.NativeFinish)
	require.Equal(t, "generation finished with an error", got.Err)
	require.NotEmpty(t, got.ContentFile)
}

func TestPostOnce_TreatsIncompleteFinishAsGenerationFailure(t *testing.T) {
	t.Parallel()
	for _, finish := range []string{"", "length", "content_filter"} {
		finish := finish
		t.Run(finish, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{
                  "id":"gen-1","model":"m","provider":"Provider",
                  "choices":[{"finish_reason":%q,"message":{"content":"partial"}}],
                  "usage":{}
                }`, finish)
			}))
			defer server.Close()

			got := postOnce(context.Background(), server.URL, "secret", []byte(`{"model":"m"}`), "candidate", 1, t.TempDir(), time.Second)
			require.Equal(t, finish, got.FinishReason)
			require.Equal(t, "generation finished incompletely", got.Err)
			require.NotEmpty(t, got.ContentFile)
		})
	}
}

func TestPostOnce_ChoiceErrorNullIsNotFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
          "id":"gen-1","model":"m","provider":"Provider",
          "choices":[{"finish_reason":"stop","error":null,"message":{"content":"ok"}}],
          "usage":{}
        }`))
	}))
	defer server.Close()

	got := postOnce(context.Background(), server.URL, "secret", []byte(`{"model":"m"}`), "candidate", 1, t.TempDir(), time.Second)
	require.Empty(t, got.Err)
	require.Equal(t, "stop", got.FinishReason)
	require.False(t, got.RouteMismatch)
}

func TestPostOnce_Non2xxIsHTTPErrorWithoutRouteMismatch(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "JSON rate limit", status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`},
		{name: "non-JSON gateway error", status: http.StatusBadGateway, body: `upstream unavailable`},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			request := []byte(`{"model":"requested-model","provider":{"only":["PinnedProvider"]}}`)
			got := postOnce(context.Background(), server.URL, "secret", request, "candidate", 1, t.TempDir(), time.Second)
			require.Equal(t, tt.status, got.HTTPStatus)
			require.Equal(t, fmt.Sprintf("HTTP %d API error (see raw response)", tt.status), got.Err)
			require.False(t, got.RouteMismatch)
			require.NotEmpty(t, got.RawFile)
		})
	}
}

func TestPostOnce_Unusable2xxResponseDoesNotBecomeRouteMismatch(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		body    string
		errWant string
	}{
		{name: "top-level API error", body: `{"error":{"message":"blocked"}}`, errWant: "API error"},
		{name: "no choices", body: `{"choices":[]}`, errWant: "no choices"},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			request := []byte(`{"model":"requested-model","provider":{"only":["PinnedProvider"]}}`)
			got := postOnce(context.Background(), server.URL, "secret", request, "candidate", 1, t.TempDir(), time.Second)
			require.Contains(t, got.Err, tt.errWant)
			require.False(t, got.RouteMismatch)
		})
	}
}

func TestPostOnce_FlagsResponseModelMismatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
          "id":"gen-1","model":"other-model","provider":"Provider",
          "choices":[{"finish_reason":"stop","message":{"content":"ok"}}],
          "usage":{}
        }`))
	}))
	defer server.Close()

	got := postOnce(context.Background(), server.URL, "secret", []byte(`{"model":"requested-model"}`), "candidate", 1, t.TempDir(), time.Second)
	require.Empty(t, got.Err)
	require.True(t, got.RouteMismatch)
}

func TestCollectGenTurns_PairsRequestAndResponseChronologically(t *testing.T) {
	t.Parallel()
	trace := capturedToolTrace(t, []capturedToolTraceTurn{
		{Start: 20, Request: `{"model":"m","messages":[{"role":"user","content":"second"}]}`, Response: directResponse("second")},
		{Start: 10, Request: `{"model":"m","messages":[{"role":"user","content":"first"}]}`, Response: directResponse("first")},
	})

	turns, err := collectGenTurns(trace, "laplace")
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.Equal(t, 0, turns[0].Index)
	require.Contains(t, turns[0].RequestBody, "first")
	require.Contains(t, turns[0].ResponseBody, "first")
	require.Equal(t, int64(20), turns[1].Start)
}

func TestPrepareBody_DropsOnlySelectedMediaMIMEBeforeRehydration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	image := []byte("image bytes")
	imageHash := fmt.Sprintf("%x", sha256.Sum256(image))
	require.NoError(t, os.WriteFile(filepath.Join(dir, imageHash+".jpg"), image, 0o600))
	audioHash := fmt.Sprintf("%064x", 7)
	audioPlaceholder := fmt.Sprintf("redacted:sha256:%s:audio/ogg:%d", audioHash, 12)
	imagePlaceholder := fmt.Sprintf("redacted:sha256:%s:image/jpeg:%d", imageHash, len(image))
	original := fmt.Sprintf(`{
      "model":"m","messages":[{"role":"user","content":[
        {"type":"text","text":"transcript"},
        {"type":"file","file":{"filename":"voice.ogg","file_data":%q}},
        {"type":"file","file":{"filename":"photo.jpg","file_data":%q}}
      ]}]}`, audioPlaceholder, imagePlaceholder)

	body, err := prepareBody(original, variantSpec{DropMediaMIMETypes: []string{"audio/ogg"}}, dir, "", false)
	require.NoError(t, err)
	require.NotContains(t, string(body), audioHash)
	require.Contains(t, string(body), "transcript")
	require.Contains(t, string(body), "data:image/jpeg;base64,")
}

func TestValidateToolCalls_DeterministicMalformedUnknownAndSchemaChecks(t *testing.T) {
	t.Parallel()
	request := []byte(`{
      "tools":[{"type":"function","function":{"name":"search","parameters":{
        "type":"object","properties":{
          "query":{"type":"string"},
          "mode":{"type":"string","enum":["fast","deep"]},
          "ids":{"type":"array","items":{"type":"integer"}}
        },"required":["query","mode","ids"],"additionalProperties":false
      }}}]
    }`)
	tests := []struct {
		name        string
		call        replayToolCall
		known       bool
		jsonValid   bool
		schemaValid bool
	}{
		{
			name:  "valid",
			call:  replayToolCall{ID: "c1", Type: "function", Name: "search", Arguments: `{"query":"q","mode":"fast","ids":[1,2]}`, EnvelopeValid: true},
			known: true, jsonValid: true, schemaValid: true,
		},
		{
			name:  "malformed JSON",
			call:  replayToolCall{ID: "c1", Type: "function", Name: "search", Arguments: `{"query":`, EnvelopeValid: true},
			known: true, jsonValid: false, schemaValid: false,
		},
		{
			name:  "unknown tool",
			call:  replayToolCall{ID: "c1", Type: "function", Name: "other", Arguments: `{}`, EnvelopeValid: true},
			known: false, jsonValid: true, schemaValid: false,
		},
		{
			name:  "schema violation",
			call:  replayToolCall{ID: "c1", Type: "function", Name: "search", Arguments: `{"query":1,"mode":"wide","ids":[1.5],"extra":true}`, EnvelopeValid: true},
			known: true, jsonValid: true, schemaValid: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateToolCalls(request, []replayToolCall{tt.call})
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tt.known, got[0].KnownTool)
			require.Equal(t, tt.jsonValid, got[0].ArgumentsJSONValid)
			require.Equal(t, tt.schemaValid, got[0].ArgumentsSchemaValid)
		})
	}
}

func TestValidateReplaySchema_FailsClosedOnUnsupportedAssertions(t *testing.T) {
	t.Parallel()
	for name, schema := range map[string]map[string]any{
		"pattern":           {"type": "string", "pattern": "^x"},
		"union type":        {"type": []any{"string", "null"}},
		"schema additional": {"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"properties no type": {"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		}},
	} {
		name, schema := name, schema
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, supported := validateReplaySchema(map[string]any{"value": json.Number("1")}, schema, "$")
			require.False(t, supported)
		})
	}
}

func TestExactToolCallMatch_CanonicalizesJSONAndIgnoresParallelOrder(t *testing.T) {
	t.Parallel()
	expected := []replayToolCall{
		{ID: "old-1", Type: "function", Name: "search", Arguments: `{"query":"q","limit":2}`, EnvelopeValid: true},
		{ID: "old-2", Type: "function", Name: "read", Arguments: `{"url":"https://example.test"}`, EnvelopeValid: true},
	}
	actual := []toolCallAssessment{
		{Call: replayToolCall{ID: "new-2", Type: "function", Name: "read", Arguments: `{ "url": "https://example.test" }`, EnvelopeValid: true}},
		{Call: replayToolCall{ID: "new-1", Type: "function", Name: "search", Arguments: `{"limit":2,"query":"q"}`, EnvelopeValid: true}},
	}

	matched, annotated := exactToolCallMatch(expected, actual)
	require.True(t, matched)
	require.True(t, annotated[0].ExactCapturedMatch)
	require.True(t, annotated[1].ExactCapturedMatch)
}

func TestToolDecision_CallStopAndMalformed(t *testing.T) {
	t.Parallel()
	valid := toolCallAssessment{
		Call:      replayToolCall{ID: "c1", Type: "function", Name: "search", Arguments: `{}`, EnvelopeValid: true},
		KnownTool: true, ArgumentsJSONValid: true, ArgumentsSchemaValid: true,
		SchemaSupported: true, RuntimeProtocolValid: true,
	}
	require.Equal(t, toolDecisionStop, toolDecision("stop", nil, ""))
	require.Equal(t, toolDecisionCall, toolDecision("tool_calls", []toolCallAssessment{valid}, ""))

	invalidJSON := valid
	invalidJSON.ArgumentsJSONValid = false
	require.Equal(t, toolDecisionMalformed, toolDecision("tool_calls", []toolCallAssessment{invalidJSON}, ""))
	require.Equal(t, toolDecisionMalformed, toolDecision("tool_calls", nil, ""))
	require.Equal(t, toolDecisionMalformed, toolDecision("stop", []toolCallAssessment{valid}, ""))
	require.Equal(t, toolDecisionMalformed, toolDecision("length", nil, ""))
	require.Equal(t, toolDecisionMalformed, toolDecision("", nil, ""))
	require.Equal(t, toolDecisionMalformed, toolDecision("", nil, "generation failed"))
}

func TestFirstReplayAssistantOutputClassifiesProviderErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "top level", raw: `{"error":{"message":"blocked"}}`},
		{name: "no choices", raw: `{"choices":[]}`},
		{name: "choice error", raw: `{"choices":[{"finish_reason":"error","error":{"message":"blocked"},"message":{}}]}`},
		{name: "error finish", raw: `{"choices":[{"finish_reason":"error","message":{}}]}`},
		{name: "missing finish", raw: `{"choices":[{"message":{"content":"partial"}}]}`},
		{name: "length finish", raw: `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := firstReplayAssistantOutput([]byte(tt.raw))
			require.NoError(t, err)
			require.NotEmpty(t, output.GenerationErr)
			require.Equal(t, toolDecisionMalformed, toolDecision(output.FinishReason, nil, output.GenerationErr))
		})
	}
}

func TestAssessToolTurnDoesNotTreatCapturedErrorAsStop(t *testing.T) {
	t.Parallel()

	request := []byte(`{"tools":[]}`)
	expected := replayAssistantOutput{FinishReason: "error", GenerationErr: "generation finished with an error"}
	actual := replayRunResult{FinishReason: "stop"}
	assessment, err := assessToolTurn(request, expected, actual)
	require.NoError(t, err)
	require.Equal(t, toolDecisionMalformed, assessment.ExpectedDecision)
	require.False(t, assessment.ExpectedDecisionAssessable)
	require.Equal(t, toolDecisionStop, assessment.ActualDecision)
	require.False(t, assessment.DecisionMatch)
}

func TestAssessToolTurnExcludesMalformedCapturedCallFromSensitivity(t *testing.T) {
	t.Parallel()
	request := []byte(`{
      "tools":[{"type":"function","function":{"name":"search","parameters":{
        "type":"object","properties":{"query":{"type":"string"}},"required":["query"]
      }}}]
    }`)
	expected := replayAssistantOutput{
		FinishReason: "tool_calls",
		ToolCalls: []replayToolCall{{
			ID: "captured", Type: "function", Name: "search", Arguments: `{"query":1}`, EnvelopeValid: true,
		}},
	}
	actual := replayRunResult{FinishReason: "stop"}
	assessment, err := assessToolTurn(request, expected, actual)
	require.NoError(t, err)
	require.Equal(t, toolDecisionMalformed, assessment.ExpectedDecision)
	require.False(t, assessment.ExpectedDecisionAssessable)
	require.False(t, assessment.DecisionMatch)
}

func TestValidateToolCalls_ValidatesNestedMemoryRuntimeProtocol(t *testing.T) {
	t.Parallel()
	request := []byte(`{
	  "tools":[{"type":"function","function":{"name":"manage_memory","parameters":{
	    "type":"object","properties":{"query":{"type":"string"}},"required":["query"]
	  }}}]
	}`)
	tests := []struct {
		name       string
		arguments  string
		runtimeOK  bool
		errorMatch string
	}{
		{
			name:      "valid batch",
			arguments: `{"query":"{\"operations\":[{\"action\":\"add\",\"content\":\"remember this\"},{\"action\":\"delete\",\"fact_id\":\"Fact:12\"}]}"}`,
			runtimeOK: true,
		},
		{
			name:       "query is not nested JSON",
			arguments:  `{"query":"not-json"}`,
			errorMatch: "invalid nested JSON",
		},
		{
			name:       "update misses fact id",
			arguments:  `{"query":"{\"action\":\"update\",\"content\":\"new\"}"}`,
			errorMatch: "valid fact_id",
		},
		{
			name:       "add misses content",
			arguments:  `{"query":"{\"action\":\"add\"}"}`,
			errorMatch: "non-empty content",
		},
		{
			name:      "executor accepts empty batch",
			arguments: `{"query":"{\"operations\":[]}"}`,
			runtimeOK: true,
		},
		{
			name:      "executor accepts empty update content",
			arguments: `{"query":"{\"action\":\"update\",\"fact_id\":\"Fact:12\",\"content\":\"\"}"}`,
			runtimeOK: true,
		},
		{
			name:       "executor does not trim action",
			arguments:  `{"query":"{\"action\":\" add \",\"content\":\"x\"}"}`,
			errorMatch: "action must be",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := []replayToolCall{{
				ID: "c1", Type: "function", Name: "manage_memory",
				Arguments: tt.arguments, EnvelopeValid: true,
			}}
			got, err := validateToolCalls(request, calls)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.True(t, got[0].ArgumentsSchemaValid)
			require.Equal(t, tt.runtimeOK, got[0].RuntimeProtocolValid)
			if tt.errorMatch != "" {
				require.Contains(t, strings.Join(got[0].RuntimeErrors, " "), tt.errorMatch)
			}
		})
	}
}

func TestParseReplayToolCalls_RejectsNonStringArguments(t *testing.T) {
	t.Parallel()
	raw := []json.RawMessage{json.RawMessage(`{
      "id":"c1","type":"function","function":{"name":"search","arguments":{"query":"q"}}
    }`)}
	calls := parseReplayToolCalls(raw)
	require.Len(t, calls, 1)
	require.False(t, calls[0].EnvelopeValid)
	require.Contains(t, calls[0].ParseError, "must be a JSON string")
}

func TestRunCapturedToolLoopLane_TeacherForcesFrozenTurnsWithoutDispatch(t *testing.T) {
	t.Parallel()
	firstRequest := `{
      "model":"captured-model","stream":false,
      "provider":{"only":["CapturedProvider"]},
      "tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}],
      "messages":[{"role":"user","content":"question"}]
    }`
	secondRequest := `{
      "model":"captured-model","stream":false,
      "provider":{"only":["CapturedProvider"]},
      "tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}],
      "messages":[
        {"role":"user","content":"question"},
        {"role":"assistant","content":null,"tool_calls":[{"id":"captured-call","type":"function","function":{"name":"search","arguments":"{\"query\":\"q\"}"}}]},
        {"role":"tool","tool_call_id":"captured-call","content":"frozen result"}
      ]
    }`
	trace := capturedToolTrace(t, []capturedToolTraceTurn{
		{Start: 10, Request: firstRequest, Response: toolResponse("captured-call", "search", `{"query":"q"}`)},
		{Start: 20, Request: secondRequest, Response: directResponse("answer")},
	})

	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, body)
		call := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = w.Write([]byte(toolResponse("candidate-call", "search", `{"query":"q"}`)))
			return
		}
		_, _ = w.Write([]byte(directResponse("answer")))
	}))
	defer server.Close()

	outDir := t.TempDir()
	blindOut := t.TempDir()
	sharedSpec := variantSpec{}
	results, bundleDirs, keys, failed, generations, err := runCapturedToolLoopLane(
		context.Background(),
		laplaceEvalCase{ID: "case-1", Agent: "laplace", Category: "tools", Subtype: "loop"},
		laplaceEvalLane{Name: "tool-loop", Kind: "captured_tool_loop"},
		trace,
		[]string{"variant-secret"},
		map[string]variantSpec{"variant-secret": {Model: "model", ClearProvider: true}},
		server.URL, "secret", t.TempDir(), outDir, blindOut, sharedSpec, time.Second,
	)
	require.NoError(t, err)
	require.Zero(t, failed)
	require.Equal(t, 2, generations)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].ToolLoop)
	require.Equal(t, 2, results[0].ToolLoop.DecisionMatches)
	require.Equal(t, 1, results[0].ToolLoop.ExactMatches)
	require.Len(t, keys, 2)
	require.Len(t, bundleDirs, 2)
	blindCases := make([]blindEvalCase, 0, len(bundleDirs))
	for _, bundleDir := range bundleDirs {
		manifestBytes, readErr := os.ReadFile(filepath.Join(blindOut, bundleDir, "manifest.json"))
		require.NoError(t, readErr)
		var manifest blindEvalManifest
		require.NoError(t, json.Unmarshal(manifestBytes, &manifest))
		require.Len(t, manifest.Cases, 1)
		require.Len(t, manifest.Cases[0].Candidates, 1)
		blindCases = append(blindCases, manifest.Cases[0])
	}

	mu.Lock()
	require.Len(t, requests, 2)
	require.NotContains(t, string(requests[0]), "frozen result")
	require.Contains(t, string(requests[1]), "frozen result")
	mu.Unlock()
	require.Contains(t, results[0].ToolLoop.Turns[0].Replay.ContentFile, "turn00")
	require.Contains(t, results[0].ToolLoop.Turns[1].Replay.ContentFile, "turn01")

	firstBundleDir := filepath.Join(blindOut, bundleDirs[0])
	firstRequestPacket, err := os.ReadFile(filepath.Join(firstBundleDir, blindCases[0].RequestFile))
	require.NoError(t, err)
	require.NotContains(t, string(firstRequestPacket), "captured-model")
	require.NotContains(t, string(firstRequestPacket), "CapturedProvider")
	require.NotContains(t, string(firstRequestPacket), "expected_decision")
	require.NotContains(t, string(firstRequestPacket), "expected_calls")
	require.NotContains(t, string(firstRequestPacket), "captured-call")
	require.NotContains(t, string(firstRequestPacket), "frozen result")

	secondBundleDir := filepath.Join(blindOut, bundleDirs[1])
	secondRequestPacket, err := os.ReadFile(filepath.Join(secondBundleDir, blindCases[1].RequestFile))
	require.NoError(t, err)
	require.NotContains(t, string(secondRequestPacket), "captured-call")
	require.Contains(t, string(secondRequestPacket), "blind_tool_call_001")
	require.Contains(t, string(secondRequestPacket), "frozen result")

	candidatePacket, err := os.ReadFile(filepath.Join(firstBundleDir, blindCases[0].Candidates[0].ContentFile))
	require.NoError(t, err)
	require.NotContains(t, string(candidatePacket), "variant-secret")
	require.NotContains(t, string(candidatePacket), "candidate-model")
	require.NotContains(t, string(candidatePacket), "decision_match")
	require.NotContains(t, string(candidatePacket), "exact_call_match")
	require.NotContains(t, string(candidatePacket), "exact_captured_match")
	require.NotContains(t, string(candidatePacket), "candidate-call")
	require.NotContains(t, string(candidatePacket), `"id"`)
	require.Contains(t, string(candidatePacket), "actual_decision")
}

type capturedToolTraceTurn struct {
	Start    int64
	Request  string
	Response string
}

func capturedToolTrace(t *testing.T, turns []capturedToolTraceTurn) []byte {
	t.Helper()
	spans := []any{map[string]any{
		"name": "laplace.Execute", "spanId": "agent", "parentSpanId": "", "startTimeUnixNano": "1",
	}}
	for i, turn := range turns {
		spans = append(spans, map[string]any{
			"name":              "llm.CreateChatCompletion",
			"spanId":            fmt.Sprintf("gen-%d", i),
			"parentSpanId":      "agent",
			"startTimeUnixNano": fmt.Sprintf("%d", turn.Start),
			"events": []any{
				map[string]any{"name": "llm.request", "attributes": []any{map[string]any{"key": "body", "value": map[string]any{"stringValue": turn.Request}}}},
				map[string]any{"name": "llm.response", "attributes": []any{map[string]any{"key": "body", "value": map[string]any{"stringValue": turn.Response}}}},
			},
		})
	}
	trace := map[string]any{"batches": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": spans}}}}}
	data, err := json.Marshal(trace)
	require.NoError(t, err)
	return data
}

func toolResponse(id, name, arguments string) string {
	response := map[string]any{
		"id": "generation", "model": "model", "provider": "Provider",
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"content": nil,
				"tool_calls": []any{map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": arguments},
				}},
			},
		}},
	}
	data, _ := json.Marshal(response)
	return string(data)
}

func directResponse(content string) string {
	response := map[string]any{
		"id": "generation", "model": "model", "provider": "Provider",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"content": content},
		}},
	}
	data, _ := json.Marshal(response)
	return string(data)
}
