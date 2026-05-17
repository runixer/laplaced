package obs

import (
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ObserveErr records err on span and sets Error status when err is non-nil,
// then returns err unchanged so callers can one-line it:
//
//	return obs.ObserveErr(span, doThing())
//
// When err is nil it is a no-op.
func ObserveErr(span trace.Span, err error) error {
	if err == nil {
		return nil
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	return err
}
