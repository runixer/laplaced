# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Laplaced is a Telegram bot written in Go, powered by Google Gemini via OpenRouter. It features:
- Long-term memory using RAG (Retrieval-Augmented Generation)
- Native voice message understanding (Gemini multimodal)
- Image/PDF analysis
- Web dashboard for statistics and debugging

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
  agent/                 # Unified agent interface and types
    archivist/           # Fact extraction from conversations
    enricher/            # Query enrichment for RAG
    laplace/             # Main chat agent with tool calls
    merger/              # Topic merge verification
    reranker/            # Topic relevance reranking
    splitter/            # Topic extraction from conversations
    prompts/             # Shared prompt parameter types
  bot/                   # Core bot logic, message handlers, Telegram updates processing
  config/                # Configuration loading (YAML + env vars)
  storage/               # SQLite repository layer (pure Go, no CGO)
  rag/                   # Vector search, topic retrieval, context building
  memory/                # Facts extraction, topic processing, long-term memory
  openrouter/            # LLM client (Gemini/OpenRouter API)
  telegram/              # Telegram API client wrapper
  testutil/              # Centralized test mocks, fixtures, and helpers
  yandex/                # Yandex SpeechKit client (legacy, unused)
  web/                   # HTTP server for dashboard and webhooks
  i18n/                  # Localization (en/ru)
  markdown/              # Markdown processing
  agentlog/              # Agent execution logging
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

### Agent Architecture

All LLM-powered operations use agents from `internal/agent/`. Each agent implements `agent.Agent`:

```go
type Agent interface {
    Type() AgentType
    Execute(ctx context.Context, req *Request) (*Response, error)
}
```

**Agent Types:**
- **Laplace** (`agent/laplace/`): Main chat agent with tool calls (search_history, search_web, manage_memory)
- **Reranker** (`agent/reranker/`): Selects most relevant topics from vector search candidates using tool calls
- **Enricher** (`agent/enricher/`): Expands user queries with context for better RAG retrieval
- **Splitter** (`agent/splitter/`): Extracts topic summaries from conversation chunks
- **Merger** (`agent/merger/`): Verifies if two topics should be merged
- **Archivist** (`agent/archivist/`): Extracts and manages user facts from conversations

**Agent Wiring (in `cmd/bot/main.go`):**
```go
ragService.SetEnricherAgent(enricherAgent)
ragService.SetSplitterAgent(splitterAgent)
ragService.SetRerankerAgent(rerankerAgent)
ragService.SetMergerAgent(mergerAgent)
memoryService.SetArchivistAgent(archivistAgent)
```

**SharedContext**: Agents receive user profile and recent topics via `agent.SharedContext` to avoid redundant DB queries.

### Critical Data Invariants (MUST READ!)

**User Data Isolation:**
- Message IDs (`history.id`) are **GLOBAL auto-increment**, NOT per-user
- Topic IDs, Fact IDs are also global
- **ANY query using ID ranges MUST include `user_id` filter!**

```sql
-- WRONG: captures messages from other users!
UPDATE history SET topic_id = ? WHERE id >= ? AND id <= ?

-- CORRECT: always filter by user_id
UPDATE history SET topic_id = ? WHERE user_id = ? AND id >= ? AND id <= ?
```

**Why this matters:**
- Multiple users send messages concurrently
- Their message IDs interleave: user1 gets ID 100, user2 gets ID 101, user1 gets ID 102
- A range query `WHERE id BETWEEN 100 AND 102` captures BOTH users' data
- This causes catastrophic data leakage between users

**Checklist for any storage function using ID ranges:**
1. Does it have a `user_id` parameter?
2. Does the SQL WHERE clause include `AND user_id = ?`?
3. If operating on topics/facts, does it verify ownership?

### Personal Data in Code (IMPORTANT!)

**NEVER put real personal data in tracked files.**

This bot processes real conversations. When analyzing logs, debugging, writing examples, or documenting bugs — always depersonalize before putting into tracked files.

**Gitignored directories (real data OK here):**
- `logs/` — runtime logs with real conversations
- `data/` — SQLite databases with user data
- `docs/plans/` — internal planning documents
- `.claude/` — Claude Code working files

**Tracked files (depersonalize!):**
- `*_test.go` — use `@testuser`, `John Doe`, generic topics
- `*.yaml` prompts — use `Mary`, `Alice`, `@johndoe` in examples
- `*.md` documentation — describe bugs abstractly, no real excerpts
- Any file that goes to GitHub

**Depersonalization checklist:**
1. Replace real names → `John`, `Mary`, `Alice`, `Bob`
2. Replace real handles → `@testuser`, `@johndoe`, `@news_channel`
3. Replace conversation excerpts → describe abstractly ("user asked about X")
4. Replace personal details → generic equivalents ("hobby discussion", "family topic")

### Terminology (IMPORTANT!)

**Session / Active Session:**
- Messages with `topic_id IS NULL` (not yet processed into topics)
- Becomes a topic after inactivity timeout (default 1h, configurable via `rag.chunk_interval`)
- NOT "user typing" or "waiting for response" — just unprocessed messages

**Topic:**
- Compressed summary of a conversation chunk
- Created when session is archived (after inactivity timeout or force-close)
- Has vector embedding for RAG retrieval

**Message Grouping:**
- Separate concept! Waits for user to finish typing (few seconds)
- Groups rapid messages into one processing batch
- NOT the same as session archival

## Architectural Decisions

Before implementing significant changes (especially when modifying function contracts, adding dependencies, or when a solution feels like a "hack"):

1. **Stop and describe the problem** — what exactly are we trying to solve?
2. **Propose alternatives** — at least 2-3 approaches with trade-offs
3. **Ask for confirmation** — even if the user says "just do it"
4. **If it's a hack — say so explicitly** BEFORE writing code

**Signs you should stop and discuss:**
- A function starts returning data "for someone else to use later"
- Single responsibility is being violated
- Solution contradicts best practices of the tool (Prometheus, Go, OpenTelemetry, etc.)
- Changes touch 3+ files for one "simple" feature
- You're working around a limitation instead of solving the root cause

**Example:** Instead of silently implementing "record all metrics atomically at the end of processing" (which violates Prometheus best practices), stop and say: "This is a hack. The real problem is X. Alternatives: (1) OpenTelemetry for correlated tracing, (2) increase scrape interval, (3) live with [6h] smoothing window."

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

### Available Test Helpers (`internal/testutil/`)

All test mocks are centralized in `internal/testutil/` package:

**Mocks (`mocks.go`):**
- `MockBotAPI` — mock for Telegram API
- `MockStorage` — mock for all storage repositories (implements all repo interfaces)
- `MockOpenRouterClient` — mock for LLM client
- `MockFileDownloader` — mock for file downloads
- `MockYandexClient` — mock for speech recognition
- `MockVectorSearcher` — mock for vector search interface

**Fixtures (`fixtures.go`):**
- `TestUser()`, `TestUsers()` — sample users
- `TestFacts()` — sample facts
- `TestTopic()`, `TestTopics()` — sample topics
- `TestMessages()` — sample conversation messages

**Helpers (`helpers.go`):**
- `TestLogger()` — discarding logger for tests
- `TestConfig()` — default test configuration
- `TestTranslator(t)` — translator with test locale files
- `Ptr[T](v)` — generic helper to create pointer from value

### Writing Tests for New Functions

1. For pure functions (no dependencies): test directly with table-driven tests
2. For methods with dependencies: use mocks from `internal/testutil/`
3. Add translations to test locale files if function uses `translator.Get()`

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

## Refactoring & Cyclomatic Complexity

### Checking Complexity

```bash
~/go/bin/gocyclo -top 10 .    # Top 10 most complex functions
```

### When to Refactor

**Good candidates for extraction:**
- Pure functions with no dependencies (easiest to test)
- Code reused in multiple places
- Isolated logic blocks with clear input/output
- Deeply nested conditionals that can be flattened

**Don't refactor just for metrics:**
- Linear pipelines (sequential steps) are often clearer as one function
- Functions used exactly once may not benefit from extraction
- High complexity from many `if err != nil` checks is normal in Go

### Decision Framework

1. **Analyze the function** — Is it truly complex or just long?
2. **Identify extraction candidates** — Look for reusable or isolatable blocks
3. **Consider readability** — Will splitting improve or fragment understanding?
4. **Prefer partial refactoring** — Extract 2-3 helpers, not 7 tiny functions
5. **Always add tests** — Extracted functions must have table-driven tests

### Example Assessment

```
Before: buildContext (complexity 46)
Analysis: Linear pipeline, but has 2 clear extractable blocks
Action: Extract formatCoreIdentityFacts + deduplicateTopics
After: buildContext (34), two tested helpers
Result: Better than splitting into 7 single-use functions
```

## Configuration

Config loaded from `configs/config.yaml`, overridable via environment variables prefixed with `LAPLACED_`.

Key env vars:
- `LAPLACED_TELEGRAM_TOKEN` - Bot token
- `LAPLACED_OPENROUTER_API_KEY` - LLM API key
- `LAPLACED_ALLOWED_USER_IDS` - Comma-separated Telegram user IDs

## Language & Prompts

Default language is configurable via `bot.language` in config. Supported: `en`, `ru`.

Translation and prompt files: `internal/i18n/locales/{en,ru}.yaml`

Key prompt sections in locale files:
- `bot.system_prompt` — main LLM system prompt
- `bot.voice_instruction` — instructions for voice message handling
- `memory.system_prompt` — fact extraction (archivist) prompt
- `memory.consolidation_prompt` — deduplication prompt
- `rag.*` — topic extraction and enrichment prompts

## Git Workflow

### When to Commit

After completing a task (feature, refactoring, bug fix), **always offer to commit**. Don't wait for the user to ask.

**Before committing, check:**
1. Does this change need a CHANGELOG entry? (see [Changelog](#changelog) section)
2. Are there any security fixes? → Add to `### Security`
3. New features or config? → Add to `### Added`
4. Bug fixes? → Add to `### Fixed`

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

## Changelog

This project uses [Keep a Changelog](https://keepachangelog.com/) format.

### Philosophy

**Changelog is for users, not developers.** Write what changed from user's perspective, not implementation details.

**Good:**
```markdown
- Per-message latency breakdown — track LLM, tools, Telegram timing separately
- Voice messages now use native Gemini audio understanding
```

**Bad (too technical):**
```markdown
- `laplaced_bot_message_llm_duration_seconds{user_id}` — total LLM time per message
- Added `job_type` label with values "interactive" and "background"
```

### What to Include

| Include | Skip |
|---------|------|
| New features users can use | Internal refactoring |
| Bug fixes that affected users | Code cleanup |
| Security fixes | Metric/label names |
| Breaking changes | Test improvements |
| Config option changes | Minor dependency updates |

### Workflow

1. Add significant changes to `[Unreleased]` during development
2. Skip trivial fixes — batch them into "Various bug fixes" if needed
3. When releasing, move `[Unreleased]` to new version section
4. Update comparison links at the bottom

### Release Process

**IMPORTANT: Before any release, remind the user to test on dev environment!**

1. **Test on dev** (see [Pre-release Testing](#pre-release-testing) below)
2. Update CHANGELOG.md: move `[Unreleased]` to new version
3. Commit with exact prefix: `git commit -m "Release vX.Y.Z"` (**MUST start with "Release v"** — CI skips these commits)
4. Tag and push: `git tag vX.Y.Z && git push && git push --tags`
5. Wait for release.yml to complete (CI is skipped for release commits)
6. Deploy via Gitea MCP (see `.claude/deploy.md`)

CI automatically extracts release notes from CHANGELOG.md for GitHub Releases.

## Pre-release Testing

**Always test changes on dev before release.** Ask the user to test and suggest specific test cases.

### Dev Environment

- Run locally: `go run cmd/bot/main.go`
- Logs: save to `logs/` directory for review

### Test Cases by Change Type

**Prompt changes (i18n/locales/*.yaml):**
- Voice messages: send voice, check transcription quote format (`> 🎤`)
- Forwarded voice: forward voice from another user, check attribution
- Memory extraction: chat, then force-process session, verify facts in DB
- RAG: ask about past conversations, verify context retrieval

**Bot logic changes (internal/bot/):**
- Message grouping: send multiple messages quickly
- Image/PDF: send files for analysis
- Error handling: test edge cases (empty messages, large files)

**Memory/RAG changes:**
- Fact extraction: verify new facts appear correctly
- Deduplication: check similar facts aren't duplicated
- Topic creation: verify session archival works

**Metrics changes:**
- Check Grafana dashboard after test messages
- Verify new metrics appear with correct labels

### Suggesting Test Cases

When preparing a release, proactively suggest test cases to the user:
```
Before release, please test on dev:
1. [specific test based on changes]
2. [another test]
...

Run the bot locally and verify. I'll wait.
```

## CI/CD

GitHub Actions workflows:

**`.github/workflows/ci.yml`** — runs on push to main and PRs:
- lint (golangci-lint)
- test (with coverage)
- **Skipped for commits starting with "Release v"** (to avoid duplicate runs with release.yml)

**`.github/workflows/release.yml`** — runs on tag push (`v*`):
- lint, test
- build binaries (linux/amd64, linux/arm64)
- build & push multi-arch Docker image to GHCR
- create GitHub Release with binaries and changelog

Docker image: `ghcr.io/runixer/laplaced:latest`

```bash
# Check CI status
gh run list

# Watch specific run
gh run watch <run_id> --exit-status
```

## Related Documentation

- @.claude/deploy.md — Production deployment via Gitea
