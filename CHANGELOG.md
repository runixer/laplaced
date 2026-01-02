# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Added startup warning when `allowed_user_ids` is empty (bot rejects all messages in this case)
- Added Session Inspector in web UI: view active sessions with user name, message count, timestamps (local timezone), countdown timer until auto-processing, and context size
- Added "Force Process" with real-time progress bar via SSE, showing detailed results: topics extracted/merged, facts created/updated/deleted, API usage (tokens and cost)

### Changed
- Improved test coverage: rag 67%→75.5%, web 57%→70.9%, bot 67%→68.1%

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

[Unreleased]: https://github.com/runixer/laplaced/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/runixer/laplaced/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/runixer/laplaced/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/runixer/laplaced/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/runixer/laplaced/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/runixer/laplaced/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/runixer/laplaced/compare/v0.1.0-beta...v0.1.1
[0.1.0-beta]: https://github.com/runixer/laplaced/releases/tag/v0.1.0-beta
