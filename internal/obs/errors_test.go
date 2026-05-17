package obs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkcodes "go.opentelemetry.io/otel/codes"
)

func TestObserveErr_Nil_IsNoop(t *testing.T) {
	getSpans := withTestProvider(t)
	_, span := otel.Tracer("test").Start(context.Background(), "op")

	got := ObserveErr(span, nil)
	span.End()

	require.NoError(t, got)
	spans := getSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, sdkcodes.Unset, spans[0].Status.Code, "nil err must not set status")
	assert.Empty(t, spans[0].Events, "nil err must not record exception")
}

func TestObserveErr_NonNil_SetsErrorAndPassesThrough(t *testing.T) {
	getSpans := withTestProvider(t)
	_, span := otel.Tracer("test").Start(context.Background(), "op")

	want := errors.New("boom")
	got := ObserveErr(span, want)
	span.End()

	assert.Same(t, want, got, "must pass err through unchanged")

	spans := getSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, sdkcodes.Error, spans[0].Status.Code)
	assert.Equal(t, "boom", spans[0].Status.Description)

	// RecordError adds an "exception" event with the err message.
	require.NotEmpty(t, spans[0].Events, "RecordError should add an event")
	assert.Equal(t, "exception", spans[0].Events[0].Name)
}
