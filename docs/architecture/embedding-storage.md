# Embedding Storage & Vector Search

Этот документ описывает архитектуру хранения embeddings и принятые решения по масштабированию.

## Обзор

Laplaced использует embeddings для семантического поиска по долгосрочной памяти:
- **Topics** — сжатые саммари прошлых разговоров
- **Facts** — структурированные факты о пользователе

При каждом запросе:
1. Генерируется embedding запроса через OpenRouter API
2. Вычисляется косинусное сходство со всеми векторами пользователя
3. Top-N результатов включаются в контекст LLM

## Текущая архитектура

### Модель embedding

```yaml
model: google/gemini-embedding-001
dimensions: 3072
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
```

**Формат:**
- В БД: JSON string (~39 KB на embedding)
- В памяти: `[]float32` (~12 KB на embedding)
- Overhead JSON vs binary: **3.2x**

### Vector Search (in-memory)

При старте все embeddings загружаются в RAM. Поиск — brute-force cosine similarity:

```go
// internal/rag/rag.go
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

**Сложность:** O(n × d), где n = количество векторов, d = 3072 dimensions.

## Эмпирические данные

### Источник данных

Анализ production базы (user 201810803):
- Период: 165 дней (5.5 месяцев)
- Активность: 89.7 сообщений/день (очень активный пользователь)

### Измерения

| Метрика | Значение |
|---------|----------|
| Topics | 1,031 |
| Topics/day | 6.2 |
| History messages | 14,801 |
| Messages/day | 89.7 |

### Размер embeddings

| Формат | Per embedding | Total (1031 topics) |
|--------|---------------|---------------------|
| В памяти (`[]float32`) | 12.3 KB | **12.4 MB** |
| В БД (JSON text) | 38.3 KB | 38.5 MB |
| Оптимально (binary) | 12.0 KB | 12.1 MB |

## Экстраполяция: семейный сценарий

**Целевой use case:** 5 человек × 5 лет активного использования.

### Расчёт

```
Множитель = (60 месяцев / 5.5 месяцев) × 5 пользователей = 54.5x
```

| Метрика | Сейчас (1 user, 5.5 мес) | Прогноз (5 users, 5 лет) |
|---------|--------------------------|--------------------------|
| Topics | 1,031 | ~56,000 |
| RAM для embeddings | 12.4 MB | **~675 MB** |
| DB size (JSON) | 38.5 MB | ~2.1 GB |
| DB size (binary) | 12.1 MB | ~660 MB |

### Latency прогноз

Brute-force search O(n × 3072):

| Topics | Cosine operations | Estimated latency |
|--------|-------------------|-------------------|
| 1,000 | 3M multiplications | <1 ms |
| 10,000 | 30M multiplications | ~5 ms |
| 56,000 | 172M multiplications | ~30 ms |

## Принятое решение

### Решение: Observability-first подход

Вместо преждевременной оптимизации (sqlite-vec, HNSW) — добавляем метрики для мониторинга реальной нагрузки.

**Обоснование:**

1. **RAM ~675 MB** — приемлемо для домашнего сервера
2. **Latency ~30 ms** — незаметно для пользователя (LLM занимает 2-5 секунд)
3. **DB size ~2 GB** — не критично для SQLite
4. **Сложность sqlite-vec** — modernc.org/sqlite не поддерживает loadable extensions, требуется реализация vtab module с нуля

### Когда пересмотреть решение

Триггеры для оптимизации (отслеживаем через метрики):

| Метрика | Порог | Действие |
|---------|-------|----------|
| `vector_search_duration_seconds` p95 | > 100 ms | Рассмотреть HNSW index |
| `vector_index_memory_bytes` | > 2 GB | Рассмотреть disk-based index |
| `vector_index_size` | > 500K | Рассмотреть approximate search |

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
laplaced_vector_search_duration_seconds{type}  // type=topics|facts

// Количество просканированных векторов
laplaced_vector_search_vectors_scanned{type}
```

### Vector Index State

```go
// Размер индекса (обновляется при Load/Reload)
laplaced_vector_index_size{type}  // type=topics|facts

// Память (приблизительно: size × 3072 × 4 bytes)
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
laplaced_vector_index_size{type="topics"}
predict_linear(laplaced_vector_index_size{type="topics"}[30d], 365*24*3600)  # 1 year projection
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
- Экономия 3.2x в размере БД
- Требует миграции существующих данных

**Статус:** Рассмотреть при DB size > 5 GB.

## Связанные документы

- [ROADMAP.md](../plans/ROADMAP.md) — план развития, пункт "Embedding Observability"
