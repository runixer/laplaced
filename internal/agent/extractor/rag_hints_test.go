package extractor

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestExtractor_buildJSONSchema(t *testing.T) {
	ex := &Extractor{}
	schema := ex.buildJSONSchema()

	require.NotNil(t, schema)
	assert.Equal(t, "artifact_metadata", schema.Name)
	assert.True(t, schema.Strict)
	assert.Equal(t, "object", schema.Schema["type"])
	assert.Equal(t, false, schema.Schema["additionalProperties"])
	assert.ElementsMatch(t,
		[]string{"summary", "keywords", "entities", "rag_hints"},
		schema.Schema["required"],
	)

	properties := schema.Schema["properties"].(map[string]interface{})
	assert.Equal(t, "string", properties["summary"].(map[string]interface{})["type"])
	for _, field := range []string{"keywords", "entities", "rag_hints"} {
		property := properties[field].(map[string]interface{})
		assert.Equal(t, "array", property["type"])
		assert.Equal(t, "string", property["items"].(map[string]interface{})["type"])
	}
}

func TestExtractor_Execute_SendsStrictJSONSchema(t *testing.T) {
	llmResponse := `{"summary":"summary","keywords":[],"entities":[],"rag_hints":[]}`
	mockClient := &testutil.MockLLMClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		format, ok := req.ResponseFormat.(llm.ResponseFormatJSONSchema)
		if !ok {
			return false
		}
		return format.Type == "json_schema" &&
			format.JSONSchema.Name == "artifact_metadata" &&
			format.JSONSchema.Strict
	})).Return(testutil.MockChatResponse(llmResponse), nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(testutil.MockEmbeddingResponse(), nil)

	ex, artifact, mockStorage := executableExtractorFixture(t, mockClient)
	mockStorage.On("UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "ready"
	})).Return(nil).Once()
	resp, err := ex.Execute(context.Background(), &agent.Request{
		Params: map[string]any{ParamArtifact: artifact},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	mockClient.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func TestExtractor_Execute_TerminalAttemptDropsOnlyRAGHints(t *testing.T) {
	getSpans := withExtractorTracingCapture(t)
	llmResponse := `{
		"summary": "Summary survives malformed hints",
		"keywords": ["preserved"],
		"entities": ["entity"],
		"rag_hints": {"unexpected": "object"}
	}`

	mockClient := &testutil.MockLLMClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).
		Return(testutil.MockEmbeddingResponse(), nil)

	ex, artifact, mockStorage := executableExtractorFixture(t, mockClient)
	ex.cfg.Agents.Extractor.MaxRetries = 3
	artifact.RetryCount = 2

	var ready storage.Artifact
	mockStorage.On("UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State == "ready"
	})).Run(func(args mock.Arguments) {
		ready = args.Get(0).(storage.Artifact)
	}).Return(nil).Once()

	resp, err := ex.Execute(context.Background(), &agent.Request{
		Params: map[string]any{ParamArtifact: artifact},
	})
	require.NoError(t, err)
	result, ok := resp.Structured.(*ProcessResult)
	require.True(t, ok)
	assert.Equal(t, "Summary survives malformed hints", result.Summary)
	assert.Equal(t, []string{"preserved"}, result.Keywords)
	assert.Empty(t, result.RAGHints)
	require.NotNil(t, ready.RAGHints)
	assert.Equal(t, "[]", *ready.RAGHints)
	assert.Equal(t, "ready", ready.State)
	assert.Zero(t, ready.RetryCount)
	assert.NotNil(t, ready.Embedding)

	var executeSpan *tracetest.SpanStub
	for _, span := range getSpans() {
		if span.Name == "extractor.Execute" {
			spanCopy := span
			executeSpan = &spanCopy
			break
		}
	}
	require.NotNil(t, executeSpan)
	attrs := extractorAttributes(executeSpan.Attributes)
	assert.True(t, attrs["extractor.rag_hints_dropped"].AsBool())
	assert.True(t, attrs["extractor.json_repaired"].AsBool())
	assert.False(t, attrs["extractor.parse_error"].AsBool())

	mockClient.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func TestExtractor_Execute_NonTerminalMalformedHintsStillRetry(t *testing.T) {
	llmResponse := `{"summary":"summary","keywords":[],"entities":[],"rag_hints":{"bad":true}}`
	mockClient := &testutil.MockLLMClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	ex, artifact, mockStorage := executableExtractorFixture(t, mockClient)
	ex.cfg.Agents.Extractor.MaxRetries = 3
	artifact.RetryCount = 1

	resp, err := ex.Execute(context.Background(), &agent.Request{
		Params: map[string]any{ParamArtifact: artifact},
	})
	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse extraction JSON")
	assert.Equal(t, "failed", artifact.State)
	assert.Equal(t, 2, artifact.RetryCount)
	mockClient.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
	mockClient.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func executableExtractorFixture(
	t *testing.T,
	mockClient *testutil.MockLLMClient,
) (*Extractor, *storage.Artifact, *testutil.MockStorage) {
	t.Helper()

	mockStorage := &testutil.MockStorage{}
	// The ready-state update has a more specific expectation in the terminal
	// degradation test; this default covers processing and failed updates.
	mockStorage.On("UpdateArtifact", mock.MatchedBy(func(a storage.Artifact) bool {
		return a.State != "ready"
	})).Return(nil)

	cfg := testutil.TestConfig()
	cfg.Agents.Extractor.MaxFileSizeMB = 10
	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	translator := testutil.TestTranslator(t)
	fileStorage := files.NewFileStorage(t.TempDir(), testutil.TestLogger())

	testData := []byte("synthetic image data")
	artifact := testExtractorArtifact("image", int64(len(testData)))
	require.NoError(t, os.MkdirAll(fileStorage.GetFullPath("test"), 0o755))
	require.NoError(t, os.WriteFile(fileStorage.GetFullPath(artifact.FilePath), testData, 0o644))

	ex := New(executor, translator, cfg, testutil.TestLogger(), fileStorage, mockClient, mockStorage)
	return ex, artifact, mockStorage
}

func withExtractorTracingCapture(t *testing.T) func() tracetest.SpanStubs {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	return exporter.GetSpans
}

func extractorAttributes(values []attribute.KeyValue) map[attribute.Key]attribute.Value {
	result := make(map[attribute.Key]attribute.Value, len(values))
	for _, value := range values {
		result[value.Key] = value.Value
	}
	return result
}
