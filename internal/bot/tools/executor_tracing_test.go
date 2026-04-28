package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/testutil"
)

// TestExecuteToolCall_UnknownTool_RecordsSpan asserts the dispatcher span
// covers the error path — unknown tool names still get a span with tool.ok
// false and Error status. Uses the cheapest error path so we don't need to
// wire up an entire tool's dependency graph.
func TestExecuteToolCall_UnknownTool_RecordsSpan(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "known_tool"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	_, err := exec.ExecuteToolCall(context.Background(), CallContext{UserID: 99}, "missing_tool", `{}`)
	require.Error(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "tool_executor.ExecuteToolCall", span.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "missing_tool", attrs["tool.name"].AsString())
	assert.Equal(t, int64(99), attrs["user.id"].AsInt64())
	assert.False(t, attrs["tool.ok"].AsBool(), "unknown tool must flip tool.ok to false")
	assert.Equal(t, sdkcodes.Error, span.Status.Code, "err-return path must set Error status")
}

func TestExecuteToolCall_ContentEventsGatedByToggle(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "known_tool"}}
	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	run := func() []string {
		getSpans := testutil.WithTracingCapture(t)
		_, _ = exec.ExecuteToolCall(context.Background(), CallContext{UserID: 1}, "missing", `{"foo":"bar"}`)
		spans := getSpans()
		require.Len(t, spans, 1)
		names := make([]string, 0, len(spans[0].Events))
		for _, ev := range spans[0].Events {
			names = append(names, ev.Name)
		}
		return names
	}

	t.Run("disabled: only exception event from ObserveErr", func(t *testing.T) {
		prev := obs.ContentEnabled()
		t.Cleanup(func() { obs.SetContentEnabled(prev) })
		obs.SetContentEnabled(false)

		names := run()
		assert.NotContains(t, names, "tool.args")
		assert.NotContains(t, names, "tool.result")
	})

	t.Run("enabled: tool.args present (no result on err path)", func(t *testing.T) {
		prev := obs.ContentEnabled()
		t.Cleanup(func() { obs.SetContentEnabled(prev) })
		obs.SetContentEnabled(true)

		names := run()
		assert.Contains(t, names, "tool.args")
		// result.Content is empty on error paths, so tool.result is not
		// emitted — this is the documented behavior.
		assert.NotContains(t, names, "tool.result")
	})
}
