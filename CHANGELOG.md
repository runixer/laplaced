# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- **No more false "I can't read this file" disclaimer when an old photo and a fresh photo land in the same answer.** When the bot pulled a related image out of memory to compare against a freshly attached photo, both files reached the model with the same generic name (Telegram photos default to `photo.jpg`) — and if the older photo's stored description was about a different device than what the new photo showed, the chat model would defensively prepend "Я не смог прочитать этот файл" to its reply, even though it had clearly read the new photo and went on to answer correctly about it. Historical files pulled out of memory are now labelled distinctly (e.g. "memory artifact #1053") and given a unique name internally, so the model can tell which photo is "the one you just sent" vs "the one we discussed before".

### Added
- **OpenRouter provider routing.** The bot now prefers Google (Vertex) for all Gemini calls, with Google AI Studio as a fallback if Vertex is unavailable. Vertex has a published SLA and dynamic shared quota; AI Studio runs on a capacity-constrained shared pool with tighter per-key limits and noticeably more frequent "model overloaded" errors during peak hours. Non-Gemini models (Perplexity search, etc.) continue to route freely thanks to the default fallback behavior. Configurable via `openrouter.provider.order` in YAML or `LAPLACED_OPENROUTER_PROVIDER_ORDER` env. The actual provider that served each request is now logged alongside cost, so fallbacks are visible.
- **Alternative image model: `openai/gpt-5.4-image-2` is now a one-edit swap.** The `generate_image` tool schema is built from two new config lists — `agents.image_generator.supported_image_sizes` and `supported_aspect_ratios` — so switching providers no longer leaves the LLM advertising sizes or ratios the upstream model would `400` on. `default.yaml` ships the verified nano-banana set and a commented OpenAI alternative block. Trade-offs to be aware of when switching to `openai/gpt-5.4-image-2`: ~10× slower per image (`timeout: 180s` is required — the default 90s will trip), ~2× cost (~$0.13 per image at 1K vs ~$0.07 for nano banana), 2K is the model ceiling (4K is rejected upstream), and the extreme `1:4 / 4:1 / 1:8 / 8:1` ratios aren't accepted. Image quality on benchmarks is generally ahead of nano banana, so the swap is worth the latency hit when speed isn't critical.

### Security
- Upgraded the Go toolchain from 1.24 to 1.25.9, closing 13 known stdlib CVEs (in `crypto/tls`, `crypto/x509`, `html/template`, `net/url`, `os`). CI now also runs `govulncheck` as a blocking job on every push and PR so future CVE exposures are caught at merge time rather than release time.

## [0.8.0] - 2026-04-22

### Added
- **Image generation and editing (nano banana).** The bot can now draw pictures in response to natural-language requests ("draw a samurai cat"), edit photos attached in the same message ("make it sepia", "replace the background"), combine multiple attachments into one image, and re-work images from earlier turns — if a photo from a past conversation comes up in reranker context, the LLM can cite its artifact ID and the tool automatically mixes it with any new attachment into a single generation. Uses `google/gemini-3.1-flash-image-preview` via OpenRouter; supports all 14 aspect ratios the model accepts (1:1, 16:9, 21:9, 1:4, 8:1, etc.) and four resolutions (0.5K / 1K / 2K / 4K).
  - Default aspect ratio is **9:16** (vertical phone format) for text-to-image requests; edits preserve the input photo's own ratio.
  - Outputs larger than **2 MB** (typically 2K/4K) are delivered as a Telegram **Document** (no recompression) instead of a Photo — which Telegram always downscales to ~1280 px on the long side. Single items go as one `sendDocument`; 2–10 items as a grouped document album. Threshold is tunable via `agents.image_generator.document_threshold_bytes`.
  - Generated images become regular artifacts (full RAG integration): weeks later, asking "what did you draw for me about X?" retrieves them back via vector search.
  - Captions render **Markdown → HTML** the same way text replies do, so `**bold**`, `_italic_`, `` `code` `` work in the picture's caption.
  - **Cost impact:** ~$0.07 per 1K image, ~$0.10 per 2K, ~$0.15 per 4K (measured on Gemini 3.1 Flash Image Preview pricing). A single "draw me X" turn costs ~$0.08–0.15 depending on resolution — an order of magnitude more than a text turn. To disable entirely, set `agents.image_generator.model: ""` in your config.
- **`generate-image` testbot command** — `go run ./cmd/testbot generate-image "<prompt>" [--aspect-ratio 16:9] [--size 2K]` for prompt iteration without running the full bot.

### Fixed
- **Leaked internal "thinking" blocks no longer appear in replies.** A fraction of chat responses were prefixing the real answer with raw reasoning text (`![thought ... CRITICAL INSTRUCTION ...`). Once one leaked, history retrieval fed it back as context on the next turn and the model imitated the pattern — cold leak rate ~10–15% climbed to 100% once 4+ precedents accumulated in a session. Fixed by reverting the chat model from `-customtools` back to plain `google/gemini-3.1-pro-preview` (the customtools variant's internal system prompt was the source of the specific `![thought CRITICAL INSTRUCTION` shape), setting `thinking_level: "low"` explicitly on the chat agent so reasoning is channelled into the separate `reasoning_details` field instead of the response body, and adding a defensive sanitizer that strips any leading reasoning markers that still slip through. Stress test against a worst-case poisoned context: 0 leaks in 10 turns with the new settings, vs 1–5 leaks per 10 turns on the old config.
- **Enricher no longer pivots ambiguous words into tech/business framings.** Cold queries about baby care ("swaddling") or a relationship metaphor ("lightning rod") were being rewritten as "system deployment" and "risk management", and a doctor's surname was being re-interpreted as a business-vendor name — mangling the search query and hurting retrieval. Prompt now explicitly preserves everyday / medical / family / metaphorical meanings and no longer asks the model to "include technologies". Measured on 30 labelled real queries: hard-query Recall@5 improved from 0.857 to 1.000; overall enricher now matches the raw-query baseline on Recall@10 instead of losing ~3–7 percentage points.
- **`manage_people` tool now recognises composite `"Name (@handle)"` references.** The bot sometimes saw a person rendered as `Person:42 John Doe (@johndoe)` in context and copied that whole string into the tool's `name` argument, which then failed the lookup and the model had to retry on the next turn. Tool now strips a trailing `(@handle)` and retries the lookup, saving one round-trip and preventing a transient "person not found" error the user would briefly see logged.

## [0.7.2] - 2026-04-21

### Fixed
- **Artifact / people candidate limits were never wired into vector search.** The `Reranker.{Artifacts,People}.CandidatesLimit` config fields existed and validated (default 20) but the search functions ignored them — `SearchArtifactsBySummary` returned every artifact above the similarity threshold unbounded, `SearchPeople` used a hardcoded 20. A pre-existing bug, but invisible before v0.7.0: `gemini-embedding-2-preview` produces higher similarities than `gemini-embedding-001`, so hundreds of artifacts now pass the threshold. One real prod turn was feeding 278 artifacts into the reranker, inflating its input context from the benchmarked ~3K to 305K tokens, tripling per-message cost, and measurably degrading answer quality (too much loosely-related noise, diluted signal). Limits are now read from config and applied post-sort.

## [0.7.1] - 2026-04-21

### Fixed
- **Embedding dimension mismatch broke RAG retrieval in v0.7.0.** The re-embed migration rewrote all stored vectors at the configured dimension (1536), but the per-query, per-new-topic, per-fact, and per-artifact embedding calls were never updated to pass the new `Dimensions` field — the OpenRouter server defaulted those to 3072. Cosine similarity returns 0 on length mismatch, so every vector search in prod returned zero topic / fact / people / artifact candidates, and the reranker was silently never invoked (gated on candidate count > 0). All 14 production `EmbeddingRequest` call sites now forward `cfg.Embedding.Dimensions`, and a static test fails the build if a future call site forgets it.


**Model upgrade release: Gemini 3.1 family + Embedding 2 Preview.**

### Changed
- **Embedding model → `google/gemini-embedding-2-preview` @ 1536 dimensions.** First full re-embed of all existing vectors (topics, facts, people, artifacts) on startup — ~45 seconds one-time downtime, ~$0.04 in tokens on a prod-sized DB. Chosen after a benchmark on real `reranker_logs` data that showed **+60% relative Recall@5** over `gemini-embedding-001`, with 1536 specifically beating 768 / 3072 on Recall@20. See [docs/architecture/embedding-storage.md](docs/architecture/embedding-storage.md) for methodology and results. Task prefixes (`task: search result | query: …`) from the Gemini docs were tested and **measurably hurt** retrieval on our enricher-generated queries — not used.
- **Chat agent → `google/gemini-3.1-pro-preview-customtools`.** Drop-in API-compatible variant tuned to prefer registered custom functions over the built-in general tool. Same pricing and 1M context.
- **Default agent (splitter / enricher / merger / archivist base) → `google/gemini-3.1-flash-lite-preview`**, same for reranker and extractor. Smaller, faster, cheaper — sufficient for the classification-style tasks these agents perform.
- **Migration is forward-only.** Vector spaces of v1 and v2 are incompatible, so rolling back requires restoring a pre-migration DB snapshot, which loses any messages written in the v0.7.0 window.

### Added
- `embedding.dimensions` config option (YAML + `LAPLACED_EMBEDDING_DIMENSIONS` env) — forwarded to OpenRouter as the `dimensions` parameter.
- `embedding_version` column on topics, structured_facts, people, artifacts (migration 009). On startup the bot re-embeds any rows whose stored version differs from the configured one.
- `cmd/embed-benchmark` — reproducible retrieval-quality benchmark harness.

### Fixed
- Cost fields in OpenRouter debug/info logs now print the USD value (`cost_usd=0.0123`) instead of the raw Go pointer address.

## [0.6.2] - 2026-04-21

### Security
- **OpenRouter 200-with-body-error silent success** — the client treated HTTP 200 responses that contained a JSON `error` payload (e.g. `{"error":{"code":404}}` when the upstream provider is unavailable) as successful empty responses. Combined with the RAG chunking loop, this could drive unbounded retry storms costing hundreds of dollars in wasted splitter/LLM tokens during an upstream outage. The client now detects body-level errors and retries them like any other transient failure, returning a hard error after `maxRetries` instead of a silent empty response.
- **RAG chunking circuit breaker** — the background topic-chunking loop called the paid splitter LLM before embeddings, then discarded the splitter result if embeddings failed, leaving messages unprocessed so the next tick would re-run splitter on the same chunk. During an embeddings-provider outage this drained tokens continuously. Persistently-failing chunks now enter an exponential-backoff cooldown (5m → 10m → 20m, capped at 6h) so the same chunk is not reattempted dozens of times per minute.
- **Backfill interval default raised from 1s to 1m** — topic chunking is background work with no real-time requirement. The 1-second default meant that during the outage above the retry loop fired roughly as fast as the LLM could respond, amplifying the cost of the two bugs above. The code-level `DefaultBackfillInterval` was already `1 * time.Minute`; this only brings the YAML default in line.

### Fixed
- **Text MIME type normalization** — Gemini API rejects `text/x-web-markdown` with 400 error. Unsupported text types (markdown, etc.) are now normalized to `text/plain` when sending to LLM, allowing `.md` files to be processed correctly.

## [0.6.1] - 2026-02-04

### Fixed
- **Tool error handling** — `manage_memory` and `manage_people` tools now return proper Go errors instead of masking failures in result strings. Previously, errors were returned as "Error..." text with nil error, causing no error logs in laplace.go and incorrect tool results sent to LLM. All 21 affected tests updated to check for errors correctly.
- **Reranker fact ID hallucination** — Flash confused `[Fact:N]` format with `[Person:N]`/`[Topic:N]` and returned Fact IDs as Person/Topic IDs. Fixed by using compact format for reranker (facts without `[Fact:N]` prefix) while preserving full format for Archivist which needs IDs for update/delete operations.
- **Codecov configuration** — fixed ignore patterns to use full path matching instead of substring, properly excluded test packages from coverage calculations.

### Internal
- **Test coverage improvements** — significantly improved coverage across all packages: agent (45.3% → 95.5%), markdown (76.6% → 81.8%), telegram (71.1% → 93.2%), files (59.8% → 88.4%), bot (80.3% → 87.5%), web (67.6% → 81.9%), RAG (82.8%), agentlog (40%+ added).
- **User data isolation tests** — added comprehensive `Test*UserIsolation` test suites across all storage packages to prove users cannot access or modify each other's data.
- **Agent refactoring** — extracted `agent/context` package for SharedContext, added agent builders for easier testing, refactored reranker into focused modules (candidates, filtering, parsing, fallbacks, tool_executor).
- **Bot test split** — split monolithic `bot_test.go` into focused files (bot_handlers_test.go, bot_errors_test.go, bot_forwarded_people_test.go, bot_rpc_test.go) for better maintainability.
- **RAG testability** — extracted `rag.MaintenanceService` interface, removed deprecated `rag.NewService` in favor of builder pattern, added SSE testing helpers.
- **Testing guide** — added `docs/TESTING.md` with comprehensive testing standards, patterns, and available helpers.
- **Config tests** — added 400+ lines of config tests for coverage improvement.
- **Migration tests** — refactored for clarity and added backfill test.

## [0.6.0] - 2026-02-02

**Long Context RAG release: Files now part of memory.**

### Added
- **Artifacts system** — files sent to the bot (images, voice, PDF, video, documents) are now saved as artifacts with semantic metadata. Files are deduplicated by SHA256 hash, processed in background by Extractor agent (Gemini Flash), and indexed for RAG retrieval. The bot can now answer questions like "what was in that PDF?" with full file content passed to context.
- **Extractor agent** — new LLM-powered agent that extracts structured metadata from files (summary, keywords, entities, rag_hints). Uses Gemini Flash multimodal capabilities for ~500 tokens output per artifact. Processes files in background with configurable concurrency and retry logic.
- **Artifacts web UI** — new `/ui/debug/artifacts` page showing all stored artifacts with metadata, state, and processing status. Includes file type icons, search filters, and detailed view.
- **Video notes support** — video messages (circles) are now saved as artifacts and included in RAG retrieval.
- **Compact history markers for text documents** — text files (.txt, .md, etc.) now save as `📄 filename (artifact:N)` in message history instead of full content. Full document content is still passed to LLM via `LLMParts` and enricher/reranker see it through `rawQuery`. This prevents topic summaries from bloating with file contents during session archival while maintaining full RAG retrieval capabilities.
- **Gemini file format validation** — files with unsupported MIME types are rejected before processing to prevent API errors. Supported types: images (PNG, JPEG, WebP, HEIC), PDF, videos (MP4, MOV, AVI, WebM), all audio types, and text files.

### Changed
- **Reranker with artifacts** — RAG candidates now include artifacts alongside topics and people. Reranker shows artifact summaries with similarity scores and selects relevant artifacts for full context loading. Configurable per-type limits (topics, people, artifacts).
- **Reranker candidate display with scores** — reranker candidates now show similarity scores in format `[Artifact:123] (0.68) pdf: "api-docs.pdf" | api, rest | Entities: OAuth2, JWT | Summary...`. Makes reranker decisions more transparent.
- **Laplace agent error logging** — agent execution logs are now saved even when errors occur (e.g., "max empty response retries reached"). Previously, when Laplace execution failed, the `LogExecution()` call was skipped, causing DebugRequestBody/DebugResponseBody to be lost forever. This made debugging production issues impossible since the actual request/response bodies were not logged. The agent now returns a partial Response with Error field set, allowing full logging including all conversation turns and API request/response bodies for failed executions.
- **Empty LLM response handling** — empty responses (0 completion tokens) now trigger automatic retry with cache-busting nonce. Fixed bug where empty responses with finish_reason: "stop" were treated as valid.
- **Reranker XML prompts** — reranker prompts now use XML tags for better structure compliance with Google prompting guidelines.
- **Config refactor** — artifact extraction settings moved from `artifacts.*` to `agents.extractor.*`. Artifacts config now only contains storage settings (enabled, storage_path, allowed_types).

### Removed
- **Yandex SpeechKit** — legacy speech recognition client removed. All voice messages now use native Gemini multimodal understanding.

## [0.5.4] - 2026-01-27

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
- **Docker image cleanup** — disabled automatic deletion of container images from GHCR. The cleanup job was incorrectly removing tagged images, causing "manifest not found" errors when pulling versioned tags.

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

[Unreleased]: https://github.com/runixer/laplaced/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/runixer/laplaced/compare/v0.7.2...v0.8.0
[0.7.2]: https://github.com/runixer/laplaced/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/runixer/laplaced/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/runixer/laplaced/compare/v0.6.2...v0.7.0
[0.6.2]: https://github.com/runixer/laplaced/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/runixer/laplaced/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/runixer/laplaced/compare/v0.5.4...v0.6.0
[0.5.4]: https://github.com/runixer/laplaced/compare/v0.5.3...v0.5.4
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
