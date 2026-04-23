// Package obs houses cross-cutting observability wiring (tracing today,
// metrics/logs later). It owns the lifecycle of the global OpenTelemetry
// TracerProvider so the rest of the code can fetch a tracer via
// otel.Tracer(...) without knowing about exporters or SDK configuration.
package obs

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TracingConfig is the minimal contract tracing.Init needs from caller config.
// Defined here (not imported from internal/config) to keep internal/obs free
// of upstream dependencies — useful for tests and for future reuse.
type TracingConfig struct {
	Enabled      bool
	OTLPEndpoint string
	ServiceName  string
}

// ShutdownFunc flushes pending spans and releases exporter resources.
// Safe to call with a context that has a timeout — the SDK respects deadlines.
type ShutdownFunc func(context.Context) error

// noopShutdown returns nil; used when tracing is disabled or init was skipped.
func noopShutdown(context.Context) error { return nil }

// InitTracing configures the global TracerProvider.
//
// When cfg.Enabled is false, InitTracing is a no-op: the OTel default
// (noop provider) stays in effect, otel.Tracer(...) returns cheap no-op
// spans, and the returned ShutdownFunc does nothing.
//
// When enabled, it builds an OTLP/gRPC exporter pointed at cfg.OTLPEndpoint
// (loopback, insecure — this is for a local collector like Alloy on the
// same host). Sampling is AlwaysSample: low-volume workload, short
// retention downstream.
//
// serviceVersion is recorded as the service.version resource attribute;
// callers typically pass the build-time Version variable.
func InitTracing(ctx context.Context, cfg TracingConfig, serviceVersion string) (ShutdownFunc, error) {
	if !cfg.Enabled {
		return noopShutdown, nil
	}
	if cfg.OTLPEndpoint == "" {
		return noopShutdown, fmt.Errorf("obs: OTLPEndpoint is required when tracing is enabled")
	}

	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "laplaced"
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("obs: create OTLP exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		// Merge only fails on schema URL conflicts. If it does, fall back
		// to the non-merged custom resource — better a working tracer with
		// a minimal resource than no tracer at all.
		res = resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
