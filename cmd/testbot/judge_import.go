package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/runixer/laplaced/cmd/testbot/snapshot"
	"github.com/spf13/cobra"
)

var judgeImportCmd = &cobra.Command{
	Use:   "judge-import",
	Short: "Convert a filled labels-todo.md into structured labels.json",
	Long: `Read a markdown file produced (and then filled in) from judge-prepare and emit
a JSON file consumable by eval-rerank.

Only rows with non-empty Score are emitted. Bad rows (out-of-range score,
non-numeric ID, missing columns) are skipped silently — labelling is human
work and the parser must be resilient. Compare row counts in stderr to spot
under-labelling.

Example:
  testbot judge-import \
    --md=data/replay/reranker-round2-2026-04-28/labels-todo.md \
    --out=data/replay/reranker-round2-2026-04-28/labels.json`,
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runJudgeImport,
}

func init() {
	judgeImportCmd.Flags().String("md", "", "Filled labels markdown file")
	judgeImportCmd.Flags().String("out", "", "Output labels.json (default: <md-dir>/labels.json)")
	_ = judgeImportCmd.MarkFlagRequired("md")
	rootCmd.AddCommand(judgeImportCmd)
}

func runJudgeImport(cmd *cobra.Command, _ []string) error {
	mdPath := mustGetString(cmd, "md")
	outPath := mustGetString(cmd, "out")
	if outPath == "" {
		outPath = filepath.Join(filepath.Dir(mdPath), "labels.json")
	}

	f, err := os.Open(mdPath)
	if err != nil {
		return fmt.Errorf("open md: %w", err)
	}
	defer func() { _ = f.Close() }()

	labels, err := snapshot.ParseLabelsMarkdown(f)
	if err != nil {
		return fmt.Errorf("parse md: %w", err)
	}
	if len(labels) == 0 {
		return fmt.Errorf("no labels parsed from %s — did you fill any Score cells?", mdPath)
	}

	data, err := json.MarshalIndent(labels, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	totalLabels := 0
	tracesWithLabels := 0
	for _, tl := range labels {
		n := len(tl.Topics) + len(tl.People) + len(tl.Artifacts)
		if n > 0 {
			tracesWithLabels++
		}
		totalLabels += n
	}
	fmt.Printf("Wrote %s\n", outPath)
	fmt.Printf("  traces: %d  labelled: %d  total labels: %d\n",
		len(labels), tracesWithLabels, totalLabels)
	return nil
}
