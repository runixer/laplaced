package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/telegram"
)

const (
	// Telegram removes a live draft 30 seconds after its last accepted update.
	// Refreshing at 20 seconds leaves room for a transient request failure while
	// staying far below the documented per-peer draft/action flood limits.
	richDraftHeartbeatInterval = 20 * time.Second
	// All draft/status/RAG snapshots share Telegram's 20/5s and 40/30s peer
	// budget. One call per 1.2 seconds leaves headroom for typing actions emitted
	// before the draft exists and idempotent retries with the same draft id.
	richDraftMinUpdateInterval = 1200 * time.Millisecond
	// Preview delivery is best-effort and must not hold the synchronous SSE
	// callback for the persistent final's full request timeout.
	richDraftRequestTimeout   = 5 * time.Second
	richDraftTransientBackoff = 5 * time.Second
	// A terminal catch-up is preview-only and must never appreciably delay the
	// separately confirmed persistent final. It never waits out a server
	// cooldown; this is the total budget for its one best-effort API attempt.
	richDraftTerminalCatchupBudget = 2 * time.Second
	richDraftMaxStatusLines        = 24
	// Rich drafts have a 32,768 semantic-character limit rather than the
	// legacy editMessageText 4096 UTF-16 limit. Keeping the Markdown source at
	// 24 KiB leaves room for the bounded <tg-thinking> journey and HTML
	// expansion while the renderer's structural checks remain authoritative.
	richDraftMaxSourceBytes = 24 << 10
)

type richDraftStats struct {
	updates          int
	contentSnapshots int
	overflow         bool
	duration         time.Duration
	terminalCatchup  richDraftTerminalCatchupOutcome
}

type richDraftTerminalCatchupOutcome string

const (
	richDraftCatchupSent            richDraftTerminalCatchupOutcome = "sent"
	richDraftCatchupSkippedNoTail   richDraftTerminalCatchupOutcome = "skipped_no_tail"
	richDraftCatchupSkippedCooldown richDraftTerminalCatchupOutcome = "skipped_cooldown"
	richDraftCatchupSkippedRender   richDraftTerminalCatchupOutcome = "skipped_render"
	richDraftCatchupFailed          richDraftTerminalCatchupOutcome = "failed"
)

// richDraftSink owns an ephemeral Telegram Rich Message preview. Unlike
// streamSink it never owns persistent delivery: Close only stops callbacks and
// the heartbeat. responsePath must still perform one ordinary confirmed final
// send before history may be persisted.
type richDraftSink struct {
	api        telegram.BotAPI
	translator *i18n.Translator
	lang       string
	cfg        config.StreamingConfig
	chatID     int64
	threadID   *int
	draftID    int
	logger     *slog.Logger
	now        func() time.Time
	baseCtx    context.Context

	mu sync.Mutex

	active         bool
	finalized      bool
	overflow       bool
	maxSourceBytes int
	buf            strings.Builder
	statusLog      []string
	placeholder    string
	lastPayload    string
	frozenPayload  string
	lastDraftAt    time.Time
	lastAttemptAt  time.Time
	lastDraftLen   int
	cooldownTill   time.Time
	stats          richDraftStats

	cancelHeartbeat context.CancelFunc
	refreshTimer    *time.Timer
	refreshForce    bool
}

func newRichDraftSink(
	ctx context.Context,
	api telegram.BotAPI,
	translator *i18n.Translator,
	lang string,
	cfg config.StreamingConfig,
	chatID int64,
	threadID int,
	draftID int,
	logger *slog.Logger,
) *richDraftSink {
	s := &richDraftSink{
		api:            api,
		translator:     translator,
		lang:           lang,
		cfg:            cfg,
		chatID:         chatID,
		draftID:        draftID,
		logger:         logger,
		now:            time.Now,
		baseCtx:        context.WithoutCancel(ctx),
		maxSourceBytes: richDraftMaxSourceBytes,
	}
	if threadID != 0 {
		s.threadID = &threadID
	}

	// Incoming Telegram message ids are positive signed 32-bit integers and
	// are unique inside the chat, making the triggering id a stable draft key.
	// Fail closed if a malformed envelope cannot provide such an id.
	if api == nil {
		logger.Warn("rich draft disabled: Telegram API is unavailable")
		return s
	}
	if chatID == 0 || draftID <= 0 || int64(draftID) > math.MaxInt32 {
		logger.Warn("rich draft disabled: invalid routing identifiers")
		return s
	}

	s.placeholder = translator.Get(lang, "bot.streaming.thinking")
	if strings.TrimSpace(s.placeholder) == "" {
		s.placeholder = "..."
	}

	payload, err := s.renderLocked()
	if err != nil {
		logger.Warn("rich draft disabled: initial render failed", "error", err)
		return s
	}
	if err := s.sendLocked(payload); err != nil {
		// Drafts are ephemeral and have a stable id, so an unknown result is not
		// persistent-delivery evidence. This turn simply remains buffered.
		logger.Warn("rich draft initial preview failed; continuing buffered", "error", err)
		return s
	}

	s.active = true
	heartbeatCtx, cancel := context.WithCancel(context.Background())
	s.cancelHeartbeat = cancel
	go s.heartbeat(heartbeatCtx)
	return s
}

func (s *richDraftSink) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && !s.finalized
}

func (s *richDraftSink) Status(toolName, arguments string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.callbackAllowedLocked() || s.overflow {
		return
	}
	if s.appendStatusLocked(s.statusTextLocked(toolName, arguments)) {
		s.refreshLocked(false)
	}
}

func (s *richDraftSink) RAG(enrichedQuery string) {
	if strings.TrimSpace(enrichedQuery) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.callbackAllowedLocked() || s.overflow {
		return
	}
	line := fmt.Sprintf(
		s.translator.Get(s.lang, "bot.streaming.rag_searching"),
		htmlSafeArg(enrichedQuery),
	)
	if s.appendStatusLocked(line) {
		s.refreshLocked(false)
	}
}

func (s *richDraftSink) Delta(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.callbackAllowedLocked() || s.overflow {
		return
	}

	// FullText from the completed agent response remains the final source of
	// truth. Rich drafts therefore use their own larger source-byte budget;
	// max_buffer_chars remains the legacy editMessageText safety margin.
	text = strings.ToValidUTF8(text, "\uFFFD")
	firstContent := s.buf.Len() == 0
	remaining := s.maxSourceBytes - s.buf.Len()
	prefix, truncated := richDraftSourcePrefix(text, remaining)
	if prefix != "" {
		s.buf.WriteString(prefix)
	}
	if truncated {
		s.freezeOverflowLocked()
		return
	}

	now := s.now()
	if firstContent || now.Sub(s.lastDraftAt) >= s.cfg.GetEditThrottle() ||
		s.buf.Len()-s.lastDraftLen >= s.cfg.GetEditMinChars() {
		s.refreshLocked(false)
	}
}

// richDraftSourcePrefix returns the longest continuous UTF-8 prefix that fits
// maxBytes. It never skips an oversized rune to admit a later smaller one.
func richDraftSourcePrefix(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	if maxBytes <= 0 {
		return "", true
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

// freezeOverflowLocked captures the one capped payload that may still be sent
// or retried. Later deltas/statuses cannot mutate it, while an already queued
// coalescing timer and heartbeat remain able to deliver the exact snapshot.
func (s *richDraftSink) freezeOverflowLocked() {
	s.overflow = true
	s.stats.overflow = true

	payload, err := s.renderLocked()
	if err != nil {
		// Structural overflow is local and preview-only. Keep replaying the last
		// Telegram-confirmed payload; persistent final delivery remains separate.
		s.logger.Debug("rich draft capped snapshot render skipped", "error", err)
		payload = s.lastPayload
	}
	s.frozenPayload = payload
	s.refreshLocked(false)
}

func (s *richDraftSink) callbackAllowedLocked() bool {
	return s.active && !s.finalized
}

func (s *richDraftSink) appendStatusLocked(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	if len(s.statusLog) == 0 {
		s.statusLog = append(s.statusLog, html.EscapeString(s.placeholder))
	}
	if s.statusLog[len(s.statusLog)-1] == line {
		return false
	}
	if len(s.statusLog) >= richDraftMaxStatusLines {
		// Preserve the initial placeholder and the newest bounded journey.
		copy(s.statusLog[1:], s.statusLog[2:])
		s.statusLog = s.statusLog[:len(s.statusLog)-1]
	}
	s.statusLog = append(s.statusLog, line)
	return true
}

func (s *richDraftSink) statusTextLocked(toolName, arguments string) string {
	argText := extractToolArgText(toolName, arguments)
	if argText != "" {
		argKey := "bot.streaming.tool_" + toolName + "_arg"
		argTmpl := s.translator.Get(s.lang, argKey)
		if argTmpl != "" && argTmpl != argKey && strings.Contains(argTmpl, "%s") {
			return fmt.Sprintf(argTmpl, htmlSafeArg(argText))
		}
	}

	bareKey := "bot.streaming.tool_" + toolName
	bare := s.translator.Get(s.lang, bareKey)
	if bare == "" || bare == bareKey {
		bare = s.translator.Get(s.lang, "bot.streaming.tool_generic")
	}
	if bare == "" {
		return "…"
	}
	return bare
}

func (s *richDraftSink) refreshLocked(force bool) {
	if !s.callbackAllowedLocked() {
		return
	}
	if delay := s.nextSendDelayLocked(); delay > 0 {
		s.scheduleRefreshLocked(delay, force)
		return
	}
	payload := s.frozenPayload
	if !s.overflow {
		var err error
		payload, err = s.renderLocked()
		if err != nil {
			// An incomplete Markdown prefix can become valid on a later delta. Skip
			// only this ephemeral snapshot; never downgrade the persistent final.
			s.logger.Debug("rich draft snapshot render skipped", "error", err)
			return
		}
	}
	if payload == "" {
		return
	}
	if !force && payload == s.lastPayload {
		return
	}
	s.cancelRefreshLocked()
	if err := s.sendLocked(payload); err != nil {
		s.handleSendErrorLocked(err, force, "snapshot")
	}
}

func (s *richDraftSink) nextSendDelayLocked() time.Duration {
	now := s.now()
	next := s.lastAttemptAt.Add(richDraftMinUpdateInterval)
	if s.cooldownTill.After(next) {
		next = s.cooldownTill
	}
	if next.After(now) {
		return next.Sub(now)
	}
	return 0
}

// scheduleRefreshLocked coalesces arbitrarily many callbacks into one latest
// snapshot. It also gives a tool status that arrived just after another update
// a bounded delayed send instead of losing it until the 20s heartbeat.
func (s *richDraftSink) scheduleRefreshLocked(delay time.Duration, force bool) {
	if !s.callbackAllowedLocked() {
		return
	}
	s.refreshForce = s.refreshForce || force
	if s.refreshTimer != nil {
		return
	}
	if delay < 0 {
		delay = 0
	}
	s.refreshTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		force := s.refreshForce
		s.refreshForce = false
		s.refreshTimer = nil
		s.refreshLocked(force)
	})
}

func (s *richDraftSink) cancelRefreshLocked() {
	if s.refreshTimer != nil {
		s.refreshTimer.Stop()
		s.refreshTimer = nil
	}
	s.refreshForce = false
}

func (s *richDraftSink) disableLocked() {
	s.active = false
	s.cancelRefreshLocked()
	if s.cancelHeartbeat != nil {
		s.cancelHeartbeat()
	}
}

func (s *richDraftSink) handleSendErrorLocked(err error, retryUnchanged bool, operation string) {
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != 429 {
		// A confirmed capability/payload rejection disables only the preview.
		// The completed Rich Message still uses the separately confirmed final.
		s.disableLocked()
		s.logger.Warn("rich draft disabled after confirmed rejection", "operation", operation, "error", err)
		return
	}

	backoff := richDraftTransientBackoff
	if errors.As(err, &apiErr) && apiErr.Code == 429 && apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
		backoff = time.Duration(apiErr.Parameters.RetryAfter) * time.Second
	}
	if backoff < richDraftMinUpdateInterval {
		backoff = richDraftMinUpdateInterval
	}
	s.cooldownTill = s.now().Add(backoff)
	s.scheduleRefreshLocked(backoff, retryUnchanged)
	s.logger.Warn("rich draft update failed; backing off", "operation", operation, "backoff", backoff, "error", err)
}

func (s *richDraftSink) renderLocked() (string, error) {
	visibleSource := ""
	if s.buf.Len() > 0 {
		var err error
		visibleSource, err = suppressGeneratedMediaDraftSource(sanitizeModelPresentation(s.buf.String()))
		if err != nil {
			return "", fmt.Errorf("suppress generated-media draft directives: %w", err)
		}
	}
	hasVisibleContent := strings.TrimSpace(visibleSource) != ""

	var out strings.Builder
	characters, blocks, maxDepth, maxTableColumns := 0, 0, 0, 0
	if len(s.statusLog) > 0 || !hasVisibleContent {
		lines := s.statusLog
		if len(lines) == 0 {
			lines = []string{html.EscapeString(s.placeholder)}
		}
		// Rich HTML collapses source whitespace inside <tg-thinking>. Use the
		// explicit rich line-break element for the wire payload while retaining
		// newlines for conservative semantic-character accounting.
		statusText := strings.Join(lines, "\n")
		statusHTML := strings.Join(lines, "<br>")
		out.WriteString("<tg-thinking>")
		out.WriteString(statusHTML)
		out.WriteString("</tg-thinking>")
		// Status translations may contain trusted inline tags. Counting their
		// source text is conservative and keeps the combined content + thinking
		// payload below Telegram's semantic limit.
		characters += utf8.RuneCountInString(html.UnescapeString(statusText))
		blocks++
		maxDepth = 2 // <tg-thinking> plus an optional trusted inline style.
	}

	if hasVisibleContent {
		balanced := markdown.BalanceOpenMarkers(visibleSource)
		rendered, stats, err := markdown.ToRichHTMLPreview(balanced)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(rendered) == "" {
			return "", errors.New("rich draft rendered empty content")
		}
		characters += stats.Characters
		blocks += stats.Blocks
		if stats.MaxDepth > maxDepth {
			maxDepth = stats.MaxDepth
		}
		maxTableColumns = stats.MaxTableColumns
		out.WriteString(rendered)
	}
	if characters > richMessageSafeCharacterLimit ||
		blocks > richMessageSafeBlockLimit ||
		maxDepth > richMessageMaxDepth ||
		maxTableColumns > richMessageMaxTableColumns {
		return "", fmt.Errorf("rich draft snapshot exceeds structural limits")
	}

	payload := out.String()
	if strings.TrimSpace(payload) == "" {
		return "", errors.New("rich draft payload is empty")
	}
	if len(payload) > richMessageMaxRenderedBytes {
		return "", fmt.Errorf("rich draft payload has %d bytes, limit is %d", len(payload), richMessageMaxRenderedBytes)
	}
	return payload, nil
}

func (s *richDraftSink) sendLocked(payload string) error {
	ctx, cancel := context.WithTimeout(s.baseCtx, richDraftRequestTimeout)
	defer cancel()
	return s.sendWithContextLocked(ctx, payload)
}

func (s *richDraftSink) sendWithContextLocked(ctx context.Context, payload string) error {
	start := time.Now()
	err := s.api.SendRichMessageDraft(ctx, telegram.SendRichMessageDraftRequest{
		ChatID:          s.chatID,
		MessageThreadID: s.threadID,
		DraftID:         int64(s.draftID),
		RichMessage: telegram.InputRichMessage{
			HTML:                payload,
			SkipEntityDetection: true,
		},
	})
	s.stats.duration += time.Since(start)
	s.stats.updates++
	s.lastAttemptAt = s.now()
	if err != nil {
		return err
	}
	s.lastDraftAt = s.lastAttemptAt
	if payload != s.lastPayload {
		if s.buf.Len() > s.lastDraftLen {
			s.stats.contentSnapshots++
		}
		s.lastDraftLen = s.buf.Len()
	}
	s.lastPayload = payload
	s.cooldownTill = time.Time{}
	return nil
}

func (s *richDraftSink) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(richDraftHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			s.heartbeatLocked()
			active := s.callbackAllowedLocked()
			s.mu.Unlock()
			if !active {
				return
			}
		}
	}
}

func (s *richDraftSink) heartbeatLocked() {
	if !s.callbackAllowedLocked() || s.now().Sub(s.lastDraftAt) < richDraftHeartbeatInterval {
		return
	}
	if delay := s.nextSendDelayLocked(); delay > 0 {
		s.scheduleRefreshLocked(delay, true)
		return
	}
	payload := s.lastPayload
	if payload == "" {
		return
	}
	s.cancelRefreshLocked()
	if err := s.sendLocked(payload); err != nil {
		s.handleSendErrorLocked(err, true, "heartbeat")
	}
}

// FinalizePreview makes callbacks terminal, then gives a buffered content tail
// one bounded best-effort chance to become visible before the persistent final
// replaces this ephemeral draft. Failure is deliberately preview-only: there
// is no retry, fallback, or coupling to the persistent delivery outcome. An
// active server cooldown suppresses the attempt; the single terminal burst may
// bypass the local sustained-rate floor because the regular cadence leaves
// bounded headroom below Telegram's documented peer limits.
func (s *richDraftSink) FinalizePreview() richDraftStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized {
		return s.stats
	}
	wasActive := s.active
	s.finalized = true
	s.disableLocked()
	if !wasActive || s.buf.Len() <= s.lastDraftLen {
		s.stats.terminalCatchup = richDraftCatchupSkippedNoTail
		return s.stats
	}
	// A server-provided cooldown is authoritative: do not wait it out on the
	// terminal path. The local 1.2s sustained-rate floor is intentionally not
	// applied to this single terminal burst. Regular snapshots can produce at
	// most ~25 calls per 30 seconds, leaving ample room below Telegram's 40/30s
	// and 20/5s peer budgets while preserving the full bounded request timeout.
	if s.cooldownTill.After(s.now()) {
		s.stats.terminalCatchup = richDraftCatchupSkippedCooldown
		s.logger.Debug("rich draft terminal catch-up skipped: active cooldown", "until", s.cooldownTill)
		return s.stats
	}

	payload := s.frozenPayload
	if !s.overflow {
		var err error
		payload, err = s.renderLocked()
		if err != nil {
			s.stats.terminalCatchup = richDraftCatchupSkippedRender
			s.logger.Debug("rich draft terminal catch-up render skipped", "error", err)
			return s.stats
		}
	}
	if payload == "" || payload == s.lastPayload {
		s.stats.terminalCatchup = richDraftCatchupSkippedNoTail
		return s.stats
	}
	ctx, cancel := context.WithTimeout(s.baseCtx, richDraftTerminalCatchupBudget)
	defer cancel()
	if err := s.sendWithContextLocked(ctx, payload); err != nil {
		s.stats.terminalCatchup = richDraftCatchupFailed
		s.logger.Warn("rich draft terminal catch-up failed; continuing persistent final", "error", err)
		return s.stats
	}
	s.stats.terminalCatchup = richDraftCatchupSent
	return s.stats
}

// Close makes draft callbacks terminal and waits for any in-flight heartbeat
// call by taking the same mutex. It deliberately performs no Telegram method:
// the subsequent persistent send replaces the preview, otherwise Telegram
// expires it after 30 seconds.
func (s *richDraftSink) Close() richDraftStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finalized {
		s.finalized = true
		s.disableLocked()
	}
	return s.stats
}
