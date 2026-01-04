# Flash Reranker

Этот документ описывает архитектуру agentic reranker для фильтрации RAG-кандидатов.

## Обзор

Flash Reranker — компонент RAG pipeline, который использует LLM (Gemini Flash) для интеллектуального отбора релевантных топиков и людей из памяти.

**Проблема:** Vector search возвращает Top-50 кандидатов по косинусному сходству, но:
- Все 50 в контекст не влезут (~100K токенов)
- Косинусное сходство не учитывает контекст разговора
- Нет понимания связей между людьми, проектами, событиями

**Решение:** Agentic reranker — Flash сам изучает кандидатов и выбирает релевантные.

## Архитектура

```
┌─────────────────────────────────────────────────────────────────┐
│                      RETRIEVAL PIPELINE                         │
├─────────────────────────────────────────────────────────────────┤
│  1. CONTEXTUALIZED QUERY (Flash)                                │
│  2. VECTOR SEARCH → Top-50 candidates (summaries only)          │
│  3. FLASH RERANKER (agentic):                                   │
│     Turn 1: Flash получает 50 summaries (~3K tokens)            │
│             Flash вызывает get_topics_content([ids])            │
│     Turn 2: Flash получает full content запрошенных топиков     │
│             Flash возвращает JSON с финальным выбором           │
│  4. CONTEXT ASSEMBLY с выбранными топиками                      │
└─────────────────────────────────────────────────────────────────┘
```

### Agentic Flow

```
┌──────────────────────────────────────────────────────────────────┐
│ Turn 1: Initial Request                                          │
├──────────────────────────────────────────────────────────────────┤
│ System: Reranker prompt                                          │
│ User:                                                            │
│   - Текущая дата                                                 │
│   - Contextualized Query (суть разговора)                        │
│   - Текущая пачка сообщений от пользователя                      │
│   - Профиль пользователя (key facts)                             │
│   - 50 кандидатов (ID | Дата | Размер | Тема)                    │
│ Tools: get_topics_content(ids: int[])                            │
└──────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────┐
│ Response 1: Tool Call                                            │
├──────────────────────────────────────────────────────────────────┤
│ get_topics_content([42, 18, 5, 12, 7, 33, 8, 15, 21, 44])        │
└──────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────┐
│ Turn 2: Tool Result                                              │
├──────────────────────────────────────────────────────────────────┤
│ Full content of 10 topics (~25K tokens)                          │
└──────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────┐
│ Response 2: Final JSON                                           │
├──────────────────────────────────────────────────────────────────┤
│ {"topics": [42, 18, 5], "people": []}                            │
└──────────────────────────────────────────────────────────────────┘
```

## Принятые решения

### Почему Agentic подход

| Подход | Токены | Проблема |
|--------|--------|----------|
| Full-content | 50 × ~2K = 100K+ | Слишком много токенов |
| Two-stage | 3K + 30K | Мы решаем что смотреть, не Flash |
| **Agentic** | 3K + (N × ~2K) | Flash сам выбирает, загружает только нужное |

Анализ реальных данных (1225 топиков):
- 79% топиков ≤ 2K токенов
- Медиана: 500-1K токенов
- Гиганты (>10K): только 1%

Типичный сценарий: Flash запросит 10-15 топиков (~25K токенов) вместо всех 50.

### Почему Unified Reranker (Topics + People)

В v0.5 появится People table (социальный граф). Люди и топики связаны контекстом:

**Пример запроса:** "Помнишь Петрова из ProjectX?"
- Нужен профиль Петрова (People)
- Нужны топики где обсуждали ProjectX, митинги, командировки

Один reranker видит всех кандидатов и выбирает оптимальный микс.
Раздельные лимиты: 0-5 топиков, 0-10 людей.

### Почему Batch Tools

```go
// Хорошо: один вызов
get_topics_content([42, 18, 5, 12, 7])

// Плохо: 5 round-trips
get_topics_content([42])
get_topics_content([18])
get_topics_content([5])
...
```

Flash выбирает batch для изучения → 2 round-trips вместо 10+.

### Почему JSON + Response Healing

OpenRouter Response Healing plugin автоматически исправляет:
- JSON в markdown блоках
- Синтаксические ошибки
- Посторонний текст вокруг JSON

```go
Plugins: []openrouter.Plugin{{ID: "response-healing"}},
ResponseFormat: openrouter.ResponseFormat{Type: "json_object"},
```

## Форматы данных

### Формат кандидата (summary)

```
[ID:42] 2025-07-25 | 20 msgs, ~16K chars | Развертывание ML-инфраструктуры на H100
[ID:18] 2025-12-21 | 172 msgs, ~47K chars | Обсуждение коллеги Марии
[ID:5]  2026-01-02 | 3 msgs, ~800 chars | Быстрый вопрос про Docker
```

Размер в символах помогает Flash оценить "вес" топика перед загрузкой.

### Формат tool result (full content)

```
=== Topic 42 ===
Дата: 2025-07-25 | 20 msgs | ~16K chars
Тема: Развертывание ML-инфраструктуры на H100, настройка vLLM для Qwen3

[User (@username) (2025-07-25 14:30:00)]: Как настроить vLLM на H100?
[Assistant]: Для H100 рекомендую следующие настройки...
[Переслано от Петров пользователем User в 2025-07-25 14:35:00]: А у нас такие же проблемы
[User (@username) (2025-07-25 14:36:00)]: Да, помню Петрова

=== Topic 18 ===
Дата: 2025-12-21 | 172 msgs | ~47K chars
Тема: Обсуждение коллеги Марии и её работы с Анной
...
```

### Формат финального ответа

**v0.4.2+:** Новый формат с reason и excerpt:

```json
{
  "topics": [
    {"id": 42, "reason": "Обсуждение vLLM на H100 — прямой ответ на вопрос"},
    {"id": 18, "reason": "Упоминание Петрова", "excerpt": "[User]: Помню Петрова из ProjectX...\n[Assistant]: Да, он занимался..."}
  ]
}
```

**Поля:**
- `id` — ID топика (обязательно)
- `reason` — краткое объяснение (1-2 предложения) почему выбран (обязательно)
- `excerpt` — только для топиков >25K chars: Flash вырезает релевантные сообщения вместо использования полного контента

**Зачем excerpt:**
- Топики >25K chars — редкость (~1%), но занимают много контекста
- Вместо загрузки полного топика (~50K tokens), Flash извлекает только релевантные сообщения (~2-5K)
- Формат excerpt: сохраняет полные сообщения `[User]: ..., [Assistant]: ...`

**Правила excerpt (v0.4.3):**
- Сохранять пары User→Assistant — не вырезать сообщения пользователя
- Не обрезать сообщения на полуслове — включать полный текст
- Включать 1-2 соседних сообщения для контекста
- Excerpt должен отвечать на ОРИГИНАЛЬНЫЙ запрос, а не на расширенный

**Backward compatibility:** Парсер поддерживает старый формат `[42, 18, 5]`

## Fallback Strategy

**Принцип:** Если Flash успел сделать tool call — его выбор (requestedIDs) ценнее чем голый косинус.

| Ситуация | Что есть | Действие |
|----------|----------|----------|
| Flash вернул JSON без tools | Финальный выбор | Принять |
| Flash вернул валидный JSON после tools | Финальный выбор | Принять |
| Tool call сделан, JSON невалидный | requestedIDs | Top-5 из requestedIDs |
| Timeout после tool call | requestedIDs | Top-5 из requestedIDs |
| 3+ tool calls (лимит) | requestedIDs | Top-5 из requestedIDs |
| Timeout до tool call | Ничего | Vector top-5 |
| Ошибка API | Ничего | Vector top-5 |

**Реализация:**

```go
type rerankerState struct {
    requestedIDs []int  // накапливаем из всех tool calls
}

// После каждого tool call:
state.requestedIDs = append(state.requestedIDs, toolCall.IDs...)

// При fallback:
if len(state.requestedIDs) > 0 {
    return state.requestedIDs[:min(5, len(state.requestedIDs))]
}
return vectorTop5
```

Flash обычно ставит более релевантные первыми в массиве — берём первые 5.

## Tool Schema

```json
{
  "name": "get_topics_content",
  "description": "Загрузить полное содержимое топиков для детального изучения. Учитывай размер — большие топики (>10K chars) загружай только если тема очень релевантна.",
  "parameters": {
    "type": "object",
    "properties": {
      "ids": {
        "type": "array",
        "items": {"type": "integer"},
        "description": "ID топиков для загрузки"
      }
    },
    "required": ["ids"]
  }
}
```

## Метрики

| Метрика | Тип | Labels | Описание |
|---------|-----|--------|----------|
| `laplaced_reranker_duration_seconds` | histogram | user_id | Общее время (все turns) |
| `laplaced_reranker_tool_calls_total` | counter | user_id | Количество tool calls |
| `laplaced_reranker_candidates_input` | histogram | user_id | Summaries на входе |
| `laplaced_reranker_candidates_output` | histogram | user_id | Выбрал в итоге |
| `laplaced_reranker_cost_usd_total` | counter | user_id | Стоимость reranker |
| `laplaced_reranker_fallback_total` | counter | user_id, reason | Fallback срабатывания |

## Конфигурация

```yaml
rag:
  reranker_enabled: true
  reranker_model: "google/gemini-3-flash-preview"
  reranker_candidates: 50       # сколько summaries показать
  reranker_max_topics: 5        # лимит топиков в выборе
  reranker_max_people: 10       # лимит людей (v0.5)
  reranker_timeout: "10s"       # timeout на весь reranker flow
  reranker_max_tool_calls: 3    # максимум tool calls
```

## Ожидаемый эффект

| Метрика | v0.3.9 (baseline) | v0.4 (target) | v0.4 (actual) |
|---------|-------------------|---------------|---------------|
| E2E latency p95 | ~50s | ~25s | ~26s ✅ |
| Context: Topics | ~24K tokens | ~5K tokens | ~7K tokens ✅ |
| Cost per message | ~$0.40 | ~$0.20 | ~$0.05 ✅ |

> **Примечание:** Actual результаты получены при тестировании на dev (2026-01-04). Стоимость ниже ожидаемой за счёт агрессивной фильтрации (50 → 5 топиков) и использования Flash для reranker.

### v0.4.2 Improvements

**Reason & Excerpt:**
- **Debuggability:** В UI видно почему Flash выбрал каждый топик
- **Context reduction:** Для гигантских топиков (>25K chars) контекст сократится ещё больше
- **Quality monitoring:** Можно анализировать reasons для улучшения промптов

### v0.4.3 Protocol Enforcement

**Forced Tool Calling:**
- На первой итерации используется `tool_choice: {type: "function", function: {name: "get_topics_content"}}`
- Flash ОБЯЗАН вызвать tool перед возвратом результата — это гарантия изучения контента
- Gemini API ограничение: `tool_choice` несовместим с `response_format: json_object`
- Решение: `response_format` включается только после первого tool call

**Reasoning Mode:**
- Включён `reasoning.effort: "low"` для улучшения качества tool calls
- Gemini 2.5+ модели используют internal thinking перед ответом

**Protocol Violation Detection:**
- Если Flash возвращает ответ без tool calls → fallback на vector top
- Метрика: `laplaced_reranker_fallback_total{reason="protocol_violation"}`
- Safety net на случай если `tool_choice` не сработает

**Query Enrichment Fix:**
- Добавлено правило "НЕ УГАДЫВАЙ" в enrichment prompt
- Предотвращает замену незнакомых слов на похожие технические термины
- Пример: "мениск" теперь не превращается в "MinIO"

## Ссылки

- [ROADMAP v0.4](../plans/ROADMAP.md#1-flash-reranker-p1--main-feature)
- [Embedding Storage](./embedding-storage.md)
