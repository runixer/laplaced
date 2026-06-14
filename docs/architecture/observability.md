# Observability (OpenTelemetry + Prometheus)

Этот документ описывает наблюдаемость бота: трассировку OpenTelemetry (каждый ход
разговора как дерево спанов), сигналы аномалий, метрики Prometheus и связку
логов с трейсами.

## Обзор

С v0.9.0 каждый ход эмитит родительский спан с дочерними спанами на каждый шаг,
пересекающий границу процесса (вызовы LLM, эмбеддинги, RAG, reranker, enricher,
инструменты, генерация изображений). Трассировка по умолчанию **выключена**;
включается блоком `telemetry`. Метрики Prometheus существуют параллельно и не
заменены трейсами.

## Конфигурация

`internal/obs/tracing.go`, секция `telemetry`:

```yaml
telemetry:
  enabled: false
  exporter: "otlp"             # "otlp" (gRPC в коллектор) | "stdout" (dev)
  otlp_endpoint: "localhost:4317"
  service_name: "laplaced"
  trace_content: false         # писать ли полный текст промптов/ответов в span events
```

Сэмплирование — AlwaysSample (низкий объём, короткое хранение). Ресурс несёт
`service.name`, `service.version` и `laplaced.trace_content` (помечает трейс в
debug-режиме).

## Иерархия спанов

Корневой спан — **`bot.processMessageGroup`** (`internal/bot/process_group.go`).
Ключевые дочерние спаны:

| Спан | Где |
|------|-----|
| `laplace.Execute` | главный чат-агент (tool loop) |
| `llm.CreateChatCompletion` / `llm.CreateChatCompletionStream` | вызовы чат-LLM |
| `llm.CreateEmbeddings` | эмбеддинги |
| `enricher.Execute` | обогащение запроса |
| `reranker.Execute` | реранкинг кандидатов |
| `rag.Retrieve` | выборка контекста |
| `imagegen.Generate` | генерация изображений |
| `reactor.Execute` | эмодзи-реакция |
| фоновые: `rag.processChunk`, `rag.processArtifactExtraction`, `rag.processConsolidation`, `archivist.Execute`, `splitter.Execute`, `merger.Execute`, `extractor.Execute`, `memory.ProcessSession` | фоновая обработка памяти |

Корневой спан несёт сводку хода: `user.id`, `bot.user_query_preview` +
`bot.user_query_hash`, `bot.reply_preview` + `bot.reply_hash`,
`bot.has_current_media`, `bot.llm_calls_count`, `bot.tool_calls_count`,
`bot.total_cost_usd`, `bot.tools_used`. Превью + хэш ответа всегда включены (даже
при `trace_content: false`) — поэтому ход находится в Tempo по тексту ответа
через TraceQL вида `{ span.bot.reply_preview =~ ".*X.*" }`.

Спан `llm.CreateChatCompletion` несёт `gen_ai.request.model`,
`gen_ai.usage.{input,output}_tokens`, `llm.cost_usd`, `llm.attempts` и per-attempt
события с классом ошибки и HTTP-статусом (видны ретраи и edge-ошибки провайдера).

## Аномалии

Аномалии живут как span-атрибуты на корневом спане (`recordLaplaceAnomalies`),
не как Prometheus-счётчики — TraceQL даёт и счёт, и сам трейс с контекстом. Общий
префикс `bot.anomaly.*`:

| Атрибут | Условие |
|---------|---------|
| `bot.anomaly.empty_response` | финальный ответ пуст (после sanitize); юзеру ушёл локализованный fallback |
| `bot.anomaly.sanitized` | срезаны артефакты галлюцинации (`</tool_code>`, `default_api:`); событие `bot.anomaly.sanitized_from` с оригиналом (гейтится `trace_content`) |
| `bot.anomaly.false_disclaimer` | модель сказала «не могу прочитать файл», но выдала >300 осмысленных символов (+ `disclaimer_phrase`) |
| `bot.anomaly.echo_user_message` | дословное эхо последнего сообщения пользователя (Gemini bug; + `echo_output_tokens`) |
| `bot.anomaly.fabricated_url` | вставлена ссылка, которой не было в результатах поиска; citation guard развернул её (+ `fabricated_url_count`, событие `fabricated_urls`) |

## Редактирование base64

`internal/obs/content.go`: перед записью контента в span events data-URL'ы
(base64-картинки, часто 1–5 MB) заменяются на
`redacted:sha256:<hex>:<mime>:<size>`. Это держит трейсы в пределах
`max_bytes_per_trace`; при необходимости реплея оригинальные файлы
восстанавливаются из снапшота блоб-хранилища по `content_hash`.

## Propagation и логи

- **W3C trace context:** в исходящие HTTP-запросы к LLM-шлюзу инжектится
  заголовок `traceparent` (`internal/llm/client.go`), так что OTel-инструментованный
  шлюз (litellm, vLLM) подцепляет свои спаны к трейсу бота.
- **slog ↔ трейс:** записи лога на уровне хода наследуют `trace_id`/`span_id`
  (`obs.LoggerWithSpan`), поэтому строка в Loki и трейс в Tempo
  кросс-ссылаются автоматически.

## Метрики Prometheus

`internal/bot/metrics.go` (сосуществуют с трейсами). Среди прочих:
`laplaced_bot_messages_processed_total{user_id,status}`,
`laplaced_bot_message_llm_duration_seconds`,
`laplaced_bot_message_tool_duration_seconds`,
`laplaced_bot_message_llm_first_token_seconds`,
`laplaced_bot_message_telegram_edit_count`,
`laplaced_bot_context_tokens_by_source{source}`,
`laplaced_bot_response_flags_total{emoji}`.

## Связанные документы

- [message-processing-flow.md](./message-processing-flow.md) — где какие спаны возникают
- [streaming.md](./streaming.md) — first-token и edit-count метрики
