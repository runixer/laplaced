# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Laplaced is a Telegram bot written in Go, powered by Google Gemini via OpenRouter. It features:
- Long-term memory using RAG (Retrieval-Augmented Generation)
- Voice recognition via Yandex SpeechKit
- Image/PDF analysis
- Web dashboard for statistics

## Build & Run Commands

```bash
# Run bot locally
go run cmd/bot/main.go --config configs/config.yaml

# Run with Docker
docker-compose up -d --build

# Run tests
go test ./...

# Format code
go fmt ./...

# Build binary
go build -o laplaced cmd/bot/main.go
```

## Architecture

```
cmd/bot/main.go          # Entry point, dependency wiring
internal/
  bot/                   # Core bot logic, message handlers, Telegram updates processing
  config/                # Configuration loading (YAML + env vars)
  storage/               # SQLite repository layer (pure Go, no CGO)
  rag/                   # Vector search, topic retrieval, context building
  memory/                # Facts extraction, topic processing, long-term memory
  openrouter/            # LLM client (Gemini/OpenRouter API)
  telegram/              # Telegram API client wrapper
  yandex/                # Yandex SpeechKit client for voice
  web/                   # HTTP server for dashboard and webhooks
  i18n/                  # Localization (en/ru)
  markdown/              # Markdown processing
```

### Key Patterns

- **Strict Dependency Injection**: All services receive dependencies via constructors (`NewBot`, `NewService`). No global state.
- **Structured logging**: Use `*slog.Logger` passed via DI. Always use structured fields: `logger.Info("msg", "key", value)`.
- **Context propagation**: Pass `context.Context` to all I/O, DB, and LLM operations.
- **Error wrapping**: Always wrap errors with context: `fmt.Errorf("failed to X: %w", err)`.

### Data Flow

1. Telegram update → `bot.ProcessUpdate()`
2. Message grouping (waits for user to finish typing)
3. RAG retrieval: vector search for relevant topics/facts
4. Context assembly: system prompt + profile facts + RAG results + session messages
5. LLM generation via OpenRouter
6. Response sent to user
7. Session archival → topic creation → facts extraction (async)

### Memory System

- **Short-term**: Recent messages in active session
- **Long-term**: Topics (conversation summaries) and structured facts stored in SQLite with vector embeddings
- **Profile**: Up to 50 facts about the user, always included in context
- **RAG**: Vector similarity search retrieves relevant past discussions

## Configuration

Config loaded from `configs/config.yaml`, overridable via environment variables prefixed with `LAPLACED_`.

Key env vars:
- `LAPLACED_TELEGRAM_TOKEN` - Bot token
- `LAPLACED_OPENROUTER_API_KEY` - LLM API key
- `LAPLACED_ALLOWED_USER_IDS` - Comma-separated Telegram user IDs

## Language

Default language is configurable via `bot.language` in config. Supported: `en`, `ru`.
Translation files in `locales/` directory.

## CI/CD

GitHub Actions workflow (`.github/workflows/ci.yml`):
- **On push to main**: lint, test, build binaries (amd64/arm64), build & push Docker image
- **On tag v***: all above + create GitHub Release with binaries

Docker image: `ghcr.io/runixer/laplaced:latest`

```bash
# Check CI status
gh run list

# Create release
git tag v0.1.x && git push --tags
```

@.claude/deploy.md
