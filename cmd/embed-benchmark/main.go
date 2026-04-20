// Package main runs a retrieval-quality benchmark for embedding models on
// real production data. It samples real user messages that look like
// retrieval queries (help/remember/what-about-X), then for each candidate
// embedding configuration:
//
//   - embeds all topic summaries in the haystack
//   - embeds the test queries
//   - computes Recall@k where the "gold" topic is the one containing the
//     original message (via topic_id)
//
// Also reports wall time and token counts — useful for sizing the full
// production re-embedding job.
//
// Usage:
//
//	go run ./cmd/embed-benchmark --queries=50 --db=data/prod/laplaced.db
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runixer/laplaced/internal/app"
	"github.com/runixer/laplaced/internal/config"
	_ "modernc.org/sqlite"
)

const (
	openrouterBase = "https://openrouter.ai/api/v1"
	parallelism    = 8
	chunkSize      = 32
)

type topic struct {
	ID      int64
	Summary string
}

type query struct {
	LogID   int64
	Content string
	Golds   []int64 // topic IDs the reranker judged relevant for this query
}

type variant struct {
	Name   string
	Model  string
	Dim    int
	Prefix bool
}

var variants = []variant{
	{"v1-3072-baseline", "google/gemini-embedding-001", 3072, false},
	{"v2-768-plain", "google/gemini-embedding-2-preview", 768, false},
	{"v2-1536-plain", "google/gemini-embedding-2-preview", 1536, false},
	{"v2-3072-plain", "google/gemini-embedding-2-preview", 3072, false},
	{"v2-768-prefix", "google/gemini-embedding-2-preview", 768, true},
	{"v2-1536-prefix", "google/gemini-embedding-2-preview", 1536, true},
	{"v2-3072-prefix", "google/gemini-embedding-2-preview", 3072, true},
}

type stats struct {
	r1, r5, r20, r50 int
	mrr              float64
	goldSim          []float64
	total            int
	wallMs           float64
	promptTokens     int64
}

var httpClient = &http.Client{Timeout: 120 * time.Second}

func main() {
	nQueries := flag.Int("queries", 50, "number of real user queries to test")
	dbPath := flag.String("db", "data/prod/laplaced.db", "sqlite db path")
	limitTopics := flag.Int("limit-topics", 0, "cap topics in haystack (0 = all)")
	flag.Parse()

	if err := app.LoadEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: .env load: %v\n", err)
	}
	cfg, err := config.Load("")
	if err != nil {
		die("load config: %v", err)
	}
	apiKey := cfg.OpenRouter.APIKey
	if apiKey == "" {
		die("LAPLACED_OPENROUTER_API_KEY not set")
	}

	ctx := context.Background()

	// 1. Load haystack
	topics, err := loadTopics(*dbPath, *limitTopics)
	if err != nil {
		die("load topics: %v", err)
	}
	fmt.Printf("Haystack: %d topics\n", len(topics))

	// 2. Load real queries
	queries, err := loadQueries(*dbPath, *nQueries)
	if err != nil {
		die("load queries: %v", err)
	}
	fmt.Printf("Queries:  %d real user messages\n", len(queries))

	// Build topic-id → index in haystack
	idToIdx := make(map[int64]int, len(topics))
	for i, t := range topics {
		idToIdx[t.ID] = i
	}

	// Filter queries where at least one gold topic is still in the haystack.
	kept := queries[:0]
	for _, q := range queries {
		keepGolds := q.Golds[:0]
		for _, g := range q.Golds {
			if _, ok := idToIdx[g]; ok {
				keepGolds = append(keepGolds, g)
			}
		}
		if len(keepGolds) > 0 {
			q.Golds = keepGolds
			kept = append(kept, q)
		}
	}
	queries = kept
	fmt.Printf("          %d queries with gold in haystack (mean golds/query = %.1f)\n\n",
		len(queries), meanGolds(queries))

	if len(queries) < 10 {
		die("too few queries (%d), increase --queries", len(queries))
	}

	// 3. Run each variant
	results := make(map[string]*stats)
	for _, v := range variants {
		fmt.Printf("Variant %-20s  (%s dim=%d prefix=%v)\n", v.Name, shortModel(v.Model), v.Dim, v.Prefix)
		s, err := evalVariant(ctx, apiKey, topics, queries, idToIdx, v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", err)
			continue
		}
		results[v.Name] = s
		fmt.Printf("  wall=%.1fs  prompt_tokens=%d\n", s.wallMs/1000, s.promptTokens)
	}

	// 4. Print results table
	fmt.Println("\n=== RESULTS ===")
	fmt.Printf("%-22s  %6s  %6s  %6s  %6s  %8s  %10s  %8s  %10s\n",
		"config", "R@1", "R@5", "R@20", "R@50", "MRR", "gold_sim", "wall_s", "tokens")
	fmt.Println(strings.Repeat("-", 100))
	for _, v := range variants {
		s, ok := results[v.Name]
		if !ok {
			continue
		}
		fmt.Printf("%-22s  %6.3f  %6.3f  %6.3f  %6.3f  %8.4f  %10.4f  %8.1f  %10d\n",
			v.Name,
			float64(s.r1)/float64(s.total),
			float64(s.r5)/float64(s.total),
			float64(s.r20)/float64(s.total),
			float64(s.r50)/float64(s.total),
			s.mrr,
			mean(s.goldSim),
			s.wallMs/1000,
			s.promptTokens,
		)
	}

	// Cost extrapolation
	if base, ok := results["v2-3072-plain"]; ok {
		fmt.Println("\n=== MIGRATION COST ESTIMATE (v2-3072-plain as reference) ===")
		fmt.Printf("Tokens for %d topics: %d\n", len(topics), base.promptTokens)
		// Price assumption — see openrouter.ai/docs; print raw tokens, price estimate below.
		fmt.Printf("Queries were tiny — they add a negligible amount to production embedding cost.\n")
		fmt.Printf("Assumed price: $0.15 / 1M input tokens (Gemini embedding 001 pricing).\n")
		fmt.Printf("Estimated cost for this haystack: $%.3f\n", float64(base.promptTokens)/1e6*0.15)
	}

	fmt.Println("\nLegend:")
	fmt.Println("  R@k   — fraction of queries where gold topic_id is in top-k matches.")
	fmt.Println("  MRR   — mean reciprocal rank of gold (1.0 = always first).")
	fmt.Println("  wall_s — total time to embed haystack + queries (parallelism = 8, chunk = 32).")
}

func loadTopics(dbPath string, limit int) ([]topic, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q := `SELECT id, summary FROM topics WHERE summary IS NOT NULL AND LENGTH(summary) > 30 ORDER BY id`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := db.QueryContext(context.Background(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []topic
	for rows.Next() {
		var t topic
		if err := rows.Scan(&t.ID, &t.Summary); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// loadQueries reads real RAG retrieval cases from reranker_logs: the enriched
// query that went into vector search, plus the topic IDs the reranker LLM
// ultimately judged relevant (selected_ids_json). These are the closest we
// have to gold-standard retrieval labels in production.
func loadQueries(dbPath string, n int) ([]query, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), `
		SELECT id, enriched_query, selected_ids_json FROM reranker_logs
		WHERE enriched_query IS NOT NULL AND LENGTH(enriched_query) > 10
		  AND selected_ids_json IS NOT NULL
		  AND selected_ids_json NOT IN ('[]', '{}', '')
		ORDER BY created_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []query
	for rows.Next() {
		var q query
		var selectedJSON string
		if err := rows.Scan(&q.LogID, &q.Content, &selectedJSON); err != nil {
			return nil, err
		}
		golds, err := parseSelectedIDs(selectedJSON)
		if err != nil {
			continue // skip malformed
		}
		if len(golds) == 0 {
			continue
		}
		q.Golds = golds
		out = append(out, q)
	}
	return out, rows.Err()
}

// parseSelectedIDs handles two formats: new (v0.4.2+) list of {id,reason,...}
// and legacy {"topics":[id,...]} wrapper.
func parseSelectedIDs(raw string) ([]int64, error) {
	// Try new format first.
	var newFmt []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &newFmt); err == nil && len(newFmt) > 0 {
		out := make([]int64, 0, len(newFmt))
		for _, item := range newFmt {
			if item.ID > 0 {
				out = append(out, item.ID)
			}
		}
		return out, nil
	}
	// Try legacy wrapper format.
	var legacy struct {
		Topics []int64 `json:"topics"`
	}
	if err := json.Unmarshal([]byte(raw), &legacy); err == nil {
		return legacy.Topics, nil
	}
	return nil, fmt.Errorf("unrecognized format")
}

func evalVariant(ctx context.Context, apiKey string, topics []topic, queries []query, idToIdx map[int64]int, v variant) (*stats, error) {
	docs := make([]string, len(topics))
	qs := make([]string, len(queries))
	for i, t := range topics {
		if v.Prefix {
			docs[i] = "title: none | text: " + t.Summary
		} else {
			docs[i] = t.Summary
		}
	}
	for i, q := range queries {
		if v.Prefix {
			qs[i] = "task: search result | query: " + q.Content
		} else {
			qs[i] = q.Content
		}
	}

	start := time.Now()
	var promptTokens atomic.Int64
	docVecs, err := embedBatched(ctx, apiKey, docs, v, &promptTokens)
	if err != nil {
		return nil, fmt.Errorf("embed docs: %w", err)
	}
	queryVecs, err := embedBatched(ctx, apiKey, qs, v, &promptTokens)
	if err != nil {
		return nil, fmt.Errorf("embed queries: %w", err)
	}
	wall := time.Since(start)

	s := &stats{total: len(queries), wallMs: float64(wall.Milliseconds()), promptTokens: promptTokens.Load()}
	for i, q := range queries {
		goldIdxSet := make(map[int]bool, len(q.Golds))
		for _, g := range q.Golds {
			if idx, ok := idToIdx[g]; ok {
				goldIdxSet[idx] = true
			}
		}
		if len(goldIdxSet) == 0 {
			continue
		}
		qv := queryVecs[i]
		type idxSim struct {
			idx int
			sim float64
		}
		ranked := make([]idxSim, len(topics))
		for j := range topics {
			ranked[j] = idxSim{j, cosine(qv, docVecs[j])}
		}
		sort.Slice(ranked, func(a, b int) bool { return ranked[a].sim > ranked[b].sim })
		firstGoldRank := -1
		for rank, r := range ranked {
			if goldIdxSet[r.idx] {
				if firstGoldRank == -1 {
					firstGoldRank = rank
					s.goldSim = append(s.goldSim, r.sim)
				}
			}
		}
		if firstGoldRank >= 0 {
			s.mrr += 1.0 / float64(firstGoldRank+1)
			if firstGoldRank < 1 {
				s.r1++
			}
			if firstGoldRank < 5 {
				s.r5++
			}
			if firstGoldRank < 20 {
				s.r20++
			}
			if firstGoldRank < 50 {
				s.r50++
			}
		}
	}
	if s.total > 0 {
		s.mrr /= float64(s.total)
	}
	return s, nil
}

func meanGolds(queries []query) float64 {
	if len(queries) == 0 {
		return 0
	}
	var sum int
	for _, q := range queries {
		sum += len(q.Golds)
	}
	return float64(sum) / float64(len(queries))
}

func embedBatched(ctx context.Context, apiKey string, inputs []string, v variant, tokens *atomic.Int64) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for start := 0; start < len(inputs); start += chunkSize {
		end := start + chunkSize
		if end > len(inputs) {
			end = len(inputs)
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(start, end int) {
			defer wg.Done()
			defer func() { <-sem }()
			vecs, promptTokens, err := embedOnce(ctx, apiKey, inputs[start:end], v)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			tokens.Add(promptTokens)
			for i, vec := range vecs {
				out[start+i] = vec
			}
		}(start, end)
	}
	wg.Wait()
	return out, firstErr
}

func embedOnce(ctx context.Context, apiKey string, inputs []string, v variant) ([][]float32, int64, error) {
	payload := map[string]any{
		"model":      v.Model,
		"input":      inputs,
		"dimensions": v.Dim,
	}
	body, _ := json.Marshal(payload)
	var r struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
		Error *struct{ Message string } `json:"error"`
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", openrouterBase+"/embeddings", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			if attempt == 3 {
				return nil, 0, err
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, 0, fmt.Errorf("unmarshal: %w (body=%s)", err, truncate(string(b), 300))
		}
		if r.Error != nil || len(r.Data) != len(inputs) {
			if attempt == 3 {
				msg := ""
				if r.Error != nil {
					msg = r.Error.Message
				}
				return nil, 0, fmt.Errorf("exhausted retries (err=%q data_len=%d want=%d body=%s)", msg, len(r.Data), len(inputs), truncate(string(b), 300))
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		out := make([][]float32, len(inputs))
		for _, item := range r.Data {
			if item.Index < 0 || item.Index >= len(inputs) {
				return nil, 0, fmt.Errorf("bad index %d", item.Index)
			}
			out[item.Index] = item.Embedding
		}
		return out, int64(r.Usage.PromptTokens), nil
	}
	return nil, 0, fmt.Errorf("exhausted retries (unreachable)")
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func shortModel(m string) string {
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		return m[idx+1:]
	}
	return m
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
