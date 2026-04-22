package laplace

import (
	"context"

	"github.com/stretchr/testify/mock"
)

// mockToolHandler is the package-local test double for laplace.ToolHandler.
// It lives here rather than in internal/testutil because ToolCallContext /
// ToolResult are defined in the laplace package — pulling them into testutil
// would create an import cycle (testutil ↔ laplace). See
// docs/TESTING.md "Import Cycle Handling".
type mockToolHandler struct {
	mock.Mock
}

func (m *mockToolHandler) ExecuteToolCall(ctx context.Context, tcc ToolCallContext, toolName, arguments string) (*ToolResult, error) {
	args := m.Called(ctx, tcc, toolName, arguments)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ToolResult), args.Error(1)
}
