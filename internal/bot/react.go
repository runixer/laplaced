package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reactor"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
)

// reactTimeout bounds the background reaction flow (LLM decision + transport
// call) so a stuck call can't hold the shutdown WaitGroup forever.
const reactTimeout = 30 * time.Second

// maybeReact spawns the fire-and-forget reaction flow for the just-received
// message group, in parallel with the main RAG+LLM response. No-op when the
// reactor agent isn't wired, the feature is disabled, or the transport can't
// react. ctx must be the shutdown-safe context carrying agent.SharedContext.
func (b *Bot) maybeReact(ctx context.Context, userID storage.ScopeID, convID, messageID, query string, parts []interface{}, logger *slog.Logger) {
	if b.reactorAgent == nil || !b.cfg.Agents.Reactor.Enabled {
		return
	}
	caps := b.transport.Capabilities()
	if !caps.SupportsReactions || len(caps.AvailableReactions) == 0 {
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.reactToMessage(ctx, userID, convID, messageID, query, parts, caps.AvailableReactions, logger)
	}()
}

// reactToMessage asks the reactor agent whether (and how) to react to the
// message, then sets the reaction. Failures are logged and swallowed — a
// missed reaction never affects the turn.
func (b *Bot) reactToMessage(ctx context.Context, userID storage.ScopeID, convID, messageID, query string, parts []interface{}, allowed []string, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, reactTimeout)
	defer cancel()

	// Media (voice/images) from the current group so the decision isn't blind.
	var media []interface{}
	for _, p := range parts {
		if fp, ok := p.(llm.FilePart); ok {
			media = append(media, fp)
		}
	}

	// Best-effort recent history for context; may race with the current
	// message being written to history — harmless either way.
	recent, err := b.msgRepo.GetRecentHistory(userID, 6)
	if err != nil {
		recent = nil
	}

	req := &agent.Request{
		Query:  query,
		Shared: agent.FromContext(ctx),
		Params: map[string]any{
			reactor.ParamAllowedReactions: allowed,
			reactor.ParamHistory:          recent,
			reactor.ParamMediaParts:       media,
		},
	}

	resp, err := b.reactorAgent.Execute(ctx, req)
	if err != nil {
		logger.Debug("reactor failed", "error", err)
		return
	}
	res, ok := resp.Structured.(*reactor.Result)
	if !ok || res.Emoji == "" {
		logger.Debug("reactor declined to react")
		return
	}

	start := time.Now()
	if err := b.transport.SetReaction(ctx, convID, messageID, res.Emoji); err != nil {
		logger.Warn("failed to set message reaction", "emoji", res.Emoji, "error", err)
		return
	}
	logger.Info("message reaction set", "emoji", res.Emoji)
	RecordMessageReaction(userID, time.Since(start).Seconds())
}
