package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// testExtractorArtifact creates a test artifact with given parameters.
func testExtractorArtifact(fileType string, fileSize int64) *storage.Artifact {
	return &storage.Artifact{
		ID:           1,
		UserID:       testutil.TestUserID,
		FileType:     fileType,
		FilePath:     "test/file.jpg",
		FileSize:     fileSize,
		MimeType:     "image/jpeg",
		OriginalName: "test.jpg",
		ContentHash:  "abc123",
		State:        "pending",
	}
}

// TestExtractor_XmlEscape tests the xmlEscape helper function.
func TestExtractor_XmlEscape(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "ampersand",
			input:    "AT&T",
			expected: "AT&amp;T",
		},
		{
			name:     "less than",
			input:    "a < b",
			expected: "a &lt; b",
		},
		{
			name:     "greater than",
			input:    "a > b",
			expected: "a &gt; b",
		},
		{
			name:     "mixed special characters",
			input:    "<script>alert('AT&T')</script>",
			expected: "&lt;script&gt;alert('AT&amp;T')&lt;/script&gt;",
		},
		{
			name:     "all special chars",
			input:    "< & >",
			expected: "&lt; &amp; &gt;",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "already escaped",
			input:    "AT&amp;T",
			expected: "AT&amp;amp;T", // double-escaped as expected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := xmlEscape(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestExtractor_GetArtifact tests the getArtifact helper function.
func TestExtractor_GetArtifact(t *testing.T) {
	extractor := &Extractor{}
	artifact := testExtractorArtifact("image", 1024)

	tests := []struct {
		name     string
		params   map[string]any
		expected *storage.Artifact
	}{
		{
			name: "artifact present in params",
			params: map[string]any{
				ParamArtifact: artifact,
			},
			expected: artifact,
		},
		{
			name:     "artifact missing from params",
			params:   map[string]any{},
			expected: nil,
		},
		{
			name:     "nil params",
			params:   nil,
			expected: nil,
		},
		{
			name: "wrong type in params",
			params: map[string]any{
				ParamArtifact: "not an artifact",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &agent.Request{Params: tt.params}
			result := extractor.getArtifact(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestExtractor_GetContext tests the getContext helper function.
func TestExtractor_GetContext(t *testing.T) {
	extractor := &Extractor{}

	tests := []struct {
		name          string
		req           *agent.Request
		ctx           context.Context
		expectProfile bool
	}{
		{
			name: "SharedContext from request",
			req: &agent.Request{
				Shared: &agent.SharedContext{
					Profile:      "<user_profile>test profile</user_profile>",
					RecentTopics: "<recent_topics>topic1</recent_topics>",
					InnerCircle:  "<inner_circle>circle</inner_circle>",
				},
			},
			ctx:           context.Background(),
			expectProfile: true,
		},
		{
			name: "SharedContext from context.Context",
			req:  &agent.Request{},
			ctx: agent.WithContext(context.Background(), &agent.SharedContext{
				Profile:      "<user_profile>ctx profile</user_profile>",
				RecentTopics: "<recent_topics>ctx topic</recent_topics>",
				InnerCircle:  "<inner_circle>ctx circle</inner_circle>",
			}),
			expectProfile: true,
		},
		{
			name:          "No SharedContext - returns empty defaults",
			req:           &agent.Request{},
			ctx:           context.Background(),
			expectProfile: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, recentTopics, innerCircle := extractor.getContext(tt.ctx, tt.req)

			if tt.expectProfile {
				assert.Contains(t, profile, "profile")
				assert.Contains(t, recentTopics, "topic")
				assert.Contains(t, innerCircle, "circle")
			} else {
				assert.Equal(t, "<user_profile>\n</user_profile>", profile)
				assert.Equal(t, "<recent_topics>\n</recent_topics>", recentTopics)
				assert.Equal(t, "<inner_circle>\n</inner_circle>", innerCircle)
			}
		})
	}
}

// TestExtractor_Execute_Success tests successful extraction flow.
func TestExtractor_Execute_Success(t *testing.T) {
	// LLM response with extraction result
	llmResponse := `{
		"summary": "A beautiful sunset photo showing orange and pink clouds over the horizon",
		"keywords": ["sunset", "sky", "nature", "photography", "landscape"],
		"entities": ["horizon", "clouds"],
		"rag_hints": ["What time of day was this photo taken?", "What are the dominant colors?"]
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(testutil.MockEmbeddingResponse(), nil)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	// Create temp file storage
	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data for testing")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	artifact := testExtractorArtifact("image", int64(len(testData)))
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	require.NoError(t, err)

	// Verify response
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.Content)

	result, ok := resp.Structured.(*ProcessResult)
	require.True(t, ok)
	assert.Equal(t, int64(1), result.ArtifactID)
	assert.Contains(t, result.Summary, "sunset")
	assert.Len(t, result.Keywords, 5)
	assert.Len(t, result.Entities, 2)
	assert.Len(t, result.RAGHints, 2)
	assert.Greater(t, result.Duration, time.Duration(0))
	assert.Greater(t, result.Tokens.TotalTokens(), 0)

	// Verify artifact was updated correctly
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "ready" &&
			a.Summary != nil &&
			a.Keywords != nil &&
			a.Entities != nil &&
			a.RAGHints != nil &&
			a.Embedding != nil &&
			a.RetryCount == 0 &&
			a.LastFailedAt == nil &&
			a.ProcessedAt != nil
	}))

	mockClient.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_FileTooLarge tests file size validation.
func TestExtractor_Execute_FileTooLarge(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 1
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	// Create artifact larger than limit
	artifact := testExtractorArtifact("image", 2*1024*1024) // 2MB, limit is 1MB
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file too large")

	// Verify markFailed was called
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" &&
			a.ErrorMessage != nil &&
			strings.Contains(*a.ErrorMessage, "file too large") &&
			a.RetryCount == 1 &&
			a.LastFailedAt != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_ZeroSizeFile tests zero-size file validation.
func TestExtractor_Execute_ZeroSizeFile(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	// Create artifact with zero size
	artifact := testExtractorArtifact("image", 0)
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty file")

	// Verify markFailed was called
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" &&
			a.ErrorMessage != nil &&
			strings.Contains(*a.ErrorMessage, "empty file") &&
			a.RetryCount == 1 &&
			a.LastFailedAt != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_NoArtifact tests missing artifact in request.
func TestExtractor_Execute_NoArtifact(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	req := &agent.Request{
		Params: map[string]any{},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no artifact provided")
}

// TestExtractor_Execute_FileReadError tests file read failure.
func TestExtractor_Execute_FileReadError(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	// Create artifact with non-existent file path
	artifact := testExtractorArtifact("image", 1024)
	artifact.FilePath = "nonexistent/file.jpg"
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read file")

	// Verify markFailed was called
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" && a.ErrorMessage != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_LLMError tests LLM call failure.
func TestExtractor_Execute_LLMError(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, assert.AnError)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	artifact := testExtractorArtifact("image", int64(len(testData)))
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "LLM call failed")

	// Verify markFailed was called
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" && a.ErrorMessage != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_EmptyLLMResponse tests empty LLM response.
func TestExtractor_Execute_EmptyLLMResponse(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	// Return empty response (no choices)
	emptyResp := testutil.MockChatResponse("")
	emptyResp.Choices = nil // Override with nil to simulate empty response
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(emptyResp, nil)

	mockStorage := &testutil.MockStorage{}
	// Expect UpdateArtifact call for state -> processing
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	artifact := testExtractorArtifact("image", int64(len(testData)))
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_InvalidJSON tests invalid JSON response.
func TestExtractor_Execute_InvalidJSON(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("this is not valid json"), nil)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	artifact := testExtractorArtifact("image", int64(len(testData)))
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse extraction JSON")

	// Verify markFailed was called
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" && a.ErrorMessage != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_EmbeddingError tests embedding generation failure.
func TestExtractor_Execute_EmbeddingError(t *testing.T) {
	llmResponse := `{
		"summary": "Test summary",
		"keywords": ["test"],
		"entities": [],
		"rag_hints": []
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(openrouter.EmbeddingResponse{}, assert.AnError)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	artifact := testExtractorArtifact("image", int64(len(testData)))
	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to generate summary embedding")

	// Verify markFailed was called
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" && a.ErrorMessage != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_UpdateArtifactError tests artifact update failure.
func TestExtractor_Execute_UpdateArtifactError(t *testing.T) {
	// This test verifies that markFailed is called when UpdateArtifact fails.
	// The actual error path is exercised in other tests (LLMError, EmbeddingError, etc.)
	// Here we test markFailed directly to verify retry tracking.

	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	artifact := testExtractorArtifact("image", 1024)
	artifact.RetryCount = 2

	extractor.markFailed(artifact, "test error message")

	// Verify artifact was updated with failed state
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" &&
			a.ErrorMessage != nil &&
			*a.ErrorMessage == "test error message" &&
			a.RetryCount == 3 &&
			a.LastFailedAt != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_BuildMultimodalMessages_Image tests image file message building.
func TestExtractor_BuildMultimodalMessages_Image(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	testData := []byte("fake image data")
	artifact := testExtractorArtifact("image", int64(len(testData)))
	artifact.MimeType = "image/png"

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	messages, err := extractor.buildMultimodalMessages(context.Background(), req, artifact, testData)
	require.NoError(t, err)
	require.Len(t, messages, 2)

	// System message
	assert.Equal(t, "system", messages[0].Role)

	// User message with content parts
	userMsg := messages[1]
	assert.Equal(t, "user", userMsg.Role)
	contentParts, ok := userMsg.Content.([]interface{})
	require.True(t, ok)
	require.Len(t, contentParts, 2)

	// First part should be FilePart
	filePart, ok := contentParts[0].(openrouter.FilePart)
	require.True(t, ok)
	assert.Equal(t, "file", filePart.Type)
	assert.Contains(t, filePart.File.FileData, "data:image/png;base64,")
	assert.Equal(t, "test.jpg", filePart.File.FileName)

	// Verify base64 encoding
	expectedBase64 := base64.StdEncoding.EncodeToString(testData)
	assert.Contains(t, filePart.File.FileData, expectedBase64)
}

// TestExtractor_BuildMultimodalMessages_PDF tests PDF file message building.
func TestExtractor_BuildMultimodalMessages_PDF(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	testData := []byte("%PDF-1.4 fake pdf content")
	artifact := testExtractorArtifact("pdf", int64(len(testData)))
	artifact.OriginalName = "document.pdf"

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	messages, err := extractor.buildMultimodalMessages(context.Background(), req, artifact, testData)
	require.NoError(t, err)

	userMsg := messages[1]
	contentParts := userMsg.Content.([]interface{})
	filePart := contentParts[0].(openrouter.FilePart)

	assert.Contains(t, filePart.File.FileData, "data:application/pdf;base64,")
	assert.Equal(t, "document.pdf", filePart.File.FileName)
}

// TestExtractor_BuildMultimodalMessages_Audio tests audio/voice message building.
func TestExtractor_BuildMultimodalMessages_Audio(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	testData := []byte("OGG audio data")
	artifact := testExtractorArtifact("voice", int64(len(testData)))
	artifact.MimeType = "" // Test fallback mime type

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	messages, err := extractor.buildMultimodalMessages(context.Background(), req, artifact, testData)
	require.NoError(t, err)

	userMsg := messages[1]
	contentParts := userMsg.Content.([]interface{})
	filePart := contentParts[0].(openrouter.FilePart)

	// Should use fallback mime type for voice
	assert.Contains(t, filePart.File.FileData, "data:audio/ogg;base64,")
}

// TestExtractor_BuildMultimodalMessages_VideoNote tests video_note message building.
func TestExtractor_BuildMultimodalMessages_VideoNote(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	testData := []byte("MP4 video data")
	artifact := testExtractorArtifact("video_note", int64(len(testData)))
	artifact.MimeType = "" // Force fallback mime type

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	messages, err := extractor.buildMultimodalMessages(context.Background(), req, artifact, testData)
	require.NoError(t, err)

	userMsg := messages[1]
	contentParts := userMsg.Content.([]interface{})
	filePart := contentParts[0].(openrouter.FilePart)

	assert.Contains(t, filePart.File.FileData, "data:video/mp4;base64,")
}

// TestExtractor_BuildMultimodalMessages_Document tests document (text) message building.
func TestExtractor_BuildMultimodalMessages_Document(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	testData := []byte("This is plain text content from a document")
	artifact := testExtractorArtifact("document", int64(len(testData)))

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	messages, err := extractor.buildMultimodalMessages(context.Background(), req, artifact, testData)
	require.NoError(t, err)

	userMsg := messages[1]
	contentParts := userMsg.Content.([]interface{})

	// Documents use TextPart instead of FilePart
	textPart := contentParts[0].(openrouter.TextPart)
	assert.Equal(t, "text", textPart.Type)
	assert.Equal(t, string(testData), textPart.Text)
}

// TestExtractor_BuildMultimodalMessages_UnknownType tests fallback for unknown file types.
func TestExtractor_BuildMultimodalMessages_UnknownType(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	testData := []byte("unknown file data")
	artifact := testExtractorArtifact("unknown_type", int64(len(testData)))
	artifact.MimeType = ""

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	messages, err := extractor.buildMultimodalMessages(context.Background(), req, artifact, testData)
	require.NoError(t, err)

	userMsg := messages[1]
	contentParts := userMsg.Content.([]interface{})
	filePart := contentParts[0].(openrouter.FilePart)

	// Should use application/octet-stream for unknown types
	assert.Contains(t, filePart.File.FileData, "data:application/octet-stream;base64,")
}

// TestExtractor_Execute_WithUserContext tests extraction with user_context.
func TestExtractor_Execute_WithUserContext(t *testing.T) {
	llmResponse := `{
		"summary": "Test summary",
		"keywords": ["test"],
		"entities": [],
		"rag_hints": []
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(testutil.MockEmbeddingResponse(), nil)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	// Set user context with special characters that need XML escaping
	userContext := "This is a photo from <my vacation> & it shows AT&T logo"
	artifact := testExtractorArtifact("image", int64(len(testData)))
	artifact.UserContext = &userContext

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	require.NoError(t, err)

	// Verify the context was passed and XML escaped
	mockClient.AssertCalled(t, "CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		if len(req.Messages) < 2 {
			return false
		}
		userMsg := req.Messages[1]
		contentParts, ok := userMsg.Content.([]interface{})
		if !ok || len(contentParts) < 2 {
			return false
		}
		// Second part should be the user prompt text
		textPart, ok := contentParts[1].(openrouter.TextPart)
		if !ok {
			return false
		}
		// Verify XML escaping was applied
		return strings.Contains(textPart.Text, "&lt;my vacation&gt;") &&
			strings.Contains(textPart.Text, "AT&amp;T")
	}))

	assert.NotNil(t, resp)
}

// TestExtractor_Execute_RetryTracking tests retry count tracking on failure.
func TestExtractor_Execute_RetryTracking(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, assert.AnError)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	// Start with existing retry count
	lastFailed := time.Now().Add(-1 * time.Hour)
	artifact := testExtractorArtifact("image", int64(len(testData)))
	artifact.RetryCount = 1
	artifact.LastFailedAt = &lastFailed

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	assert.Nil(t, resp)
	assert.Error(t, err)

	// Verify retry count was incremented
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "failed" && a.RetryCount == 2 && a.LastFailedAt != nil
	}))

	mockStorage.AssertExpectations(t)
}

// TestExtractor_Execute_SuccessResetsRetry tests that success clears retry tracking.
func TestExtractor_Execute_SuccessResetsRetry(t *testing.T) {
	llmResponse := `{
		"summary": "Test summary",
		"keywords": ["test"],
		"entities": [],
		"rag_hints": []
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(testutil.MockEmbeddingResponse(), nil)

	mockStorage := &testutil.MockStorage{}
	mockStorage.On("UpdateArtifact", mock.Anything).Return(nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	translator := testutil.TestTranslator(t)

	tmpDir := t.TempDir()
	fileStorage := files.NewFileStorage(tmpDir, testutil.TestLogger())

	// Create test file
	testData := []byte("fake image data")
	testPath := fileStorage.GetFullPath("test/file.jpg")
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0755))
	require.NoError(t, os.WriteFile(testPath, testData, 0644))

	extractor := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)

	// Start with retry tracking set
	lastFailed := time.Now().Add(-1 * time.Hour)
	artifact := testExtractorArtifact("image", int64(len(testData)))
	artifact.RetryCount = 2
	artifact.LastFailedAt = &lastFailed

	req := &agent.Request{
		Params: map[string]any{
			ParamArtifact: artifact,
		},
	}

	resp, err := extractor.Execute(context.Background(), req)
	require.NoError(t, err)

	// Verify retry tracking was cleared
	mockStorage.AssertCalled(t, "UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "ready" &&
			a.RetryCount == 0 &&
			a.LastFailedAt == nil &&
			a.ProcessedAt != nil
	}))

	assert.NotNil(t, resp)
}

// TestExtractor_Type tests Type() method.
func TestExtractor_Type(t *testing.T) {
	extractor := &Extractor{}
	assert.Equal(t, agent.TypeExtractor, extractor.Type())
}

// TestExtractor_Capabilities tests Capabilities() method.
func TestExtractor_Capabilities(t *testing.T) {
	extractor := &Extractor{}
	caps := extractor.Capabilities()

	assert.False(t, caps.IsAgentic)
	assert.Equal(t, "json", caps.OutputFormat)
	assert.ElementsMatch(t, []string{"image", "audio", "pdf", "video_note"}, caps.SupportedMedia)
}

// TestExtractor_Description tests Description() method.
func TestExtractor_Description(t *testing.T) {
	extractor := &Extractor{}
	desc := extractor.Description()
	assert.NotEmpty(t, desc)
	assert.Contains(t, strings.ToLower(desc), "artifact")
}

// TestExtractor_ProcessResult_Marshal tests ProcessResult JSON marshaling.
func TestExtractor_ProcessResult_Marshal(t *testing.T) {
	result := &ProcessResult{
		ArtifactID: 123,
		Summary:    "Test summary",
		Keywords:   []string{"keyword1", "keyword2"},
		Entities:   []string{"entity1"},
		RAGHints:   []string{"hint1", "hint2"},
		Duration:   100 * time.Millisecond,
		Tokens: agent.TokenUsage{
			Prompt:     100,
			Completion: 50,
			Total:      150,
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled ProcessResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, result.ArtifactID, unmarshaled.ArtifactID)
	assert.Equal(t, result.Summary, unmarshaled.Summary)
	assert.Equal(t, result.Keywords, unmarshaled.Keywords)
	assert.Equal(t, result.Entities, unmarshaled.Entities)
	assert.Equal(t, result.RAGHints, unmarshaled.RAGHints)
}
