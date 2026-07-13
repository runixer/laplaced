package main

import (
	"context"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/llm"
)

type agentClientRoute struct {
	client         llm.Client
	enableThinking bool
}

type routedClient struct {
	fallback   llm.Client
	embeddings llm.Client
	routes     map[agent.AgentType]agentClientRoute
}

func (c *routedClient) CreateChatCompletion(ctx context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	route := c.route(ctx)
	applyChatOptions(&req, route.enableThinking)
	return route.client.CreateChatCompletion(ctx, req)
}

func (c *routedClient) CreateChatCompletionStream(ctx context.Context, req llm.ChatCompletionRequest) (*llm.ChatCompletionStream, error) {
	route := c.route(ctx)
	applyChatOptions(&req, route.enableThinking)
	return route.client.CreateChatCompletionStream(ctx, req)
}

func (c *routedClient) route(ctx context.Context) agentClientRoute {
	if agentType, ok := agent.AgentTypeFromContext(ctx); ok {
		if route, exists := c.routes[agentType]; exists {
			return route
		}
	}
	return agentClientRoute{client: c.fallback}
}

func applyChatOptions(req *llm.ChatCompletionRequest, enableThinking bool) {
	if !enableThinking {
		return
	}
	kwargs := make(map[string]any, len(req.ChatTemplateKwargs)+1)
	for key, value := range req.ChatTemplateKwargs {
		kwargs[key] = value
	}
	kwargs["enable_thinking"] = true
	req.ChatTemplateKwargs = kwargs
}

func (c *routedClient) CreateEmbeddings(ctx context.Context, req llm.EmbeddingRequest) (llm.EmbeddingResponse, error) {
	return c.embeddings.CreateEmbeddings(ctx, req)
}
