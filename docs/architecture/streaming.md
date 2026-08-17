# Streaming (потоковые ответы с лентой размышлений)

Этот документ описывает потоковую отдачу ответа: вместо молчаливого ожидания
бот показывает плейсхолдер или эфемерный rich-draft, ленту шагов и постепенно
дописывает текст по мере генерации.

## Обзор

Legacy-стриминг включён по умолчанию и работает на транспортах с
`Capabilities.SupportsStreaming`. Rich draft имеет отдельный default-off
rollout switch. В Telegram есть два изолированных пути:

- legacy output — постоянный плейсхолдер и `editMessageText`
  (`internal/bot/streaming.go`, `streamSink`);
- rich private canary — эфемерный `sendRichMessageDraft`, затем отдельный
  постоянный `sendRichMessage` (`internal/bot/rich_streaming.go`,
  `richDraftSink`); этот путь требует `mode=send`, eligible native user и
  `telegram.rich_messages.draft_streaming_enabled=true`.

Mattermost не стримит и получает один завершённый ответ.

```
1. Получили сообщение      → legacy placeholder или rich `<tg-thinking>` draft
2. RAG/инструменты          → обновляем bounded status journey
3. LLM отдаёт deltas        → throttled edit/draft snapshots
4. Агент завершился         → persistent edit (legacy) или новый confirmed rich send
5. Только persistent success → history и reaction-link
```

## Лента размышлений (status log)

Пока бот «думает», в начало preview дописываются строки статуса. Legacy sink
использует `<blockquote expandable>`, rich draft — нативный `<tg-thinking>`:

- `RAG(enrichedQuery)` — строка про обогащённый поисковый запрос;
- `Status(toolName, args)` — статус каждого инструмента с инлайн-аргументом
  (например, поисковый запрос или промпт генерации картинки), HTML-экранированным
  и обрезанным до 200 символов.

Строки внутри rich `<tg-thinking>` разделяются явным `<br>`: source newline в
Rich HTML схлопывается клиентами как обычный whitespace.

## Прогрессивные snapshots

`Delta(text)` копит буфер и обновляет текущий preview:

- первый content-дельта (переход «статус → контент») редактирует сразу;
- далее — по дросселю: прошло `edit_throttle_ms` **или** накопилось
  `edit_min_chars` новых символов;
- текущий буфер прогоняется через `markdown.BalanceOpenMarkers` (автозакрытие
  незакрытых `**`, `` ` ``, ```` ``` ````);
- legacy preview использует `markdown.ToHTML`;
- rich preview использует `markdown.ToRichHTMLPreview`: тот же AST/allowlist,
  что у финала, но без активных ссылок/anchors/media. На transport включён
  `skip_entity_detection`, поэтому незавершённый URL тоже не кликабелен.

Ни один preview не показывает внутренний delivery protocol. Полные standalone
`###MEDIA:...###`/`###SPLIT###` строки, malformed reserved MEDIA-строки и
неоднозначный незавершённый terminal prefix временно удаляются только из
presentation view; исходный буфер остаётся неизменным для финального planner.
Изображения в draft не загружаются: они впервые появляются в подтверждённом
persistent Rich Message. Числовые внутренние `artifact:<id>`/`artifact id`
также удаляются из legacy и rich preview, включая частично пришедший terminal
prefix; tool-loop по-прежнему видит доверенный raw result для chaining.

Rich draft живёт 30 секунд после принятого snapshot. Heartbeat через 15 секунд
от последнего принятого snapshot поддерживает его во время долгого
LLM/tool stall. После первого draft
`sendChatAction` прекращается, чтобы оба API не делили flood-budget.
Все draft/status/RAG/heartbeat updates дополнительно проходят общий минимум 1.2s
и coalescing latest snapshot: это оставляет запас под Telegram peer limits
20/5s и 40/30s. `429.retry_after` соблюдается, а network/5xx включает 5s
best-effort cooldown; preview request ограничен 5s и не меняет final delivery.

## Переполнение

У двух Telegram-путей разные границы:

- legacy `editMessageText` использует совместимый конфиг
  `max_buffer_chars` (по умолчанию 3400 байт), после чего preview замирает, а на
  финале первое сообщение редактируется и продолжение отправляется отдельно;
- native rich draft использует внутренний source budget 24 KiB. Пересекающая
  delta дописывается максимальным непрерывным UTF-8-префиксом, затем content
  preview фиксируется. Последний capped snapshot, его retry и heartbeat всё ещё
  разрешены, но поздние deltas/statuses уже не меняют зафиксированный payload.
  Если сам capped snapshot не проходит локальный structural/rendered-size
  preflight, API не вызывается и heartbeat повторяет последний уже
  подтверждённый Telegram payload.

Rich sink вообще не использует preview как источник финала: полный
`resp.Content` проходит существующий rich preflight и обычный
`sendRichMessage` delivery path. В budget rich-preview оставлен запас под
bounded `<tg-thinking>`; перед каждым draft также проверяются объединённые
semantic characters/blocks/depth, размер rendered HTML и table width.

Rich draft эфемерен и не имеет `message_id`, reply, history или реакции. Его
`Close()` на error/cleanup-путях только блокирует поздние callbacks и heartbeat.
Перед попыткой persistent text/media-финала `FinalizePreview()` сначала делает
callbacks терминальными, затем даёт ещё не показанному content-хвосту
ровно одну best-effort catch-up попытку. Активный cooldown после
`retry_after` или transient-ошибки её отменяет;
локальный 1.2s sustained-rate floor этот единичный terminal burst может
обойти, так как обычная частота оставляет запас ниже Telegram peer limits.
Весь API request ограничен двумя секундами.
Ошибка catch-up не повторяется и не меняет судьбу persistent delivery.
Финальный send остаётся one-shot: network/5xx/malformed success имеет outcome
`unknown`, не повторяется и не записывается в историю.

## Метрики

- `laplaced_bot_message_llm_first_token_seconds` — время до первого delta;
- `laplaced_bot_message_telegram_edit_count` — persistent legacy edits;
- `laplaced_bot_message_telegram_rich_draft_count` — все logical API attempts
  ephemeral rich draft;
- `laplaced_bot_message_telegram_rich_draft_content_snapshot_count` — успешно
  принятые snapshots, продвинувшие content prefix;
- `laplaced_bot_message_telegram_rich_draft_overflow_total` — число turns, в
  которых только ephemeral preview достиг внутреннего budget.
- `laplaced_bot_message_telegram_rich_draft_terminal_catchup_total{outcome}` —
  исход одной terminal preview-попытки (`sent`, `skipped_no_tail`,
  `skipped_cooldown`, `skipped_render` или `failed`).

Draft count считает логические вызовы Bot API client. Физические HTTP retry с
тем же idempotent draft ID видны в общих Telegram request/retry metrics.

## Конфигурация

```yaml
bot:
  streaming:
    enabled: true
    edit_throttle_ms: 1000   # мин. интервал между progressive snapshots
    edit_min_chars: 80       # ранний snapshot после N новых символов
    max_buffer_chars: 3400   # только legacy editMessageText preview
telegram:
  rich_messages:
    draft_streaming_enabled: false
```

`LAPLACED_BOT_STREAMING_ENABLED=false` выключает только legacy edit streaming.
`LAPLACED_TELEGRAM_RICH_MESSAGES_DRAFT_STREAMING_ENABLED=false` независимо
выключает ephemeral rich preview; persistent rich final остаётся включённым для
canary, если `mode=send`.

## Связанные документы

- [message-processing-flow.md](./message-processing-flow.md) — шаг 6 «Отправка ответа»
- [telegram-html-rendering.md](./telegram-html-rendering.md) — Markdown → HTML и разбиение
- [../telegram-rich-messages.md](../telegram-rich-messages.md) — Rich Message ingress/egress и delivery semantics
- [transports.md](./transports.md) — `SupportsStreaming`
