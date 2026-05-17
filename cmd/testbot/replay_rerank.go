package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/runixer/laplaced/cmd/testbot/snapshot/replay"
	"github.com/runixer/laplaced/internal/agent"
	"github.com/spf13/cobra"
)

var replayRerankCmd = &cobra.Command{
	Use:   "replay-rerank",
	Short: "Re-run captured reranker traces through the production reranker",
	Long: `Reads each trace JSON in <snapshot>/traces/, reconstructs the reranker.Execute
input from the captured event bodies + snapshot DB, dispatches through the real
reranker agent, and writes a per-trace ReplayResult JSON plus a summary.json.

The snapshot DB at <snapshot>/laplaced.db must be passed via --db so the
reconstructor's GetTopicsByIDs / GetPeopleByIDs / GetArtifactsByIDs lookups
hit the frozen state, and the reranker's tool calls (get_topics_content) load
content from the same DB.

Output goes to <snapshot>/replay/<variant>/<trace_id>.json plus summary.json.

Example:
  testbot \
    --db data/replay/reranker-round2-2026-04-28/laplaced.db \
    replay-rerank \
    --snapshot data/replay/reranker-round2-2026-04-28 \
    --variant baseline \
    --concurrency 3`,
	RunE: runReplayRerank,
}

func init() {
	replayRerankCmd.Flags().String("snapshot", "", "Snapshot directory (must contain traces/*.json)")
	replayRerankCmd.Flags().String("variant", "baseline", "Variant label (baseline, no_match_patch, ...)")
	replayRerankCmd.Flags().Int("concurrency", 3, "Number of traces to replay in parallel")
	_ = replayRerankCmd.MarkFlagRequired("snapshot")
	rootCmd.AddCommand(replayRerankCmd)
}

func runReplayRerank(cmd *cobra.Command, _ []string) error {
	tb := getTestBot(cmd)
	if tb == nil {
		return fmt.Errorf("testbot not initialised — replay-rerank needs full bot setup")
	}
	if tb.services == nil || tb.services.RerankerAgent == nil {
		return fmt.Errorf("reranker agent not wired in services")
	}

	snapDir := mustGetString(cmd, "snapshot")
	variant := mustGetString(cmd, "variant")
	concurrency, _ := cmd.Flags().GetInt("concurrency")

	if info, err := os.Stat(snapDir); err != nil || !info.IsDir() {
		return fmt.Errorf("snapshot directory does not exist: %s", snapDir)
	}

	cfg := replay.Config{
		SnapshotDir: snapDir,
		Variant:     variant,
		Agent:       "reranker",
		Concurrency: concurrency,
		Logger:      tb.logger,
	}

	build := replay.NewRerankerBuilder(tb.store, func(ctx context.Context, userID int64) *agent.SharedContext {
		return tb.services.ContextService.Load(ctx, userID)
	})

	summary, err := replay.Run(
		cmd.Context(),
		cfg,
		build,
		replay.ExtractRerankerOutput,
		tb.services.RerankerAgent,
	)
	if err != nil {
		return fmt.Errorf("replay run: %w", err)
	}

	outDir := filepath.Join(snapDir, "replay", variant)
	fmt.Printf("Replay complete\n")
	fmt.Printf("  output dir:        %s\n", outDir)
	fmt.Printf("  traces:            %d\n", summary.TraceCount)
	fmt.Printf("  replayed ok:       %d\n", summary.ReplayedOK)
	fmt.Printf("  replayed error:    %d\n", summary.ReplayedErr)
	fmt.Printf("  total cost (USD):  $%.4f\n", summary.TotalCostUSD)
	fmt.Printf("  skipped lookups:   %d\n", summary.TotalSkippedLookups)
	fmt.Printf("  duration:          %s\n", summary.FinishedAt.Sub(summary.StartedAt))
	return nil
}
