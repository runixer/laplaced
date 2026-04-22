package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fakeImageGen is a minimal in-memory test double for ImageGenerator.
type fakeImageGen struct {
	lastRequest ImageGenRequest
	response    *ImageGenResponse
	err         error
}

func (f *fakeImageGen) Generate(ctx context.Context, req ImageGenRequest) (*ImageGenResponse, error) {
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func newImageTestExecutor(t *testing.T) (*ToolExecutor, *fakeImageGen, *testutil.MockStorage, string) {
	t.Helper()
	mockStore := new(testutil.MockStorage)
	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "generate_image"}}
	cfg.Agents.ImageGenerator.MaxOutputImages = 4
	cfg.Agents.ImageGenerator.MaxInputImages = 4

	tmp := t.TempDir()
	fs := files.NewFileStorage(tmp, testutil.TestLogger())

	exec := NewToolExecutor(new(testutil.MockOpenRouterClient), mockStore, mockStore, cfg, testutil.TestLogger())
	gen := &fakeImageGen{}
	exec.SetImageGenerator(gen)
	exec.SetArtifactRepository(mockStore)
	exec.SetFileStorage(fs)
	return exec, gen, mockStore, tmp
}

func TestPerformImageGeneration_HappyPath(t *testing.T) {
	exec, gen, mockStore, tmp := newImageTestExecutor(t)

	gen.response = &ImageGenResponse{
		Images: []ImageGenImage{
			{MimeType: "image/png", Data: []byte("\x89PNG_fake_bytes_1")},
			{MimeType: "image/png", Data: []byte("\x89PNG_fake_bytes_2")},
		},
		TextContent: "done",
	}

	mockStore.On("GetByHash", int64(100), mock.Anything).Return(nil, nil)
	// AddArtifact called once per output image; return two distinct IDs in order.
	mockStore.On("AddArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.UserID == 100 &&
			a.FileType == "image" &&
			a.MimeType == "image/png" &&
			a.State == "pending" &&
			a.MessageID == 0 &&
			a.UserContext != nil && *a.UserContext == "a cat"
	})).Return(int64(1), nil).Once()
	mockStore.On("AddArtifact", mock.Anything).Return(int64(2), nil).Once()

	result, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 100},
		map[string]interface{}{
			"prompt":       "a cat",
			"aspect_ratio": "16:9",
			"image_size":   "2K",
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []int64{1, 2}, result.GeneratedArtifactIDs)
	assert.Contains(t, result.Content, "artifact:1")
	assert.Contains(t, result.Content, "artifact:2")
	assert.Contains(t, result.Content, "Model note: done")

	// Agent was called with the right parameters
	assert.Equal(t, int64(100), gen.lastRequest.UserID)
	assert.Equal(t, "a cat", gen.lastRequest.Prompt)
	assert.Equal(t, "16:9", gen.lastRequest.AspectRatio)
	assert.Equal(t, "2K", gen.lastRequest.ImageSize)

	// Files actually landed on disk under the user's directory
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Len(t, entries, 1, "should create one user directory")
	assert.True(t, strings.HasPrefix(entries[0].Name(), "user_100"))
}

func TestPerformImageGeneration_EmptyPromptRejected(t *testing.T) {
	exec, _, _, _ := newImageTestExecutor(t)

	_, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1},
		map[string]interface{}{"prompt": "   "},
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is required")
}

func TestPerformImageGeneration_MissingDependenciesReturnsStopRetry(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "generate_image"}}
	exec := NewToolExecutor(nil, nil, nil, cfg, testutil.TestLogger())

	result, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1},
		map[string]interface{}{"prompt": "x"},
	)
	// Must NOT return a raw error (which the LLM would retry on).
	// Returns a normal Result with instructions to the LLM to stop retrying.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Content, "not configured")
	assert.Contains(t, result.Content, "Do NOT call generate_image again", "must instruct LLM to stop")
	assert.Empty(t, result.GeneratedArtifactIDs)
}

func TestPerformImageGeneration_CurrentMessageImagesUsedWhenNoArtifactIDs(t *testing.T) {
	exec, gen, mockStore, _ := newImageTestExecutor(t)
	gen.response = &ImageGenResponse{
		Images: []ImageGenImage{{MimeType: "image/png", Data: []byte("bytes")}},
	}
	mockStore.On("GetByHash", mock.Anything, mock.Anything).Return(nil, nil)
	mockStore.On("AddArtifact", mock.Anything).Return(int64(42), nil)

	attached := openrouter.FilePart{
		Type: "file",
		File: openrouter.File{FileName: "photo.jpg", FileData: "data:image/jpeg;base64,AAAA"},
	}

	_, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1, CurrentMessageImages: []openrouter.FilePart{attached}},
		map[string]interface{}{"prompt": "add sepia"},
	)
	require.NoError(t, err)

	require.Len(t, gen.lastRequest.InputImages, 1)
	assert.Equal(t, "photo.jpg", gen.lastRequest.InputImages[0].File.FileName)
}

func TestPerformImageGeneration_ArtifactIDsResolvedAndUserIsolated(t *testing.T) {
	exec, gen, mockStore, tmp := newImageTestExecutor(t)
	gen.response = &ImageGenResponse{
		Images: []ImageGenImage{{MimeType: "image/png", Data: []byte("x")}},
	}

	// Seed an artifact on disk for the user.
	refPath := "user_1/2026-01/ref.png"
	absRef := filepath.Join(tmp, refPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absRef), 0o755))
	require.NoError(t, os.WriteFile(absRef, []byte("ref-bytes"), 0o644))

	mockStore.On("GetArtifact", int64(1), int64(77)).Return(&storage.Artifact{
		ID: 77, UserID: 1, FileType: "image", FilePath: refPath,
		MimeType: "image/png", OriginalName: "ref.png",
	}, nil)
	// GetArtifact for a foreign user's ID returns nil (user isolation is
	// enforced by the repository itself via WHERE user_id=?; the mock just
	// mirrors that behavior).
	mockStore.On("GetArtifact", int64(1), int64(999)).Return(nil, nil)

	mockStore.On("GetByHash", mock.Anything, mock.Anything).Return(nil, nil)
	mockStore.On("AddArtifact", mock.Anything).Return(int64(100), nil)

	_, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1},
		map[string]interface{}{
			"prompt":             "edit",
			"input_artifact_ids": []interface{}{float64(77), float64(999)}, // JSON number decoding
		},
	)
	require.NoError(t, err)
	require.Len(t, gen.lastRequest.InputImages, 1, "only the resolvable artifact should be passed")
	assert.Equal(t, "ref.png", gen.lastRequest.InputImages[0].File.FileName)
	assert.Contains(t, gen.lastRequest.InputImages[0].File.FileData, "data:image/png;base64,")
}

// Regression: when the LLM sends BOTH a current-turn photo AND references an
// older artifact by ID ("mix this new photo with that one from memory"), the
// tool must pass BOTH images to the model — not just one or the other.
// Regression: when the image model rejects the request (safety / bad input /
// upstream 400), performImageGeneration must NOT return an error. Returning
// an error causes laplace.executeToolCalls to emit "Tool execution failed: …",
// which the LLM interprets as "try a different prompt" and kicks off a retry
// storm (we've seen 6+ consecutive failing calls burning ~$0.50 per turn).
// Instead we return a normal Result whose Content explicitly tells the LLM
// to stop and apologize.
func TestPerformImageGeneration_UpstreamErrorReturnsStopRetryResult(t *testing.T) {
	exec, gen, _, _ := newImageTestExecutor(t)
	gen.err = errors.New("openrouter API error: 400 Bad Request")

	result, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1},
		map[string]interface{}{"prompt": "problematic"},
	)
	require.NoError(t, err, "tool must return result, not error, so LLM doesn't retry")
	require.NotNil(t, result)
	assert.Contains(t, result.Content, "Do NOT call generate_image again")
	assert.Contains(t, result.Content, "Apologize")
	assert.Empty(t, result.GeneratedArtifactIDs)
}

// Safety-filter path: model returns choice[0].Message with empty Images but
// non-empty Content explaining why. Same anti-retry behavior expected.
func TestPerformImageGeneration_SafetyRefusalReturnsStopRetryResult(t *testing.T) {
	exec, gen, _, _ := newImageTestExecutor(t)
	gen.response = &ImageGenResponse{
		Images:      nil,
		TextContent: "I cannot generate images of real public figures.",
	}

	result, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1},
		map[string]interface{}{"prompt": "draw X"},
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Content, "no images")
	assert.Contains(t, result.Content, "Do NOT call generate_image again")
	assert.Contains(t, result.Content, "Apologize")
	// Model's own refusal text should be surfaced for the user-facing reply.
	assert.Contains(t, result.Content, "real public figures")
}

func TestPerformImageGeneration_MergesCurrentMessageAndArtifactIDs(t *testing.T) {
	exec, gen, mockStore, tmp := newImageTestExecutor(t)
	gen.response = &ImageGenResponse{
		Images: []ImageGenImage{{MimeType: "image/png", Data: []byte("out")}},
	}

	refPath := "user_1/2026-01/old.png"
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(tmp, refPath)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, refPath), []byte("old-bytes"), 0o644))

	mockStore.On("GetArtifact", int64(1), int64(42)).Return(&storage.Artifact{
		ID: 42, UserID: 1, FileType: "image", FilePath: refPath,
		MimeType: "image/png", OriginalName: "old.png",
	}, nil)
	mockStore.On("GetByHash", mock.Anything, mock.Anything).Return(nil, nil)
	mockStore.On("AddArtifact", mock.Anything).Return(int64(200), nil)

	currentAttached := openrouter.FilePart{
		Type: "file",
		File: openrouter.File{FileName: "new.jpg", FileData: "data:image/jpeg;base64,TkVX"},
	}

	_, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1, CurrentMessageImages: []openrouter.FilePart{currentAttached}},
		map[string]interface{}{
			"prompt":             "mix these",
			"input_artifact_ids": []interface{}{float64(42)},
		},
	)
	require.NoError(t, err)
	require.Len(t, gen.lastRequest.InputImages, 2, "both new photo AND referenced artifact must be passed")
	// Order: current-message images first, then referenced artifacts.
	assert.Equal(t, "new.jpg", gen.lastRequest.InputImages[0].File.FileName)
	assert.Equal(t, "old.png", gen.lastRequest.InputImages[1].File.FileName)
}

func TestPerformImageGeneration_MaxOutputImagesCap(t *testing.T) {
	exec, gen, mockStore, _ := newImageTestExecutor(t)
	exec.cfg.Agents.ImageGenerator.MaxOutputImages = 2
	gen.response = &ImageGenResponse{
		Images: []ImageGenImage{
			{MimeType: "image/png", Data: []byte("a")},
			{MimeType: "image/png", Data: []byte("b")},
			{MimeType: "image/png", Data: []byte("c")},
		},
	}
	mockStore.On("GetByHash", mock.Anything, mock.Anything).Return(nil, nil)
	mockStore.On("AddArtifact", mock.Anything).Return(int64(1), nil)

	res, err := exec.performImageGeneration(context.Background(),
		CallContext{UserID: 1},
		map[string]interface{}{"prompt": "x"},
	)
	require.NoError(t, err)
	assert.Len(t, res.GeneratedArtifactIDs, 2, "should cap at MaxOutputImages")
}

func TestParseArtifactIDs(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  []int64
	}{
		{"nil", nil, nil},
		{"int64 slice", []int64{1, 2}, []int64{1, 2}},
		{"json numbers", []interface{}{float64(7), float64(8)}, []int64{7, 8}},
		{"mixed ints", []interface{}{1, int64(2)}, []int64{1, 2}},
		{"wrong type", "not a list", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseArtifactIDs(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
