# Laplaced

[![CI](https://github.com/runixer/laplaced/actions/workflows/ci.yml/badge.svg)](https://github.com/runixer/laplaced/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/runixer/laplaced/graph/badge.svg)](https://codecov.io/gh/runixer/laplaced)
[![Go Report Card](https://goreportcard.com/badge/github.com/runixer/laplaced?v=1)](https://goreportcard.com/report/github.com/runixer/laplaced)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[English](README.md) | Русский

Умный чат-бот для семьи с долгосрочной памятью. Работает в Telegram или
Mattermost и совместим с любым OpenAI-совместимым LLM API (OpenRouter, litellm,
vLLM) — построен и настроен вокруг Google Gemini.

**Что умеет:**
- Общается с долгосрочной памятью — помнит прошлые разговоры через RAG (векторный поиск по саммари тем, фактам и графу людей)
- Понимает голосовые сообщения нативно (Gemini multimodal), а также картинки, PDF и видеокружочки
- Хранит файлы как память — присланные файлы становятся «артефактами», которые бот может найти и перечитать спустя недели
- Генерирует и редактирует изображения по запросу («нарисуй кота-самурая», «сделай это фото в сепии»)
- Ставит эмодзи-реакцию, когда это уместно, и отдаёт ответ потоково с живой «лентой размышлений»
- Имеет веб-панель для просмотра памяти, агентов и трейсов

## Быстрый старт

### Docker (рекомендуется)

```bash
# Создаём конфиг
mkdir -p data
cat > .env << 'EOF'
LAPLACED_TELEGRAM_TOKEN=токен_бота
LAPLACED_LLM_API_KEY=ключ_api
LAPLACED_ALLOWED_USER_IDS=123456789
EOF

# Запускаем
docker run -d --name laplaced \
  --env-file .env \
  -v $(pwd)/data:/data \
  ghcr.io/runixer/laplaced:latest
```

### Docker Compose

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced
cp .env.example .env
# Отредактируй .env — впиши свои токены
docker-compose up -d
```

### Из исходников

**Требования:** Go 1.25+

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced
go run cmd/bot/main.go
```

## Конфигурация

Настройка через переменные окружения (рекомендуется) или YAML-конфиг. Дефолты
лежат в [`internal/config/default.yaml`](internal/config/default.yaml); у каждого
поля есть соответствующая переменная `LAPLACED_*`.

**Обязательные переменные:**
```bash
LAPLACED_TELEGRAM_TOKEN=токен_бота
LAPLACED_LLM_API_KEY=ключ_api
LAPLACED_ALLOWED_USER_IDS=123456789,987654321  # ⚠️ Обязательно! Пустой = отклонять всех
```

Полный сгруппированный список опций см. в [`.env.example`](.env.example).

> **Важно:** `LAPLACED_ALLOWED_USER_IDS` должен содержать хотя бы один ID. Если список пуст, бот отклоняет все сообщения.

Секреты можно задавать напрямую, через переменные окружения или подтягивать из
HashiCorp Vault inline-ссылками вида `vault:secret/path#key` — см.
[docs/architecture/vault-secrets.md](docs/architecture/vault-secrets.md).

## Транспорты

Бот общается с пользователями через абстракцию транспорта; выбор — через
`LAPLACED_TRANSPORT`:

- **`telegram`** (по умолчанию) — long polling (работает за NAT) или webhook (меньше задержка):
  ```bash
  LAPLACED_TELEGRAM_WEBHOOK_URL=https://your-domain.com   # режим webhook
  ```
- **`mattermost`** — работает на Mattermost-совместимом сервере (например, мессенджер
  Time) по REST + WebSocket. Доступ — фиксированный список ID или гейтинг по
  корпоративному SSO. См. [docs/architecture/transports.md](docs/architecture/transports.md).

## LLM-бэкенд

По умолчанию — публичный OpenRouter API, но `LAPLACED_LLM_BASE_URL` направляет
клиента на любой OpenAI-совместимый эндпоинт (litellm, vLLM, self-hosted шлюз).
`LAPLACED_LLM_IMAGE_INPUT_FORMAT` переключает кодирование медиа между форматом
OpenRouter/Gemini (`file`) и стандартными частями OpenAI (`openai`).

## Хранилище

- **База данных:** по умолчанию SQLite (чистый Go, без CGO). Поставь
  `LAPLACED_DATABASE_DRIVER=postgres` и `LAPLACED_DATABASE_*` для PostgreSQL.
  Один и тот же код репозиториев работает на обоих через слой диалектов.
- **Файлы (артефакты):** по умолчанию локальный диск; настрой `artifacts.s3.*`
  (`LAPLACED_ARTIFACTS_S3_*`), чтобы хранить блобы в S3-совместимом бакете,
  например в Yandex Object Storage.

## Веб-панель

Веб-панель работает на порту `9081` (настраивается через `LAPLACED_SERVER_PORT`),
под HTTP Basic Auth (включена по умолчанию). Если пароль не задан, он
генерируется и печатается в логах при старте.

```bash
LAPLACED_AUTH_USERNAME=admin
LAPLACED_AUTH_PASSWORD=твой_пароль   # пусто = сгенерировать автоматически
```

Панель даёт просмотр запросов/ответов LLM по каждому агенту, память (факты, темы,
людей, артефакты) и трейсы OpenTelemetry.

**⚠️ Осторожно:** Показывает конфиденциальные данные. Не выставляй наружу!

## Наблюдаемость

Опциональная трассировка OpenTelemetry покрывает каждый ход — вызовы LLM,
эмбеддинги, RAG-поиск, реранкинг, выполнение инструментов и генерацию
изображений — с сигналами аномалий на спанах. По умолчанию выключена; включается
через `LAPLACED_TELEMETRY_ENABLED=true` с указанием OTLP-коллектора в
`LAPLACED_TELEMETRY_OTLP_ENDPOINT`. Также экспортируются метрики Prometheus. См.
[docs/architecture/observability.md](docs/architecture/observability.md).

## Архитектура

```
cmd/bot/          — точка входа, связывание зависимостей
internal/
  agent/          — LLM-агенты (chat, reranker, enricher, splitter, merger,
                    archivist, extractor, reactor, imagegen)
  bot/            — обработка сообщений, транспорты, стриминг, инструменты
  rag/            — векторный поиск, выборка памяти, сборка контекста
  memory/         — извлечение фактов и людей
  storage/        — репозитории SQLite/PostgreSQL (слой диалектов)
  files/          — блоб-хранилище артефактов (диск / S3)
  llm/            — OpenAI-совместимый LLM-клиент
  telegram/       — клиент Telegram API
  mattermost/     — клиент Mattermost/Time (REST + WebSocket)
  obs/            — трассировка OpenTelemetry
  secrets/        — разрешение секретов из HashiCorp Vault
  web/, ui/       — HTTP-сервер веб-панели
  i18n/, markdown/ — локализация и рендеринг Markdown
```

Подробная документация в [docs/architecture/](docs/architecture/).

## Участие в разработке

См. [CONTRIBUTING.ru.md](CONTRIBUTING.ru.md). PR приветствуются!

## Лицензия

MIT — см. [LICENSE](LICENSE).
