package merger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/testutil"
)

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

// TestMerger_Execute_RecordsSpan checks both the should_merge bool and the
// per-topic size attrs that let TraceQL surface "merge decisions on small
// topic pairs" vs "huge consolidation rejections" without joining out to the
// DB. The IDs themselves live on rag.processConsolidation (parent).
func TestMerger_Execute_RecordsSpan(t *testing.T) {
	getSpans := withTracingCapture(t)

	mockClient := &testutil.MockLLMClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(`{"should_merge": true, "new_summary": "merged"}`), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	m := New(executor, testutil.TestTranslator(t), cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{UserID: "7"},
		Params: map[string]any{
			ParamTopic1Summary: "first",
			ParamTopic2Summary: "second topic",
		},
	}
	_, err := m.Execute(context.Background(), req)
	require.NoError(t, err)

	var found *tracetest.SpanStub
	for i := range getSpans() {
		s := getSpans()[i]
		if s.Name == "merger.Execute" {
			found = &s
			break
		}
	}
	require.NotNil(t, found)
	attrs := collectAttrs(found.Attributes)
	assert.Equal(t, "7", attrs["user.id"].AsString())
	assert.Equal(t, int64(5), attrs["merger.topic1_size_chars"].AsInt64())
	assert.Equal(t, int64(12), attrs["merger.topic2_size_chars"].AsInt64())
	assert.True(t, attrs["merger.should_merge"].AsBool())
	assert.False(t, attrs["merger.parse_error"].AsBool())
}
