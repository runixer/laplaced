package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/runixer/laplaced/cmd/testbot/snapshot"
	"github.com/spf13/cobra"
)

var analyzeRerankCmd = &cobra.Command{
	Use:   "analyze-rerank",
	Short: "Compute baseline metrics over a reranker snapshot (no LLM, no labels)",
	Long: `Walk every trace in a snapshot, extract reranker.Execute span data, and emit
distributional summaries: fallback breakdown, candidate funnel per type, halluc
rate, cost, latency. Useful as the baseline against which prompt experiments
are diffed; does not require manual labels.

Example:
  testbot analyze-rerank --snapshot=data/replay/reranker-round2-2026-04-28/
  testbot analyze-rerank --snapshot=data/replay/reranker-round2-2026-04-28/ \
    --json=/tmp/baseline.json`,
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runAnalyzeRerank,
}

func init() {
	analyzeRerankCmd.Flags().String("snapshot", "", "Snapshot directory")
	analyzeRerankCmd.Flags().String("json", "", "Optional JSON output path (default: text only)")
	_ = analyzeRerankCmd.MarkFlagRequired("snapshot")
	rootCmd.AddCommand(analyzeRerankCmd)
}

func runAnalyzeRerank(cmd *cobra.Command, _ []string) error {
	snapDir := mustGetString(cmd, "snapshot")
	jsonOut := mustGetString(cmd, "json")

	manifest, err := snapshot.ReadManifest(filepath.Join(snapDir, "manifest.yaml"))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if len(manifest.Traces) == 0 {
		return fmt.Errorf("manifest has 0 traces")
	}

	spans := make([]*snapshot.RerankerSpanData, 0, len(manifest.Traces))
	skipped := 0
	for _, e := range manifest.Traces {
		body, readErr := os.ReadFile(filepath.Join(snapDir, e.File))
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: read: %v\n", e.TraceID, readErr)
			skipped++
			continue
		}
		sp, extractErr := snapshot.ExtractRerankerSpan(e.TraceID, body)
		if extractErr != nil {
			if !errors.Is(extractErr, snapshot.ErrNoRerankerSpan) {
				fmt.Fprintf(os.Stderr, "  skip %s: %v\n", e.TraceID, extractErr)
			}
			skipped++
			continue
		}
		spans = append(spans, sp)
	}
	if len(spans) == 0 {
		return fmt.Errorf("no usable spans (skipped %d)", skipped)
	}

	report := snapshot.AnalyzeSpans(spans)
	fmt.Print(snapshot.FormatReport(report))
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "\n(skipped %d trace(s))\n", skipped)
	}

	if jsonOut != "" {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshal json: %w", marshalErr)
		}
		if err := os.WriteFile(jsonOut, data, 0o600); err != nil { // #nosec G703 -- testbot CLI, jsonOut supplied by operator
			return fmt.Errorf("write %s: %w", jsonOut, err)
		}
		fmt.Fprintf(os.Stderr, "JSON report: %s\n", jsonOut)
	}

	return nil
}
