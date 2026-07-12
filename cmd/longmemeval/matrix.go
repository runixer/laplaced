package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type matrixConfig struct {
	Variants []matrixVariant `yaml:"variants"`
}

type matrixVariant struct {
	Name         string `yaml:"name"`
	ChatBaseURL  string `yaml:"chat_base_url"`
	ChatModel    string `yaml:"chat_model"`
	ChatProxy    string `yaml:"chat_proxy"`
	ChatThinking bool   `yaml:"chat_thinking"`
}

func loadMatrix(path string) ([]matrixVariant, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read matrix: %w", err)
	}
	var matrix matrixConfig
	if err := yaml.Unmarshal(data, &matrix); err != nil {
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
	}
	return matrix.Variants, nil
}

func (v matrixVariant) apply(base options) options {
	base.chatBaseURL = v.ChatBaseURL
	base.chatModel = v.ChatModel
	base.chatProxy = v.ChatProxy
	base.chatThinking = v.ChatThinking
	return base
}
