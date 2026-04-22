package imagegen

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// tinyPNGBase64 is a 1x1 transparent PNG encoded as base64.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

func testCfg() *config.ImageGeneratorConfig {
	return &config.ImageGeneratorConfig{
		AgentConfig:        config.AgentConfig{Model: "google/gemini-3.1-flash-image-preview"},
		Timeout:            "30s",
		DefaultAspectRatio: "1:1",
		DefaultImageSize:   "1K",
	}
}

func buildResponse(content string, images ...string) openrouter.ChatCompletionResponse {
	outs := make([]openrouter.ImageOutput, 0, len(images))
	for _, url := range images {
		outs = append(outs, openrouter.ImageOutput{
			Type: "image_url",
			ImageURL: openrouter.ImageURLValue{
				URL: url,
			},
		})
	}
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{
			{
				Message: openrouter.ResponseMessage{
					Role:    "assistant",
					Content: content,
					Images:  outs,
				},
			},
		},
	}
}

func TestGenerate_EmptyPromptReturnsError(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	agent := New(mockOR, testCfg(), testutil.TestLogger())

	_, err := agent.Generate(context.Background(), Request{Prompt: "   "})

	assert.Error(t, err)
	mockOR.AssertNotCalled(t, "CreateChatCompletion", mock.Anything, mock.Anything)
}

func TestGenerate_SendsModalitiesAndImageConfig(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	dataURL := "data:image/png;base64," + tinyPNGBase64
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("here is your image", dataURL), nil).
		Run(func(args mock.Arguments) {
			req := args.Get(1).(openrouter.ChatCompletionRequest)
			assert.Equal(t, []string{"image", "text"}, req.Modalities)
			require.NotNil(t, req.ImageConfig)
			assert.Equal(t, "16:9", req.ImageConfig.AspectRatio)
			assert.Equal(t, "2K", req.ImageConfig.ImageSize)
			assert.Equal(t, "google/gemini-3.1-flash-image-preview", req.Model)
		})

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	resp, err := agent.Generate(context.Background(), Request{
		UserID:      123,
		Prompt:      "a cat",
		AspectRatio: "16:9",
		ImageSize:   "2K",
	})

	require.NoError(t, err)
	require.Len(t, resp.Images, 1)
	assert.Equal(t, "image/png", resp.Images[0].MimeType)
	expected, _ := base64.StdEncoding.DecodeString(tinyPNGBase64)
	assert.Equal(t, expected, resp.Images[0].Data)
	assert.Equal(t, "here is your image", resp.TextContent)
	mockOR.AssertExpectations(t)
}

func TestGenerate_UsesDefaultsWhenRequestFieldsEmpty(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	dataURL := "data:image/png;base64," + tinyPNGBase64
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("", dataURL), nil).
		Run(func(args mock.Arguments) {
			req := args.Get(1).(openrouter.ChatCompletionRequest)
			require.NotNil(t, req.ImageConfig)
			assert.Equal(t, "1:1", req.ImageConfig.AspectRatio)
			assert.Equal(t, "1K", req.ImageConfig.ImageSize)
		})

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	_, err := agent.Generate(context.Background(), Request{Prompt: "a cat"})
	require.NoError(t, err)
}

// Regression: editing (input images present) must NOT force the default
// aspect ratio — the model should preserve the input photo's own ratio.
func TestGenerate_EditingSkipsDefaultAspectRatio(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	dataURL := "data:image/png;base64," + tinyPNGBase64
	cfg := testCfg()
	cfg.DefaultAspectRatio = "9:16" // mirror the new production default
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("", dataURL), nil).
		Run(func(args mock.Arguments) {
			req := args.Get(1).(openrouter.ChatCompletionRequest)
			// ImageConfig may be non-nil due to ImageSize default, but
			// AspectRatio must be empty so the model uses the input image's.
			if req.ImageConfig != nil {
				assert.Empty(t, req.ImageConfig.AspectRatio, "editing must not force aspect ratio")
			}
		})

	agent := New(mockOR, cfg, testutil.TestLogger())
	_, err := agent.Generate(context.Background(), Request{
		Prompt: "make it sepia",
		InputImages: []openrouter.FilePart{{
			Type: "file",
			File: openrouter.File{FileName: "photo.jpg", FileData: "data:image/jpeg;base64,AAAA"},
		}},
	})
	require.NoError(t, err)
}

func TestGenerate_PassesInputImagesAsContentParts(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	dataURL := "data:image/png;base64," + tinyPNGBase64
	input := openrouter.FilePart{
		Type: "file",
		File: openrouter.File{
			FileName: "input.png",
			FileData: "data:image/png;base64,SGVsbG8=",
		},
	}
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("edited", dataURL), nil).
		Run(func(args mock.Arguments) {
			req := args.Get(1).(openrouter.ChatCompletionRequest)
			require.Len(t, req.Messages, 1)
			parts, ok := req.Messages[0].Content.([]interface{})
			require.True(t, ok, "content should be multimodal parts slice")
			// First part is the text prompt
			_, isText := parts[0].(openrouter.TextPart)
			assert.True(t, isText, "first part must be text")
			// Second part is the input image as an OpenAI-compatible
			// image_url part (image-generation models reject the "file" shape).
			ip, isImg := parts[1].(openrouter.ImageURLPart)
			assert.True(t, isImg, "second part must be ImageURLPart")
			assert.Equal(t, "image_url", ip.Type)
			assert.Equal(t, "data:image/png;base64,SGVsbG8=", ip.ImageURL.URL)
		})

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	_, err := agent.Generate(context.Background(), Request{
		Prompt:      "make it sepia",
		InputImages: []openrouter.FilePart{input},
	})
	require.NoError(t, err)
}

func TestGenerate_NoImagesInResponseReturnsError(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("I cannot generate that kind of image."), nil)

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	_, err := agent.Generate(context.Background(), Request{Prompt: "a cat"})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "I cannot generate")
}

func TestGenerate_UpstreamErrorIsWrapped(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("rate limited"))

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	_, err := agent.Generate(context.Background(), Request{Prompt: "a cat"})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "chat completion failed")
}

func TestGenerate_MultipleImagesAllReturned(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	dataURL := "data:image/png;base64," + tinyPNGBase64
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("three options", dataURL, dataURL, dataURL), nil)

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	resp, err := agent.Generate(context.Background(), Request{Prompt: "3 variants of a cat"})

	require.NoError(t, err)
	assert.Len(t, resp.Images, 3)
}

func TestGenerate_MalformedDataURLSkipped(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	bad := "not-a-data-url"
	good := "data:image/png;base64," + tinyPNGBase64
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(buildResponse("", bad, good), nil)

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	resp, err := agent.Generate(context.Background(), Request{Prompt: "a cat"})

	require.NoError(t, err, "should still succeed with one valid image")
	assert.Len(t, resp.Images, 1)
}

func TestDecodeDataURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMIME string
		wantErr  bool
	}{
		{"valid png", "data:image/png;base64," + tinyPNGBase64, "image/png", false},
		{"valid jpeg", "data:image/jpeg;base64," + tinyPNGBase64, "image/jpeg", false},
		{"missing prefix", "image/png;base64," + tinyPNGBase64, "", true},
		{"no semicolon", "data:image/pngbase64," + tinyPNGBase64, "", true},
		{"non-base64 encoding", "data:image/png;quoted-printable,xx", "", true},
		{"bad base64 payload", "data:image/png;base64,!!!not base64!!!", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, _, err := decodeDataURL(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantMIME, mime)
		})
	}
}

// Simulate a context deadline by passing an already-expired context.
func TestGenerate_CancelledContextPropagates(t *testing.T) {
	mockOR := new(testutil.MockOpenRouterClient)
	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, context.Canceled)

	agent := New(mockOR, testCfg(), testutil.TestLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := agent.Generate(ctx, Request{Prompt: "x"})
	assert.Error(t, err)
}

// regression: timeout parsing.
func TestTimeoutParse(t *testing.T) {
	cfg := testCfg()
	cfg.Timeout = "5s"
	d := cfg.GetTimeout()
	assert.Equal(t, 5*time.Second, d)
}
