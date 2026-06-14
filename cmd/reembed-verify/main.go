// Package main is a read-only diagnostic for an embedding-model rename.
//
// When the configured embedding model string changes (e.g. graduating
// "google/gemini-embedding-2-preview" → "google/gemini-embedding-2"), the
// startup re-embed (internal/rag/reembed.go) re-generates every stored vector
// because the `embedding_version` tag no longer matches. This tool answers,
// WITHOUT touching the DB, three questions that the in-place re-embed can't:
//
//   - identity: is the new model the same vector space? It re-embeds each
//     row's content and reports the cosine(old, new) distribution.
//   - speed:    wall time at a given --parallelism / --batch, so we can size
//     the real migration and experiment with faster knobs.
//   - cost:     prompt tokens consumed (+ a $ estimate via --price-per-1m).
//
// It reuses the production internal/llm client so provider routing and request
// shape match the real re-embed path. Nothing is written back.
//
// Usage:
//
//	go run ./cmd/reembed-verify --db data/prod/laplaced.db
//	go run ./cmd/reembed-verify --parallelism 16 --batch 64 --limit 500
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runixer/laplaced/internal/app"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/llm"
	_ "modernc.org/sqlite"
)

// row is one entity to re-embed: its content and the vector currently on disk.
type row struct {
	id      int64
	content string
	oldVec  []float32
}

// tableSpec describes how to pull (id, content, embedding) for one table.
type tableSpec struct {
	kind  string
	query string
	// compose turns the scanned columns into the embedding input text. For
	// most tables that is a single column; people compose name+aliases+bio.
	scan func(rows *sql.Rows) (row, bool, error)
}

func main() {
	dbPath := flag.String("db", "data/prod/laplaced.db", "sqlite db path (read-only)")
	model := flag.String("model", "google/gemini-embedding-2", "new embedding model to test")
	dim := flag.Int("dim", 1536, "embedding dimensions")
	parallelism := flag.Int("parallelism", 8, "concurrent embedding requests")
	batch := flag.Int("batch", 32, "inputs per embedding request")
	limit := flag.Int("limit", 0, "cap rows per table (0 = all)")
	driftThreshold := flag.Float64("drift-threshold", 0.9999, "count rows with cosine(old,new) below this")
	pricePerM := flag.Float64("price-per-1m", 0.15, "USD per 1M input tokens, for the cost estimate")
	tablesCSV := flag.String("tables", "topics,facts,people,artifacts", "comma-separated tables to check")
	flag.Parse()

	if err := app.LoadEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: .env load: %v\n", err)
	}
	cfg, err := config.Load("")
	if err != nil {
		die("load config: %v", err)
	}
	if cfg.LLM.APIKey == "" {
		die("LAPLACED_LLM_API_KEY not set")
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := llm.NewClient(logger, cfg.LLM.APIKey, cfg.LLM.ProxyURL, cfg.LLM.BaseURL, cfg.LLM.Provider.ToRouting())
	if err != nil {
		die("new llm client: %v", err)
	}

	db, err := sql.Open("sqlite", *dbPath+"?mode=ro")
	if err != nil {
		die("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	specs := buildSpecs(*tablesCSV, *limit)

	fmt.Printf("model=%s dim=%d parallelism=%d batch=%d db=%s\n\n",
		*model, *dim, *parallelism, *batch, *dbPath)

	var grandCos []float64
	var grandTokens int64
	var grandRows int
	var grandWall time.Duration

	for _, spec := range specs {
		rows, err := loadRows(ctx, db, spec)
		if err != nil {
			die("load %s: %v", spec.kind, err)
		}
		if len(rows) == 0 {
			fmt.Printf("%-10s no rows\n", spec.kind)
			continue
		}

		start := time.Now()
		var tokens atomic.Int64
		newVecs, err := embedAll(ctx, client, rows, *model, *dim, *parallelism, *batch, &tokens)
		wall := time.Since(start)
		if err != nil {
			die("embed %s: %v", spec.kind, err)
		}

		cosines := make([]float64, 0, len(rows))
		for i := range rows {
			cosines = append(cosines, cosine(rows[i].oldVec, newVecs[i]))
		}

		printTableReport(spec.kind, cosines, wall, tokens.Load(), *driftThreshold)

		grandCos = append(grandCos, cosines...)
		grandTokens += tokens.Load()
		grandRows += len(rows)
		grandWall += wall
	}

	if grandRows > 0 {
		fmt.Println()
		printTableReport("ALL", grandCos, grandWall, grandTokens, *driftThreshold)
		fmt.Printf("\nestimated cost: %d tokens × $%.2f/1M = $%.4f\n",
			grandTokens, *pricePerM, float64(grandTokens)/1e6**pricePerM)
		fmt.Printf("(wall above is summed across tables; a single startup run does them sequentially too)\n")
	}
}

func buildSpecs(tablesCSV string, limit int) []tableSpec {
	lim := ""
	if limit > 0 {
		lim = fmt.Sprintf(" LIMIT %d", limit)
	}
	all := map[string]tableSpec{
		"topics": {
			kind:  "topics",
			query: "SELECT id, COALESCE(summary,''), embedding FROM topics WHERE embedding IS NOT NULL ORDER BY id" + lim,
			scan:  scanSimple,
		},
		"facts": {
			kind:  "facts",
			query: "SELECT id, COALESCE(content,''), embedding FROM structured_facts WHERE embedding IS NOT NULL ORDER BY id" + lim,
			scan:  scanSimple,
		},
		"people": {
			kind:  "people",
			query: "SELECT id, display_name, username, aliases, bio, embedding FROM people WHERE embedding IS NOT NULL ORDER BY id" + lim,
			scan:  scanPerson,
		},
		"artifacts": {
			kind:  "artifacts",
			query: "SELECT id, COALESCE(summary,''), embedding FROM artifacts WHERE embedding IS NOT NULL ORDER BY id" + lim,
			scan:  scanSimple,
		},
	}
	var out []tableSpec
	for _, name := range splitCSV(tablesCSV) {
		if s, ok := all[name]; ok {
			out = append(out, s)
		} else {
			die("unknown table %q", name)
		}
	}
	return out
}

func loadRows(ctx context.Context, db *sql.DB, spec tableSpec) ([]row, error) {
	rows, err := db.QueryContext(ctx, spec.query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, keep, err := spec.scan(rows)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// scanSimple reads (id, content, embedding) and skips empty content — matching
// reembedSelect, so the row count here equals the real migration's.
func scanSimple(rows *sql.Rows) (row, bool, error) {
	var r row
	var embBytes []byte
	if err := rows.Scan(&r.id, &r.content, &embBytes); err != nil {
		return r, false, err
	}
	if r.content == "" {
		return r, false, nil
	}
	if err := json.Unmarshal(embBytes, &r.oldVec); err != nil {
		return r, false, fmt.Errorf("parse embedding id=%d: %w", r.id, err)
	}
	return r, true, nil
}

// scanPerson mirrors storage.ComposePersonEmbeddingText: display name +
// username + aliases (JSON array) + bio.
func scanPerson(rows *sql.Rows) (row, bool, error) {
	var r row
	var name, username, aliasesJSON, bio *string
	var embBytes []byte
	if err := rows.Scan(&r.id, &name, &username, &aliasesJSON, &bio, &embBytes); err != nil {
		return r, false, err
	}
	var s string
	if name != nil {
		s = *name
	}
	if username != nil && *username != "" {
		s += " " + *username
	}
	if aliasesJSON != nil && *aliasesJSON != "" && *aliasesJSON != "null" {
		var aliases []string
		if err := json.Unmarshal([]byte(*aliasesJSON), &aliases); err == nil {
			for _, a := range aliases {
				if a != "" {
					s += " " + a
				}
			}
		}
	}
	if bio != nil && *bio != "" {
		s += " " + *bio
	}
	r.content = s
	if r.content == "" {
		return r, false, nil
	}
	if err := json.Unmarshal(embBytes, &r.oldVec); err != nil {
		return r, false, fmt.Errorf("parse embedding id=%d: %w", r.id, err)
	}
	return r, true, nil
}

// embedAll re-embeds every row's content, returning vectors aligned to rows.
func embedAll(ctx context.Context, client llm.Client, rows []row, model string, dim, parallelism, batch int, tokens *atomic.Int64) ([][]float32, error) {
	out := make([][]float32, len(rows))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for start := 0; start < len(rows); start += batch {
		end := start + batch
		if end > len(rows) {
			end = len(rows)
		}
		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(start, end int) {
			defer wg.Done()
			defer func() { <-sem }()

			inputs := make([]string, end-start)
			for i := start; i < end; i++ {
				inputs[i-start] = rows[i].content
			}
			resp, err := client.CreateEmbeddings(ctx, llm.EmbeddingRequest{
				Model:      model,
				Input:      inputs,
				Dimensions: dim,
			})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if len(resp.Data) != len(inputs) {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("expected %d results, got %d", len(inputs), len(resp.Data))
				}
				mu.Unlock()
				return
			}
			tokens.Add(int64(resp.Usage.PromptTokens))
			for _, item := range resp.Data {
				if item.Index < 0 || item.Index >= len(inputs) {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("bad index %d", item.Index)
					}
					mu.Unlock()
					return
				}
				out[start+item.Index] = item.Embedding
			}
		}(start, end)
	}
	wg.Wait()
	return out, firstErr
}

func printTableReport(kind string, cosines []float64, wall time.Duration, tokens int64, driftThreshold float64) {
	if len(cosines) == 0 {
		return
	}
	sorted := append([]float64(nil), cosines...)
	sort.Float64s(sorted)
	drifted := 0
	for _, c := range sorted {
		if c < driftThreshold {
			drifted++
		}
	}
	fmt.Printf("%-10s n=%-6d cos[min=%.6f p1=%.6f p50=%.6f mean=%.6f max=%.6f]  drift(<%.4f)=%d  wall=%.1fs tokens=%d\n",
		kind, len(sorted),
		sorted[0], percentile(sorted, 0.01), percentile(sorted, 0.50), mean(sorted), sorted[len(sorted)-1],
		driftThreshold, drifted,
		wall.Seconds(), tokens,
	)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
