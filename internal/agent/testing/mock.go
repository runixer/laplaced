// Package testing provides test utilities for the agent package.
package testing

import (
	"context"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/stretchr/testify/mock"
)

// MockAgent is a testify mock for agent.Agent interface.
type MockAgent struct {
	mock.Mock
}

// Type returns the agent type.
func (m *MockAgent) Type() agent.AgentType {
	args := m.Called()
	return agent.AgentType(args.String(0))
}

// Execute runs the mock agent.
func (m *MockAgent) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*agent.Response), args.Error(1)
}

// Ensure MockAgent implements agent.Agent.
var _ agent.Agent = (*MockAgent)(nil)
