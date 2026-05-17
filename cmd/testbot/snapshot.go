package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/runixer/laplaced/cmd/testbot/snapshot"
	"github.com/spf13/cobra"
)

const (
	defaultTempoURL     = "https://tempo.runixer.ru"
	defaultRerankerSpan = "reranker.Execute"
	defaultDBSource     = "data/prod/laplaced.db"
	defaultReplayRoot   = "data/replay"
)

var snapshotRerankCmd = &cobra.Command{
	Use:   "snapshot-rerank",
	Short: "Snapshot recent reranker traces from Tempo for offline replay",
	Long: `Capture a batch of recent reranker traces from Tempo together with a copy
of the production database for later offline replay and prompt evaluation.

The output directory contains:
  traces/<trace_id>.json   — full trace JSONs (raw from Tempo)
  laplaced.db              — copy of --db-source at snapshot time
  manifest.yaml            — listing of traces, window, filter, source DB

This command does NOT need the testbot's database or services.

Example:
  testbot snapshot-rerank --window=7d --limit=50

  testbot snapshot-rerank \
    --filter='{ name = "reranker.Execute" && span.reranker.fallback_reason != "" }' \
    --limit=20 \
    --out=data/replay/reranker-fallbacks/`,
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runSnapshotRerank,
}

func init() {
	snapshotRerankCmd.Flags().String("tempo-url", defaultTempoURL, "Tempo HTTP base URL")
	snapshotRerankCmd.Flags().String("window", "7d", "Time window ending now (e.g. 7d, 24h, 1h). Tempo caps at 168h.")
	snapshotRerankCmd.Flags().String("filter", "", "TraceQL filter (default: { name = \""+defaultRerankerSpan+"\" })")
	snapshotRerankCmd.Flags().String("span", defaultRerankerSpan, "Span name to record in manifest (informational)")
	snapshotRerankCmd.Flags().String("agent", "reranker", "Agent label for manifest")
	snapshotRerankCmd.Flags().Int("limit", 50, "Max traces to fetch")
	snapshotRerankCmd.Flags().String("out", "", "Output directory (default: "+defaultReplayRoot+"/<agent>-<unix-ts>/)")
	snapshotRerankCmd.Flags().String("db-source", defaultDBSource, "Source DB to copy into the snapshot (use empty string to skip)")
	rootCmd.AddCommand(snapshotRerankCmd)
}

func runSnapshotRerank(cmd *cobra.Command, _ []string) error {
	tempoURL := mustGetString(cmd, "tempo-url")
	window := mustGetString(cmd, "window")
	filter := mustGetString(cmd, "filter")
	spanName := mustGetString(cmd, "span")
	agentLabel := mustGetString(cmd, "agent")
	limit := mustGetInt(cmd, "limit")
	outDir := mustGetString(cmd, "out")
	dbSource := mustGetString(cmd, "db-source")

	if filter == "" {
		filter = `{ name = "` + spanName + `" }`
	}

	dur, err := time.ParseDuration(window)
	if err != nil {
		return fmt.Errorf("invalid --window %q: %w", window, err)
	}
	if dur <= 0 {
		return fmt.Errorf("--window must be positive, got %s", window)
	}
	end := time.Now().UTC()
	start := end.Add(-dur)

	if outDir == "" {
		outDir = filepath.Join(defaultReplayRoot, fmt.Sprintf("%s-%d", agentLabel, end.Unix()))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	tc := snapshot.NewTempoClient(tempoURL)
	fmt.Printf("Searching Tempo: %s [%s..%s] limit=%d\n",
		filter, start.Format(time.RFC3339), end.Format(time.RFC3339), limit)

	metas, err := tc.Search(ctx, filter, start.Unix(), end.Unix(), limit)
	if err != nil {
		return fmt.Errorf("tempo search: %w", err)
	}
	if len(metas) == 0 {
		return fmt.Errorf("no traces matched filter; try widening --window or relaxing --filter")
	}
	fmt.Printf("Found %d traces, fetching...\n", len(metas))

	manifest := &snapshot.Manifest{
		Version:     snapshot.ManifestVersion,
		Agent:       agentLabel,
		SpanName:    spanName,
		TempoURL:    tempoURL,
		Filter:      filter,
		WindowStart: start,
		WindowEnd:   end,
		CapturedAt:  time.Now().UTC(),
	}

	var failures []string
	for i, m := range metas {
		body, err := tc.FetchTrace(ctx, m.TraceID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", m.TraceID, err))
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s FAILED: %v\n", i+1, len(metas), m.TraceID, err)
			continue
		}
		rel, err := snapshot.WriteTrace(outDir, m.TraceID, body)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", m.TraceID, err))
			continue
		}
		manifest.Traces = append(manifest.Traces, snapshot.TraceManifestEntry{
			TraceID:        m.TraceID,
			File:           rel,
			DurationMs:     m.DurationMs,
			StartTimeUnix:  m.StartTimeUnix(),
			ServiceVersion: snapshot.ExtractServiceVersion(body),
		})
		if (i+1)%10 == 0 || i+1 == len(metas) {
			fmt.Printf("  [%d/%d]\n", i+1, len(metas))
		}
	}
	manifest.TraceCount = len(manifest.Traces)

	if dbSource != "" {
		fmt.Printf("Copying DB %s -> %s/\n", dbSource, outDir)
		dbRel, err := snapshot.CopyDB(outDir, dbSource)
		if err != nil {
			return fmt.Errorf("copy DB: %w", err)
		}
		manifest.DBPath = dbRel
	}

	if err := snapshot.WriteManifestFile(outDir, manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Printf("\nSnapshot written to %s\n", outDir)
	fmt.Printf("  traces:  %d (failed: %d)\n", manifest.TraceCount, len(failures))
	if manifest.DBPath != "" {
		fmt.Printf("  db:      %s\n", manifest.DBPath)
	}
	if versions := uniqueServiceVersions(manifest.Traces); len(versions) > 1 {
		fmt.Printf("  warning: mixed service.version across traces — %s\n", strings.Join(versions, ", "))
	}
	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d fetch failures:\n", len(failures))
		for _, f := range failures {
			fmt.Fprintf(os.Stderr, "  - %s\n", f)
		}
		return fmt.Errorf("%d trace(s) failed to fetch", len(failures))
	}
	return nil
}

func uniqueServiceVersions(entries []snapshot.TraceManifestEntry) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, e := range entries {
		if e.ServiceVersion == "" {
			continue
		}
		if _, ok := seen[e.ServiceVersion]; ok {
			continue
		}
		seen[e.ServiceVersion] = struct{}{}
		out = append(out, e.ServiceVersion)
	}
	return out
}
