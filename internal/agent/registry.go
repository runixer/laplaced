package agent

import (
	"fmt"
	"sync"
)

// Registry holds all registered agents.
type Registry struct {
	agents map[AgentType]Agent
	mu     sync.RWMutex
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[AgentType]Agent),
	}
}

// Register adds an agent to the registry.
func (r *Registry) Register(agent Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.Type()] = agent
}

// Get retrieves an agent by type.
// Returns nil if not found.
func (r *Registry) Get(t AgentType) Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[t]
}

// MustGet retrieves an agent or panics (for initialization).
func (r *Registry) MustGet(t AgentType) Agent {
	agent := r.Get(t)
	if agent == nil {
		panic(fmt.Sprintf("agent %s not registered", t))
	}
	return agent
}

// List returns all registered agents.
func (r *Registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Agent, 0, len(r.agents))
	for _, a := range r.agents {
		list = append(list, a)
	}
	return list
}

// Types returns all registered agent types.
func (r *Registry) Types() []AgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]AgentType, 0, len(r.agents))
	for t := range r.agents {
		types = append(types, t)
	}
	return types
}

// Has checks if an agent type is registered.
func (r *Registry) Has(t AgentType) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[t]
	return ok
}

// Count returns the number of registered agents.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}
