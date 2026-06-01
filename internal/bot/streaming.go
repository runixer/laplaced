package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// streamingMaxStatusArgChars caps the substituted argument in a status edit.
// 200 covers most search queries and image prompts in full; longer prompts
// get a trailing ellipsis. The cap is intentionally generous so the status
// log preserves enough context to be useful when several steps are stacked.
const streamingMaxStatusArgChars = 200

// toolArgField maps a tool's function name to the JSON field name in its
// arguments string that should be surfaced in the status line. Tools missing
// from this map get the bare (no-arg) status. The mapping mirrors the
// argument schemas declared in internal/agent/laplace/tools.go and
// configs/default.yaml.
var toolArgField = map[string]string{
	"internet_search": "query",
	"search_history":  "query",
	"search_people":   "query",
	"generate_image":  "prompt",
}

// streamSinkMode is a tag describing the current presentation state of the
// in-flight reply bubble. It governs how Delta/Status mutate the message and
// how Finalize wraps things up.
type streamSinkMode int

const (
	streamModeStatus   streamSinkMode = iota // showing "💭 Думаю..." or a tool status
	streamModeContent                        // streaming user-visible content as plain text
	streamModeOverflow                       // buffer crossed max_buffer_chars; no more in-bubble edits
)

// streamSink owns the lifecycle of the streaming reply bubble: a single
// Telegram message that begins as a placeholder, transitions through
// per-tool status edits, then progressively reveals the LLM's final
// answer via throttled editMessageText calls.
//
// The sink is created BEFORE laplace.Execute runs, so the placeholder is
// visible to the user during RAG enrichment and the first LLM turn (which
// can take several seconds before any deltas arrive).
//
// All public methods are safe for concurrent use by the agent's content
// callbacks and the bot's tool-status callbacks.
type streamSink struct {
	api          telegram.BotAPI
	translator   *i18n.Translator
	lang         string
	cfg          config.StreamingConfig
	chatID       int64
	threadIDPtr  *int
	replyToMsgID int
	logger       *slog.Logger
	now          func() time.Time

	mu          sync.Mutex
	msgID       int
	mode        streamSinkMode
	buf         strings.Builder
	lastEditAt  time.Time
	editCount   int
	hadFinalize bool

	// placeholderText is the initial "thinking" string sent before any
	// agent activity. Seeded once and reused as the first entry in
	// statusLog when the second status line arrives, so the visible
	// journey starts from "Thinking..." not from the second step.
	placeholderText string

	// statusLog accumulates per-step status lines (HTML-formatted) as
	// they're reached: RAG enrichment, each tool start. Rendered as a
	// Telegram <blockquote expandable> above the streamed content.
	// Empty for simple turns with no RAG result and no tool calls — in
	// that case we render plain content with no prefix.
	//
	// The log keeps growing even after content streaming begins:
	// some models emit a content preamble ("Let me look that up…")
	// before issuing tool_calls, which flips us into content mode early.
	// We still want the subsequent tool's status edit to land above the
	// already-streamed text, so Status/RAG remain active in content mode.
	statusLog []string
}

// newStreamSink sends the initial placeholder ("💭 Думаю...") and returns a
// configured sink ready to receive Status/Delta calls. If sending the
// placeholder fails, the sink is returned with msgID=0; subsequent Status
// and Delta calls become no-ops and Finalize falls through to the regular
// finalize+send pipeline.
func newStreamSink(
	ctx context.Context,
	api telegram.BotAPI,
	translator *i18n.Translator,
	lang string,
	cfg config.StreamingConfig,
	chatID int64,
	threadID int,
	replyToMsgID int,
	logger *slog.Logger,
) *streamSink {
	s := &streamSink{
		api:          api,
		translator:   translator,
		lang:         lang,
		cfg:          cfg,
		chatID:       chatID,
		replyToMsgID: replyToMsgID,
		logger:       logger,
		now:          time.Now,
		mode:         streamModeStatus,
	}
	if threadID != 0 {
		s.threadIDPtr = &threadID
	}

	placeholder := translator.Get(lang, "bot.streaming.thinking")
	if strings.TrimSpace(placeholder) == "" {
		placeholder = "..."
	}
	s.placeholderText = placeholder
	req := telegram.SendMessageRequest{
		ChatID:           chatID,
		MessageThreadID:  s.threadIDPtr,
		Text:             placeholder,
		ReplyToMessageID: replyToMsgID,
	}
	msg, err := api.SendMessage(ctx, req)
	if err != nil {
		logger.Warn("streamSink: failed to send placeholder; streaming disabled for this turn", "error", err)
		return s
	}
	s.msgID = msg.MessageID
	return s
}

// Status records a "running this tool" step and re-renders the status log
// in the in-flight message. When `arguments` is the tool's raw JSON and the
// tool has a known primary field (search query, image prompt, etc.) the
// localized message is formatted with the extracted value italicized inline,
// e.g. "🌐 Ищу в интернете: <i>как настроить vLLM</i>". Each step appends
// to the log; previous steps stay visible above the current one as a
// Telegram <blockquote>. Active in both status and content modes — a tool
// call that arrives after content has already started streaming still
// dorws its status line above the in-flight text.
func (s *streamSink) Status(toolName, arguments string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.msgID == 0 || s.mode == streamModeOverflow {
		return
	}
	s.appendStatusLocked(s.statusTextLocked(toolName, arguments))
}

// RAG records the enricher's rephrased query as a status step. Active in
// both status and content modes (see Status); quietly skipped if
// enrichment returned nothing useful or the bubble is in overflow.
func (s *streamSink) RAG(enrichedQuery string) {
	if enrichedQuery == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.msgID == 0 || s.mode == streamModeOverflow {
		return
	}
	line := fmt.Sprintf(
		s.translator.Get(s.lang, "bot.streaming.rag_searching"),
		htmlSafeArg(enrichedQuery),
	)
	s.appendStatusLocked(line)
}

// appendStatusLocked adds a new line to the status log and re-renders the
// bubble. Idempotent for consecutive duplicates. On the first non-trivial
// status, the initial "Thinking…" placeholder is seeded as the first log
// entry so the journey reads naturally. Caller must hold s.mu.
func (s *streamSink) appendStatusLocked(line string) {
	if line == "" {
		return
	}
	if len(s.statusLog) == 0 && s.placeholderText != "" {
		// Seed: preserve the original "💭 Думаю…" as step 1 of the journey.
		s.statusLog = append(s.statusLog, html.EscapeString(s.placeholderText))
	}
	if n := len(s.statusLog); n > 0 && s.statusLog[n-1] == line {
		return // consecutive duplicate (e.g. same tool fired twice)
	}
	s.statusLog = append(s.statusLog, line)

	switch s.mode {
	case streamModeContent:
		// Already streaming content. Re-render the bubble with the
		// updated status block ABOVE the in-flight content so the user
		// sees the new step pop in over the existing text.
		s.editContentLocked()
	default:
		// Still in the status phase — just refresh the blockquote.
		s.editLocked(renderStatusBlock(s.statusLog), "HTML")
	}
}

// renderStatusBlock formats the accumulated status log as a Telegram
// <blockquote expandable> — collapsible past ~4 lines, plain bar otherwise.
// Empty log returns "". Each line is assumed already HTML-formatted.
func renderStatusBlock(log []string) string {
	if len(log) == 0 {
		return ""
	}
	return "<blockquote expandable>" + strings.Join(log, "\n") + "</blockquote>"
}

// renderStatusPrefix returns the blockquote followed by two newlines so
// it visually separates from streamed content. "" when the log is empty.
func renderStatusPrefix(log []string) string {
	block := renderStatusBlock(log)
	if block == "" {
		return ""
	}
	return block + "\n\n"
}

// statusTextLocked resolves a tool name to a localized status string,
// preferring the "<name>_arg" key with the tool's primary argument
// substituted when a usable arg is available. Falls back to the bare key,
// then to "tool_generic", then to a literal ellipsis. Caller must hold s.mu.
func (s *streamSink) statusTextLocked(toolName, arguments string) string {
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
		bare = "…"
	}
	return bare
}

// extractToolArgText pulls the primary user-facing argument from a tool
// call's JSON arguments string. Returns "" when the tool isn't mapped,
// the JSON is malformed, or the field is missing/empty/non-string.
func extractToolArgText(toolName, arguments string) string {
	field, ok := toolArgField[toolName]
	if !ok || arguments == "" {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return ""
	}
	v, ok := parsed[field]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// htmlSafeArg prepares a tool argument for inline HTML rendering: trims
// whitespace, truncates to streamingMaxStatusArgChars (preserving rune
// boundaries) with an ellipsis, and HTML-escapes the result. Always safe
// to feed into a "<i>%s</i>" template.
func htmlSafeArg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse whitespace so multi-line prompts (image gen, long search
	// queries) render as a single tidy status line.
	s = strings.Join(strings.Fields(s), " ")

	runes := []rune(s)
	if len(runes) > streamingMaxStatusArgChars {
		s = string(runes[:streamingMaxStatusArgChars]) + "…"
	}
	return html.EscapeString(s)
}

// Delta receives a fragment of the LLM's user-visible content. The first
// non-empty Delta switches the bubble from "status" to "content" mode (and
// emits an immediate edit so the user sees the response start). Subsequent
// Deltas are coalesced via the configured throttle.
func (s *streamSink) Delta(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.msgID == 0 || s.mode == streamModeOverflow {
		// Always grow the buffer — Finalize uses it as the source of truth
		// regardless of whether we keep editing the bubble.
		s.buf.WriteString(text)
		return
	}
	prevLen := s.buf.Len()
	s.buf.WriteString(text)

	if s.buf.Len() > s.cfg.GetMaxBufferChars() {
		// Past the cap: stop editing this bubble. Finalize will switch to
		// the multi-message finalizeResponse path.
		s.mode = streamModeOverflow
		return
	}

	// Always edit on the very first content fragment so the bubble flips
	// out of the status placeholder immediately. After that, throttle.
	first := s.mode == streamModeStatus
	s.mode = streamModeContent
	if first {
		s.editContentLocked()
		return
	}

	// Throttle: edit only if either enough time has passed OR enough new
	// chars have accumulated since the last edit.
	throttle := s.cfg.GetEditThrottle()
	minChars := s.cfg.GetEditMinChars()
	now := s.now()
	if now.Sub(s.lastEditAt) >= throttle || (s.buf.Len()-prevLen) >= minChars {
		s.editContentLocked()
	}
}

// editContentLocked renders the current buffer as Telegram-flavored HTML
// (running it through BalanceOpenMarkers first so unclosed `**` / “ ` “ /
// triple-backtick fences don't confuse goldmark), prefixed with the frozen
// status journey, and edits the message. On any rendering error, falls back
// to escaped plaintext so the user still sees the in-flight content. Caller
// must hold s.mu.
func (s *streamSink) editContentLocked() {
	raw := s.buf.String()
	prefix := renderStatusPrefix(s.statusLog)
	balanced := markdown.BalanceOpenMarkers(raw)
	htmlText, err := markdown.ToHTML(balanced)
	if err != nil {
		s.logger.Debug("streamSink: ToHTML failed mid-stream, falling back to plain", "error", err)
		s.editLocked(prefix+htmlEscapeForFallback(raw), "HTML")
		return
	}
	s.editLocked(prefix+htmlText, "HTML")
}

// editLocked sends an editMessageText with the given body and parse_mode.
// Caller must hold s.mu. Errors are logged at debug/warn but never returned —
// streaming is best-effort UX, not a correctness primitive.
func (s *streamSink) editLocked(text, parseMode string) {
	if s.msgID == 0 || strings.TrimSpace(text) == "" {
		return
	}
	req := telegram.EditMessageTextRequest{
		ChatID:    s.chatID,
		MessageID: s.msgID,
		Text:      text,
		ParseMode: parseMode,
	}
	// Use a short-lived context so a stuck edit doesn't pin the goroutine
	// past the rest of the turn.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.api.EditMessageText(ctx, req)
	s.lastEditAt = s.now()
	s.editCount++
	if err != nil {
		if errors.Is(err, telegram.ErrMessageNotModified) {
			// Same content as before — Telegram considers this a no-op.
			return
		}
		// Don't bail: an edit can fail for transient reasons (rate limit,
		// network). The next throttle window will retry with the latest buf.
		s.logger.Warn("streamSink edit failed", "error", err, "chat_id", s.chatID, "message_id", s.msgID)
	}
}

// finalizeArgs carries the inputs to Finalize. Kept as a struct because the
// caller may need to pass additional context-dependent settings later
// (e.g. user_id for metrics).
type finalizeArgs struct {
	UserID    storage.ScopeID
	FullText  string
	HadError  bool
	ErrorText string // localized error message (used when HadError)
}

// Finalize is the single closing point for the sink. It chooses the right
// terminal state based on what the agent produced:
//
//   - HadError: edit the bubble with the localized api_error string.
//   - empty FullText: edit the bubble with the localized empty_response string.
//   - normal: edit the bubble with markdown.ToHTML(FullText) + parse_mode HTML.
//   - overflow: edit the bubble with the FIRST chunk of finalizeResponse
//     output (HTML), and let the caller send the remaining chunks via
//     sendResponses through the returned slice.
//
// Returns:
//   - additionalResponses: [] in non-overflow paths; [chunk2, chunk3, ...] in
//     the overflow path (caller sends these as new messages).
//   - editCount: total number of editMessageText calls issued (for metrics).
func (s *streamSink) Finalize(
	args finalizeArgs,
	finalizeResponse func(text string) ([]telegram.SendMessageRequest, error),
) ([]telegram.SendMessageRequest, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hadFinalize = true

	// No placeholder — we never had a bubble to edit. Caller falls back to
	// the existing send path entirely.
	if s.msgID == 0 {
		return nil, s.editCount
	}

	// Compute prefix from the current statusLog. This picks up any
	// status entries appended mid-stream (tool calls that arrived after
	// a content preamble) and includes them in the final render.
	prefix := renderStatusPrefix(s.statusLog)

	if args.HadError {
		text := args.ErrorText
		if text == "" {
			text = s.translator.Get(s.lang, "bot.api_error")
		}
		s.editLocked(prefix+htmlEscapeForFallback(text), "HTML")
		return nil, s.editCount
	}

	if strings.TrimSpace(args.FullText) == "" {
		text := s.translator.Get(s.lang, "bot.empty_response")
		s.editLocked(prefix+htmlEscapeForFallback(text), "HTML")
		return nil, s.editCount
	}

	// Build full HTML render via the existing pipeline, even if we didn't
	// overflow — this mirrors finalizeResponse's chunking, list-numbering
	// fixes, and ToHTML output.
	chunks, err := finalizeResponse(args.FullText)
	if err != nil || len(chunks) == 0 {
		// Fallback: HTML-escape the raw text and ship as plain.
		s.logger.Warn("streamSink: finalizeResponse failed; falling back to escaped plain", "error", err)
		s.editLocked(prefix+htmlEscapeForFallback(args.FullText), "HTML")
		return nil, s.editCount
	}

	// Edit the placeholder with the first chunk's HTML (prefixed by the
	// status journey). Remaining chunks are returned for the caller to
	// send as new messages — those don't carry the prefix since the
	// journey only needs to appear once at the top of the conversation.
	s.editLocked(prefix+chunks[0].Text, chunks[0].ParseMode)
	return chunks[1:], s.editCount
}

// htmlEscapeForFallback escapes raw text so it can safely be sent with
// parse_mode=HTML when we don't have a Markdown render handy.
func htmlEscapeForFallback(s string) string {
	return html.EscapeString(s)
}

// streamingToolStatusKey is exported for tests to assert the mapping
// between tool name and i18n key.
func streamingToolStatusKey(toolName string) string {
	return fmt.Sprintf("bot.streaming.tool_%s", toolName)
}
