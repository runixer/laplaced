# Laplaced

[![CI](https://github.com/runixer/laplaced/actions/workflows/ci.yml/badge.svg)](https://github.com/runixer/laplaced/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/runixer/laplaced/graph/badge.svg)](https://codecov.io/gh/runixer/laplaced)
[![Go Report Card](https://goreportcard.com/badge/github.com/runixer/laplaced?v=1)](https://goreportcard.com/report/github.com/runixer/laplaced)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

English | [Русский](README.ru.md)

A smart Telegram bot for family use. Powered by Google Gemini via OpenRouter.

**What it does:**
- Chats using LLM with long-term memory (RAG)
- Understands voice messages natively (Gemini multimodal)
- Understands images and PDFs
- Has a web dashboard for debugging

## Quick Start

### Docker (recommended)

```bash
# Create config
mkdir -p data
cat > .env << 'EOF'
LAPLACED_TELEGRAM_TOKEN=your_bot_token
LAPLACED_OPENROUTER_API_KEY=your_api_key
LAPLACED_ALLOWED_USER_IDS=123456789
EOF

# Run
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
# Edit .env with your tokens
docker-compose up -d
```

### From Source

**Requirements:** Go 1.24+

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced
go run cmd/bot/main.go
```

## Configuration

Configure via environment variables (recommended) or YAML config.

**Required variables:**
```bash
LAPLACED_TELEGRAM_TOKEN=your_bot_token
LAPLACED_OPENROUTER_API_KEY=your_api_key
LAPLACED_ALLOWED_USER_IDS=123456789,987654321  # ⚠️ Required! Empty = reject all
```

See [`.env.example`](.env.example) for all options.

> **Note:** `LAPLACED_ALLOWED_USER_IDS` must contain at least one user ID. If empty, the bot will reject all messages.

## Telegram Modes

- **Long Polling** (default) — simpler, works behind NAT
- **Webhook** — lower latency, better for production

```bash
# For webhook mode:
LAPLACED_TELEGRAM_WEBHOOK_URL=https://your-domain.com
```

## Debug Interface

Built-in web UI for debugging. **Disabled by default**.

```bash
LAPLACED_WEB_ENABLED=true
LAPLACED_WEB_PASSWORD=your_password
```

Useful for understanding how RAG works and inspecting memory.

**⚠️ Warning:** Exposes sensitive data. Don't expose publicly.

## Architecture

```
cmd/bot/          — entry point
internal/bot/     — message handling
internal/rag/     — vector search, memory retrieval
internal/memory/  — facts extraction
internal/storage/ — SQLite
```

See [docs/architecture/](docs/architecture/) for detailed documentation (in Russian).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). PRs welcome!

## License

MIT — see [LICENSE](LICENSE).
