package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/runixer/laplaced/internal/rag"
	"github.com/spf13/cobra"
)

var processSessionCmd = &cobra.Command{
	Use:   "process-session",
	Short: "Force process active session into topic",
	Long: `Process the active session (unprocessed messages) into a topic and extract facts.
This is equivalent to what happens automatically when a session is archived after
inactivity timeout.

Example:
  testbot send "My name is Alice" && testbot process-session`,
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		userID := getUserID(cmd)

		// Create context with signal handling for graceful shutdown
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Check for unprocessed messages first
		messages, err := tb.store.GetUnprocessedMessages(userID)
		if err != nil {
			return fmt.Errorf("failed to get unprocessed messages: %w", err)
		}

		if len(messages) == 0 {
			fmt.Println("No unprocessed messages to process")
			return nil
		}

		// Use ForceCloseSessionWithProgress to get detailed statistics
		// Provide no-op callback since we don't need progress updates
		stats, err := tb.bot.ForceCloseSessionWithProgress(ctx, userID, func(e rag.ProgressEvent) {})
		if err != nil {
			return fmt.Errorf("failed to process session: %w", err)
		}

		fmt.Printf("Processed session: %d messages archived\n", stats.MessagesProcessed)
		if stats.TopicsExtracted > 0 {
			fmt.Printf("Topics extracted: %d\n", stats.TopicsExtracted)
		}

		// Show fact extraction statistics
		if stats.FactsCreated > 0 || stats.FactsUpdated > 0 || stats.FactsDeleted > 0 {
			fmt.Printf("Extracted facts: %d added, %d updated, %d removed\n",
				stats.FactsCreated, stats.FactsUpdated, stats.FactsDeleted)
		}

		// Show people extraction statistics
		if stats.PeopleAdded > 0 || stats.PeopleUpdated > 0 || stats.PeopleMerged > 0 {
			fmt.Printf("People: %d added, %d updated, %d merged\n",
				stats.PeopleAdded, stats.PeopleUpdated, stats.PeopleMerged)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(processSessionCmd)
}
