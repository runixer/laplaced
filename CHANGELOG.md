# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

_Next version: v0.5 (Social) — People Table, Social Graph_

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

[Unreleased]: https://github.com/runixer/laplaced/compare/v0.4.5...HEAD
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
