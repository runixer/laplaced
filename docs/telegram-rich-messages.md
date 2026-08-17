# Telegram Rich Messages

Telegram has two independent representations for formatted incoming messages:

- `Message.rich_message` carries a canonical block AST;
- ordinary `Message.text`/`Message.caption` carry visible text plus formatting
  metadata in `entities`/`caption_entities`.

Laplaced always decodes both representations. This is full incoming message
support rather than a model-output subset or an egress rollout option.

Every Bot API 10.2 final `RichText` and `RichBlock` variant is decoded
tolerantly, including nested details, lists, tables, quotes, collages and
slideshows. The canonical AST is projected to bounded Markdown plus ordered
media descriptors before it enters the ordinary grouping, history, RAG and LLM
file pipeline. Unsupported, unavailable and over-limit media remain visible as
bounded per-occurrence markers instead of dropping the whole turn. Draft-only
`thinking` is not valid in a received persistent message.

Classic entities need a separate projection even when rich ingress is enabled.
Telegram clients remove presentation delimiters from the visible string: for
example, a user-authored inline-code `` `$x_i$` `` arrives as text `$x_i$` with
a `code` entity. The entity projector overlays typed formatting on the original
text before grouping, history, RAG and the LLM. Unformatted source fragments,
including Markdown links and LaTeX delimiters, remain byte-identical. It maps
Telegram offsets and lengths from UTF-16 code units to UTF-8 byte boundaries,
validates bounds, overlap and
the documented nesting rules, and applies fixed entity/output budgets. Known
entity types have deterministic projections; unsupported future types keep
their visible text and mark the projection partial.

Projection failure is atomic. A malformed, crossing or over-limit entity array
never yields a partially formatted prefix: the complete source string is
passed on unchanged through the established text/caption path. Messages without entities
keep the established byte-identical text/caption path. Captions retain their
existing media association, and ingress diagnostics do not log message content,
URLs or user identifiers.

Outgoing Rich Messages are a separate, opt-in concern. Model Markdown is parsed
with Goldmark and serialized as allowlisted Rich HTML. Raw HTML, active Markdown
images/media, direct `tg://user` mentions and unsafe URL schemes cannot become
Telegram entities. A policy-equivalent legacy HTML representation is prepared
before the first persistent send. Only a local render rejection or a named
rich-format Bot API rejection may select it; an ambiguous network/decode result
is never resent in another format.

## v2 release contract

v2 uses one preflighted delivery plan for every persistent reply in the
eligible private-chat rich path. The plan is immutable before its first network
call and consists of operations where one operation is exactly one Bot API
request. Within that path, artifact delivery V1 is deliberately private-only:
the model gets `send_artifacts` and explicit image presentation modes only for
an eligible private rich-send turn; runtime authorization independently fails
closed everywhere else.

The release contract is:

- incoming `rich_message`, classic text entities and caption entities are
  always decoded through the bounded projection described above;
- an eligible private-chat canary receives complete text replies as persistent
  Rich Messages, including safe links, headings, lists, tables, quotes, code,
  spoilers and LaTeX;
- rich text is packed only at top-level block boundaries. A physical line whose
  complete trimmed value is `###SPLIT###` creates an explicit part boundary;
  the same token inside prose, inline/fenced code, tables, lists, quotes or
  other protected structures remains content;
- optional rich draft streaming shows ephemeral tool/RAG/content progress and
  is stopped before the persistent final owns delivery;
- one to ten trusted generated Photo previews can be uploaded in one multipart
  final. After successful image tools, the model receives turn-local `MEDIA:1`,
  `MEDIA:2`, ... placement references only for generated delivery modes that
  contain a preview. `original` consumes no MEDIA ordinal. A standalone
  top-level physical line such as `###MEDIA:1###` or `###MEDIA:2,3###` chooses
  the position, order and grouping: one Photo is a bare image block, two to four
  use a collage, and five to ten use a slideshow. Several groups may be
  interleaved with Rich HTML and may cross explicit `###SPLIT###` boundaries;
- the placement protocol is all-or-nothing. If no directive is present, all
  Photos use the established automatic top gallery. A malformed, duplicate,
  missing, unavailable or invented reference invalidates the complete authored
  layout; reserved directive lines are removed and every loadable Photo is
  delivered automatically in generation order. More than ten generated items
  likewise disables authored placement and uses bounded legacy batches;
- `MEDIA` references never authorize a fetch or upload. Artifact lookup,
  bytes, media/attachment IDs, `tg://` references and Rich HTML are built only
  by the application from user-isolated generated artifacts. Model-authored
  image URLs and media HTML remain inert, and internal artifact references are
  scrubbed from model-authored user-visible output;
- generated images use an explicit delivery policy: `preview` is the default;
  `original` replaces the Photo with a byte-exact Document; and
  `preview_and_original` is used only when the user explicitly asks for both.
  A requested 2K/4K resolution never implies `original`, and file size never
  creates an automatic Document sidecar. A rich/beautiful post alone remains
  `preview`; rich presentation plus explicitly requested originals means
  `preview_and_original`;
- the private-only `send_artifacts` tool stages existing user-owned artifacts
  from the app-authored trusted inventory without performing a network call.
  Stored images support `auto`, `preview`, `original` and
  `preview_and_original`; non-images support only `auto`/`original` and are
  Documents. Current-message images remain automatic `generate_image` inputs,
  while their inventory IDs may be used for `send_artifacts`;
- Telegram Rich Messages have no Document block. Every explicitly requested
  original is therefore a separate Document operation after the complete rich
  post, in stable request/generation order. A preview that cannot satisfy the
  Telegram Photo envelope deterministically degrades to one Document during
  preflight, before the ledger or any network call;
- more than ten generated items, non-image/corrupt media, an invalid Photo
  envelope, or a layout that does not fit the rich limits uses a preplanned
  legacy sequence of homogeneous batches of at most ten items plus bounded
  text parts;
- every logical delivery has one content-free durable ledger. All confirmed
  transport message IDs are atomically associated with one assistant history
  row and ordered M:N `history_artifact_refs`, so a reaction to any rich part,
  album or Document resolves the same reply. Re-sending a stored artifact adds
  a reference and never moves its creator provenance/`artifacts.message_id`;
- if a later operation is rejected/unknown, or the process stops after a
  confirmation but before history persistence, reactions to the confirmed
  prefix still resolve through the content-free ledger. Such a flag keeps the
  delivery trace but intentionally has no invented reply preview/history row;
- a restart seals any interrupted operation as `unknown` (or
  `partial_unknown`) and skips operations that had not started. It never
  guesses whether Telegram persisted an in-flight request and never replays it.

Groups, business messages and direct-message topics deliberately retain the
established legacy path and are not offered `send_artifacts`. Video/audio/voice
producers, Documents embedded *inside* a Rich Message, more than ten
model-directed Photos and persistent rich edits are outside the v2 contract;
explicit artifact originals remain supported as separate private-chat Document
operations.

## Rollout runbook

```yaml
telegram:
  rich_messages:
    mode: "off"            # off | shadow | send
    allowed_user_ids: []   # native Telegram ids; empty means nobody
    draft_streaming_enabled: false
```

Environment equivalents:

- `LAPLACED_TELEGRAM_RICH_MESSAGES_MODE`
- `LAPLACED_TELEGRAM_RICH_MESSAGES_ALLOWED_USER_IDS` (comma-separated)
- `LAPLACED_TELEGRAM_RICH_MESSAGES_DRAFT_STREAMING_ENABLED`

`shadow` performs the same bounded rich preflight and records its decision, but
still sends the legacy representation. `send` uses `sendRichMessage` only for
listed users in eligible private chats. Rich draft streaming has its own
default-off switch and does not inherit `bot.streaming.enabled`, which remains
the legacy edit-streaming switch. The persistent final is always a separate
confirmed send. Neither `rich_message` decoding nor classic entity projection
is gated by any rollout setting.

Advance one stage at a time, keeping the same candidate image/config except for
the named feature switches:

1. **Off:** deploy with `mode=off`, an empty allowlist and rich draft streaming
   disabled. Verify health, restart count, polling ownership and ingress
   counters. This is also the egress kill switch. Keep one active bot process
   per database: startup recovery deliberately treats any pre-existing
   `sending` operation as interrupted.
2. **Shadow:** add only the operator IDs and select `mode=shadow`. Leave drafts
   disabled. Exercise the representative corpus and compare shadow decisions
   with the delivered legacy messages. Ordinary output is unchanged; the one
   deliberate compatibility fix is that a previously invalid homogeneous
   album above ten items is now delivered as bounded `10 + remainder` batches.
3. **Operator send:** select `mode=send` for those same IDs, still with drafts
   disabled. Check text, 1/2/4/5/10 generated Photos, an 11-photo legacy
   fallback, 4K staying preview-only, explicit `original` replacing preview,
   explicit `preview_and_original`, stored image preview/original/both, stored
   PDF original, standalone and inline split markers, copy/forward, reply and
   reactions on the first and a later persistent part on Desktop and Android.
4. **Operator draft:** enable `draft_streaming_enabled` only after persistent
   sends are clean. Exercise a short answer, a long answer, a slow tool and a
   slow image generation; each turn must end with exactly one durable logical
   reply and no post-final draft revival.
5. **Expand:** add users in small allowlist batches. Soak each batch for at
   least 20–50 varied turns (and one normal client session) before the next.
   `allowed_user_ids: []` never means global enablement.

At every stage inspect these low-cardinality series and the corresponding
error logs/traces:

- `laplaced_bot_rich_message_shadow_evaluations_total{outcome,fallback_reason}`;
- `laplaced_bot_rich_message_final_deliveries_total{content_kind,path,outcome,fallback_reason}`;
- `laplaced_bot_rich_message_rate_limited_total{content_kind,path}`;
- `laplaced_bot_rich_message_native_attachments` and
  `laplaced_bot_rich_message_native_attachment_bytes`;
- `laplaced_bot_message_telegram_rich_draft_count`,
  `laplaced_bot_message_telegram_rich_draft_content_snapshot_count` and
  `laplaced_bot_message_telegram_rich_draft_overflow_total`;
- `laplaced_bot_message_telegram_rich_draft_terminal_catchup_total{outcome}`.

Stop expansion immediately on a duplicate durable final, a history/reaction
link without confirmed delivery, any model-authored media fetch, an increasing
`outcome="unknown"`, a new sustained rich rejection/API-fallback class, or any
increase in the rate-limit counter. Draft-specific 429s or unexpectedly high
draft-call counts first require disabling `draft_streaming_enabled`; likewise,
disable it if terminal catch-up `outcome="failed"` is sustained or a client ever
shows a draft revival after the persistent final. Persistent delivery can
remain in operator `send` while it is rechecked. For persistent
send regressions return the affected allowlist to `shadow`; use `mode=off` for
an immediate global rich-egress stop. Do not retry an `unknown` turn manually
until Telegram/client state has been checked, because the request may already
have persisted.

A rollout stage is accepted only when the container and application health are
green, restart count is unchanged, no second poller/409 exists, expected
fallbacks are classified, and the client-visible no-duplicate checklist passes.
Changing rollout mode does not disable or roll back rich ingress.

Groups, business messages and direct-message topics fail closed to legacy.
Errors and ordinary media captions retain their established persistent
delivery paths. For eligible generated previews the planner either follows a
fully validated turn-local MEDIA layout or inserts one trusted gallery at the
top automatically, then completes the rich post before sending explicitly
requested originals/stored artifacts as separate Photo or Document operations.
No byte threshold adds a sidecar. More than ten model-directed previews and
local rich preflight failures use the fully prepared legacy media/text plan
instead. The model never supplies media bytes, identifiers or an upload target.

Persistent sends use explicit `confirmed`, `rejected`, `partial_rejected`,
`unknown` and `partial_unknown` outcomes. Network errors, malformed successes
and Telegram 5xx responses are `unknown` and are never resent through another
format or followed by a generic message. This rule also applies to multipart
rich-media uploads: only a named rich-format rejection can atomically activate
the already prepared legacy suffix. The complete assistant history row and
reaction/artifact links are created only after every operation of the logical
reply is confirmed, including packed rich text, previews and explicit artifact
operations.

## Rich streaming

Eligible private-chat canaries with `draft_streaming_enabled=true` use
Telegram's purpose-built `sendRichMessageDraft` primitive:

1. The triggering message id becomes the stable, non-zero draft id.
2. `<tg-thinking>` carries the bounded RAG/tool journey.
3. Accumulated SSE deltas pass through `BalanceOpenMarkers` and the same safe
   Goldmark Rich HTML allowlist as the final response.
4. Preview rendering suppresses every active link/anchor and sets
   `skip_entity_detection=true`; links become active only in the completed,
   policy-checked final.
5. Snapshots use the configured throttle plus a shared 1.2-second peer floor.
   A one-shot heartbeat scheduled 15 seconds from the latest accepted snapshot
   keeps the 30-second ephemeral preview alive during long stalls, and periodic
   `sendChatAction` stops once the draft exists.
6. Before a persistent-final attempt the sink first becomes callback-terminal,
   then an unseen coalesced content tail gets at most one best-effort catch-up
   attempt within a two-second total budget. Error/cleanup close remains
   network-free. The ordinary
   buffered `sendRichMessage` path sends the completed answer and alone owns
   confirmed/rejected/unknown classification, history and reaction linkage.
   For an eligible generated photo, the draft heartbeat is stopped before one
   multipart final uploads the photo and Rich HTML together; the draft never
   becomes a second durable message.

There is one draft per logical turn. A standalone `###SPLIT###` still creates
multiple persistent Rich Messages: the first persistent send removes the live
draft, so later parts are expected to appear atomically rather than stream as
separate bubbles. Multiple concurrent draft IDs are deliberately not used
because Telegram clients do not handle them uniformly.

The draft source has its own 24 KiB internal budget, independent of the legacy
`max_buffer_chars` setting. A crossing delta contributes its largest valid
UTF-8 prefix, then the exact capped payload is frozen for any scheduled send,
retry or heartbeat. This bounds preview work without truncating the separately
rendered persistent final. If the capped payload itself fails local structural
or rendered-size checks, no invalid draft call is made and the last previously
confirmed preview payload remains the heartbeat source.

Draft failure never causes a second persistent attempt and never counts as
delivery. A confirmed draft 4xx disables only the preview for that turn; a
network/5xx result may be retried later with the same id because updates are
ephemeral and idempotent. The final message keeps the existing one-shot unknown
outcome rule.

Telegram's alternative `editMessageText.rich_message` edits an already durable
message. It remains a future option for groups/business contexts where drafts
are unavailable; mixing it with the draft path in one turn is intentionally
avoided.
