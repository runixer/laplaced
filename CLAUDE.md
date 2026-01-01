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

## Testing

### Running Tests

```bash
go test ./...                    # All tests
go test ./internal/bot/... -v    # Specific package with verbose output
```

### Test Style

- **Framework**: `testify/assert` for assertions, `testify/mock` for mocks
- **Pattern**: Table-driven tests with subtests (`t.Run`)
- **Location**: `*_test.go` files alongside source code

### Available Test Helpers (`internal/bot/bot_test.go`)

- `createTestTranslator(t)` — creates `*i18n.Translator` with test translations
- `MockBotAPI` — mock for Telegram API
- `MockStorage` — mock for all storage repositories
- `MockOpenRouterClient` — mock for LLM client
- `MockFileDownloader` — mock for file downloads
- `MockYandexClient` — mock for speech recognition

### Writing Tests for New Functions

1. For pure functions (no dependencies): test directly with table-driven tests
2. For methods with dependencies: use existing mocks from `bot_test.go`
3. Add translations to `createTestTranslator` if function uses `translator.Get()`

Example structure:
```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    InputType
        expected OutputType
    }{
        {"case 1", input1, expected1},
        {"case 2", input2, expected2},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := myFunction(tt.input)
            assert.Equal(t, tt.expected, result)
        })
    }
}
```

## Configuration

Config loaded from `configs/config.yaml`, overridable via environment variables prefixed with `LAPLACED_`.

Key env vars:
- `LAPLACED_TELEGRAM_TOKEN` - Bot token
- `LAPLACED_OPENROUTER_API_KEY` - LLM API key
- `LAPLACED_ALLOWED_USER_IDS` - Comma-separated Telegram user IDs

## Language

Default language is configurable via `bot.language` in config. Supported: `en`, `ru`.
Translation files in `locales/` directory.

## Git Workflow

### When to Commit

After completing a task (feature, refactoring, bug fix), **always offer to commit**. Don't wait for the user to ask.

### Commit Style

- **Format**: Short imperative subject, optional body with details
- **Subject**: Start with verb (Add, Fix, Refactor, Update, Remove)
- **Length**: Subject ≤50 chars, body lines ≤72 chars

```bash
# Single-line for simple changes
git commit -m "Fix typo in error message"

# Multi-line for complex changes
git commit -m "$(cat <<'EOF'
Refactor buildContext: extract helper methods

- Extract formatCoreIdentityFacts and deduplicateTopics
- Reduce cyclomatic complexity from 46 to 34
- Add tests for extracted methods
EOF
)"
```

### Pre-commit Hook

Project has `golangci-lint` pre-commit hook. If commit fails, fix lint errors and retry.

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
