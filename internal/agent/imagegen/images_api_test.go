package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
)

func imagesCfg() *config.ImageGeneratorConfig {
	return &config.ImageGeneratorConfig{
		AgentConfig:           config.AgentConfig{Model: "openai/gpt-image-2.6-sunburst"},
		API:                   config.ImageAPIImages,
		Quality:               "medium",
		Timeout:               "30s",
		DefaultAspectRatio:    "9:16",
		DefaultImageSize:      "1K",
		SupportedImageSizes:   []string{"1K", "2K"},
		SupportedAspectRatios: []string{"1:1", "3:4", "4:3", "9:16"},
		PixelSizes: map[string]map[string]string{
			"1K": {"1:1": "1536x1536", "3:4": "1152x1536", "4:3": "1536x1152", "9:16": "864x1536"},
			"2K": {"1:1": "2048x2048", "3:4": "1536x2048", "4:3": "2048x1536", "9:16": "1152x2048"},
		},
	}
}

// pngDataURL builds a real w×h PNG so dataURLDimensions can read its header.
func pngDataURL(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func imagesOK() llm.ImageGenerationResponse {
	cost := 0.035
	return llm.ImageGenerationResponse{
		Data:  []llm.GeneratedImageData{{B64JSON: tinyPNGBase64, MediaType: "image/png"}},
		Usage: llm.Usage{PromptTokens: 13, CompletionTokens: 664, Cost: &cost},
	}
}

func TestGenerateViaImages_PixelSizeFromTable(t *testing.T) {
	tests := []struct {
		name     string
		req      Request
		wantSize string
	}{
		{"text-to-image uses default ratio and tier", Request{Prompt: "a cat"}, "864x1536"},
		{"explicit ratio and tier", Request{Prompt: "a cat", AspectRatio: "4:3", ImageSize: "2K"}, "2048x1536"},
		{"ratio outside the table lets the provider pick", Request{Prompt: "a cat", AspectRatio: "21:9"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOR := new(testutil.MockLLMClient)
			mockOR.On("CreateImages", mock.Anything, mock.MatchedBy(func(r llm.ImageGenerationRequest) bool {
				return r.Size == tt.wantSize && r.Quality == "medium" && r.Model == "openai/gpt-image-2.6-sunburst"
			})).Return(imagesOK(), nil)

			resp, err := New(mockOR, imagesCfg(), testutil.TestLogger()).Generate(context.Background(), tt.req)
			require.NoError(t, err)
			require.Len(t, resp.Images, 1)
			assert.Equal(t, "image/png", resp.Images[0].MimeType)
			require.NotNil(t, resp.Cost)
			mockOR.AssertExpectations(t)
			mockOR.AssertNotCalled(t, "CreateChatCompletion", mock.Anything, mock.Anything)
		})
	}
}

func TestGenerateViaImages_EditKeepsInputFraming(t *testing.T) {
	mockOR := new(testutil.MockLLMClient)
	input := pngDataURL(t, 1000, 760) // ≈4:3 landscape
	mockOR.On("CreateImages", mock.Anything, mock.MatchedBy(func(r llm.ImageGenerationRequest) bool {
		return r.Size == "1536x1152" && len(r.InputReferences) == 1 &&
			r.InputReferences[0].Type == "image_url" && r.InputReferences[0].ImageURL.URL == input
	})).Return(imagesOK(), nil)

	_, err := New(mockOR, imagesCfg(), testutil.TestLogger()).Generate(context.Background(), Request{
		Prompt:      "make it autumn",
		InputImages: []llm.FilePart{{Type: "file", File: llm.File{FileName: "photo.jpg", FileData: input}}},
	})
	require.NoError(t, err)
	mockOR.AssertExpectations(t)
}

func TestGenerateViaImages_SafetyRejectionIsTyped(t *testing.T) {
	mockOR := new(testutil.MockLLMClient)
	mockOR.On("CreateImages", mock.Anything, mock.Anything).
		Return(nil, llm.NewSafetyBlockErrorForTest("Your request was rejected by the safety system.", 400))

	_, err := New(mockOR, imagesCfg(), testutil.TestLogger()).Generate(context.Background(), Request{Prompt: "x"})
	var failure *ImagegenFailure
	require.True(t, errors.As(err, &failure))
	assert.Equal(t, KindSafetyBlock, failure.Kind)
}

func TestGenerateViaImages_EmptyOrUndecodableData(t *testing.T) {
	tests := []struct {
		name     string
		resp     llm.ImageGenerationResponse
		wantKind FailureKind
	}{
		{"no data", llm.ImageGenerationResponse{}, KindUnknownNoImages},
		{"bad base64", llm.ImageGenerationResponse{Data: []llm.GeneratedImageData{{B64JSON: "%%%"}}}, KindUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOR := new(testutil.MockLLMClient)
			mockOR.On("CreateImages", mock.Anything, mock.Anything).Return(tt.resp, nil)
			_, err := New(mockOR, imagesCfg(), testutil.TestLogger()).Generate(context.Background(), Request{Prompt: "x"})
			require.Error(t, err)
			var failure *ImagegenFailure
			if tt.wantKind == KindUnknown {
				assert.False(t, errors.As(err, &failure))
				return
			}
			require.True(t, errors.As(err, &failure))
			assert.Equal(t, tt.wantKind, failure.Kind)
		})
	}
}

func TestGenerateViaImages_MediaTypeFallsBackToSniffing(t *testing.T) {
	mockOR := new(testutil.MockLLMClient)
	resp := imagesOK()
	resp.Data[0].MediaType = ""
	mockOR.On("CreateImages", mock.Anything, mock.Anything).Return(resp, nil)

	out, err := New(mockOR, imagesCfg(), testutil.TestLogger()).Generate(context.Background(), Request{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "image/png", out.Images[0].MimeType)
}

func TestEffectiveAspectRatio_ChatEditSendsNoRatio(t *testing.T) {
	a := New(new(testutil.MockLLMClient), testCfg(), testutil.TestLogger())
	ratio, source := a.effectiveAspectRatio(Request{
		InputImages: []llm.FilePart{{File: llm.File{FileData: pngDataURL(t, 100, 100)}}},
	})
	assert.Equal(t, "", ratio)
	assert.Equal(t, aspectSourceNone, source)
}

func TestNearestAspectRatio(t *testing.T) {
	supported := []string{"1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", "21:9", "bogus"}
	tests := []struct {
		w, h int
		want string
	}{
		{1080, 1920, "9:16"},
		{1920, 1080, "16:9"},
		{1280, 960, "4:3"},
		{960, 1280, "3:4"},
		{1000, 1000, "1:1"},
		{2560, 1080, "21:9"},
		{1200, 1700, "2:3"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, nearestAspectRatio(tt.w, tt.h, supported), "%dx%d", tt.w, tt.h)
	}
	assert.Equal(t, "", nearestAspectRatio(10, 10, []string{"x", "0:1"}))
}

func TestDataURLDimensions_Unreadable(t *testing.T) {
	_, _, ok := dataURLDimensions("data:image/webp;base64,AAAA")
	assert.False(t, ok)
	_, _, ok = dataURLDimensions("not a data url")
	assert.False(t, ok)
}
