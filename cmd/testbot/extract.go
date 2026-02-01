package main

import (
	"context"
	"fmt"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/extractor"
	"github.com/spf13/cobra"
)

var extractCmd = &cobra.Command{
	Use:   "extract <artifact_id>",
	Short: "Manually extract metadata from an artifact",
	Long: `Process an artifact through the Extractor agent to extract structured metadata,
generate summary embedding, and make it available for search. This is normally done
automatically by the background worker, but this command allows manual testing.

Example:
  testbot extract 1`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		userID := getUserID(cmd)

		// Parse artifact ID
		artifactID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid artifact ID: %w", err)
		}

		// Get artifact from database
		artifact, err := tb.store.GetArtifact(userID, artifactID)
		if err != nil {
			return fmt.Errorf("failed to get artifact: %w", err)
		}
		if artifact == nil {
			return fmt.Errorf("artifact not found")
		}

		fmt.Printf("Extracting artifact %d...\n", artifactID)
		fmt.Printf("  File: %s\n", artifact.OriginalName)
		fmt.Printf("  Type: %s\n", artifact.FileType)
		fmt.Printf("  Size: %d bytes\n", artifact.FileSize)
		fmt.Printf("  State: %s\n", artifact.State)

		// Create context with signal handling
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Create extractor request
		req := &agent.Request{
			Params: map[string]any{
				extractor.ParamArtifact: artifact,
			},
			Shared: &agent.SharedContext{
				UserID: userID,
			},
		}

		// Execute extractor
		resp, err := tb.services.ExtractorAgent.Execute(ctx, req)
		if err != nil {
			return fmt.Errorf("extraction failed: %w", err)
		}

		// Extract result
		result := resp.Structured.(*extractor.ProcessResult)

		fmt.Printf("\nExtraction complete:\n")
		fmt.Printf("  Artifact ID: %d\n", result.ArtifactID)
		fmt.Printf("  Summary: %s\n", result.Summary)
		fmt.Printf("  Keywords: %v\n", result.Keywords)
		fmt.Printf("  Entities: %v\n", result.Entities)
		fmt.Printf("  RAG Hints: %v\n", result.RAGHints)
		fmt.Printf("  Duration: %v\n", result.Duration)
		fmt.Printf("  Tokens: %d\n", result.Tokens.TotalTokens())

		// Verify artifact was updated
		updated, err := tb.store.GetArtifact(userID, artifactID)
		if err != nil {
			return fmt.Errorf("failed to verify artifact update: %w", err)
		}

		fmt.Printf("\nArtifact state updated:\n")
		fmt.Printf("  State: %s\n", updated.State)
		if updated.Summary != nil {
			fmt.Printf("  Summary: %s\n", *updated.Summary)
		}
		if updated.Keywords != nil {
			fmt.Printf("  Keywords: %s\n", *updated.Keywords)
		}
		if updated.Entities != nil {
			fmt.Printf("  Entities: %s\n", *updated.Entities)
		}
		if updated.RAGHints != nil {
			fmt.Printf("  RAG Hints: %s\n", *updated.RAGHints)
		}
		if len(updated.Embedding) > 0 {
			fmt.Printf("  Embedding dimension: %d\n", len(updated.Embedding))
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(extractCmd)
}
