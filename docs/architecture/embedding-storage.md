# Embedding Storage & Vector Search

Этот документ описывает архитектуру хранения embeddings и принятые решения по масштабированию.

## Обзор

Laplaced использует embeddings для семантического поиска по долгосрочной памяти:
- **Topics** — сжатые саммари прошлых разговоров
- **Facts** — структурированные факты о пользователе
- **People** — извлечённые из разговоров люди (v0.5.1)
- **Artifacts** — семантические embeddings файлов (v0.6.0)

При каждом запросе:
1. Генерируется embedding запроса через OpenRouter API
2. Вычисляется косинусное сходство со всеми векторами пользователя
3. Top-N результатов включаются в контекст LLM

## Текущая архитектура

### Модель embedding

До v0.7.0:

```yaml
model: google/gemini-embedding-001
dimensions: 3072
normalization: L2 normalized (от провайдера, только для dim=3072)
```

С v0.7.0 — см. [Миграция на Embedding 2 Preview (v0.7.0)](#миграция-на-embedding-2-preview-v070):

```yaml
model: google/gemini-embedding-2-preview
dimensions: 1536
normalization: L2 normalized (провайдер нормализует все размерности)
```

> **Note:** Некоторые прогнозные таблицы ниже использовали dim=768 как допущение при принятии ранних решений по масштабированию. В актуальных расчётах памяти используется `embeddingMemoryBytes` из `internal/rag/metrics.go`, которое считается от текущей размерности.

### Хранение в SQLite

Embeddings хранятся как **JSON text** в BLOB полях:

```sql
CREATE TABLE topics (
    ...
    embedding BLOB  -- JSON: "[-0.022, 0.010, ...]"
);

CREATE TABLE facts (
    ...
    embedding BLOB  -- JSON: "[-0.015, 0.008, ...]"
);

CREATE TABLE people (
    ...
    embedding BLOB  -- JSON: "[-0.008, 0.012, ...]"  -- v0.5.1
);

CREATE TABLE artifacts (
    ...
    embedding BLOB  -- JSON: "[-0.012, 0.015, ...]"  -- v0.6.0
);
```

**Формат:**
- В БД: JSON string (~9 KB на embedding)
- В памяти: `[]float32` (~3 KB на embedding)
- Overhead JSON vs binary: **3x**

### Vector Search (in-memory)

При старте все embeddings загружаются в RAM. Поиск — brute-force cosine similarity:

```go
// internal/rag/retrieval.go
func cosineSimilarity(a, b []float32) float32 {
    var dot, magA, magB float32
    for i := 0; i < len(a); i++ {
        dot += a[i] * b[i]
        magA += a[i] * a[i]
        magB += b[i] * b[i]
    }
    return dot / (sqrt(magA) * sqrt(magB))
}
```

**Сложность:** O(n × d), где n = количество векторов, d = 768 dimensions.

## Эмпирические данные

### Источник данных

Snapshot production-базы на 2026-04-21:

- Период: 2025-07-19 → 2026-04-20 = **275 дней** (~9 месяцев)
- Активный primary user (плюс история групповых чатов от ~9 других участников, которая тоже сохраняется и эмбеддится в контексте разговоров primary user'а)
- Активность: ~105 сообщений/день
- People добавлены в v0.5.1, Artifacts в v0.6.0 — для них период накопления короче

### Измерения

| Метрика         | Значение          |
|-----------------|-------------------|
| Topics          | 5,058             |
| Topics/day      | ~18.4             |
| Facts           | 412               |
| People          | 281               |
| Artifacts       | 725               |
| History messages | 28,912           |
| Messages/day    | ~105              |
| DB file size    | 931 MB            |

### Размер embeddings

Реально измеренный размер embedding в БД — это JSON-строка `"[-0.022, 0.010, ...]"` в BLOB, overhead ~3.2x от бинарного float32.

**До v0.7.0 (dim = 3072):**

| Формат                  | Per embedding | × 6,476 vectors |
|-------------------------|---------------|------------------|
| В памяти (`[]float32`)  | 12.0 KB       | ~78 MB           |
| В БД (JSON text)        | **39.2 KB** (измерено) | ~253 MB  |
| Оптимально (binary)     | 12.0 KB       | ~78 MB           |

**После v0.7.0 (dim = 1536):**

| Формат                  | Per embedding | × 6,476 vectors |
|-------------------------|---------------|------------------|
| В памяти (`[]float32`)  | 6.0 KB        | **~39 MB**       |
| В БД (JSON text)        | ~19.6 KB      | **~127 MB**      |
| Оптимально (binary)     | 6.0 KB        | ~39 MB           |

Пере­ход на 1536 даёт разовую экономию ~39 MB RAM и ~126 MB места в БД на текущем объёме, без деградации retrieval-качества (см. [Бенчмарк](#результаты)).

## Экстраполяция: семейный сценарий

**Целевой use case:** 5 человек × 5 лет активного использования (25 user-years).

Текущие данные: 275 дней × 1 активный primary user ≈ **0.75 user-year**. Множитель до целевого объёма:

```
25 user-years / 0.75 user-year ≈ 33x
```

| Метрика                       | Сейчас (0.75 user-year) | Прогноз (25 user-years) |
|-------------------------------|-------------------------|--------------------------|
| Topics                        | 5,058                   | ~168,000                |
| Facts                         | 412                     | ~14,000                 |
| People                        | 281                     | ~9,300                  |
| Artifacts                     | 725                     | ~24,000                 |
| **Всего embeddings**          | **~6,500**              | **~215,000**            |
| RAM (dim=1536, binary)        | ~39 MB                  | **~1.3 GB**             |
| DB size (JSON, dim=1536)      | ~127 MB                 | ~4.1 GB                 |
| DB size (binary, dim=1536)    | ~39 MB                  | ~1.3 GB                 |

> Линейная экстраполяция — upper bound. На практике facts/people насыщаются (круг знакомых конечен, ключевые факты о человеке исчисляются десятками), а topics/artifacts продолжают расти. Реальная траектория — sublinear, вероятно в 1.5-2x ниже прогноза.

### Latency прогноз

Brute-force cosine search — O(n × d), где d = 1536 (после v0.7.0).

| Vectors | Multiplications | Estimated latency |
|---------|-----------------|-------------------|
| 1,000   | 1.5M            | <0.3 ms           |
| 10,000  | 15M             | ~1.5 ms           |
| 100,000 | 150M            | ~15 ms            |
| 215,000 | 330M            | ~30 ms            |

Даже на целевом объёме (5 users × 5 лет) latency ~30 ms — незначительно относительно LLM-turnaround (2–30 секунд). Триггер для пере­хода на approximate search остаётся p95 > 50 ms, см. [Когда пересмотреть решение](#когда-пересмотреть-решение).

## Миграция на Embedding 2 Preview (v0.7.0)

С v0.7.0 проект переходит с `gemini-embedding-001` на `google/gemini-embedding-2-preview`. Пространства v1 и v2 **несовместимы** (cosine similarity не имеет смысла между ними), поэтому миграция требует полного re-embed всех существующих векторов — **forward-only, без возможности отката** без восстановления pre-migration дампа БД.

### Мотивация

Выбор модели и её параметров сделан не по спецификации, а на реальных данных. Ground truth — записи `reranker_logs`:

- `enriched_query` — запрос, который reranker реально отправил в vector search
- `selected_ids_json` — topic IDs, которые reranker-LLM признал релевантными **после просмотра полного контента** кандидатов (т.е. это суждение судьи, а не самого embedding-поиска)

Эта таблица даёт "natural labels" — что векторный поиск должен был найти, но не обязательно нашёл.

### Методика бенчмарка

- **Snapshot:** prod-копия БД на 2026-04-21 (5054 topics в haystack, 61 уникальный поисковый кейс, в среднем 4.3 gold topics на запрос)
- **Метрика:** Recall@k — попал ли хотя бы один gold в top-k векторного поиска. Плюс MRR (mean reciprocal rank первого gold) и mean gold similarity.
- **Скрипт:** `cmd/embed-benchmark/main.go` — запускается как `go run ./cmd/embed-benchmark --queries=100`. Source of truth — пересборка результатов при изменении enricher'а или модели.

### Результаты

| config              | R@1   | R@5   | R@20  | R@50  | MRR   | wall (5054 docs) |
|---------------------|-------|-------|-------|-------|-------|------------------|
| v1-3072 (до v0.7.0) | 0.066 | 0.295 | 0.541 | 0.754 | 0.168 | 58s              |
| v2-768-plain        | 0.082 | 0.475 | 0.639 | **0.820** | **0.252** | 23s         |
| **v2-1536-plain**   | 0.049 | 0.492 | **0.689** | 0.770 | 0.242 | 34s              |
| v2-3072-plain       | 0.066 | **0.492** | 0.639 | 0.787 | 0.246 | 41s          |
| v2-768-prefix       | 0.066 | 0.328 | 0.557 | 0.721 | 0.179 | 27s              |
| v2-1536-prefix      | 0.049 | 0.295 | 0.574 | 0.754 | 0.169 | 35s              |
| v2-3072-prefix      | 0.033 | 0.295 | 0.557 | 0.721 | 0.159 | 46s              |

### Принятые решения

#### 1. Модель — `gemini-embedding-2-preview`

v2 заметно превосходит v1 на нашей нагрузке:
- R@5: **+60% относительно** (0.295 → 0.492)
- MRR: **+46%** (0.168 → 0.246)

#### 2. Размерность — **1536**

- Лучший R@20 (0.689) — важный операционный показатель: reranker получает `candidates_limit=50`, но плотное покрытие gold внутри top-20 снижает риск потерять релевантный topic при усечении.
- Совпадает с 3072 по R@5 (0.492), экономя 50% памяти и ~15% wall-time на поиск.
- 768 тоже приемлем (всего на ~0.02 R@5 ниже), если RAM станет узким местом.

#### 3. Task prefixes — **не применяем**

Контринтуитивный результат, противоречащий [спецификации Gemini](../external/gemini/embeddings.md#task-types-with-embeddings-2), которая рекомендует формат `task: search result | query: {content}` для queries и `title: none | text: {content}` для docs.

На наших данных **это ухудшает Recall@5 с ~0.49 до ~0.30 во всех размерностях**.

Гипотеза: `enriched_query` в проде — это не естественные вопросы, а описательные keyword-листы от enricher LLM ("Антуриум, Lechuza, автополив, субстрат, Pon, хлороз..."). Паттерн `task: search result | query: ...` рассчитан на NL-формулировки и с vocabulary-based строками работает хуже, чем plain.

Если формат enricher'а когда-нибудь изменится на NL-вопросы — бенчмарк стоит перезапустить.

#### 4. Синхронный re-embed на старте

Изначально планировалась фоновая горутина для постепенной миграции векторов. Бенчмарк показал, что полный re-embed продакшен-базы (~6400 items) занимает:
- **~45 секунд** wall-time при parallelism=8
- **~$0.04** (~270K input-токенов × $0.15/M)

Это на два порядка быстрее ожиданий и делает фоновый путь избыточно сложным. В v0.7.0 при обнаружении старых векторов бот выполняет re-embed **синхронно** на старте с логом прогресса, до поднятия Telegram webhook. Однократная потеря ~45 секунд uptime в обмен на атомарность и простоту кода.

### Откат

Миграция forward-only. Если в проде обнаруживается регрессия качества, откат требует:
1. Снять pre-migration дамп БД *до* деплоя (обязательный шаг)
2. При откате — остановить сервис, восстановить дамп, задеплоить старый образ
3. Сообщения, пришедшие за окно v0.7.0 uptime, теряются

Для personal-scale бота это приемлемо, но окно надо коммуницировать явно перед деплоем.

## Принятое решение

### Решение: Observability-first подход

Вместо преждевременной оптимизации (sqlite-vec, HNSW) — добавляем метрики для мониторинга реальной нагрузки.

**Обоснование (расчёт для целевого объёма 5 users × 5 лет, dim=1536):**

1. **RAM ~1.3 GB** — в пределах домашнего сервера, при сублинейном росте на практике вероятно ~0.8 GB
2. **Latency ~30 ms** — незаметно для пользователя (LLM-turnaround 2–30 секунд)
3. **DB size ~4 GB (JSON) / ~1.3 GB (binary)** — SQLite справляется, переход на binary формат добавим при необходимости
4. **Сложность sqlite-vec** — modernc.org/sqlite не поддерживает loadable extensions, требуется реализация vtab module с нуля

### Когда пересмотреть решение

Триггеры для оптимизации (отслеживаем через метрики):

| Метрика | Порог | Действие |
|---------|-------|----------|
| `vector_search_duration_seconds` p95 | > 50 ms | Рассмотреть HNSW index |
| `vector_index_memory_bytes` | > 500 MB | Рассмотреть disk-based index |
| `vector_index_size` | > 500K | Рассмотреть approximate search |

## Виды embeddings (v0.6.0)

1. **Topics** — сжатые саммари прошлых разговоров
2. **Facts** — структурированные факты о пользователе
3. **People** — извлечённые из разговоров люди (v0.5.1)
4. **Artifacts** — семантические embedding файлов для поиска (v0.6.0)

### Embedding источники

| Тип | Текст для векторизации |
|-----|------------------------|
| Topic | `Topic Summary: {summary}\n\nConversation Log:\n{log}` |
| Fact | `{fact.Content}` |
| Person | `{DisplayName} {Username} {Aliases} {Bio}` |
| Artifact | `{summary}` (из Extractor agent) |

## Prometheus метрики

### Naming convention

Все метрики используют namespace `laplaced`:

```
laplaced_{subsystem}_{metric}_{unit}
```

### Embedding API

```go
// Время генерации embedding (OpenRouter API)
laplaced_embedding_request_duration_seconds{model, status}

// Счётчик запросов
laplaced_embedding_requests_total{model, status}

// Токены (для отслеживания использования)
laplaced_embedding_tokens_total{model}

// Стоимость (кумулятивная, USD)
laplaced_embedding_cost_usd_total{model}
```

### Vector Search

```go
// Время поиска
laplaced_vector_search_duration_seconds{type}  // type=topics|facts|people|artifacts

// Количество просканированных векторов
laplaced_vector_search_vectors_scanned{type}
```

### Vector Index State

```go
// Размер индекса (обновляется при Load/Reload)
laplaced_vector_index_size{type}  // type=topics|facts|people|artifacts

// Память (приблизительно: size × 768 × 4 bytes)
laplaced_vector_index_memory_bytes{type}
```

## Grafana Dashboard

Рекомендуемые панели:

### Cost Tracking
```promql
rate(laplaced_embedding_cost_usd_total[1h]) * 3600  # $/hour
sum(increase(laplaced_embedding_cost_usd_total[30d]))  # Monthly cost
```

### Latency Monitoring
```promql
histogram_quantile(0.95, rate(laplaced_vector_search_duration_seconds_bucket[5m]))
histogram_quantile(0.95, rate(laplaced_embedding_request_duration_seconds_bucket[5m]))
```

### Growth Projection
```promql
# Topics growth
laplaced_vector_index_size{type="topics"}
predict_linear(laplaced_vector_index_size{type="topics"}[30d], 365*24*3600)  # 1 year projection

# People growth (v0.5.1)
laplaced_vector_index_size{type="people"}
predict_linear(laplaced_vector_index_size{type="people"}[30d], 365*24*3600)

# Artifacts growth (v0.6.0)
laplaced_vector_index_size{type="artifacts"}
predict_linear(laplaced_vector_index_size{type="artifacts"}[30d], 365*24*3600)

# Total vectors (all types)
sum(laplaced_vector_index_size)
```

### Efficiency
```promql
rate(laplaced_embedding_tokens_total[1h])  # Tokens per hour
avg(laplaced_vector_search_vectors_scanned)  # Avg vectors per search
```

## Альтернативы (отложены)

### sqlite-vec / vtab module

modernc.org/sqlite (pure Go) не поддерживает loadable extensions. Варианты:

1. **Свой vtab module** — реализовать HNSW/IVF на Go, зарегистрировать через `vtab.RegisterModule()`
2. **CGo + mattn/go-sqlite3** — использовать настоящий sqlite-vec, но теряем pure Go
3. **Внешний vector store** — Qdrant, Milvus, pgvector

**Статус:** Отложено до достижения порогов в метриках.

### Binary embedding storage

Замена JSON на binary float32:
- Экономия 3x в размере БД
- Требует миграции существующих данных

**Статус:** Рассмотреть при DB size > 5 GB.

## История изменений

- **v0.7.0** — переход на `gemini-embedding-2-preview`, размерность 1536, task prefixes отключены (ухудшают retrieval на наших данных); синхронный re-embed всех существующих векторов на старте
- **v0.6.0** — добавлены Artifacts embeddings для поиска файлов. (В документе этой версии ошибочно указывалось изменение размерности на 768 — это изменение затронуло только docs, в коде и в проде всегда было 3072. Исправлено в v0.7.0.)
- **v0.5.1** — добавлены People embeddings для поиска людей в разговорах
- **v0.4.x** — начальная версия с Topics и Facts

## Связанные документы

- [People Graph](../features/people.md) — описание функционала людей (v0.5.1)
- [Artifacts System](./artifacts-system.md) — описание системы артефактов (v0.6.0)
