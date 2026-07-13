package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestIngestionCacheKey(t *testing.T) {
	cache, err := newIngestionCache(t.TempDir())
	require.NoError(t, err)
	cfg := testutil.TestConfig()
	cfg.Bot.Language = "en"
	eval := evalCase{QuestionID: "q1", Question: "ignored", Answer: "ignored"}
	sessions := []datedSession{{
		ID: "s1", Date: time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC),
		Messages: []evalMessage{{Role: "user", Content: "remember this"}},
	}}

	key, err := cache.key(eval, sessions, "full", cfg, options{})
	require.NoError(t, err)

	changedQuestion := eval
	changedQuestion.Question = "different question"
	sameKey, err := cache.key(changedQuestion, sessions, "full", cfg, options{judge: true, judgeModel: "different"})
	require.NoError(t, err)
	require.Equal(t, key, sameKey)

	changedSessions := append([]datedSession(nil), sessions...)
	changedSessions[0].Messages = []evalMessage{{Role: "user", Content: "changed memory"}}
	changedKey, err := cache.key(eval, changedSessions, "full", cfg, options{})
	require.NoError(t, err)
	require.NotEqual(t, key, changedKey)

	changedCfg := *cfg
	changedCfg.Embedding.Dimensions++
	changedConfigKey, err := cache.key(eval, sessions, "full", &changedCfg, options{})
	require.NoError(t, err)
	require.NotEqual(t, key, changedConfigKey)

	answerRouteKey, err := cache.key(eval, sessions, "full", cfg, options{roleRoutes: map[agent.AgentType]matrixAgentRoute{
		agent.TypeLaplace: {BaseURL: "http://answer", Model: "answer"},
	}})
	require.NoError(t, err)
	require.Equal(t, key, answerRouteKey)

	splitterRouteKey, err := cache.key(eval, sessions, "full", cfg, options{roleRoutes: map[agent.AgentType]matrixAgentRoute{
		agent.TypeSplitter: {BaseURL: "http://splitter", Model: "splitter"},
	}})
	require.NoError(t, err)
	require.NotEqual(t, key, splitterRouteKey)
}

func TestIngestionCachePublishAndMaterialize(t *testing.T) {
	cache, err := newIngestionCache(t.TempDir())
	require.NoError(t, err)
	source := filepath.Join(t.TempDir(), "source.db")
	require.NoError(t, os.WriteFile(source, []byte("snapshot"), 0o600))

	require.NoError(t, cache.publish(context.Background(), "key", source))
	destination := filepath.Join(t.TempDir(), "working.db")
	hit, err := cache.materialize("key", destination)
	require.NoError(t, err)
	require.True(t, hit)
	data, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, []byte("snapshot"), data)

	require.NoError(t, os.WriteFile(destination, []byte("mutated"), 0o600))
	second := filepath.Join(t.TempDir(), "second.db")
	hit, err = cache.materialize("key", second)
	require.NoError(t, err)
	require.True(t, hit)
	data, err = os.ReadFile(second)
	require.NoError(t, err)
	require.Equal(t, []byte("snapshot"), data)
}

func TestIngestionCacheMiss(t *testing.T) {
	cache, err := newIngestionCache(t.TempDir())
	require.NoError(t, err)
	hit, err := cache.materialize("missing", filepath.Join(t.TempDir(), "working.db"))
	require.NoError(t, err)
	require.False(t, hit)
}
