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

```yaml
model: google/gemini-embedding-001
dimensions: 768
normalization: L2 normalized
```

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

Анализ production базы (user 201810803):
- Период: 165 дней (5.5 месяцев)
- Активность: 89.7 сообщений/день (очень активный пользователь)
- People добавлены в v0.5.1 (анализ на более поздних данных)
- Artifacts добавлены в v0.6.0

### Измерения

| Метрика | Значение |
|---------|----------|
| Topics | 1,031 |
| Topics/day | 6.2 |
| Facts | ~500 (оценка) |
| People | ~150 (оценка, v0.5.1) |
| Artifacts | ~100 (оценка, v0.6.0) |
| History messages | 14,801 |
| Messages/day | 89.7 |

### Размер embeddings

| Формат | Per embedding | Total (1031 topics) |
|--------|---------------|---------------------|
| В памяти (`[]float32`) | 3.1 KB | **3.2 MB** |
| В БД (JSON text) | 9.2 KB | 9.5 MB |
| Оптимально (binary) | 3.0 KB | 3.1 MB |

**С People и Artifacts (v0.6.0):**
- People: ~150 × 3.1 KB = **0.5 MB** в памяти
- Facts: ~500 × 3.1 KB = **1.5 MB** в памяти
- Artifacts: ~100 × 3.1 KB = **0.3 MB** в памяти
- Всего с People+Facts+Artifacts: **5.5 MB** в памяти

## Экстраполяция: семейный сценарий

**Целевой use case:** 5 человек × 5 лет активного использования.

### Расчёт

```
Множитель = (60 месяцев / 5.5 месяцев) × 5 пользователей = 54.5x
```

| Метрика | Сейчас (1 user, 5.5 мес) | Прогноз (5 users, 5 лет) |
|---------|--------------------------|--------------------------|
| Topics | 1,031 | ~56,000 |
| Facts | ~500 | ~27,000 |
| People | ~150 | ~8,000 |
| Artifacts | ~100 | ~5,000 |
| RAM для embeddings | 5.5 MB | **~270 MB** |
| DB size (JSON) | ~12.5 MB | ~675 MB |
| DB size (binary) | ~5.5 MB | ~280 MB |

### Latency прогноз

Brute-force search O(n × 768):

| Topics | Cosine operations | Estimated latency |
|--------|-------------------|-------------------|
| 1,000 | 768K multiplications | <0.3 ms |
| 10,000 | 7.68M multiplications | ~1.5 ms |
| 56,000 | 43M multiplications | ~8 ms |

**С People, Facts и Artifacts (v0.6.0):**
- Всего векторов: 56,000 (topics) + 27,000 (facts) + 8,000 (people) + 5,000 (artifacts) = **96,000**
- Latency: ~13 ms (всё ещё приемлемо)

## Принятое решение

### Решение: Observability-first подход

Вместо преждевременной оптимизации (sqlite-vec, HNSW) — добавляем метрики для мониторинга реальной нагрузки.

**Обоснование:**

1. **RAM ~270 MB** — приемлемо для домашнего сервера
2. **Latency ~13 ms** — незаметно для пользователя (LLM занимает 2-5 секунд)
3. **DB size ~675 MB** — не критично для SQLite
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

- **v0.6.0** — добавлены Artifacts embeddings для поиска файлов; размерность изменена на 768
- **v0.5.1** — добавлены People embeddings для поиска людей в разговорах
- **v0.4.x** — начальная версия с Topics и Facts

## Связанные документы

- [People Graph](../features/people.md) — описание функционала людей (v0.5.1)
- [Artifacts System](./artifacts-system.md) — описание системы артефактов (v0.6.0)
