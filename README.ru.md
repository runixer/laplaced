# Laplaced

[![CI](https://github.com/runixer/laplaced/actions/workflows/ci.yml/badge.svg)](https://github.com/runixer/laplaced/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/runixer/laplaced/graph/badge.svg)](https://codecov.io/gh/runixer/laplaced)
[![Go Report Card](https://goreportcard.com/badge/github.com/runixer/laplaced?v=1)](https://goreportcard.com/report/github.com/runixer/laplaced)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[English](README.md) | Русский

Умный Telegram-бот для семьи. Работает на Google Gemini через OpenRouter.

**Что умеет:**
- Общается через LLM с долгосрочной памятью (RAG)
- Понимает голосовые сообщения нативно (Gemini multimodal)
- Понимает картинки и PDF
- Есть веб-панель для отладки

## Быстрый старт

### Docker (рекомендуется)

```bash
# Создаём конфиг
mkdir -p data
cat > .env << 'EOF'
LAPLACED_TELEGRAM_TOKEN=токен_бота
LAPLACED_OPENROUTER_API_KEY=ключ_api
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

**Требования:** Go 1.24+

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced
go run cmd/bot/main.go
```

## Конфигурация

Настройка через переменные окружения (рекомендуется) или YAML конфиг.

**Обязательные переменные:**
```bash
LAPLACED_TELEGRAM_TOKEN=токен_бота
LAPLACED_OPENROUTER_API_KEY=ключ_api
LAPLACED_ALLOWED_USER_IDS=123456789,987654321  # ⚠️ Обязательно! Пустой = отклонять всех
```

Все опции см. в [`.env.example`](.env.example).

> **Важно:** `LAPLACED_ALLOWED_USER_IDS` должен содержать хотя бы один ID пользователя. Если список пуст, бот будет отклонять все сообщения.

## Режимы Telegram

- **Long Polling** (по умолчанию) — проще, работает за NAT
- **Webhook** — меньше задержка, лучше для прода

```bash
# Для webhook режима:
LAPLACED_TELEGRAM_WEBHOOK_URL=https://your-domain.com
```

## Отладочный интерфейс

Встроенная веб-морда для отладки. **По умолчанию выключена**.

```bash
LAPLACED_WEB_ENABLED=true
LAPLACED_WEB_PASSWORD=твой_пароль
```

Полезно для понимания как работает RAG и просмотра памяти.

**⚠️ Осторожно:** Показывает конфиденциальные данные. Не выставляй наружу!

## Архитектура

```
cmd/bot/          — точка входа
internal/bot/     — обработка сообщений
internal/rag/     — векторный поиск, память
internal/memory/  — извлечение фактов
internal/storage/ — SQLite
```

Подробная документация в [docs/architecture/](docs/architecture/).

## Участие в разработке

См. [CONTRIBUTING.ru.md](CONTRIBUTING.ru.md). PR приветствуются!

## Лицензия

MIT — см. [LICENSE](LICENSE).
