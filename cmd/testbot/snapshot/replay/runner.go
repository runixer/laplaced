package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/storage"
)

// BuildRequestFunc is the per-agent hook that turns a parsed snapshot trace
// span (raw bytes are loaded by the runner) into an agent.Request the
// production agent can consume. It also returns the original output for
// later side-by-side comparison, plus any candidates we couldn't resolve.
type BuildRequestFunc func(
	ctx context.Context,
	traceID string,
	traceJSON []byte,
) (BuildResult, error)

// BuildResult bundles everything the runner needs after reconstruction:
// the request to dispatch, the input snapshot for the JSON record, the
// captured original output to compare against, and any DB lookup misses.
type BuildResult struct {
	UserID   storage.ScopeID
	Request  *agent.Request
	Input    ReplayInput
	Original AgentOutput
	Skipped  []SkippedLookup
}

// ExtractReplayOutputFunc converts an agent.Response back into the comparable
// AgentOutput shape. Each agent surfaces results differently
// (reranker uses Response.Structured of type *reranker.Result; enricher uses
// Response.Content; etc.), so this hook stays per-agent.
type ExtractReplayOutputFunc func(resp *agent.Response) AgentOutput

// Config bundles run parameters. Concurrency=0 means serial. Variant is a
// label written to the result JSON ("baseline", "no_match_patch", …) and
// determines the output subdirectory.
type Config struct {
	SnapshotDir string
	OutputDir   string // resolved path: <SnapshotDir>/replay/<Variant> by default
	Variant     string
	Agent       string // "reranker"
	Concurrency int
	Logger      *slog.Logger
}

// Run iterates `<snapshot>/traces/*.json`, dispatches each through `agent`,
// and writes per-trace JSON + summary.json. It never aborts on per-trace
// errors — the goal is "ship a partial replay even if 5/50 traces fail".
//
// The agent argument is the production agent (e.g. services.RerankerAgent),
// not a mock. Tool calls during agent.Execute will hit the snapshot DB via
// whatever MessageRepository the agent was wired with.
func Run(
	ctx context.Context,
	cfg Config,
	build BuildRequestFunc,
	extract ExtractReplayOutputFunc,
	ag agent.Agent,
) (Summary, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Variant == "" {
		cfg.Variant = "baseline"
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = filepath.Join(cfg.SnapshotDir, "replay", cfg.Variant)
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o750); err != nil {
		return Summary{}, fmt.Errorf("mkdir output: %w", err)
	}

	tracesDir := filepath.Join(cfg.SnapshotDir, "traces")
	entries, err := traceFiles(tracesDir)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{
		Agent:      cfg.Agent,
		Variant:    cfg.Variant,
		StartedAt:  time.Now(),
		TraceCount: len(entries),
	}
	if len(entries) == 0 {
		summary.FinishedAt = time.Now()
		return summary, writeSummary(cfg.OutputDir, summary)
	}

	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)

	for _, fname := range entries {
		if ctx.Err() != nil {
			break
		}

		fname := fname
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			res := runOne(ctx, cfg, build, extract, ag, tracesDir, fname)
			mu.Lock()
			done++
			summary.TotalSkippedLookups += len(res.Skipped)
			summary.TotalCostUSD += res.Replay.CostUSD
			if res.ReplayError != "" {
				summary.ReplayedErr++
			} else {
				summary.ReplayedOK++
			}
			progress := done
			mu.Unlock()
			cfg.Logger.Info("replay progress",
				"done", progress, "total", len(entries),
				"trace", res.TraceID,
				"err", res.ReplayError != "")
		}()
	}
	wg.Wait()

	summary.FinishedAt = time.Now()
	return summary, writeSummary(cfg.OutputDir, summary)
}

func runOne(
	ctx context.Context,
	cfg Config,
	build BuildRequestFunc,
	extract ExtractReplayOutputFunc,
	ag agent.Agent,
	tracesDir, fname string,
) ReplayResult {
	traceID := stripExt(fname)
	res := ReplayResult{
		TraceID: traceID,
		Agent:   cfg.Agent,
		Variant: cfg.Variant,
		RanAt:   time.Now(),
	}

	body, err := os.ReadFile(filepath.Join(tracesDir, fname))
	if err != nil {
		res.ReplayError = fmt.Sprintf("read trace: %v", err)
		writeResult(cfg.OutputDir, res, cfg.Logger)
		return res
	}

	br, err := build(ctx, traceID, body)
	if err != nil {
		res.ReplayError = fmt.Sprintf("reconstruct: %v", err)
		writeResult(cfg.OutputDir, res, cfg.Logger)
		return res
	}
	res.UserID = br.UserID
	res.Input = br.Input
	res.Original = br.Original
	res.Skipped = br.Skipped

	start := time.Now()
	resp, err := ag.Execute(ctx, br.Request)
	if err != nil {
		res.ReplayError = fmt.Sprintf("agent.Execute: %v", err)
		writeResult(cfg.OutputDir, res, cfg.Logger)
		return res
	}
	res.Replay = extract(resp)
	if res.Replay.LatencyMs == 0 {
		res.Replay.LatencyMs = time.Since(start).Milliseconds()
	}
	if res.Replay.CostUSD == 0 && resp.Tokens.Cost != nil {
		res.Replay.CostUSD = *resp.Tokens.Cost
	}

	writeResult(cfg.OutputDir, res, cfg.Logger)
	return res
}

func traceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read traces dir %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func writeResult(outDir string, r ReplayResult, logger *slog.Logger) {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		logger.Error("marshal replay result", "trace", r.TraceID, "err", err)
		return
	}
	path := filepath.Join(outDir, r.TraceID+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		logger.Error("write replay result", "path", path, "err", err)
	}
}

func writeSummary(outDir string, s Summary) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.json"), body, 0o600); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

func stripExt(name string) string {
	return name[:len(name)-len(filepath.Ext(name))]
}
