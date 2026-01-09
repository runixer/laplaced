package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mockAgent is a simple Agent implementation for testing.
type mockAgent struct {
	agentType AgentType
}

func (m *mockAgent) Type() AgentType {
	return m.agentType
}

func (m *mockAgent) Execute(ctx context.Context, req *Request) (*Response, error) {
	return &Response{Content: "mock response"}, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	registry := NewRegistry()

	agent := &mockAgent{agentType: TypeEnricher}
	registry.Register(agent)

	retrieved := registry.Get(TypeEnricher)
	assert.Equal(t, agent, retrieved)
}

func TestRegistry_Get_NotFound(t *testing.T) {
	registry := NewRegistry()

	retrieved := registry.Get(TypeEnricher)
	assert.Nil(t, retrieved)
}

func TestRegistry_MustGet_Success(t *testing.T) {
	registry := NewRegistry()
	agent := &mockAgent{agentType: TypeEnricher}
	registry.Register(agent)

	assert.NotPanics(t, func() {
		retrieved := registry.MustGet(TypeEnricher)
		assert.Equal(t, agent, retrieved)
	})
}

func TestRegistry_MustGet_Panics(t *testing.T) {
	registry := NewRegistry()

	assert.Panics(t, func() {
		registry.MustGet(TypeEnricher)
	})
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()

	agent1 := &mockAgent{agentType: TypeEnricher}
	agent2 := &mockAgent{agentType: TypeArchivist}
	registry.Register(agent1)
	registry.Register(agent2)

	list := registry.List()
	assert.Len(t, list, 2)
}

func TestRegistry_Types(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&mockAgent{agentType: TypeEnricher})
	registry.Register(&mockAgent{agentType: TypeArchivist})

	types := registry.Types()
	assert.Len(t, types, 2)
	assert.Contains(t, types, TypeEnricher)
	assert.Contains(t, types, TypeArchivist)
}

func TestRegistry_Has(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockAgent{agentType: TypeEnricher})

	assert.True(t, registry.Has(TypeEnricher))
	assert.False(t, registry.Has(TypeArchivist))
}

func TestRegistry_Count(t *testing.T) {
	registry := NewRegistry()
	assert.Equal(t, 0, registry.Count())

	registry.Register(&mockAgent{agentType: TypeEnricher})
	assert.Equal(t, 1, registry.Count())

	registry.Register(&mockAgent{agentType: TypeArchivist})
	assert.Equal(t, 2, registry.Count())
}

func TestRegistry_OverwriteOnReregister(t *testing.T) {
	registry := NewRegistry()

	agent1 := &mockAgent{agentType: TypeEnricher}
	agent2 := &mockAgent{agentType: TypeEnricher}

	registry.Register(agent1)
	registry.Register(agent2)

	retrieved := registry.Get(TypeEnricher)
	assert.Equal(t, agent2, retrieved)
	assert.Equal(t, 1, registry.Count())
}
