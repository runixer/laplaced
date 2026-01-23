# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- **User data isolation enforcement** — added `user_id` parameter to all storage methods that operate on user-owned data (GetFactsByIDs, GetTopicsByIDs, GetPeopleByIDs, GetFactsByTopicID, DeleteTopic, DeleteTopicCascade, SetTopicFactsExtracted, SetTopicConsolidationChecked, UpdateMessageTopic, GetMessagesByIDs, UpdateFactsTopic). All queries now include `WHERE user_id = ?` to prevent cross-user data leakage. Critical fix for `GetFactsByIDs` which previously accepted user-provided fact IDs from LLM tool calls without ownership validation.

### Internal
- **Test helper extraction** — created `internal/rag/test_helpers.go` with RAG-specific test utilities (TestRAGService, TestRAGServiceNoStart, TestRAGServiceWithSetup, SetupCommonRAGMocks, MockTopic, MockFact, MockPerson). Reduced test boilerplate from 15-25 lines to 3-12 lines per test, eliminated code duplication across consolidation_test.go, vector_test.go, shutdown_test.go.

### Added
- **Unified ID format with explicit prefixes** — all IDs now use explicit prefixes to prevent confusion between different entity types: `[Fact:N]` for user profile facts, `[Topic:N]` for conversation topics, `[Person:N]` for people in memory. Reranker returns prefixed IDs in JSON (`"id": "Topic:42"`, `"id": "Person:5"`), prompts require prefixed format, display updated to show `[Fact:123]` instead of generic `[ID:123]`. Hybrid parsing accepts both prefixed format (preferred) and numeric fallback (with warning logs) for backward compatibility.

### Changed
- **Reranker ID parsing** — `TopicSelection` and `PersonSelection` now use string IDs with prefixes, added `GetNumericID()` methods for backward compatibility. Accepts both `"Topic:42"` (new) and `42` (legacy with warning).
- **Archivist JSON parsing** — supports `fact_id`/`person_id` fields (prefixed strings like `"Fact:1522"`, `"Person:5"`) alongside legacy `id` (numeric) with warning logs on fallback.
- **manage_memory and manage_people tools** — accept both `"Fact:123"`/`"Person:123"` format (preferred) and numeric IDs (with warning) for update/delete operations.

### Fixed
- **OpenRouter retry on response read timeout** — fixed retry logic that failed to catch timeout errors during `io.ReadAll(resp.Body)`. Previously, network timeouts while reading the response body (error: `context deadline exceeded (Client.Timeout or context cancellation while reading body)`) were not retried, causing immediate request failure. Now all retryable errors (timeout, connection errors) trigger exponential backoff with up to 3 retry attempts.
- **OpenRouter request timeout increased** — global HTTP client timeout increased from 120s to 300s (5 minutes) to accommodate large contexts (30-40K tokens) that require longer generation time. This fixes timeout errors on complex queries with rich RAG context.
- **OpenRouter context size logging** — added `context_chars` and `estimated_tokens` fields to request logs for better observability. Now logs show the actual size of context being sent to LLM (e.g., `context_chars=154725 estimated_tokens=38681`), making it easier to diagnose performance issues and timeout errors.
- **Archivist JSON format error** — added explicit JSON example to archivist prompt to prevent LLM from generating wrong format. Previously, the prompt only described the expected format textually, causing Gemini to return raw fact arrays (`{"facts": [{...}]}`) instead of operation structure (`{"facts": {"added": [...], "updated": [...], "removed": [...]}}`). Added fallback parser to handle malformed responses gracefully with warning log. This fixes `parse_error: json: cannot unmarshal array into Go struct field Result.facts` errors on production.
- **Archivist profile bio duplication** — when updating existing people profiles, the archivist now intelligently merges new information with old bio instead of concatenating them. Added explicit prompt instructions: "You see the old bio in <people> section — DO NOT repeat its content! Add ONLY new information that is not already in the old bio". This fixes duplicate information appearing like "NetSec инженер в Альфа-Капитал. Владелец Keenetic" repeated twice in the same bio.
- **Archivist aggressive profile compression** — people bios now preserve significant details (work history, technical expertise, major life events) instead of aggressively compressing to 2-3 sentences. The prompt was changed from "Write 2-3 sentences" to "Write 4-8 sentences for complex profiles" with explicit instructions to preserve specific technologies, years of experience, relocations, and notable achievements. This fixes profiles losing critical context like "12 years at company", "moved to city", or specific project details.
- **Person merge username/telegram_id loss** — when merging people records, the target now correctly inherits username and telegram_id from source if target doesn't have them. Previously these fields were lost during merge operations.
- **Archivist automatic deduplication** — the archivist agent now ALWAYS checks for duplicate people records and suggests merges, even when no new people are added. Previously it only checked duplicates when adding new people, leaving existing duplicates unmerged.
- **Testbot check-people shows actual database IDs** — the `check-people` command now displays actual database IDs in brackets (e.g., `[67] John Doe`), matching the format of `check-topics` and `check-messages`. Previously it showed sequential numbers, causing confusion during debugging.
- **LaTeX arrow symbols** — `\uparrow` (↑) and `\downarrow` (↓) for notation like `Invest ↑, Debt ↓`

### Removed
- **Legacy database code** — dropped old `facts` table (replaced by `structured_facts`), removed entity column migration logic, deleted migration scripts (`migrations/001_cleanup_other_facts.sql`, `migrations/002_drop_entity_column.sql`). All installations already on fresh schema.

## [0.5.3] - 2026-01-18

### Added
- **Testbot CLI tool** — interactive testing without Telegram: `go run ./cmd/testbot send "message"`, `check-facts`, `check-topics`, `process-session`, `clear-*` commands. Supports `--db` flag to test on production data copy safely.

### Fixed
- **LaTeX rendering improvements** — expanded symbol coverage and fixed subscript/superscript handling: added 10 missing Greek letters (η, ζ, ι, κ, ν, ξ, τ, υ, χ, ψ), geometry symbols (△, ∠, ∥, ⊥), vector notation (`\vec{v}` → v⃗), and proper brace removal for Cyrillic/Unicode letters in subscripts (`_{груза}` → `_груза`, `_{\Delta}` → `_Δ`). Fixed backslash-space rendering (`\ ` → space) and escaped dollar handling in formulas. Added font modifier support (mathbf, mathit, mathrm, mathsf, mathtt, mathcal) — formulas like `$\mathbf{168 рядов}$` now render as "168 рядов" instead of keeping LaTeX commands. Added limit and multiple integral operators (`\lim` → lim, `\limsup`, `\liminf`, `\iint` → ∬, `\iiint` → ∭, `\oint` → ∮).
- **LaTeX parser refactored to state machine** — replaced regex-based parsing with manual tokenizer for more reliable formula detection. Fixed numerous edge cases with escaped dollars (`\$`), nested delimiters, and terminator detection (`. , ! ? : ; ) ]`).

## [0.5.2] - 2026-01-13

**LaTeX rendering for Telegram: formulas now readable as Unicode text.**

### Added
- **LaTeX to Unicode converter** — LaTeX formulas like `$500 \text{ г} \times 4 \text{ недели} = 2.0 \text{ кг}$` now render as readable text (`500 г × 4 недели = 2.0 кг`) instead of raw LaTeX code
- **Math symbol support** — Greek letters (α, β, π, Σ), operators (×, ÷, ±, ≤, ≥), arrows (→, ⟹), and trigonometric functions (sin, cos, tg)
- **Typographic conversion** — inch/foot marks like `1"` and `6'` automatically convert to `1″` and `6′` in formulas
- **People v0.5.1 test coverage** — 35 tests for People Graph functionality (extractForwardedPeople, performSearchPeople, performUpdatePerson, performMergePeople, performCreatePerson, performDeletePerson, applyPeopleUpdates)
- **MockEmbeddingResponse helper** — centralized mock for OpenRouter embedding responses

### Fixed
- **Data race between web server and webhook config** — moved WebhookPath/WebhookSecret computation before web server creation to avoid concurrent read/write

## [0.5.1] - 2026-01-11

### Added
- **People Graph** — the bot now tracks people mentioned in conversations with bios, circles, and aliases
- **Inner Circle in context** — system prompt includes people from Family and Work_Inner circles for better personalization
- **search_people tool** — Laplace can search for people by name, username, or description
- **manage_people tool** — update, delete, or merge people records through chat commands
- **Forwarded message extraction** — people are automatically extracted from forwarded messages with their Telegram ID and username
- **People Debug UI** — new `/ui/debug/people` page to view all people grouped by circle
- **Reranker people support** — relevant people are now included in RAG context alongside topics

## [0.5.0] - 2026-01-10

**Architecture release: Unified agent framework and template-based prompts.**

### Added
- **Unified agent architecture** — all LLM-powered operations now use consistent `agent.Agent` interface with typed parameters
- **SharedContext** — user profile and recent topics passed between agents, avoiding redundant DB queries
- **Centralized test mocks** — `internal/testutil/` package with `MockStorage`, `MockAgent`, and test fixtures
- **MockAgent** — enables testing of agent-dependent code without LLM calls

### Changed
- **Agent migration** — Laplace, Reranker, Enricher, Splitter, Merger, Archivist moved to `internal/agent/` with dedicated packages
- **Named templates** — all i18n prompts migrated from positional `%s` to named `{{.Name}}` placeholders for type safety
- **RAG modularization** — monolithic `rag.go` split into focused modules: retrieval, consolidation, session, processing, extraction, vector
- **Test architecture** — centralized mocks reduce duplication, `.Maybe()` pattern handles background loop side effects

### Removed
- **Deduplicator remnants** — removed unused `DeduplicatorParams` and `memory.consolidation_prompt`

### Fixed
- **All tests passing** — previously skipped tests now work with MockAgent integration

## [0.4.8] - 2026-01-09

**Agent Debug UI: Full visibility into LLM conversations.**

### Added
- **Agent Debug UI** — unified visualization for all agents showing full OpenRouter API requests/responses with syntax-highlighted JSON
- **Multi-turn conversation viewer** — collapsible accordions showing all LLM iterations for Laplace (tool calls) and Reranker (agentic loop)
- **Reranker hallucination filter** — detects and removes invalid topic IDs returned by LLM, with dedicated metric

### Changed
- **Prompt structure improved** — system prompt sections reordered for better LLM comprehension, enhanced XML formatting

### Fixed
- **Tests restored** — 13 tests temporarily skipped in v0.4.7 now working again
- **Telegram reactions** — fixed invalid emoji causing API errors

### Removed
- **Deduplicator agent** — removed non-functional fact deduplication logic (~400 lines)

## [0.4.7] - 2026-01-08

**FIFO message processing, prompt engineering updates, and stability fixes.**

### Changed
- **Archivist prompt restructured** — XML tags for sections, clearer output format specification (per Google prompting guidelines).

### Fixed
- **Race condition with sequential messages** — when a user sends multiple messages in quick succession (e.g., voice + text), the bot now processes them in strict FIFO order. Previously, a fast-to-process message could finish before a slow one (like a long voice memo), causing the bot to respond out of order.
- **Reranker JSON parsing** — Fixed parsing error when LLM wraps the JSON response in an array.
- **Tool usage hallucinations** — Fixed bot outputting raw JSON for memory updates instead of calling the `manage_memory` tool (added explicit prohibition in system prompt and tool description).
- **Fact extraction deadlock** — Fixed extraction blocked by consolidation deadlock; merged topics (`is_consolidated=true`) now proceed immediately, orphan topics without merge partners are auto-marked as checked.

### Internal
- **Tests** — Temporarily skipped regression tests for `ProcessMessageGroup` (Photo/PDF) and RAG `EnrichQuery` due to initialization panics. *Requires follow-up fix.*

## [0.4.6] - 2026-01-06

**Reranker optimization and maintenance tools.**

### Added
- **Recent topics in context** — system prompt now includes metadata about recent conversations (date, title, message count) for temporal awareness. Configurable via `rag.recent_topics_in_context`.
- **Database Maintenance UI** — new inspector `/ui/debug/database` for database health checks, repair, and splitting large topics.
- **Cross-User Contamination detection** — tools to identify and repair message leakage between users (caused by global auto-increment IDs).
- **Topic Splitter** — mechanism to break down oversized topics into smaller logical subtopics.
- **Max topic size** — added `max_merged_size_chars` config to prevent creating "black hole" topics during consolidation.
- **Maintenance Prompts** — new system prompts for "The Splitter" and updated prompts for "The Merger" (now aware of user profiles).
- **Optional excerpts** — new `ignore_excerpts` config to always use full topic content instead of reranker-generated excerpts

### Changed
- **Session timeout reduced** — default inactivity timeout for topic creation changed from 5h to 1h (`rag.chunk_interval`)
- **Split threshold configurable** — new `rag.split_threshold_chars` config option (default 25000 chars)
- **Splitter keeps message pairs** — user question and assistant response are never split into separate topics
- **Data Isolation Improvements** — refactored message retrieval in RAG service to prefer explicit topic-based lookups over raw ID ranges to reduce leakage risks.
- **SQLite Reliability** — explicit WAL mode setting via PRAGMA statements to ensure compatibility with modernc driver.
- **Excerpts disabled by default** — `ignore_excerpts: true` simplifies reranker prompt and improves reliability
- **Minimal thinking** — `thinking_level` default changed from "medium" to "minimal" for faster responses
- **Fewer topics** — `max_topics` default reduced from 15 to 5 for focused context

### Fixed
- Fixed broken "Context" link on Facts page — now links to associated Topic
- Improved contamination detection accuracy in dry-run mode
- Fixed multi-user repair to correctly isolate messages by user
- Overlapping topics now shown as clickable list in Database Maintenance UI

## [0.4.5] - 2026-01-05

**Multimodal RAG and prompt quality improvements.**

### Added
- **Multimodal RAG** — images and audio from user messages are now passed to enricher and reranker for better context understanding
- **Reasoning display** — Debug UI now shows Flash's internal thinking process for each reranker iteration
- **Target context budget** — configurable `target_context_chars` parameter guides Flash on output size

### Changed
- Reranker prompts completely restructured with XML sections, few-shot examples, and anti-laziness rules
- Increased `max_topics` default from 5 to 15 (Flash handles excerpts for large topics)
- Increased `timeout` default from 10s to 60s for more thorough analysis
- Added `turn_timeout` (30s default) for per-LLM-call timeout control
- Changed `thinking_level` default to "medium" for balanced speed/quality
- Prompts depersonalized for public release (example names changed)

### Fixed
- Soft validation for excerpts (warns on too short/long excerpts without blocking)
- Tool call payload monitoring (warns if >15 topics or >200K chars requested)

## [0.4.3] - 2026-01-05

**Reranker protocol enforcement and excerpt quality improvements.**

### Added
- **Forced tool calling** — Flash must call `get_topics_content` before returning results (API-level enforcement via `tool_choice`)
- **Reasoning mode** — enabled `reasoning.effort: "low"` for better tool call decisions
- **Protocol violation detection** — fallback to vector search if Flash skips tool call

### Changed
- Improved excerpt quality rules:
  - Preserve User→Assistant message pairs
  - Don't truncate messages mid-sentence
  - Include 1-2 neighboring messages for context
- Fixed query enrichment prompt to prevent word substitution (e.g., "мениск" → "MinIO")

### Fixed
- JSON parsing now handles bare array format from Flash responses

## [0.4.2] - 2026-01-04

**Reranker transparency: Understand why Flash chose each topic.**

### Added
- **Reason field** — Flash now explains why each topic was selected (1-2 sentences)
- **Excerpt field** — For large topics (>25K chars), Flash extracts only relevant messages instead of full content
- Reranker Debug UI shows reasons and excerpts in Selected Topics table
- Tooltip on candidate checkmarks shows selection reason

### Changed
- Updated reranker prompts to require reason for each selected topic
- Reranker response format changed from `[42, 18]` to `[{"id": 42, "reason": "..."}]`
- Backward compatibility maintained for old format parsing

## [0.4.1] - 2026-01-04

**Intelligence release: Smart context filtering with Flash Reranker.**

### Added
- **Flash Reranker** — LLM-based filtering of RAG candidates using agentic tool calls
  - Reduces context from ~24K to ~7K tokens (70% reduction)
  - Reduces latency from ~50s to ~26s
  - Reduces cost from ~$0.40 to ~$0.05 per message
- Reranker Debug UI (`/ui/debug/reranker`) — inspect reranker decisions, tool calls, selected topics
- Reranker metrics: duration, tool calls, candidates in/out, cost, fallback reasons
- Original query passed separately to reranker (avoids enrichment noise)

### Changed
- Reranker now applies to both auto-RAG and `search_history` tool
- Updated reranker prompts to require tool verification before topic selection

## [0.3.9] - 2026-01-04

**Final v0.3.x release. Establishes metrics baseline for v0.4 Intelligence features.**

### Added
- System prompt tracking in context metrics — enables complete context breakdown (System + Profile + Session + Topics)
- Job type label for LLM metrics — separates interactive requests from background jobs (archiver, fact extraction)

### Changed
- Updated dependencies

### Documentation
- Added message processing flow architecture diagram
- Added Architectural Decisions section to CLAUDE.md

## [0.3.8] - 2026-01-03

### Added
- Per-message latency breakdown — track LLM, tools, Telegram timing separately
- RAG source tracking — distinguish automatic retrieval from search_history tool
- LLM anomaly tracking — monitor empty responses, retries, sanitizations

### Changed
- Renamed tool `memory_search` → `search_history` for clarity
- Added call limit (3) to `internet_search` tool

### Fixed
- Tool calls (Perplexity) now correctly track user attribution
- LLM response sanitization prevents hallucination artifacts
- Empty LLM responses trigger automatic retry
- Telegram metrics recorded even on early returns

## [0.3.7] - 2026-01-03

### Added
- File download metrics for latency analysis
- RAG enrichment timing metric
- Embedding type label (topics vs facts)

### Changed
- Parallel file downloads — 5-8x speedup for photo albums
- Split CI workflows to avoid duplicate runs on release

## [0.3.6] - 2026-01-03

### Added
- User info metric for Grafana label joins

### Fixed
- RAG latency metric was dead code — now working
- User dropdown shows all users, not just active ones

## [0.3.5] - 2026-01-03

### Changed
- Improved memory extraction prompts:
  - "Zero Trust to Bot" — extract facts only from User messages
  - "Silence ≠ Consent" — unanswered hypotheses not saved
  - "No Psychology" — no interpretation of feelings
  - "Identity Protection" — prevents merging people with similar names
- Voice messages now quote full transcription with `> 🎤` prefix

### Added
- Storage metrics (database size, per-table size, cleanup stats)
- Automatic retention cleanup for fact_history and rag_logs

### Fixed
- Missing user_id labels on several metrics

## [0.3.4] - 2026-01-02

### Changed
- Voice messages now use native Gemini audio understanding instead of Yandex SpeechKit
- No external speech recognition service required

### Added
- Build info metric for version tracking

### Fixed
- Missing user_id labels on LLM and Embedding metrics
- Extended histogram buckets for message processing (up to 120s)

## [0.3.3] - 2026-01-02

### Added
- User ID label on all metrics for per-user tracking
- RAG candidates histogram
- Context tokens by source metric

## [0.3.2] - 2026-01-02

### Fixed
- Active sessions metric now tracks correct concept

## [0.3.1] - 2026-01-02

### Added
- Grafana dashboard with 35 panels (overview, latency, cost, memory health, capacity)

### Fixed
- Facts count metric updates immediately on startup

## [0.3.0] - 2026-01-02

### Added
- Startup warning when allowed_user_ids is empty
- Session Inspector in web UI with force-process feature
- Debug Chat interface for testing without Telegram
- Full metrics suite: embedding, vector search, RAG, bot, LLM, HTTP, memory
- Config option `rag.max_profile_facts`

### Changed
- Improved test coverage to 66%+
- Renamed metrics to use `laplaced_` namespace

## [0.2.2] - 2026-01-01

### Security
- Fixed password appearing in logs
- Added webhook secret token verification
- Removed bot token from webhook URL path

### Fixed
- Data race in web server
- Client IP logging behind reverse proxy
- Race conditions in RAG and MessageGrouper

## [0.2.1] - 2026-01-01

### Added
- Retry logic with exponential backoff for OpenRouter
- Configuration validation
- Incremental vector loading
- Dashboard stats caching

### Changed
- Optimized RAG retrieval (fetch only matched topics)

### Fixed
- Early startup logs now use JSON format
- Various error handling improvements

## [0.2.0] - 2026-01-01

### Added
- Extended linter configuration

### Changed
- Voice messages go through MessageGrouper

### Fixed
- Excessive log size from OpenRouter responses
- Graceful shutdown for voice messages
- Multiple race conditions

### Security
- Added ReadHeaderTimeout to prevent Slowloris attacks

## [0.1.3] - 2026-01-01

### Changed
- Refactored complex functions (buildContext, processChunk, etc.)

## [0.1.2] - 2026-01-01

### Changed
- Docker images on main branch get `latest` tag

## [0.1.1] - 2026-01-01

### Added
- GitHub Actions CI/CD workflow
- Pre-commit hook for golangci-lint
- Cross-compilation (linux/amd64, linux/arm64)

## [0.1.0-beta] - 2025-12-31

### Added
- Initial release
- LLM chat with Google Gemini via OpenRouter
- Long-term memory with RAG
- Voice transcription via Yandex SpeechKit
- Image and PDF analysis
- Web dashboard
- SQLite storage with vector embeddings
- Multi-language support (en, ru)
- Docker deployment

[Unreleased]: https://github.com/runixer/laplaced/compare/v0.5.3...HEAD
[0.5.3]: https://github.com/runixer/laplaced/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/runixer/laplaced/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/runixer/laplaced/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/runixer/laplaced/compare/v0.4.8...v0.5.0
[0.4.8]: https://github.com/runixer/laplaced/compare/v0.4.7...v0.4.8
[0.4.7]: https://github.com/runixer/laplaced/compare/v0.4.6...v0.4.7
[0.4.6]: https://github.com/runixer/laplaced/compare/v0.4.5...v0.4.6
[0.4.5]: https://github.com/runixer/laplaced/compare/v0.4.3...v0.4.5
[0.4.3]: https://github.com/runixer/laplaced/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/runixer/laplaced/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/runixer/laplaced/compare/v0.3.9...v0.4.1
[0.3.9]: https://github.com/runixer/laplaced/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/runixer/laplaced/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/runixer/laplaced/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/runixer/laplaced/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/runixer/laplaced/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/runixer/laplaced/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/runixer/laplaced/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/runixer/laplaced/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/runixer/laplaced/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/runixer/laplaced/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/runixer/laplaced/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/runixer/laplaced/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/runixer/laplaced/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/runixer/laplaced/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/runixer/laplaced/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/runixer/laplaced/compare/v0.1.0-beta...v0.1.1
[0.1.0-beta]: https://github.com/runixer/laplaced/releases/tag/v0.1.0-beta
