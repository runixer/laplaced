package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/runixer/laplaced/cmd/testbot/snapshot"
	"github.com/spf13/cobra"
)

var judgePrepareCmd = &cobra.Command{
	Use:   "judge-prepare",
	Short: "Generate a labels-todo.md from a reranker snapshot",
	Long: `Read a snapshot directory (produced by snapshot-rerank) and emit a
human-fillable markdown file with one section per trace. The file is the input
to manual labelling and later judge-import.

Topics-only MVP: trace events at the current schema version do not preserve
people / artifact candidate lists, only counts. Those sections are emitted as
TODO stubs.

Example:
  testbot judge-prepare --snapshot=data/replay/reranker-round2-2026-04-28/
  testbot judge-prepare --snapshot=data/replay/reranker-round2-2026-04-28/ \
    --out=/tmp/labels.md`,
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runJudgePrepare,
}

func init() {
	judgePrepareCmd.Flags().String("snapshot", "", "Snapshot directory (produced by snapshot-rerank)")
	judgePrepareCmd.Flags().String("out", "", "Output markdown path (default: <snapshot>/labels-todo.md)")
	_ = judgePrepareCmd.MarkFlagRequired("snapshot")
	rootCmd.AddCommand(judgePrepareCmd)
}

func runJudgePrepare(cmd *cobra.Command, _ []string) error {
	snapDir := mustGetString(cmd, "snapshot")
	outPath := mustGetString(cmd, "out")

	manifestPath := filepath.Join(snapDir, "manifest.yaml")
	manifest, err := snapshot.ReadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if len(manifest.Traces) == 0 {
		return fmt.Errorf("manifest %s lists 0 traces", manifestPath)
	}

	if outPath == "" {
		outPath = filepath.Join(snapDir, "labels-todo.md")
	}

	spans := make([]*snapshot.RerankerSpanData, 0, len(manifest.Traces))
	skipped := 0
	for _, entry := range manifest.Traces {
		tracePath := filepath.Join(snapDir, entry.File)
		body, readErr := os.ReadFile(tracePath)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: read trace: %v\n", entry.TraceID, readErr)
			skipped++
			continue
		}
		sp, extractErr := snapshot.ExtractRerankerSpan(entry.TraceID, body)
		if extractErr != nil {
			if errors.Is(extractErr, snapshot.ErrNoRerankerSpan) {
				fmt.Fprintf(os.Stderr, "  skip %s: no reranker.Execute span\n", entry.TraceID)
			} else {
				fmt.Fprintf(os.Stderr, "  skip %s: extract: %v\n", entry.TraceID, extractErr)
			}
			skipped++
			continue
		}
		spans = append(spans, sp)
	}

	if len(spans) == 0 {
		return fmt.Errorf("no usable spans extracted (skipped %d)", skipped)
	}

	md := snapshot.RenderLabelsTodo(manifest, spans)
	if err := os.WriteFile(outPath, []byte(md), 0o600); err != nil { // #nosec G703 -- testbot CLI, outPath supplied by operator
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	fmt.Printf("Wrote %s\n", outPath)
	fmt.Printf("  traces:  %d (skipped %d)\n", len(spans), skipped)
	return nil
}
