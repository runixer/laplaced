package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentTypeContext(t *testing.T) {
	_, ok := AgentTypeFromContext(context.Background())
	require.False(t, ok)

	ctx := WithAgentType(context.Background(), TypeSplitter)
	got, ok := AgentTypeFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, TypeSplitter, got)

	overridden := WithAgentType(ctx, TypeArchivist)
	got, ok = AgentTypeFromContext(overridden)
	require.True(t, ok)
	require.Equal(t, TypeArchivist, got)
}
