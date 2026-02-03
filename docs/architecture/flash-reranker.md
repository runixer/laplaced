# Flash Reranker

Этот документ описывает архитектуру agentic reranker для фильтрации RAG-кандидатов.

## Обзор

Flash Reranker — компонент RAG pipeline, который использует LLM (Gemini Flash) для интеллектуального отбора релевантных топиков, людей и артефактов из памяти.

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
│ Response 2: Tool Result                                          │
├──────────────────────────────────────────────────────────────────┤
│ Full content of 10 topics (~25K tokens)                          │
└──────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────┐
│ Response 3: Final JSON                                           │
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

### Почему Unified Reranker (Topics + People + Artifacts)

В v0.5 появился People table (социальный граф), v0.6.0 — Artifacts. Люди, топики и артефакты связаны контекстом:

**Пример запроса:** "Помнишь Петрова из ProjectX? Что было в той документации?"
- Нужен профиль Петрова (People)
- Нужны топики где обсуждали ProjectX, митинги, командировки
- Нужен PDF документ (Artifacts)

Один reranker видит всех кандидатов и выбирает оптимальный микс.
Раздельные лимиты: 0-5 топиков, 0-10 людей, 0-10 артефактов.

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
[Topic:42] 2025-07-25 | 20 msgs, ~16K chars | Развертывание ML-инфраструктуры на H100
[Topic:18] 2025-12-21 | 172 msgs, ~47K chars | Обсуждение коллеги Марии
[Topic:5]  2026-01-02 | 3 msgs, ~800 chars | Быстрый вопрос про Docker
```

Размер в символах помогает Flash оценить "вес" топика перед загрузкой.

### Формат кандидата (v0.6.0 with scores)

```
[Topic:42] (0.72) 2025-07-25 | 20 msgs, ~16K chars | Развертывание ML-инфраструктуры на H100
[Person:18] (0.65) Петров | Work_Inner | 127 mentions | Коллега по проекту X
[Artifact:123] (0.68) pdf: "api-docs.pdf" | api, rest | Entities: OAuth2, JWT
```

Cosine similarity score показывается для ориентира Flash.

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
  ],
  "people": [
    {"id": 15, "reason": "Коллега по упомянутому проекту"}
  ],
  "artifacts": [
    {"id": 123, "reason": "API документация про авторизацию"}
  ]
}
```

**Поля:**
- `id` — ID элемента (обязательно)
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

## Multimodal RAG (v0.4.5)

Flash Reranker поддерживает multimodal input — изображения и аудио из сообщения пользователя передаются в enricher и reranker.

**Проблема:** Без multimodal RAG не понимает содержимое голосовых/картинок:
```
User: [voice 30s] + "о чём тут речь?"
      ↓
RAG ищет "🎤 о чём тут речь?" → бесполезно для поиска
```

**Решение:** Передаём медиа в RAG pipeline:
```
User: [voice 30s] + "о чём тут речь?"
      ↓
Enricher ВИДИТ аудио → "Маша рассказывает о даче, собаке"
      ↓
Vector search: "дача, собака" → релевантные топики
      ↓
Reranker ВИДИТ аудио + 50 саммари → точная фильтрация
```

**Реализация:**
- `RetrievalOptions.MediaParts` — slice с `FilePart` (unified format for all media)
- `enrichQuery()` строит multimodal message при наличии медиа
- `rerankCandidates()` включает медиа в user prompt

**Cost impact:**
| Этап | Audio 30s (~2K tok) | Cost |
|------|---------------------|------|
| Enricher | +2K tokens | $0.002 |
| Reranker | +2K tokens | $0.002 |
| **Extra cost per message** | ~4K tokens | **~$0.004** |

## Artifacts Integration (v0.6.0)

Артефакты (файлы) передаются в reranker как кандидаты наравне с топиками и людьми:

```
[Artifact:123] (0.68) pdf: "api-docs.pdf" | api, rest, authentication | Entities: OAuth2, JWT, Stripe
[Artifact:456] (0.72) image: "diagram.png" | architecture, flow | Entities: Service A, Database B
```

Reranker:
1. Видит summary, keywords, entities каждого артефакта
2. Выбирает релевантные файлы (до 10)
3. Возвращает ID артефактов в финальном JSON

**Context Assembly:**
- Summary context — артефакты с высоким relevance включаются в `<artifact_context>`
- Full content — выбранные артефакты загружаются полностью как `FilePart` (base64)

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

## Конфигурация (v0.6.0)

```yaml
rag:
  reranker:
    enabled: true
    model: "google/gemini-3-flash-preview"
    timeout: "60s"              # timeout на весь reranker flow
    turn_timeout: "30s"         # timeout на каждый LLM вызов
    max_tool_calls: 3           # максимум tool calls
    thinking_level: "minimal"   # reasoning effort: minimal/low/medium/high

    # Per-type limits (v0.6.0)
    topics:
      candidates_limit: 50      # сколько summaries показать
      max: 5                    # лимит топиков в выборе
    people:
      candidates_limit: 20      # сколько людей показать
      max: 10                   # лимит людей
    artifacts:
      candidates_limit: 20      # сколько артефактов показать
      max: 10                   # лимит артефактов
      max_context_bytes: 52428800  # 50MB cumulative limit
```

**Legacy migration (v0.6.0):**
Старые поля `candidates`, `max_topics`, `max_people` автоматически мапятся в новую структуру для обратной совместимости.

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

### v0.6.0 Unified Reranker

**Three-type candidate filtering:**
- Topics, People, Artifacts — все проходят через один reranker
- Единый формат кандидатов с similarity scores
- Единый формат ответа с reason для каждого типа

**Per-type limits:**
- Topics: 5 (было 15 в ранних версиях)
- People: 10
- Artifacts: 10

## Ссылки

- [Embedding Storage](./embedding-storage.md)
