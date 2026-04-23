package obs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitTracing_Disabled_ReturnsNoopShutdown(t *testing.T) {
	// An unreachable endpoint would surface as an error if Init actually
	// tried to build an exporter. Disabled path must bypass that entirely.
	cfg := TracingConfig{
		Enabled:      false,
		OTLPEndpoint: "192.0.2.1:4317", // TEST-NET-1, guaranteed unreachable
	}

	shutdown, err := InitTracing(context.Background(), cfg, "test-version")
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	assert.NoError(t, shutdown(ctx))
}

func TestInitTracing_Enabled_EmptyEndpoint_ReturnsError(t *testing.T) {
	cfg := TracingConfig{
		Enabled:      true,
		OTLPEndpoint: "",
	}

	shutdown, err := InitTracing(context.Background(), cfg, "test-version")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OTLPEndpoint is required")
	// Even on error, the returned ShutdownFunc must be non-nil and safe to call.
	require.NotNil(t, shutdown)
	assert.NoError(t, shutdown(context.Background()))
}

func TestInitTracing_Enabled_UnreachableEndpoint_StillInits(t *testing.T) {
	// The OTLP/gRPC exporter uses non-blocking connect by default — pointing
	// at an unreachable address must not fail init, spans just buffer and
	// drop on shutdown. This guards the "collector down" degradation path.
	cfg := TracingConfig{
		Enabled:      true,
		OTLPEndpoint: "127.0.0.1:1", // port 1, nothing there
		ServiceName:  "laplaced-test",
	}

	shutdown, err := InitTracing(context.Background(), cfg, "test-version")
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Shutdown with a short timeout — we expect it to return promptly, not
	// hang trying to flush to the dead endpoint.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Error or not is fine (the exporter may report the inability to
	// connect). We only assert it returned within the deadline.
	_ = shutdown(ctx)
	assert.NoError(t, ctx.Err(), "shutdown must return before context deadline")
}
