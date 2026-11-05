package videogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
)

var mp4Bytes = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")

func testCfg() *config.VideoGeneratorConfig {
	return &config.VideoGeneratorConfig{
		AgentConfig:           config.AgentConfig{Model: "google/veo-3.1-lite"},
		Timeout:               "2s",
		PollInterval:          "1ms",
		Resolution:            "720p",
		DefaultAspectRatio:    "9:16",
		DefaultDuration:       6,
		SupportedAspectRatios: []string{"9:16", "16:9"},
		SupportedDurations:    []int{4, 6, 8},
		GenerateAudio:         true,
	}
}

func pngDataURL(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodedSize(t *testing.T, dataURL string) (int, int) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(dataURL[strings.Index(dataURL, ",")+1:])
	require.NoError(t, err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	return cfg.Width, cfg.Height
}

func TestGenerate_PollsUntilCompletedAndDownloads(t *testing.T) {
	m := new(testutil.MockLLMClient)
	cost := 0.3
	m.On("SubmitVideo", mock.Anything, mock.MatchedBy(func(r llm.VideoGenerationRequest) bool {
		return r.Model == "google/veo-3.1-lite" && r.Duration == 6 && r.AspectRatio == "9:16" &&
			r.Resolution == "720p" && r.GenerateAudio != nil && *r.GenerateAudio && len(r.FrameImages) == 0
	})).Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusPending}, nil)
	m.On("GetVideo", mock.Anything, "j").Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusInProgress}, nil).Once()
	m.On("GetVideo", mock.Anything, "j").Return(llm.VideoJob{}, errors.New("transient")).Once()
	m.On("GetVideo", mock.Anything, "j").Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusCompleted, Usage: llm.Usage{Cost: &cost}}, nil).Once()
	m.On("GetVideoContent", mock.Anything, "j", 0).Return(mp4Bytes, nil)

	resp, err := New(m, testCfg(), testutil.TestLogger()).Generate(context.Background(), Request{UserID: "u", Prompt: "a cat"})
	require.NoError(t, err)
	assert.Equal(t, "video/mp4", resp.MimeType)
	assert.Equal(t, mp4Bytes, resp.Data)
	require.NotNil(t, resp.Cost)
	m.AssertExpectations(t)
}

func TestGenerate_RequestOverridesAndFrames(t *testing.T) {
	m := new(testutil.MockLLMClient)
	first := pngDataURL(t, 1000, 1000) // square → cropped to 16:9
	m.On("SubmitVideo", mock.Anything, mock.MatchedBy(func(r llm.VideoGenerationRequest) bool {
		if r.AspectRatio != "16:9" || r.Duration != 4 || *r.GenerateAudio || len(r.FrameImages) != 2 {
			return false
		}
		w, h := decodedSize(t, r.FrameImages[0].ImageURL.URL)
		return r.FrameImages[0].FrameType == llm.VideoFrameFirst && r.FrameImages[1].FrameType == llm.VideoFrameLast &&
			w == 1000 && h == 562
	})).Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusCompleted}, nil)
	m.On("GetVideoContent", mock.Anything, "j", 0).Return(mp4Bytes, nil)

	off := false
	_, err := New(m, testCfg(), testutil.TestLogger()).Generate(context.Background(), Request{
		Prompt: "x", AspectRatio: "16:9", Duration: 4, Audio: &off,
		FirstFrame: &llm.FilePart{File: llm.File{FileData: first}},
		LastFrame:  &llm.FilePart{File: llm.File{FileData: first}},
	})
	require.NoError(t, err)
	m.AssertExpectations(t)
	m.AssertNotCalled(t, "GetVideo", mock.Anything, mock.Anything)
}

func TestGenerate_Failures(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(m *testutil.MockLLMClient)
		wantKind FailureKind
		wantText string
	}{
		{
			name: "safety rejection on submit",
			setup: func(m *testutil.MockLLMClient) {
				m.On("SubmitVideo", mock.Anything, mock.Anything).
					Return(llm.VideoJob{}, llm.NewSafetyBlockErrorForTest("rejected by the safety system", 400))
			},
			wantKind: KindSafetyBlock,
		},
		{
			name: "upstream error on submit",
			setup: func(m *testutil.MockLLMClient) {
				m.On("SubmitVideo", mock.Anything, mock.Anything).Return(llm.VideoJob{}, errors.New("llm API error: 502"))
			},
			wantKind: KindUpstreamError,
		},
		{
			name: "job failed with provider reason",
			setup: func(m *testutil.MockLLMClient) {
				m.On("SubmitVideo", mock.Anything, mock.Anything).Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusPending}, nil)
				m.On("GetVideo", mock.Anything, "j").Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusFailed, Error: "audio may be related to copyright"}, nil)
			},
			wantKind: KindJobFailed,
			wantText: "copyright",
		},
		{
			name: "never finishes",
			setup: func(m *testutil.MockLLMClient) {
				m.On("SubmitVideo", mock.Anything, mock.Anything).Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusPending}, nil)
				m.On("GetVideo", mock.Anything, "j").Return(llm.VideoJob{ID: "j", Status: llm.VideoStatusInProgress}, nil)
			},
			wantKind: KindTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := new(testutil.MockLLMClient)
			tt.setup(m)
			cfg := testCfg()
			cfg.Timeout = "50ms"
			_, err := New(m, cfg, testutil.TestLogger()).Generate(context.Background(), Request{Prompt: "x"})
			var f *Failure
			require.True(t, errors.As(err, &f), "err=%v", err)
			assert.Equal(t, tt.wantKind, f.Kind)
			assert.Contains(t, f.Text, tt.wantText)
			m.AssertNotCalled(t, "GetVideoContent", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestGenerate_EmptyPrompt(t *testing.T) {
	m := new(testutil.MockLLMClient)
	_, err := New(m, testCfg(), testutil.TestLogger()).Generate(context.Background(), Request{Prompt: " "})
	require.Error(t, err)
	m.AssertNotCalled(t, "SubmitVideo", mock.Anything, mock.Anything)
}

func TestCropDataURLToRatio(t *testing.T) {
	tests := []struct {
		name        string
		w, h        int
		ratio       string
		wantW       int
		wantH       int
		wantCropped bool
	}{
		{"landscape 4:3 to 9:16", 1200, 900, "9:16", 506, 900, true},
		{"portrait 3:4 to 16:9", 900, 1200, "16:9", 900, 506, true},
		{"already 9:16", 720, 1280, "9:16", 720, 1280, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, cropped := cropDataURLToRatio(pngDataURL(t, tt.w, tt.h), tt.ratio)
			assert.Equal(t, tt.wantCropped, cropped)
			w, h := decodedSize(t, out)
			assert.Equal(t, tt.wantW, w)
			assert.Equal(t, tt.wantH, h)
		})
	}
	same := "data:image/webp;base64,AAAA"
	out, cropped := cropDataURLToRatio(same, "9:16")
	assert.False(t, cropped)
	assert.Equal(t, same, out)
}
