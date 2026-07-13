package agent

import "context"

type agentTypeContextKey struct{}

// WithAgentType associates an LLM call with the agent that initiated it.
func WithAgentType(ctx context.Context, agentType AgentType) context.Context {
	return context.WithValue(ctx, agentTypeContextKey{}, agentType)
}

// AgentTypeFromContext returns the agent associated with the current LLM call.
func AgentTypeFromContext(ctx context.Context) (AgentType, bool) {
	agentType, ok := ctx.Value(agentTypeContextKey{}).(AgentType)
	return agentType, ok
}
