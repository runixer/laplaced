package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReferenceTime(t *testing.T) {
	contextTime := time.Date(2024, time.February, 3, 0, 0, 0, 0, time.UTC)
	requestTime := time.Date(2024, time.March, 4, 0, 0, 0, 0, time.UTC)

	ctx := WithReferenceTime(context.Background(), contextTime)
	assert.Equal(t, contextTime, ReferenceTime(ctx, nil))

	req := &Request{Shared: &SharedContext{ReferenceTime: requestTime}}
	assert.Equal(t, requestTime, ReferenceTime(ctx, req))
	assert.True(t, ReferenceTime(context.Background(), nil).IsZero())
}
