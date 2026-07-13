package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/runixer/laplaced/internal/agent"
	"gopkg.in/yaml.v3"
)

type matrixConfig struct {
	Variants []matrixVariant `yaml:"variants"`
}

type matrixVariant struct {
	Name         string            `yaml:"name"`
	ChatBaseURL  string            `yaml:"chat_base_url"`
	ChatModel    string            `yaml:"chat_model"`
	ChatProxy    string            `yaml:"chat_proxy"`
	ChatThinking bool              `yaml:"chat_thinking"`
	Agents       matrixAgentRoutes `yaml:"agents"`
}

type matrixAgentRoutes struct {
	Splitter  *matrixAgentRoute `yaml:"splitter"`
	Archivist *matrixAgentRoute `yaml:"archivist"`
	Merger    *matrixAgentRoute `yaml:"merger"`
	Enricher  *matrixAgentRoute `yaml:"enricher"`
	Reranker  *matrixAgentRoute `yaml:"reranker"`
	Answerer  *matrixAgentRoute `yaml:"answerer"`
}

type matrixAgentRoute struct {
	BaseURL      string `yaml:"base_url"`
	Model        string `yaml:"model"`
	Proxy        string `yaml:"proxy"`
	ChatThinking bool   `yaml:"chat_template_thinking"`
}

func loadMatrix(path string) ([]matrixVariant, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read matrix: %w", err)
	}
	var matrix matrixConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&matrix); err != nil {
		return nil, fmt.Errorf("decode matrix: %w", err)
	}
	if len(matrix.Variants) == 0 {
		return nil, fmt.Errorf("matrix must contain at least one variant")
	}
	seen := make(map[string]struct{}, len(matrix.Variants))
	for i := range matrix.Variants {
		variant := &matrix.Variants[i]
		variant.Name = strings.TrimSpace(variant.Name)
		if variant.Name == "" {
			return nil, fmt.Errorf("matrix variant %d has no name", i)
		}
		if _, exists := seen[variant.Name]; exists {
			return nil, fmt.Errorf("duplicate matrix variant name %q", variant.Name)
		}
		seen[variant.Name] = struct{}{}
		if (variant.ChatBaseURL == "") != (variant.ChatModel == "") {
			return nil, fmt.Errorf("variant %q must set chat_base_url and chat_model together", variant.Name)
		}
		if variant.ChatThinking && variant.ChatBaseURL == "" {
			return nil, fmt.Errorf("variant %q enables chat_thinking without a chat endpoint", variant.Name)
		}
		if variant.Agents.any() && variant.ChatBaseURL != "" {
			return nil, fmt.Errorf("variant %q cannot combine chat_* and agents routes", variant.Name)
		}
		for role, route := range variant.Agents.routes() {
			if (route.BaseURL == "") != (route.Model == "") {
				return nil, fmt.Errorf("variant %q role %s must set base_url and model together", variant.Name, role)
			}
			if route.ChatThinking && route.BaseURL == "" {
				return nil, fmt.Errorf("variant %q role %s enables chat_template_thinking without an endpoint", variant.Name, role)
			}
		}
	}
	return matrix.Variants, nil
}

func (v matrixVariant) apply(base options) options {
	base.chatBaseURL = v.ChatBaseURL
	base.chatModel = v.ChatModel
	base.chatProxy = v.ChatProxy
	base.chatThinking = v.ChatThinking
	base.roleRoutes = v.Agents.routes()
	return base
}

func (r matrixAgentRoutes) any() bool {
	return len(r.routes()) > 0
}

func (r matrixAgentRoutes) routes() map[agent.AgentType]matrixAgentRoute {
	routes := make(map[agent.AgentType]matrixAgentRoute)
	for role, route := range map[agent.AgentType]*matrixAgentRoute{
		agent.TypeSplitter: r.Splitter, agent.TypeArchivist: r.Archivist,
		agent.TypeMerger: r.Merger, agent.TypeEnricher: r.Enricher,
		agent.TypeReranker: r.Reranker, agent.TypeLaplace: r.Answerer,
	} {
		if route != nil {
			routes[role] = *route
		}
	}
	return routes
}
