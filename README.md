# Laplaced

[![CI](https://github.com/runixer/laplaced/actions/workflows/ci.yml/badge.svg)](https://github.com/runixer/laplaced/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/runixer/laplaced/graph/badge.svg)](https://codecov.io/gh/runixer/laplaced)
[![Go Report Card](https://goreportcard.com/badge/github.com/runixer/laplaced?v=1)](https://goreportcard.com/report/github.com/runixer/laplaced)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

English | [Русский](README.ru.md)

A smart chat bot for family use, with long-term memory. Runs on Telegram or
Mattermost, and works with any OpenAI-compatible LLM API (OpenRouter, litellm,
vLLM) — built and tuned around Google Gemini.

**What it does:**
- Chats with long-term memory — remembers past conversations via RAG (vector search over topic summaries, facts, and a people graph)
- Understands voice messages natively (Gemini multimodal), plus images, PDFs, and video notes
- Treats files as memory — sent files become searchable "artifacts" the bot can recall and re-read weeks later
- Generates and edits images on request ("draw a samurai cat", "make this photo sepia")
- Reacts with an emoji when it fits, and streams replies with a live "thinking" trail
- Ships a web dashboard for inspecting memory, agents, and traces

## Quick Start

### Docker (recommended)

```bash
# Create config
mkdir -p data
cat > .env << 'EOF'
LAPLACED_TELEGRAM_TOKEN=your_bot_token
LAPLACED_LLM_API_KEY=your_api_key
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

**Requirements:** Go 1.25+

```bash
git clone https://github.com/runixer/laplaced.git
cd laplaced
go run cmd/bot/main.go
```

## Configuration

Configure via environment variables (recommended) or YAML config. Defaults live
in [`internal/config/default.yaml`](internal/config/default.yaml); every field
has a matching `LAPLACED_*` environment variable.

**Required variables:**
```bash
LAPLACED_TELEGRAM_TOKEN=your_bot_token
LAPLACED_LLM_API_KEY=your_api_key
LAPLACED_ALLOWED_USER_IDS=123456789,987654321  # ⚠️ Required! Empty = reject all
```

See [`.env.example`](.env.example) for the full, grouped list of options.

> **Note:** `LAPLACED_ALLOWED_USER_IDS` must contain at least one user ID. If empty, the bot rejects all messages.

Secrets can be supplied directly, via env vars, or pulled from HashiCorp Vault
using inline `vault:secret/path#key` references — see
[docs/architecture/vault-secrets.md](docs/architecture/vault-secrets.md).

## Transports

The bot speaks to users through a transport abstraction; pick one with
`LAPLACED_TRANSPORT`:

- **`telegram`** (default) — long polling (works behind NAT) or webhook (lower latency):
  ```bash
  LAPLACED_TELEGRAM_WEBHOOK_URL=https://your-domain.com   # webhook mode
  ```
- **`mattermost`** — runs on a Mattermost-compatible server (e.g. Time messenger)
  over REST + WebSocket. Access can be a fixed allowlist or gated by corporate
  SSO. See [docs/architecture/transports.md](docs/architecture/transports.md).

## LLM backend

Defaults to the public OpenRouter API, but `LAPLACED_LLM_BASE_URL` points the
client at any OpenAI-compatible endpoint (litellm, vLLM, a self-hosted gateway).
`LAPLACED_LLM_IMAGE_INPUT_FORMAT` switches the multimodal encoding between the
OpenRouter/Gemini shape (`file`) and OpenAI-standard parts (`openai`).

## Storage

- **Database:** SQLite by default (pure-Go, no CGO). Set
  `LAPLACED_DATABASE_DRIVER=postgres` plus `LAPLACED_DATABASE_*` to use
  PostgreSQL. The same repository code runs on both via a dialect layer.
- **Files (artifacts):** local disk by default; configure `artifacts.s3.*`
  (`LAPLACED_ARTIFACTS_S3_*`) to store blobs in an S3-compatible bucket such as
  Yandex Object Storage.

## Web dashboard

A web dashboard runs on port `9081` (configurable via `LAPLACED_SERVER_PORT`),
protected by HTTP Basic Auth (on by default). If no password is set, one is
generated and printed in the logs at startup.

```bash
LAPLACED_AUTH_USERNAME=admin
LAPLACED_AUTH_PASSWORD=your_password   # leave empty to auto-generate
```

It exposes per-agent LLM request/response inspection, memory (facts, topics,
people, artifacts), and OpenTelemetry traces.

**⚠️ Warning:** Exposes sensitive data. Don't expose it publicly.

## Observability

Optional OpenTelemetry tracing covers every turn — LLM calls, embeddings, RAG
retrieval, reranking, tool execution, and image generation — with anomaly
signals on spans. Disabled by default; enable with `LAPLACED_TELEMETRY_ENABLED=true`
and point `LAPLACED_TELEMETRY_OTLP_ENDPOINT` at an OTLP collector. Prometheus
metrics are also exported. See
[docs/architecture/observability.md](docs/architecture/observability.md).

## Architecture

```
cmd/bot/          — entry point, dependency wiring
internal/
  agent/          — LLM agents (chat, reranker, enricher, splitter, merger,
                    archivist, extractor, reactor, imagegen)
  bot/            — message handling, transports, streaming, tools
  rag/            — vector search, memory retrieval, context assembly
  memory/         — facts and people extraction
  storage/        — SQLite/PostgreSQL repositories (dialect layer)
  files/          — artifact blob storage (disk / S3)
  llm/            — OpenAI-compatible LLM client
  telegram/       — Telegram API client
  mattermost/     — Mattermost/Time REST + WebSocket client
  obs/            — OpenTelemetry tracing
  secrets/        — HashiCorp Vault secret resolution
  web/, ui/       — dashboard HTTP server
  i18n/, markdown/ — localization and Markdown rendering
```

See [docs/architecture/](docs/architecture/) for detailed documentation (in Russian).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). PRs welcome!

## License

MIT — see [LICENSE](LICENSE).
