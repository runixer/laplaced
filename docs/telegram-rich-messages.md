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

## v1 release contract

v1 is the production-safe vertical slice, not a promise that every Telegram
media composition is native Rich Message output:

- incoming `rich_message`, classic text entities and caption entities are
  always decoded through the bounded projection described above;
- an eligible private-chat canary receives complete text replies as persistent
  Rich Messages, including safe links, headings, lists, tables, quotes, code,
  spoilers and LaTeX;
- optional rich draft streaming shows ephemeral tool/RAG/content progress and
  is stopped before the persistent final owns delivery;
- exactly one generated image that still qualifies as Photo can be uploaded
  together with one complete Rich HTML part in a single multipart final;
- albums, document-quality images, `###SPLIT###`, oversized/local-fallback
  layouts, groups, business messages and direct-message topics deliberately
  retain the established legacy path.

The last item is supported fallback behavior in v1, not silent partial Rich
Message support. In particular, Telegram Rich Messages have no native Document
block, so the original-quality Document path cannot simply be switched over.

v2 will introduce a shared composition/delivery planner before widening this
surface: multiple images through collage/slideshow where appropriate,
media-aware rich splitting, stable IDs for every persistent part, and an
explicit preview-versus-original policy for document-quality images. Persistent
rich edits for contexts without private-chat drafts are also a v2 candidate.

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
   counters. This is also the egress kill switch.
2. **Shadow:** add only the operator IDs and select `mode=shadow`. Leave drafts
   disabled. Exercise the representative corpus and compare shadow decisions
   with the delivered legacy messages; no client-visible output should change.
3. **Operator send:** select `mode=send` for those same IDs, still with drafts
   disabled. Check text, one generated Photo, an album, a Document, explicit
   split, copy/forward, reply and reaction on Desktop and Android.
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
  `laplaced_bot_message_telegram_rich_draft_overflow_total`.

Stop expansion immediately on a duplicate durable final, a history/reaction
link without confirmed delivery, any model-authored media fetch, an increasing
`outcome="unknown"`, a new sustained rich rejection/API-fallback class, or any
increase in the rate-limit counter. Draft-specific 429s or unexpectedly high
draft-call counts first require disabling `draft_streaming_enabled`; persistent
delivery can remain in operator `send` while it is rechecked. For persistent
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
delivery paths. A generated-media response uses one multipart
`sendRichMessage` when it contains exactly one image that qualifies for Photo
delivery and its complete text fits one Rich Message. The trusted artifact is
inserted as a top-level `<img>` block; model-authored Markdown images remain
inert text. Albums, document-quality images, split rich output and local rich
preflight failures keep the established media/caption/follow-up path.

Persistent sends use explicit `confirmed`, `rejected` and `unknown` outcomes.
Network errors, malformed successes and Telegram 5xx responses are `unknown`
and are never resent through another format or followed by a generic message.
This rule also applies to multipart rich-media uploads: only a named
rich-format rejection can fall back to the already prepared legacy media path.
The complete assistant history row and reaction/artifact links are created only
after every part of the logical reply is confirmed, including generated-media
follow-up text.

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
5. Snapshots use the configured one-second/character throttle. A 20-second
   heartbeat keeps the 30-second ephemeral preview alive during long stalls,
   and periodic `sendChatAction` stops once the draft exists.
6. Closing the sink performs no Telegram finalization call. The ordinary
   buffered `sendRichMessage` path sends the completed answer and alone owns
   confirmed/rejected/unknown classification, history and reaction linkage.
   For an eligible generated photo, the draft heartbeat is stopped before one
   multipart final uploads the photo and Rich HTML together; the draft never
   becomes a second durable message.

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
