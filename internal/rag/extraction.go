package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/enricher"
	"github.com/runixer/laplaced/internal/agent/splitter"
	"github.com/runixer/laplaced/internal/storage"
)

type ExtractedTopic struct {
	Summary    string `json:"summary"`
	StartMsgID int64  `json:"start_msg_id"`
	EndMsgID   int64  `json:"end_msg_id"`
}

func (s *Service) extractTopics(ctx context.Context, userID int64, chunk []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	if s.splitterAgent == nil {
		return nil, UsageInfo{}, fmt.Errorf("splitter agent not configured")
	}
	return s.extractTopicsViaAgent(ctx, userID, chunk)
}

// extractTopicsViaAgent delegates topic extraction to the Splitter agent.
func (s *Service) extractTopicsViaAgent(ctx context.Context, userID int64, chunk []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	req := &agent.Request{
		Params: map[string]any{
			splitter.ParamMessages: chunk,
			"user_id":              userID,
		},
	}

	// Try to get SharedContext from ctx (will be nil for background jobs)
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	}

	resp, err := s.splitterAgent.Execute(ctx, req)
	if err != nil {
		return nil, UsageInfo{}, err
	}

	result, ok := resp.Structured.(*splitter.Result)
	if !ok {
		return nil, UsageInfo{}, fmt.Errorf("unexpected result type from splitter agent")
	}

	// Convert splitter.ExtractedTopic to rag.ExtractedTopic
	topics := make([]ExtractedTopic, len(result.Topics))
	for i, t := range result.Topics {
		topics[i] = ExtractedTopic{
			Summary:    t.Summary,
			StartMsgID: t.StartMsgID,
			EndMsgID:   t.EndMsgID,
		}
	}

	usage := UsageInfo{
		PromptTokens:     resp.Tokens.Prompt,
		CompletionTokens: resp.Tokens.Completion,
		TotalTokens:      resp.Tokens.Total,
		Cost:             resp.Tokens.Cost,
	}

	return topics, usage, nil
}

func (s *Service) enrichQuery(ctx context.Context, userID int64, query string, history []storage.Message, mediaParts []interface{}) (string, string, int, error) {
	if s.enricherAgent == nil {
		return query, "", 0, nil // No enrichment if agent not configured
	}
	return s.enrichQueryViaAgent(ctx, userID, query, history, mediaParts)
}

// enrichQueryViaAgent delegates query enrichment to the Enricher agent.
func (s *Service) enrichQueryViaAgent(ctx context.Context, userID int64, query string, history []storage.Message, mediaParts []interface{}) (string, string, int, error) {
	model := s.cfg.Agents.Enricher.GetModel(s.cfg.Agents.Default.Model)
	if model == "" {
		return query, "", 0, nil
	}

	req := &agent.Request{
		Query: query,
		Params: map[string]any{
			enricher.ParamHistory: history,
		},
	}

	// Add media parts if present
	if len(mediaParts) > 0 {
		req.Params[enricher.ParamMediaParts] = mediaParts
	}

	// Try to get SharedContext from ctx
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	}

	resp, err := s.enricherAgent.Execute(ctx, req)
	if err != nil {
		return "", "", 0, err
	}

	tokens := resp.Tokens.Prompt + resp.Tokens.Completion
	enrichedQuery := strings.TrimSpace(resp.Content)

	// Get system prompt from metadata for debugging
	systemPrompt := ""
	if sp, ok := resp.Metadata["system_prompt"].(string); ok {
		systemPrompt = sp
	}

	return enrichedQuery, systemPrompt, tokens, nil
}
