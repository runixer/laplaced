# <img src="assets/logo.svg" alt="" width="32"> Laplaced

[![CI](https://github.com/runixer/laplaced/actions/workflows/ci.yml/badge.svg)](https://github.com/runixer/laplaced/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/runixer/laplaced/graph/badge.svg)](https://codecov.io/gh/runixer/laplaced)
[![Go Report Card](https://goreportcard.com/badge/github.com/runixer/laplaced?v=1)](https://goreportcard.com/report/github.com/runixer/laplaced)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[English](README.md) | Русский

Умный Telegram-бот для семьи. Работает на Google Gemini через OpenRouter.

**Что умеет:**
- Общается через LLM с долгосрочной памятью (RAG)
- Распознаёт голосовые сообщения (Yandex SpeechKit)
- Понимает картинки и PDF
- Есть веб-панель со статистикой

## Быстрый старт

**Требования:** Go 1.24+, Docker (опционально)

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced

# Вариант 1: Docker
cp .env.example .env
# Отредактируй .env — впиши свои токены
docker-compose up -d --build

# Вариант 2: Локально
go run cmd/bot/main.go

# С кастомным конфигом
go run cmd/bot/main.go --config /path/to/config.yaml
```

## Конфигурация

Два способа настроить:

1. **Переменные окружения** (рекомендуется) — скопируй `.env.example` в `.env`
2. **YAML конфиг** — все опции в [`internal/config/default.yaml`](internal/config/default.yaml)

Основные переменные:
```bash
LAPLACED_TELEGRAM_TOKEN=токен_бота
LAPLACED_OPENROUTER_API_KEY=ключ_api
LAPLACED_ALLOWED_USER_IDS=123456789,987654321  # ⚠️ Обязательно! Пустой = отклонять всех
```

> **Важно:** `LAPLACED_ALLOWED_USER_IDS` должен содержать хотя бы один ID пользователя. Если список пуст, бот будет отклонять все сообщения.

## Режимы Telegram

Два режима на выбор:

- **Long Polling** (по умолчанию) — проще, работает за NAT, не нужен публичный URL
- **Webhook** — меньше задержка, лучше для прода

```bash
# Для webhook режима укажи базовый URL (путь генерируется автоматически):
LAPLACED_TELEGRAM_WEBHOOK_URL=https://your-domain.com
```

Путь и секрет webhook генерируются автоматически из токена бота. Запросы без валидного заголовка `X-Telegram-Bot-Api-Secret-Token` отклоняются.

## Голосовые сообщения

Бот распознаёт голосовые через Yandex SpeechKit. Чтобы включить:

```bash
LAPLACED_YANDEX_API_KEY=твой_ключ
LAPLACED_YANDEX_FOLDER_ID=твой_folder_id
```

Без этих ключей — голосовые игнорируются.

## Отладочный интерфейс

Есть встроенная веб-морда для отладки. **По умолчанию выключена**.

Чтобы включить:
```bash
LAPLACED_WEB_ENABLED=true
LAPLACED_WEB_PASSWORD=твой_пароль
```

Если пароль не задан, сгенерируется случайный и выведется в консоль при запуске (в логи не пишется из соображений безопасности).

Полезно для понимания как работает RAG, просмотра памяти и отладки.

**⚠️ Осторожно:** Интерфейс показывает конфиденциальные данные — историю переписки, извлечённые факты, содержимое памяти. Не выставляй наружу!

## Структура проекта

```
cmd/bot/          — точка входа
internal/bot/     — обработка сообщений
internal/rag/     — векторный поиск, память
internal/memory/  — извлечение фактов
internal/storage/ — SQLite
```

## Участие в разработке

См. [CONTRIBUTING.ru.md](CONTRIBUTING.ru.md). PR приветствуются!

## Лицензия

MIT — см. [LICENSE](LICENSE).
