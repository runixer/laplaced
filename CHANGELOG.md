# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.7] - 2026-01-03

### Added
- **New metrics for full latency breakdown:**
  - `laplaced_file_download_duration_seconds{user_id, file_type}` — Telegram file download timing
  - `laplaced_file_downloads_total{user_id, file_type, status}` — download counter
  - `laplaced_file_size_bytes{file_type}` — file size histogram
  - `laplaced_rag_enrichment_duration_seconds{user_id}` — query enrichment LLM timing
- **Embedding type label** added to `laplaced_embedding_request_duration_seconds` and `laplaced_embedding_requests_total`:
  - `type=topics` for topic retrieval/processing embeddings
  - `type=facts` for fact retrieval embeddings

### Changed
- **Refactored file processing** into new `internal/files/` package:
  - **Parallel file downloads across message groups** — 5-8x speedup for photo albums
  - Automatic retries (3 attempts with exponential backoff)
  - Graceful degradation — continues with other files if one fails
  - Simplified `prepareUserMessage` by ~80 lines
- **Split CI workflows** to eliminate duplicate runs on release:
  - `ci.yml` — lint and test on push to main and PRs
  - `release.yml` — full release pipeline on tags only
- **Fixed GHCR image description** for multi-arch images by adding annotations at index level

## [0.3.6] - 2026-01-03

### Added
- Added `laplaced_user_info` gauge metric with labels `{user_id, username, first_name}` for Grafana label joins

### Fixed
- Fixed `laplaced_rag_latency_seconds` metric: was dead code (defined but never used), now properly records RAG retrieval time with `user_id` label
- Fixed Grafana user dropdown showing only active users — now uses `memory_facts_count` gauge (populated on startup for all users)

## [0.3.5] - 2026-01-03

### Changed
- **Improved memory extraction prompts** to prevent fact pollution:
  - Added "Zero Trust to Bot" protocol — facts extracted only from User messages, not Bot speculation
  - Added "Silence ≠ Consent" rule — Bot's unanswered hypotheses are not saved as facts
  - Added "No Psychology" rule — no interpretation of feelings, only actions
  - Added "Identity Protection" — prevents merging people with similar names (e.g., Roman P. vs Roman L.)
  - Removed aggressive consolidation instruction that caused data loss
- **Voice messages now quote full transcription** in Bot's response:
  - LLM must quote cleaned voice content with `> 🎤` prefix
  - Forwarded voice messages attributed as `> 🎤 **From [sender]:** [text]`
  - Enables archivist and RAG to work with voice message content

### Added
- Added storage metrics for database observability:
  - `laplaced_storage_size_bytes` - total database file size
  - `laplaced_storage_table_bytes{table}` - per-table size via SQLite dbstat
  - `laplaced_storage_cleanup_deleted_total{table}` - count of deleted rows during cleanup
  - `laplaced_storage_cleanup_duration_seconds{table}` - cleanup operation duration
- Added automatic retention cleanup (runs hourly with metrics update):
  - `fact_history`: keeps 100 most recent records per user
  - `rag_logs`: keeps 20 most recent records per user
  - Reclaims ~263 MB (54% of DB) on first run for existing installations

### Fixed
- Fixed missing `user_id` label on `laplaced_memory_topics_total` metric (missed in v0.3.3)
- Fixed missing `user_id` label on `laplaced_memory_facts_count` and `laplaced_memory_staleness_days` metrics
- Removed dead code: unused `factsTotal` gauge and `UpdateFactsTotal` function in memory/metrics.go

## [0.3.4] - 2026-01-02

### Changed
- **Voice messages now use native Gemini audio understanding** instead of Yandex SpeechKit transcription
  - Audio is sent directly to the LLM as base64-encoded OGG Opus
  - Supports any language Gemini understands (not limited to Russian)
  - No external speech recognition service required
  - Yandex SpeechKit integration is now optional/unused
  - Includes explicit instruction for LLM to respond in configured language and avoid reasoning leakage

### Added
- Added `laplaced_build_info{version, go_version}` metric for version tracking in Grafana

### Fixed
- Fixed missing `user_id` label on LLM and Embedding metrics (missed in v0.3.3):
  - LLM: `laplaced_llm_request_duration_seconds`, `laplaced_llm_requests_total`
  - Embedding: `laplaced_embedding_request_duration_seconds`, `laplaced_embedding_requests_total`, `laplaced_embedding_tokens_total`, `laplaced_embedding_cost_usd_total`
  - Bot: `laplaced_bot_context_tokens_by_source`
- Fixed `laplaced_memory_topics_total` metric always showing 0
- Fixed histogram buckets for `laplaced_bot_message_processing_duration_seconds`: extended from 30s to 120s max (LLM responses can take up to a minute)

## [0.3.3] - 2026-01-02

### Added
- Added `user_id` label to all Prometheus metrics for per-user tracking:
  - RAG: `laplaced_vector_search_duration_seconds`, `laplaced_vector_search_vectors_scanned`, `laplaced_rag_retrieval_total`
  - Bot: `laplaced_bot_message_processing_duration_seconds`, `laplaced_bot_messages_processed_total`
  - Memory: `laplaced_memory_fact_operations_total`, `laplaced_memory_dedup_decisions_total`
  - LLM: `laplaced_llm_tokens_total`, `laplaced_llm_cost_usd_total`
- Added `laplaced_rag_candidates` histogram metric to track candidates before filtering
- Added `laplaced_bot_context_tokens_by_source{source}` histogram metric to track context tokens by source (profile, topics, session)

## [0.3.2] - 2026-01-02

### Fixed
- Fixed `laplaced_bot_active_sessions` metric not tracking correct concept - now shows users with unprocessed messages (waiting for fact extraction), not message grouper sessions

## [0.3.1] - 2026-01-02

### Added
- Added Grafana dashboard (`grafana/dashboards/laplaced-bot.json`) with 35 panels:
  - Overview: active sessions, messages/hour, error rate, uptime, facts/topics count
  - Latency: message processing p50/p95/p99, LLM duration, embedding & vector search
  - Cost: LLM/embedding cost (24h), cost/message, monthly projection, cost over time
  - Memory Health: facts by type, fact operations, growth trends, context tokens, RAG hit/miss
  - Capacity Planning: vector index size, memory usage, goroutines, open FDs, trends

### Fixed
- Fixed `laplaced_memory_facts_count` metric not populated until 1 hour after startup (now updates immediately)

## [0.3.0] - 2026-01-02

### Added
- Added startup warning when `allowed_user_ids` is empty (bot rejects all messages in this case)
- Added Session Inspector in web UI: view active sessions with user name, message count, timestamps (local timezone), countdown timer until auto-processing, and context size
- Added "Force Process" with real-time progress bar via SSE, showing detailed results: topics extracted/merged, facts created/updated/deleted, API usage (tokens and cost)
- Added Debug Chat interface (`/ui/debug/chat`): test bot pipeline without Telegram
  - Chat UI with message history
  - Debug panel: timing breakdown (total, embedding, search, LLM), token usage, cost, context preview
  - Option to save messages to real history (checkbox, enabled by default)
- Added Prometheus metrics for embedding observability:
  - `laplaced_embedding_request_duration_seconds` - embedding API latency
  - `laplaced_embedding_requests_total` - request counter with status
  - `laplaced_embedding_tokens_total` - token usage
  - `laplaced_embedding_cost_usd_total` - cumulative cost tracking
  - `laplaced_vector_search_duration_seconds` - vector search latency
  - `laplaced_vector_search_vectors_scanned` - vectors per search
  - `laplaced_vector_index_size` - current index size
  - `laplaced_vector_index_memory_bytes` - estimated memory usage
  - `laplaced_rag_retrieval_total` - RAG retrieval hit/miss counter
- Added Prometheus metrics for application health:
  - `laplaced_bot_active_sessions` - active chat sessions gauge
  - `laplaced_bot_message_processing_duration_seconds` - end-to-end message processing latency
  - `laplaced_bot_messages_processed_total` - processed messages counter with success/error status
  - `laplaced_bot_context_tokens` - context size histogram (in tokens)
- Added Prometheus metrics for LLM observability:
  - `laplaced_llm_request_duration_seconds` - LLM request latency by model/status
  - `laplaced_llm_requests_total` - LLM request counter by model/status
  - `laplaced_llm_tokens_total` - token usage by model/type (prompt/completion)
  - `laplaced_llm_cost_usd_total` - cumulative LLM cost by model
  - `laplaced_llm_retries_total` - retry counter by model
- Added Prometheus metrics for HTTP server:
  - `laplaced_http_request_duration_seconds` - HTTP request latency by handler/method/status
  - `laplaced_http_requests_total` - HTTP request counter by handler/method/status
- Added Prometheus metrics for memory system:
  - `laplaced_memory_fact_operations_total` - fact operations counter (add/update/delete)
  - `laplaced_memory_dedup_decisions_total` - deduplication decisions counter (ignore/merge/replace/add)
  - `laplaced_memory_extraction_duration_seconds` - fact extraction latency
  - `laplaced_memory_topic_processing_duration_seconds` - topic processing latency
  - `laplaced_memory_facts_total` - current facts count by type (identity/context/status)
  - `laplaced_memory_topics_total` - current topics count
- Added `rag.max_profile_facts` config option (default: 50) - previously hardcoded limit for user profile facts

### Changed
- Improved test coverage: rag 76.8%, web 69.5%, bot 66.2%, memory 76.7%, all packages 66%+
- Renamed all Prometheus metrics to use `laplaced_` namespace for consistency
  - `telegram_api_*` → `laplaced_telegram_*`
  - `memory_*` → `laplaced_memory_*`
  - `rag_*` → `laplaced_rag_*`

### Fixed
- Fixed SSE endpoints returning 500 when wrapped with metrics middleware (responseWriter now implements http.Flusher)

## [0.2.2] - 2026-01-01

### Changed
- Switched from `math/rand` to `math/rand/v2` for better randomness in backoff jitter
- Migrated from deprecated `grpc.DialContext` to `grpc.NewClient` in Yandex SpeechKit client

### Security
- Fixed password appearing in structured logs when auto-generated - now printed to stdout only
- Added webhook secret token verification with auto-generation from bot token hash (zero-config protection against unauthorized requests)
- Removed bot token from webhook URL path - now uses separate hash to prevent token leakage in logs

### Fixed
- Fixed data race in web server: context now passed to constructor instead of being set in Start()
- Fixed remaining ignored errors in fact history debug logging (4 additional locations)
- Fixed client IP logging behind reverse proxy - now uses X-Forwarded-For/X-Real-IP headers
- Added User-Agent logging for HTTP requests
- Fixed potential race condition in `LoadNewVectors()` when multiple goroutines load vectors concurrently
- Fixed untracked goroutines in RAG service (`LoadNewVectors`, `ReloadVectors`) - now properly tracked by WaitGroup for graceful shutdown
- Fixed metrics update goroutine in web server not tracked by WaitGroup
- Fixed missing duration format validation in config (`turn_wait_duration`, `backfill_interval`, `chunk_interval`)

## [0.2.1] - 2026-01-01

### Added
- Added retry logic with exponential backoff to OpenRouter client (max 3 retries on 429/5xx)
- Added configuration validation with clear error messages for required fields and value ranges
- Added `GetTopicsByIDs()` method for efficient topic retrieval by IDs
- Added incremental vector loading: `LoadNewVectors()` fetches only new topics/facts after initial load
- Added dashboard stats caching with 5-minute TTL to reduce database load

### Changed
- Optimized RAG retrieval: now fetches only matched topics instead of all topics
- Optimized `HasFacts` filter: uses `EXISTS` subquery instead of `COUNT(*)` for faster execution
- Replaced `println()` with structured logging (`slog`) in config loading
- Removed `goto` statement in i18n package, using simpler control flow

### Fixed
- Fixed early startup logs using text format instead of JSON (now JSON from first line)
- Fixed OpenRouter client returning nil instead of error on non-OK HTTP status
- Fixed `sendResponses()` retry logic using unbounded `context.Background()` - now uses 30s timeout
- Fixed web server calling `os.Exit(1)` on failure - now triggers graceful shutdown via `cancel()`
- Fixed ignored errors in fact history operations - now logged with warning level

### Removed
- Removed dead code: unused `processSingleMessage()` function and its test
- Removed `cmd/import_history` and `cmd/reset_user` CLI utilities (to be replaced by web UI)

## [0.2.0] - 2026-01-01

### Added
- Added `.golangci.yml` with extended linter set (goimports, misspell, gocritic, gosec, bodyclose, errorlint)

### Changed
- Voice messages now go through MessageGrouper for proper grouping with text messages
- Forwarded voice messages now correctly show their original source in transcription

### Fixed
- Fixed excessive log size from OpenRouter responses - removed raw body logging and filtered out `reasoning.encrypted` blobs
- Removed noisy "Partial recognition result" debug logging from SpeechKit client
- Fixed "context canceled" warnings for typing action by using detached context with timeout
- Fixed bot token leaking in error log messages - now sanitized as `[REDACTED]`
- Fixed graceful shutdown for voice messages: file download, speech recognition, and response sending now complete even when shutdown is triggered
- Fixed file downloader missing HTTP timeout and context support
- Fixed file downloader not using configured proxy (was only using environment variables)
- Fixed graceful shutdown: MessageGrouper now properly waits for active downloads to complete
- Fixed race condition in ProcessUpdate with WaitGroup.Add() inside goroutine
- Fixed MessageGrouper not tracking active processing with WaitGroup
- Fixed voice message handler using `context.Background()` - now properly supports cancellation
- Fixed webhook handler not tracking goroutines with WaitGroup - now uses `HandleUpdateAsync` for proper shutdown
- Fixed graceful shutdown: RAG retrieval, LLM generation, and typing action now complete even when shutdown is triggered
- Fixed graceful shutdown: pending message groups in turnWait are now processed immediately instead of being dropped
- Fixed noisy "failed to get updates" error during shutdown - now properly suppressed when context is cancelled

### Security
- Fixed potential Slowloris attack vector by adding `ReadHeaderTimeout` to HTTP server

## [0.1.3] - 2026-01-01

### Changed
- Refactored `buildContext`: extracted `formatCoreIdentityFacts` and `deduplicateTopics` helpers
- Refactored `processChunk`: extracted `findChunkBounds` and `findStragglers` helpers
- Refactored `ParseRAGLog`: extracted `extractMessageContent` helper
- Refactored `performManageMemory`: extracted CRUD operation handlers
- Refactored `deduplicateAndAddFact`: extracted `addFactWithHistory` helper, removed duplicate code

### Fixed
- Fixed Go Report Card badge caching issue in README

## [0.1.2] - 2026-01-01

### Changed
- Docker images on main branch now get `latest` tag automatically

## [0.1.1] - 2026-01-01

### Added
- GitHub Actions CI/CD workflow for linting, testing, building, and releasing
- Pre-commit hook for golangci-lint
- Cross-compilation support for linux/amd64 and linux/arm64

### Fixed
- Fixed golangci-lint errors across the codebase

## [0.1.0-beta] - 2025-12-31

### Added
- Initial release of Laplaced Telegram bot
- LLM-powered chat with Google Gemini via OpenRouter
- Long-term memory using RAG (Retrieval-Augmented Generation)
- Voice message transcription via Yandex SpeechKit
- Image and PDF analysis
- Web dashboard for statistics and debugging
- SQLite storage with vector embeddings
- Multi-language support (English, Russian)
- Docker and docker-compose deployment
- Configuration via YAML and environment variables

[Unreleased]: https://github.com/runixer/laplaced/compare/v0.3.6...HEAD
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
