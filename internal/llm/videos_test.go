package llm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVideoClient_SubmitPollDownload(t *testing.T) {
	var submitted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/videos":
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &submitted))
			w.WriteHeader(http.StatusAccepted)
			// The endpoint may emit whitespace keep-alives before the JSON.
			_, _ = w.Write([]byte("\n \n{\"id\":\"job-1\",\"status\":\"pending\"}"))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/videos/job-1":
			assert.Empty(t, r.Header.Get("Content-Type"), "GET carries no body")
			_, _ = w.Write([]byte(`{"id":"job-1","status":"completed","usage":{"cost":0.3}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/videos/job-1/content":
			assert.Equal(t, "0", r.URL.Query().Get("index"))
			_, _ = w.Write([]byte("\x00\x00\x00\x18ftypmp42video-bytes"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewClient(slog.New(slog.NewJSONHandler(io.Discard, nil)), "k", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)
	audio := true

	job, err := client.SubmitVideo(context.Background(), VideoGenerationRequest{
		Model: "google/veo-3.1-lite", Prompt: "p", Duration: 6, Resolution: "720p", AspectRatio: "9:16",
		GenerateAudio: &audio,
		FrameImages:   []VideoFrameImage{{Type: "image_url", ImageURL: ImageURLValue{URL: "data:image/jpeg;base64,AA=="}, FrameType: VideoFrameFirst}},
	})
	require.NoError(t, err)
	assert.Equal(t, "job-1", job.ID)
	assert.False(t, job.Terminal())
	assert.EqualValues(t, 6, submitted["duration"])
	assert.Equal(t, true, submitted["generate_audio"])
	frames := submitted["frame_images"].([]any)
	assert.Equal(t, "first_frame", frames[0].(map[string]any)["frame_type"])

	polled, err := client.GetVideo(context.Background(), "job-1")
	require.NoError(t, err)
	assert.True(t, polled.Terminal())
	require.NotNil(t, polled.Usage.Cost)
	assert.InDelta(t, 0.3, *polled.Usage.Cost, 1e-9)

	data, err := client.GetVideoContent(context.Background(), "job-1", 0)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ftypmp42")
}

func TestVideoJob_Terminal(t *testing.T) {
	for status, want := range map[string]bool{
		VideoStatusPending: false, VideoStatusInProgress: false,
		VideoStatusCompleted: true, VideoStatusFailed: true, VideoStatusCancelled: true, VideoStatusExpired: true,
	} {
		assert.Equal(t, want, VideoJob{Status: status}.Terminal(), status)
	}
}
