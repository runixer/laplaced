# LongMemEval harness

`cmd/longmemeval` runs LongMemEval cases through Laplaced's production memory pipeline:

1. import timestamped user and assistant messages;
2. process every dataset session through Splitter, Merger, Archivist, and embeddings;
3. retrieve memory through Enricher and Reranker;
4. answer through Laplace;
5. write judge-compatible JSONL with diagnostics.

The command uses a temporary SQLite database and does not start background workers. Each dataset session is force-processed separately so its boundary is preserved.

## Dataset

Download an official LongMemEval JSON file locally. Dataset files and evaluation outputs should remain under the gitignored `data/` directory.

## Basic run

```bash
go run ./cmd/longmemeval \
  --dataset data/longmemeval_oracle.json \
  --mode oracle \
  --limit 10 \
  --output data/longmemeval-results.jsonl
```

Useful flags:

- `--case <question_id>` runs one case;
- `--mode oracle` imports only `answer_session_ids`;
- `--mode full` imports every session present in the input file;
- `--limit 0` removes the case limit;
- `--judge` evaluates each answer with the official LongMemEval V1 judge protocol;
- `--judge-model` selects the judge model (default `gpt-4o-2024-08-06`);
- `--cache-dir <path>` reuses immutable per-case ingestion snapshots;
- `--verbose` writes pipeline logs to stderr.

Every JSONL row includes the official `question_id` and `hypothesis` fields plus:

- model variant and backend;
- reference answer and question type;
- ingestion and answer durations;
- token and cost counters;
- final facts;
- fact add/update/delete history;
- optional official judge label, raw response, token usage, cost, and latency;
- evidence-session recall for candidates, post-reranker results, and final answer context.

The additional fields do not prevent the official judge from reading the file.

## Local chat with cloud embeddings

A local OpenAI-compatible endpoint can handle all chat completions while the configured default client continues to provide embeddings:

```bash
go run ./cmd/longmemeval \
  --dataset data/longmemeval_oracle.json \
  --case <question_id> \
  --chat-base-url http://localhost:8080 \
  --chat-model local-model \
  --output data/local-result.jsonl
```

The routing is explicit:

```text
chat completions and streams → local endpoint
embeddings                  → configured default endpoint
```

This supports local servers that do not implement `/v1/embeddings`.

### llama.cpp thinking

For chat templates that use `enable_thinking`, add:

```text
--chat-thinking
```

The flag sends:

```json
{
  "chat_template_kwargs": {
    "enable_thinking": true
  }
}
```

It does not use OpenRouter's `reasoning.effort`; the two controls are not interchangeable. Verify the target server's chat template before enabling this option. Thinking can greatly increase completion tokens and latency.

## Matrix mode

Matrix mode compares multiple model configurations with independent temporary databases and runtimes.

Create a local YAML file, preferably under `data/`:

```yaml
variants:
  - name: configured-cloud

  - name: local
    chat_base_url: http://localhost:8080
    chat_model: local-model

  - name: local-thinking
    chat_base_url: http://localhost:8080
    chat_model: local-model
    chat_thinking: true
```

Run variants concurrently:

```bash
go run ./cmd/longmemeval \
  --dataset data/longmemeval_oracle.json \
  --case <question_id> \
  --matrix data/longmemeval-matrix.yaml \
  --parallel 2 \
  --output data/matrix-results.jsonl
```

`--parallel` limits concurrent variants. Cases within one variant remain sequential so their LLM traffic and result order are predictable. Each variant has its own SQLite database, vector state, and service graph. JSONL writes are serialized.

Matrix mode cannot be combined with command-line chat override flags; put those settings in the matrix file instead.

### Per-agent model routing

Matrix variants can route ingestion, retrieval, and answer agents independently:

```yaml
variants:
  - name: mixed
    agents:
      splitter:
        base_url: http://localhost:8081
        model: local-model
      archivist:
        base_url: http://localhost:8082
        model: local-model
        chat_template_thinking: true
      merger:
        base_url: http://localhost:8081
        model: local-model
      enricher:
        base_url: http://localhost:8083
        model: local-model
      reranker:
        base_url: http://localhost:8083
        model: local-model
      answerer:
        base_url: http://localhost:8084
        model: stronger-local-model
```

Supported roles are `splitter`, `archivist`, `merger`, `enricher`, `reranker`, and `answerer`. Omitted roles use the configured production client and model. `chat_template_thinking` controls the local chat template's `enable_thinking`; it is distinct from provider reasoning effort. Legacy `chat_*` fields remain available as shorthand for routing all six roles together, but cannot be combined with `agents` in the same variant.

## Judge

Add `--judge` to score generated answers in the same run:

```bash
go run ./cmd/longmemeval \
  --dataset data/longmemeval_s_cleaned.json \
  --mode full \
  --judge \
  --output data/longmemeval-judged.jsonl
```

The native judge reproduces the upstream LongMemEval V1 task-specific prompts and response parsing. It uses the configured default LLM endpoint even when the evaluated chat agents use `--chat-base-url`, keeping the judge independent from local model variants. A custom judge model makes the result non-comparable with the official GPT-4o metric.

## Ingestion cache

Use `--cache-dir` to avoid repeating Splitter, Merger, Archivist, and embedding work for unchanged cases:

```bash
go run ./cmd/longmemeval \
  --dataset data/longmemeval_s_cleaned.json \
  --mode full \
  --cache-dir data/longmemeval-cache \
  --output data/longmemeval-results.jsonl
```

Each cache entry is a closed, WAL-checkpointed SQLite snapshot created after ingestion and before answering, plus a metadata sidecar mapping dataset sessions to imported message IDs. The harness always copies the database to a writable temporary path, so answer-time writes cannot mutate cached memory. Cache keys include the selected sessions, ingestion models, embedding configuration, memory/RAG configuration, and an ingestion pipeline version. Cache hits report zero ingestion tokens and cost because no ingestion API calls occur in that run.

## Current limitations

- Cost fields from a local chat backend may reflect configured pricing rather than actual local cost. Cloud embedding costs remain meaningful.

- A correct final answer does not guarantee correct memory state. Inspect `facts` and `fact_changes` when analyzing regressions.

## Offline reports and paired comparison

Build a summary without loading the dataset or calling an LLM:

```bash
go run ./cmd/longmemeval \
  --report-input data/results.jsonl \
  --report-format markdown \
  --output data/report.md
```

Compare two judged runs case-by-case:

```bash
go run ./cmd/longmemeval \
  --compare-baseline data/baseline.jsonl \
  --compare-candidate data/candidate.jsonl \
  --report-format json \
  --output data/comparison.json
```

Comparison requires identical variant/question keys, question types, modes, and judge models. It reports accuracy and evidence-recall deltas, answer-context and cost changes, plus explicit `fail_to_pass` and `pass_to_fail` case lists. Results created before evidence tracing are marked as having zero retrieval cases rather than being interpreted as zero recall.

## Recommended workflow

1. Start with one oracle case.
2. Compare the final facts and mutation history, not only the answer.
3. Use matrix mode for paired model experiments.
4. Repeat promising changes on several question types.
5. Run the full-history dataset only after the oracle path is stable.
