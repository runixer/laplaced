package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent/videogen"
	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// fakeVideoGen records the request and returns a canned response; local
// like fakeImageGen because VideoGenerator is a tools-package interface.
type fakeVideoGen struct {
	last videogen.Request
	resp *videogen.Response
	err  error
}

func (f *fakeVideoGen) Generate(_ context.Context, req videogen.Request) (*videogen.Response, error) {
	f.last = req
	return f.resp, f.err
}

func newVideoTestExecutor(t *testing.T) (*ToolExecutor, *fakeVideoGen, *testutil.MockStorage) {
	t.Helper()
	mockStore := new(testutil.MockStorage)
	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "generate_video"}}
	exec := NewToolExecutor(new(testutil.MockLLMClient), mockStore, mockStore, cfg, testutil.TestLogger())
	gen := &fakeVideoGen{}
	exec.SetVideoGenerator(gen)
	exec.SetArtifactRepository(mockStore)
	exec.SetFileStorage(files.NewFileStorage(t.TempDir(), testutil.TestLogger()))
	return exec, gen, mockStore
}

func TestPerformVideoGeneration_HappyPath(t *testing.T) {
	exec, gen, mockStore := newVideoTestExecutor(t)
	gen.resp = &videogen.Response{Data: []byte("\x00\x00\x00\x18ftypmp42"), MimeType: "video/mp4", JobID: "j"}
	mockStore.On("AddArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.UserID == "100" && a.FileType == "video" && a.MimeType == "video/mp4" &&
			a.State == "pending" && a.UserContext != nil && *a.UserContext == "a cat runs"
	})).Return(int64(7), nil).Once()

	photo := llm.FilePart{Type: "file", File: llm.File{FileName: "photo.jpg", FileData: "data:image/jpeg;base64,AA=="}}
	result, err := exec.ExecuteToolCall(context.Background(),
		CallContext{UserID: "100", CurrentMessageImages: []llm.FilePart{photo}},
		"generate_video", `{"prompt":"a cat runs","aspect_ratio":"16:9","duration":8,"audio":false}`)
	require.NoError(t, err)

	assert.Equal(t, []int64{7}, result.GeneratedArtifactIDs)
	assert.Equal(t, []artifactdelivery.Generated{{ArtifactID: 7, Mode: artifactdelivery.ModePreview, Video: true}}, result.GeneratedArtifacts)
	assert.Contains(t, result.Content, "do not add MEDIA markers")
	assert.Equal(t, "16:9", gen.last.AspectRatio)
	assert.Equal(t, 8, gen.last.Duration)
	require.NotNil(t, gen.last.Audio)
	assert.False(t, *gen.last.Audio)
	require.NotNil(t, gen.last.FirstFrame, "current-message photo becomes the first frame")
	assert.Nil(t, gen.last.LastFrame)
	mockStore.AssertExpectations(t)
}

func TestPerformVideoGeneration_FailuresStopRetry(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		mustContain string
	}{
		{"timeout", &videogen.Failure{Kind: videogen.KindTimeout}, "took too long"},
		{"safety", &videogen.Failure{Kind: videogen.KindSafetyBlock}, "content policy"},
		{"job failed quotes reason", &videogen.Failure{Kind: videogen.KindJobFailed, Text: "copyright restrictions"}, "copyright restrictions"},
		{"upstream", &videogen.Failure{Kind: videogen.KindUpstreamError, Cause: errors.New("502 secret-host")}, "temporary error"},
		{"untyped", errors.New("boom"), "rejected the request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec, gen, mockStore := newVideoTestExecutor(t)
			gen.err = tt.err
			result, err := exec.performVideoGeneration(context.Background(), CallContext{UserID: "1"},
				map[string]interface{}{"prompt": "x"})
			require.NoError(t, err, "a tool error would trigger an LLM retry storm")
			assert.Contains(t, result.Content, "Do NOT call generate_video again")
			assert.Contains(t, result.Content, tt.mustContain)
			assert.NotContains(t, result.Content, "secret-host")
			assert.Empty(t, result.GeneratedArtifactIDs)
			mockStore.AssertNotCalled(t, "AddArtifact", mock.Anything)
		})
	}
}

func TestPerformVideoGeneration_NotConfiguredAndEmptyPrompt(t *testing.T) {
	cfg := testutil.TestConfig()
	exec := NewToolExecutor(new(testutil.MockLLMClient), nil, nil, cfg, testutil.TestLogger())
	result, err := exec.performVideoGeneration(context.Background(), CallContext{UserID: "1"}, map[string]interface{}{"prompt": "x"})
	require.NoError(t, err)
	assert.Contains(t, result.Content, "not configured")

	exec2, _, _ := newVideoTestExecutor(t)
	_, err = exec2.performVideoGeneration(context.Background(), CallContext{UserID: "1"}, map[string]interface{}{"prompt": "  "})
	assert.Error(t, err)
}
