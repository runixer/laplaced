package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateImages_SendsRequestAndDecodesResponse(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aGk=","media_type":"image/png"}],"usage":{"prompt_tokens":13,"completion_tokens":664,"cost":0.02}}`))
	}))
	defer server.Close()

	client, err := NewClient(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	resp, err := client.CreateImages(context.Background(), ImageGenerationRequest{
		Model:   "openai/gpt-image-2.6-sunburst",
		Prompt:  "a cat",
		Size:    "864x1536",
		Quality: "medium",
		InputReferences: []ImageURLPart{
			{Type: "image_url", ImageURL: ImageURLValue{URL: "data:image/png;base64,aGk="}},
		},
		UserID: "42",
	})
	require.NoError(t, err)

	assert.Equal(t, "/api/v1/images", gotPath)
	assert.Equal(t, "864x1536", gotBody["size"])
	assert.Equal(t, "medium", gotBody["quality"])
	assert.NotContains(t, gotBody, "aspect_ratio", "the endpoint coarsens aspect_ratio; only explicit pixels are sent")
	refs, ok := gotBody["input_references"].([]any)
	require.True(t, ok)
	require.Len(t, refs, 1)
	assert.Equal(t, "image_url", refs[0].(map[string]any)["type"])

	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aGk=", resp.Data[0].B64JSON)
	assert.Equal(t, "image/png", resp.Data[0].MediaType)
	require.NotNil(t, resp.Usage.Cost)
	assert.InDelta(t, 0.02, *resp.Usage.Cost, 1e-9)
}

func TestCreateImages_SafetyRejection400_IsSafetyBlockAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request was rejected by the safety system.","code":400,"metadata":{"provider_name":"OpenAI"}}}`))
	}))
	defer server.Close()

	client, err := NewClient(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateImages(context.Background(), ImageGenerationRequest{Model: "m", Prompt: "p"})
	require.Error(t, err)
	assert.True(t, IsSafetyBlock(err))
	assert.True(t, strings.HasPrefix(err.Error(), "llm API error: 400"), "error text keeps the historical form: %s", err)
	assert.Equal(t, int32(1), calls.Load())
}

func TestHTTPStatusError_UnwrapsOnlyParsedEnvelope(t *testing.T) {
	withEnvelope := &httpStatusError{status: "403 Forbidden", body: "x", bodyErr: &providerBodyError{Message: "PROHIBITED_CONTENT", Code: 403}}
	assert.True(t, IsSafetyBlock(withEnvelope))

	plain := &httpStatusError{status: "502 Bad Gateway", body: "error code: 502"}
	assert.False(t, IsSafetyBlock(plain))
	assert.Nil(t, errors.Unwrap(plain))
	assert.Equal(t, "llm API error: 502 Bad Gateway: error code: 502", plain.Error())
}
