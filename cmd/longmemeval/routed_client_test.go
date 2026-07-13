package main

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRoutedClientRoutesByAgentType(t *testing.T) {
	fallback := new(testutil.MockLLMClient)
	splitter := new(testutil.MockLLMClient)
	archivist := new(testutil.MockLLMClient)

	splitter.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return req.Model == "same-model" && req.ChatTemplateKwargs == nil
	})).Return(llm.ChatCompletionResponse{}, nil).Once()
	archivist.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return req.Model == "same-model" && req.ChatTemplateKwargs["enable_thinking"] == true && req.ChatTemplateKwargs["existing"] == "kept"
	})).Return(llm.ChatCompletionResponse{}, nil).Once()
	fallback.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(llm.ChatCompletionResponse{}, nil).Once()

	client := &routedClient{
		fallback: fallback, embeddings: fallback,
		routes: map[agent.AgentType]agentClientRoute{
			agent.TypeSplitter:  {client: splitter},
			agent.TypeArchivist: {client: archivist, enableThinking: true},
		},
	}
	request := llm.ChatCompletionRequest{Model: "same-model"}
	_, err := client.CreateChatCompletion(agent.WithAgentType(context.Background(), agent.TypeSplitter), request)
	require.NoError(t, err)
	request.ChatTemplateKwargs = map[string]any{"existing": "kept"}
	_, err = client.CreateChatCompletion(agent.WithAgentType(context.Background(), agent.TypeArchivist), request)
	require.NoError(t, err)
	_, err = client.CreateChatCompletion(context.Background(), request)
	require.NoError(t, err)

	fallback.AssertExpectations(t)
	splitter.AssertExpectations(t)
	archivist.AssertExpectations(t)
}
