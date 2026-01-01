# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Added `.golangci.yml` with extended linter set (goimports, misspell, gocritic, gosec, bodyclose, errorlint)

### Fixed
- Fixed "context canceled" warnings for typing action by using detached context with timeout
- Fixed bot token leaking in error log messages - now sanitized as `[REDACTED]`
- Fixed file downloader missing HTTP timeout and context support
- Fixed file downloader not using configured proxy (was only using environment variables)

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

[Unreleased]: https://github.com/runixer/laplaced/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/runixer/laplaced/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/runixer/laplaced/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/runixer/laplaced/compare/v0.1.0-beta...v0.1.1
[0.1.0-beta]: https://github.com/runixer/laplaced/releases/tag/v0.1.0-beta
