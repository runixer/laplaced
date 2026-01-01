# Laplaced

[![Go Report Card](https://goreportcard.com/badge/github.com/runixer/laplaced?v=1)](https://goreportcard.com/report/github.com/runixer/laplaced)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

English | [Русский](README.ru.md)

A smart Telegram bot for family use. Powered by Google Gemini via OpenRouter.

**What it does:**
- Chats using LLM with long-term memory (RAG)
- Transcribes voice messages (Yandex SpeechKit)
- Understands images and PDFs
- Has a web dashboard for stats

## Quick Start

**Requirements:** Go 1.24+, Docker (optional)

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced

# Option 1: Docker
cp .env.example .env
# Edit .env with your tokens
docker-compose up -d --build

# Option 2: Local
go run cmd/bot/main.go

# With custom config file
go run cmd/bot/main.go --config /path/to/config.yaml
```

## Configuration

Two ways to configure:

1. **Environment variables** (recommended) — copy `.env.example` to `.env`
2. **YAML config** — see [`internal/config/default.yaml`](internal/config/default.yaml) for all options

Key variables:
```bash
LAPLACED_TELEGRAM_TOKEN=your_bot_token
LAPLACED_OPENROUTER_API_KEY=your_api_key
LAPLACED_ALLOWED_USER_IDS=123456789,987654321  # ⚠️ Required! Empty = reject all
```

> **Note:** `LAPLACED_ALLOWED_USER_IDS` must contain at least one user ID. If empty, the bot will reject all messages.

## Telegram Modes

Two modes available:

- **Long Polling** (default) — simpler, works behind NAT, no public URL needed
- **Webhook** — lower latency, better for production

```bash
# For webhook mode, set base URL (path is auto-generated):
LAPLACED_TELEGRAM_WEBHOOK_URL=https://your-domain.com
```

Webhook path and secret are automatically derived from the bot token. Requests without a valid `X-Telegram-Bot-Api-Secret-Token` header are rejected.

## Voice Messages

The bot transcribes voice messages using Yandex SpeechKit. To enable:

```bash
LAPLACED_YANDEX_API_KEY=your_key
LAPLACED_YANDEX_FOLDER_ID=your_folder_id
```

Without these — voice messages are ignored.

## Debug Interface

There's a built-in web UI for debugging. **Disabled by default**.

To enable:
```bash
LAPLACED_WEB_ENABLED=true
LAPLACED_WEB_PASSWORD=your_password
```

If password is not set, a random one will be generated and printed to console at startup (not logged to files/aggregators for security).

Useful for understanding how RAG works, inspecting memory, and debugging.

**⚠️ Warning:** The interface exposes sensitive data — conversation history, extracted facts, memory contents. Don't expose it publicly.

## Project Structure

```
cmd/bot/          — entry point
internal/bot/     — message handling
internal/rag/     — vector search, memory retrieval
internal/memory/  — facts extraction
internal/storage/ — SQLite
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). PRs welcome!

## License

MIT — see [LICENSE](LICENSE).
