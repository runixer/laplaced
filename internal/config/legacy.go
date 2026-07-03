package config

import (
	"log/slog"
	"os"
	"strings"
)

// This file collects backward-compatibility shims for renamed or restructured
// config. Everything here is scheduled for removal in v1.0.0.

// applyDeprecatedLLMEnvVars maps the pre-rename LAPLACED_OPENROUTER_* env vars
// onto the LLM config so existing deployments keep working across the
// openrouter->llm config rename. Each hit logs a deprecation warning; the
// current LAPLACED_LLM_* names always win because cleanenv applies them after
// this fallback.
func applyDeprecatedLLMEnvVars(cfg *Config) {
	apply := func(oldName, newName string, set func(string)) {
		v, ok := os.LookupEnv(oldName)
		if !ok || v == "" {
			return
		}
		set(v)
		slog.Warn("deprecated environment variable, support will be removed in v1.0.0", "deprecated", oldName, "use", newName)
	}
	apply("LAPLACED_OPENROUTER_API_KEY", "LAPLACED_LLM_API_KEY", func(v string) { cfg.LLM.APIKey = v })
	apply("LAPLACED_OPENROUTER_BASE_URL", "LAPLACED_LLM_BASE_URL", func(v string) { cfg.LLM.BaseURL = v })
	apply("LAPLACED_OPENROUTER_IMAGE_INPUT_FORMAT", "LAPLACED_LLM_IMAGE_INPUT_FORMAT", func(v string) { cfg.LLM.ImageInputFormat = v })
	apply("LAPLACED_OPENROUTER_PROXY_URL", "LAPLACED_LLM_PROXY_URL", func(v string) { cfg.LLM.ProxyURL = v })
	apply("LAPLACED_OPENROUTER_PROVIDER_ORDER", "LAPLACED_LLM_PROVIDER_ORDER", func(v string) {
		parts := strings.Split(v, ",")
		order := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				order = append(order, p)
			}
		}
		cfg.LLM.Provider.Order = order
	})
}

// applyRerankerLegacyDefaults migrates the pre-v0.6.0 flat reranker fields
// (candidates, max_topics, max_people) onto the nested per-type config, and
// backfills defaults for the sections that had none before v0.6.0. Runs as a
// normalization step at the start of Config.Validate, mutating the config.
func (c *Config) applyRerankerLegacyDefaults() {
	r := &c.Agents.Reranker
	if r.Topics.CandidatesLimit == 0 && r.Candidates > 0 {
		r.Topics.CandidatesLimit = r.Candidates
	}
	if r.Topics.Max == 0 && r.MaxTopics > 0 {
		r.Topics.Max = r.MaxTopics
	}
	if r.People.Max == 0 && r.MaxPeople > 0 {
		r.People.Max = r.MaxPeople
	}
	if r.People.CandidatesLimit == 0 {
		r.People.CandidatesLimit = 20
	}
	if r.Artifacts.CandidatesLimit == 0 {
		r.Artifacts.CandidatesLimit = 20
	}
	if r.Artifacts.Max == 0 {
		r.Artifacts.Max = 5
	}
}
