# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Laplaced is a Telegram bot written in Go, powered by Google Gemini via any OpenAI-compatible LLM API (OpenRouter, litellm, vLLM). It features:
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
    extractor/           # File metadata extraction (artifacts system)
    laplace/             # Main chat agent with tool calls
    merger/              # Topic merge verification
    reranker/            # Topic relevance reranking (with artifacts)
    splitter/            # Topic extraction from conversations
    prompts/             # Shared prompt parameter types
  bot/                   # Core bot logic, message handlers, Telegram updates processing
  config/                # Configuration loading (YAML + env vars)
  files/                 # File storage and processing utilities
  storage/               # SQLite repository layer (pure Go, no CGO)
  rag/                   # Vector search, topic/artifact retrieval, context building
  memory/                # Facts extraction, topic processing, long-term memory
  llm/                   # LLM client (OpenAI-compatible API: OpenRouter, litellm, vLLM)
  telegram/              # Telegram API client wrapper
  testutil/              # Centralized test mocks, fixtures, and helpers
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
- **Centralized formatting**: When multiple agents need similar data formatting (people, facts, topics), use a single function in `internal/storage/format.go` with tag/mode parameter, not N separate functions. Example: `FormatPeople(people []Person, tag string)` with constants `TagInnerCircle`, `TagRelevantPeople`, `TagPeople`. This avoids format drift where different agents format the same data differently.

### Data Flow

1. Telegram update → `bot.ProcessUpdate()`
2. Message grouping (waits for user to finish typing)
3. File handling: save artifacts (images, voice, PDF, documents) with deduplication
4. RAG retrieval: vector search for relevant topics/facts/artifacts
5. Context assembly: system prompt + profile facts + RAG results + session messages (+ artifact content)
6. LLM generation via the configured LLM API
7. Response sent to user
8. Background: Extractor agent processes pending artifacts (summary, keywords, entities)
9. Session archival → topic creation → facts extraction (async)

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
- **Reranker** (`agent/reranker/`): Selects most relevant topics, people, and artifacts from vector search candidates using tool calls
- **Enricher** (`agent/enricher/`): Expands user queries with context for better RAG retrieval
- **Splitter** (`agent/splitter/`): Extracts topic summaries from conversation chunks
- **Merger** (`agent/merger/`): Verifies if two topics should be merged
- **Archivist** (`agent/archivist/`): Extracts and manages user facts from conversations
- **Extractor** (`agent/extractor/`): Processes artifact files to extract metadata (summary, keywords, entities, rag_hints)

**Agent Wiring (in `cmd/bot/main.go`):**
```go
ragService.SetEnricherAgent(enricherAgent)
ragService.SetSplitterAgent(splitterAgent)
ragService.SetRerankerAgent(rerankerAgent)
ragService.SetMergerAgent(mergerAgent)
memoryService.SetArchivistAgent(archivistAgent)
ragService.SetExtractorAgent(extractorAgent)
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

### Config-driven data shape: find-all-callers rule (MUST READ!)

When you add a config field that changes the **shape** of data the system produces or stores (embedding dimension, encoding version, serialization format, etc.), the field must be forwarded at **every call site** that produces that data — not only the migration path.

**Why this is easy to miss:**
- Mock-based unit tests accept any struct literal; they pass whether or not the new field is set.
- Config fields have zero defaults, so a missed site compiles and runs — it just silently uses the provider's default value instead of yours.
- When the stored data and newly-produced data end up in incompatible shapes, many "compare" primitives degrade silently (cosine similarity returns 0, map lookups miss, parse errors → skip). The system returns empty results, not errors. Tests pass. Logs are clean.

**Checklist when adding such a field:**

1. **Grep every existing call site** of the affected API/struct and audit each one. Example: `git grep -n "llm\.EmbeddingRequest{"` before landing a new field on `EmbeddingRequest`.
2. **Add a static guard test** that walks the source tree and fails the build if a literal of the target struct is missing the required field. Canonical example: `internal/llm/embedding_dimensions_guard_test.go`. Cheap insurance against the same regression recurring.
3. **Post-migration smoke test on a real query path.** A migration test that only verifies schema-level state (rows updated, column set) doesn't catch shape mismatch between storage and query. Run an actual retrieval / read and assert non-empty results on a known input.
4. **Watch for metrics that should not be zero.** `laplaced_rag_candidates_sum{type="topics"} = 0` across every user was the diagnostic that unlocked the v0.7.1 post-mortem — all other health signals (container healthy, API calls succeeding, tests green) lied.

**Incident that motivated this (v0.7.0 → v0.7.1):** the embedding-dim migration correctly re-embedded 6500 vectors at the new 1536 dim; 14 unrelated embedding call sites (retrieval, new topics, new facts, search tools, extractor) kept the old literal without `Dimensions`; OpenRouter defaulted those to 3072; on-disk vectors (1536) and query vectors (3072) lived in different spaces; cosine returned 0 for every pair; RAG returned no candidates for anyone; reranker was silently never invoked (gated on `candidates > 0`). No errors, no warnings, green CI. Diagnosed through Prometheus showing zero candidates in prod.

### Wide type migrations: compiler blind spots & test hygiene (MUST READ!)

When changing a widely-used type — e.g. the partition key flip `user_id int64` →
`storage.ScopeID` (a UUID string) — most of the cost and nearly all the pain is in
**tests**, not production. Production is compiler-guided and goes fast; tests are where
you lose days. Three rakes, learned the expensive way:

1. **Implement `sql.Scanner` / `driver.Valuer` on the new type FIRST** if it lands in a
   DB column. SQLite has dynamic/affinity typing: a numeric-looking string (`"123"`) is
   coerced to INTEGER on write and scans back as `int64` — a plain string-kind destination
   then panics (`unsupported Scan`). This is **runtime-only**, invisible at build. Scanner
   (accept `int64`/`string`/`[]byte`) + Valuer (bind as string) makes round-trips lossless
   on both SQLite and Postgres. Add it before the flip, not after the first panic.

2. **The compiler covers typed positions; `interface{}` sinks do NOT.** `testify` matchers
   (`mock.On("M", x, ...)`) and `assert.Equal(t, want, got)` take `any`, so a wrong literal
   there compiles clean and fails only at runtime — one panic at a time, each masking the
   next. **Do not bulk-`sed` literals in those positions.** Fix each by reading the matched
   method signature: which arg is the partition key (flips) vs an entity id / count (stays).

3. **Tests must derive derived values the same way prod does — never hardcode the literal.**
   The scope id is computed (`ScopeID = uuidv5(transport, nativeID)`; the home Telegram id
   `123` → a UUID). Fixtures that hardcode `"123"` silently diverge from what the code
   computes via `backgroundUserIDs` / `resolveScopeID`. Go through the same helper
   (`testutil.TestUserID` / `storage.PassthroughScopeID`) everywhere so plain-call and
   loop-driven paths agree *by construction*. This single habit removes most of the churn.

Process: run `golangci-lint` (in `~/go/bin`, used by the git hook) and `go test -race` early
and often during a big flip — not just `go build` — so staticcheck/gosec/unused and data
races surface before the final commit, not at the hook gate.

### Public Repository Hygiene (MUST READ!)

**This repo is PUBLIC** (mirrored to GitHub; the image is published to a public registry). Anything in a *tracked* file — code, comments, identifiers, commit messages, tracked docs — is world-readable and permanent in git history. Keep internal-only material out of tracked files; it belongs in the gitignored locations below. Squashing a feature branch before pushing does not undo leaks already committed — get it right per-commit.

**1. English only in tracked artifacts.** Code comments, identifiers, and commit messages on tracked files MUST be English. (The global instruction to write Russian commits does NOT apply here — this public project overrides it. Chat with the user and gitignored notes may be any language.)

**2. No internal roadmap jargon.** No project-internal labels in code/comments/commit messages: stage names ("Стадия N"), design-option codes ("variant-C"), commit-spine labels ("C1".."C6"), phase numbers. They mean nothing to an outside reader and leak internal planning. Describe the mechanism instead ("principal resolution", "the int64→UUID migration"). Roadmaps live in gitignored `docs/plans/`.

**3. No internal infrastructure.** No internal hostnames/FQDNs/URLs (corporate domains, dev DB hosts, litellm/Keycloak/Jira endpoints), no secret-store paths (`psecret …`, vault paths), no internal IPs. In runbook comments use placeholders (`host=<host>`, `password=<password>`). Real values live in gitignored overlays (`configs/dev-*.yaml`) and env vars only.

**4. No real accounts or personal data.** No real logins/usernames, emails (corporate domains), Telegram/Mattermost ids, or conversation excerpts. This bot processes real conversations — depersonalize before anything lands in a tracked file.

**Gitignored locations (internal material OK here — never committed):**
- `logs/`, `data/` — runtime logs / SQLite DBs with real conversations
- `docs/plans/`, `docs/bugs/`, `docs/external/` — internal planning, bug notes, research
- `configs/dev-*.yaml`, `.env` — overlays with real endpoints (secrets via env)
- `.claude/` — Claude Code working files

**Before committing a tracked file, scan the diff for:** non-English text · stage/variant/`Cn` roadmap labels · internal hostnames / corporate domains · `psecret`/vault paths · real logins/emails/ids · conversation excerpts.

**Depersonalization quick map:** real name → `John`/`Mary`/`Alice`/`Bob` · handle → `@testuser`/`@johndoe` · email → `someone@example.com` · excerpt → abstract ("user asked about X") · generic detail → "hobby discussion", "family topic".

### Naming reflects reality — rename on refactor (IMPORTANT!)

A file/type/identifier name is a claim about what the code *is*. When a refactor changes that, rename to match — don't let a name outlive the assumption it encoded. Canonical example: the `sqlite_*.go` repositories became backend-agnostic once the `Dialect` layer landed, so the `sqlite_` prefix lied; they were renamed (prefix dropped) and the misleading `SQLiteStore` type alias retired. Reserve backend/transport-specific names for code that genuinely is specific (`NewSQLiteStore` really does open SQLite). Use `git mv` so history follows, and prefer a separate "renames only" commit so the diff stays reviewable.

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

**Testing standards, patterns, and helpers:** See @docs/TESTING.md for complete guide on:
- Test style and patterns (table-driven, mocks, helpers)
- Available test helpers in `internal/testutil/`
- User data isolation tests
- Coverage targets and exclusions

**Quick commands:**
```bash
go test ./...                    # All tests
go test ./internal/bot/... -v    # Specific package with verbose output
go test ./internal/bot/... -cover # With coverage
```

### Manual Testing with Testbot

For interactive testing and debugging without running the full Telegram bot, use the testbot CLI tool:

```bash
go run ./cmd/testbot [command] [args]
```

**IMPORTANT: Always use `go run`, not compiled binaries**

When testing changes to the bot code, always use `go run ./cmd/testbot` instead of compiling with `go build`. This ensures you're testing the **actual current code**, not a stale binary. During development, you'll frequently modify files in `internal/`, and a compiled binary won't reflect those changes.

```bash
# GOOD - tests current code
go run ./cmd/testbot send "test"

# BAD - tests old compiled code
go build -o testbot ./cmd/testbot && ./testbot send "test"
```

**NOTE:** Never grep testbot output — it's already minimized and `--verbose` is for full error logging. Use `--db ""` for clean testing with temporary database.

**Features:**
- Full LLM integration (OpenRouter/Gemini)
- Direct database access
- No Telegram required
- JSON output for automated checks
- Quiet by default (use `--verbose` for full debug logs)

**Configuration:**
- Optional: `--config path/to/config.yaml`
- Defaults: Uses embedded `configs/default.yaml`
- Environment: Reads `LAPLACED_*` env vars (API keys, user IDs)
- User ID: Auto-detected from `LAPLACED_ALLOWED_USER_IDS` (first ID)

**Testing with Production Database Copy:**

The `data/prod/laplaced.db` is a copy of the production database. To test on it:

```bash
# Run testbot on production database copy
go run ./cmd/testbot --db data/prod/laplaced.db send "test message"

# Check the results
go run ./cmd/testbot --db data/prod/laplaced.db check-facts
```

**Note:** `data/prod/laplaced.db` is already a copy and safe to use for testing. Changes here don't affect the production database.

**Database Locations:**

- **`data/laplaced.db`** (default) - Test database for quick testing with RAG/facts
- **`data/prod/laplaced.db`** - Copy of production database for testing on real data
- **`--db ""`** (empty) - Temporary database in `/tmp` for clean testing without data

**Common Commands:**

```bash
# Send a message to the bot
go run ./cmd/testbot send "What is 2+2?"

# Check facts (text or JSON)
go run ./cmd/testbot check-facts
go run ./cmd/testbot check-facts --format json

# Check topics, people, messages
go run ./cmd/testbot check-topics
go run ./cmd/testbot check-people --format json
go run ./cmd/testbot check-messages

# Process active session into topic (facts extracted automatically)
# Note: This can take 10-30 seconds - calls LLM to create topic and extract facts
go run ./cmd/testbot process-session

# Check artifacts (v0.6.0)
go run ./cmd/testbot check-artifacts

# Process pending artifacts (v0.6.0)
# Note: Calls Extractor agent to analyze files
go run ./cmd/testbot process-artifacts

# Clear data
go run ./cmd/testbot clear-facts
go run ./cmd/testbot clear-topics
go run ./cmd/testbot clear-people
```

**Workflow Example:**

```bash
# 1. Send a message
go run ./cmd/testbot send "My name is Alice and I love photography"

# 2. Check that facts don't exist yet
go run ./cmd/testbot check-facts
# → Facts for user 123: 0

# 3. Process session (creates topic and extracts facts automatically)
# Note: Can take 10-30 seconds - calls LLM for topic creation and fact extraction
go run ./cmd/testbot process-session
# → Processed session: topic 1 created from messages 1-1
# → Extracted facts: 1 added, 0 updated, 0 removed

# 4. Check extracted facts
go run ./cmd/testbot check-facts
# → Facts for user 123: 1
#   1. [identity] My name is Alice and I love photography
```

**Global Flags:**
- `--config path` - Config file path (default: auto-detect or use embedded)
- `--user ID` - User ID for operations (default: first from `LAPLACED_ALLOWED_USER_IDS`)
- `--db path` - Database path (default: `data/laplaced.db`, use `data/prod/laplaced.db` for production copy, use `--db ""` for temp DB)
- `--verbose` - Verbose debug output (shows all logs)

**Send Command Flags:**
- `--max-wait duration` - Maximum wait time for LLM response (default: 2m)
- `--output format` - Output format: text, json (default: text)
- `--check-response substring` - Verify response contains substring

**Use Cases:**
1. **Debug prompt changes**: Send messages directly to LLM without Telegram
2. **Test fact extraction**: Chat, then `process-session`, then `check-facts`
3. **Verify RAG retrieval**: Check topics/facts before and after queries
4. **Profile performance**: Test LLM latency, token usage
5. **Database inspection**: Quick checks without SQL queries
6. **Test on production data copy**: Use `--db data/prod/laplaced.db` to test with real data safely

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
- `LAPLACED_LLM_API_KEY` - LLM API key
- `LAPLACED_ALLOWED_USER_IDS` - Comma-separated Telegram user IDs
- `LAPLACED_ARTIFACTS_ENABLED` - Enable/disable artifacts system
- `LAPLACED_ARTIFACTS_STORAGE_PATH` - Path for artifact file storage

**Artifacts system config:**
```yaml
artifacts:
  enabled: true
  storage_path: "data/artifacts"
  allowed_types: ["image", "voice", "pdf", "video_note", "video", "document"]

agents:
  extractor:
    model: "google/gemini-3-flash-preview"
    max_file_size_mb: 20
    polling_interval: "30s"
    max_concurrent: 3
```

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

- **Language**: English (public repo — see [Public Repository Hygiene](#public-repository-hygiene-must-read)). No internal roadmap labels (stage names, `variant-C`, `Cn`).
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

### Git Hooks

Project ships git hooks in `scripts/githooks/`. Install once after clone:

```bash
make hooks
```

- `pre-commit` — `golangci-lint`, `go mod tidy` drift check, `go build ./...`
- `pre-push` — `go test -race -shuffle=on -short ./...`, plus `govulncheck` if installed

Skip with `--no-verify` if needed. CI is the source of truth; hooks just save round-trips.

### Multi-instance coordination (git worktrees)

When another Claude Code instance (or human) is already working on a different branch of this repo, **never `git checkout <other-branch>` in a directory the other instance is using.** It blows away their working tree, and `git stash` is repo-global — so a hurried `stash push` mixes your WIP into theirs.

Instead, give each active feature branch its own directory via `git worktree`. Objects are shared (`.git/objects`), working trees and indexes are independent.

```bash
# First time, from the main repo dir:
git worktree add ../laplaced-<feature> <branch>

# Cleanup once the branch is merged:
git worktree remove ../laplaced-<feature>
```

Convention for this repo:
- `~/projects/laplaced` — the integration dir, sits on `main` most of the time. Use it for reviewing merged work, NOT for long-running feature branches.
- `~/projects/laplaced-<feature>` — one per active branch. Start a separate Claude Code instance inside this dir.

Per-worktree setup:
- `.env` and `data/` are gitignored and don't follow the worktree. Symlink from the main dir: `ln -s ../laplaced/.env .` and `ln -s ../laplaced/data .` (or make per-worktree copies when you want isolation).
- Runtime collisions are NOT solved by worktrees. If two instances run concurrently: use `--db ""` (temp) or a distinct `--db` path for testbot, and different `LAPLACED_SERVER_PORT` for `cmd/bot`.
- Prefer committing WIP to the feature branch over `git stash` — stash is visible to every worktree and is easy to mix up.

If you realize you're on the wrong branch in a shared dir with uncommitted edits: stop, `git status` to catalog what's yours, then either (a) commit to the right branch from a worktree, or (b) cherry-pick specific files out via `git checkout <stash> -- <paths>` after stashing. Do not branch-switch through the shared dir while another instance is mid-flight.

## Changelog

This project uses [Keep a Changelog](https://keepachangelog.com/) format.

### Philosophy

**Changelog is for users, not developers.** Write what changed from user's perspective, not implementation details.

**Keep entries to ONE sentence by default.** No multi-paragraph "простыни". The CHANGELOG is scanned, not read — detail belongs in the PR description and commit message. Reserve longer entries only for breaking changes that genuinely need migration guidance.

**Good (short, plain language):**
```markdown
- Background tasks now appear in the trace dashboard when they fail.
- Per-message latency breakdown — track LLM, tools, Telegram timing separately.
- Voice messages now use native Gemini audio understanding.
```

**Bad (too long, too technical):**
```markdown
- **Every background pipeline call (splitter, archivist, merger, extractor) now emits OpenTelemetry spans with diagnostic attributes...** [three more paragraphs of detail that belong in the PR]
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

- @docs/TESTING.md — Testing standards, patterns, and helpers
- @.claude/deploy.md — Production deployment via Gitea
- @docs/architecture/artifacts-system.md — Artifacts system architecture (files storage and RAG integration)
- @docs/architecture/flash-reranker.md — Flash reranker for intelligent RAG filtering
- @docs/architecture/message-processing-flow.md — Message processing pipeline
