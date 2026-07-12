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
- `--verbose` writes pipeline logs to stderr.

Every JSONL row includes the official `question_id` and `hypothesis` fields plus:

- model variant and backend;
- reference answer and question type;
- ingestion and answer durations;
- token and cost counters;
- final facts;
- fact add/update/delete history.

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

## Current limitations

- `question_date` is recorded but not injected as the process clock. Temporal prompts may therefore observe the real current date. Session and topic timestamps still come from the dataset.
- There is no ingestion cache. Every run repeats Splitter, Archivist, embedding, and consolidation work.
- Cost fields from a local chat backend may reflect configured pricing rather than actual local cost. Cloud embedding costs remain meaningful.
- The harness does not run the official LLM judge. Pass the generated JSONL to LongMemEval's evaluation scripts.
- A correct final answer does not guarantee correct memory state. Inspect `facts` and `fact_changes` when analyzing regressions.

## Recommended workflow

1. Start with one oracle case.
2. Compare the final facts and mutation history, not only the answer.
3. Use matrix mode for paired model experiments.
4. Repeat promising changes on several question types.
5. Run the full-history dataset only after the oracle path is stable.
