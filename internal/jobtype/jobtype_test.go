package jobtype

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobTypeString(t *testing.T) {
	tests := []struct {
		jt       JobType
		expected string
	}{
		{Interactive, "interactive"},
		{Background, "background"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.jt.String())
		})
	}
}

func TestFromContext_Default(t *testing.T) {
	ctx := context.Background()
	jt := FromContext(ctx)
	assert.Equal(t, Interactive, jt, "should return Interactive as default")
}

func TestWithJobType_Interactive(t *testing.T) {
	ctx := context.Background()
	ctx = WithJobType(ctx, Interactive)
	jt := FromContext(ctx)
	assert.Equal(t, Interactive, jt)
}

func TestWithJobType_Background(t *testing.T) {
	ctx := context.Background()
	ctx = WithJobType(ctx, Background)
	jt := FromContext(ctx)
	assert.Equal(t, Background, jt)
}

func TestWithJobType_Override(t *testing.T) {
	ctx := context.Background()

	// Set to Interactive first
	ctx = WithJobType(ctx, Interactive)
	assert.Equal(t, Interactive, FromContext(ctx))

	// Override with Background
	ctx = WithJobType(ctx, Background)
	assert.Equal(t, Background, FromContext(ctx))
}

func TestWithJobType_Propagation(t *testing.T) {
	// Simulate context propagation through call stack
	ctx := context.Background()
	ctx = WithJobType(ctx, Background)

	// Child contexts should inherit the job type
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	assert.Equal(t, Background, FromContext(childCtx))
}
