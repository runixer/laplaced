package splitter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// withTracingCapture installs an in-memory exporter for the duration of the
// test and restores the previous TracerProvider on cleanup. Inlined here to
// avoid coupling to internal/testutil — the test util package already imports
// openrouter, and pulling tracetest into testutil would invert that dependency.
func withTracingCapture(t *testing.T) func() tracetest.SpanStubs {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return exporter.GetSpans
}

func collectAttrs(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	out := make(map[attribute.Key]attribute.Value, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = kv.Value
	}
	return out
}

// TestSplitter_Execute_RecordsSpan verifies the splitter.Execute span carries
// input shape on entry and topics_returned on exit. This is the parent the
// child openrouter.CreateChatCompletion span attaches to, so without it the
// motivating splitter incident has no traceable context.
func TestSplitter_Execute_RecordsSpan(t *testing.T) {
	getSpans := withTracingCapture(t)

	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "hi", CreatedAt: time.Now()},
		{ID: 101, Role: "assistant", Content: "hello", CreatedAt: time.Now()},
	}
	llmResponse := `{"topics":[{"summary":"greeting","start_msg_id":100,"end_msg_id":101}]}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)
	splitter := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{UserID: "42"},
		Params: map[string]any{ParamMessages: messages},
	}
	_, err := splitter.Execute(context.Background(), req)
	require.NoError(t, err)

	// Find the splitter.Execute span among captured spans.
	var found *tracetest.SpanStub
	for i := range getSpans() {
		s := getSpans()[i]
		if s.Name == "splitter.Execute" {
			found = &s
			break
		}
	}
	require.NotNil(t, found, "splitter.Execute span not captured")
	attrs := collectAttrs(found.Attributes)
	assert.Equal(t, "42", attrs["user.id"].AsString())
	assert.Equal(t, int64(2), attrs["splitter.input_count"].AsInt64())
	assert.Equal(t, int64(1), attrs["splitter.topics_returned"].AsInt64())
	assert.False(t, attrs["splitter.parse_error"].AsBool())
	assert.Equal(t, sdkcodes.Unset, found.Status.Code)
}

// TestSplitter_Execute_ParseErrorFlag confirms that a malformed LLM response
// sets splitter.parse_error=true on the span — the diagnostic we need to tell
// "model didn't return valid JSON" apart from "model returned gappy topics".
func TestSplitter_Execute_ParseErrorFlag(t *testing.T) {
	getSpans := withTracingCapture(t)

	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "hi", CreatedAt: time.Now()},
	}
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("not json at all"), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)
	splitter := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{UserID: "42"},
		Params: map[string]any{ParamMessages: messages},
	}
	_, err := splitter.Execute(context.Background(), req)
	require.Error(t, err)

	var found *tracetest.SpanStub
	for i := range getSpans() {
		s := getSpans()[i]
		if s.Name == "splitter.Execute" {
			found = &s
			break
		}
	}
	require.NotNil(t, found)
	attrs := collectAttrs(found.Attributes)
	assert.True(t, attrs["splitter.parse_error"].AsBool())
	assert.Equal(t, sdkcodes.Error, found.Status.Code)
}
