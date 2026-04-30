// Package obs houses cross-cutting observability wiring (tracing today,
// metrics/logs later). It owns the lifecycle of the global OpenTelemetry
// TracerProvider so the rest of the code can fetch a tracer via
// otel.Tracer(...) without knowing about exporters or SDK configuration.
package obs

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Exporter names for TracingConfig.Exporter.
const (
	ExporterOTLP   = "otlp"
	ExporterStdout = "stdout"
)

// TracingConfig is the minimal contract tracing.Init needs from caller config.
// Defined here (not imported from internal/config) to keep internal/obs free
// of upstream dependencies — useful for tests and for future reuse.
type TracingConfig struct {
	Enabled      bool
	Exporter     string // "otlp" (default) or "stdout"
	OTLPEndpoint string // required when Exporter == "otlp"
	ServiceName  string
	// TraceContent, when true, flips the content toggle via SetContentEnabled
	// so RecordContent starts attaching body-bearing span events. The trace
	// carries a resource attribute laplaced.trace_content=true so the mode
	// is self-identifying at query time.
	TraceContent bool
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
// When enabled, it builds an exporter based on cfg.Exporter:
//   - "otlp" (default): OTLP/gRPC pointed at cfg.OTLPEndpoint (loopback,
//     insecure — for a local collector like Alloy on the same host).
//   - "stdout": pretty-prints spans to stderr, for local dev where
//     network-level delivery is not what's being tested.
//
// Sampling is AlwaysSample: low-volume workload, short retention downstream.
//
// serviceVersion is recorded as the service.version resource attribute;
// callers typically pass the build-time Version variable.
func InitTracing(ctx context.Context, cfg TracingConfig, serviceVersion string) (ShutdownFunc, error) {
	if !cfg.Enabled {
		return noopShutdown, nil
	}

	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "laplaced"
	}

	resAttrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	}
	if cfg.TraceContent {
		// Self-identify traces captured while content recording is on.
		// Useful at query time to separate debug-session traces from
		// everyday ones.
		resAttrs = append(resAttrs, attribute.Bool("laplaced.trace_content", true))
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, resAttrs...),
	)
	if err != nil {
		// Merge only fails on schema URL conflicts. If it does, fall back
		// to the non-merged custom resource — better a working tracer with
		// a minimal resource than no tracer at all.
		res = resource.NewWithAttributes(semconv.SchemaURL, resAttrs...)
	}

	// Flip the content toggle BEFORE returning so spans opened by early
	// caller code already see the right state.
	SetContentEnabled(cfg.TraceContent)

	// Empty Exporter defaults to OTLP for backwards compatibility with
	// existing prod config that predates the field.
	exporterName := cfg.Exporter
	if exporterName == "" {
		exporterName = ExporterOTLP
	}

	var spanProcessor sdktrace.SpanProcessor
	switch exporterName {
	case ExporterOTLP:
		if cfg.OTLPEndpoint == "" {
			return noopShutdown, fmt.Errorf("obs: OTLPEndpoint is required when exporter is %q", ExporterOTLP)
		}
		exporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return noopShutdown, fmt.Errorf("obs: create OTLP exporter: %w", err)
		}
		// Batching is fine in long-running processes (cmd/bot) where
		// the shutdown defer gives the batcher time to drain.
		spanProcessor = sdktrace.NewBatchSpanProcessor(exporter)
	case ExporterStdout:
		// Write to stderr so it does not mix with JSON logs on stdout.
		exporter, err := stdouttrace.New(
			stdouttrace.WithWriter(os.Stderr),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return noopShutdown, fmt.Errorf("obs: create stdout exporter: %w", err)
		}
		// Sync processor: short-lived CLI processes would otherwise
		// exit before a batcher flushes.
		spanProcessor = sdktrace.NewSimpleSpanProcessor(exporter)
	default:
		return noopShutdown, fmt.Errorf("obs: unknown exporter %q (expected %q or %q)", cfg.Exporter, ExporterOTLP, ExporterStdout)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(spanProcessor),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
