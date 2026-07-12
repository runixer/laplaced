package main

import (
	"context"

	"github.com/runixer/laplaced/internal/llm"
)

type routedClient struct {
	chat           llm.Client
	embeddings     llm.Client
	enableThinking bool
}

func (c *routedClient) CreateChatCompletion(ctx context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	c.applyChatOptions(&req)
	return c.chat.CreateChatCompletion(ctx, req)
}

func (c *routedClient) CreateChatCompletionStream(ctx context.Context, req llm.ChatCompletionRequest) (*llm.ChatCompletionStream, error) {
	c.applyChatOptions(&req)
	return c.chat.CreateChatCompletionStream(ctx, req)
}

func (c *routedClient) applyChatOptions(req *llm.ChatCompletionRequest) {
	if c.enableThinking {
		req.ChatTemplateKwargs = map[string]any{"enable_thinking": true}
	}
}

func (c *routedClient) CreateEmbeddings(ctx context.Context, req llm.EmbeddingRequest) (llm.EmbeddingResponse, error) {
	return c.embeddings.CreateEmbeddings(ctx, req)
}
