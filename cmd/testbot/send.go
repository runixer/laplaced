package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send <message>",
	Short: "Send a test message to the bot",
	Long: `Send a test message through the bot pipeline without Telegram integration.
This is useful for testing prompts, RAG retrieval, and fact extraction.

Example:
  testbot send "What is 2+2?"
  testbot send "My name is Alice" --check-response "Alice" --output json`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		// Get flag values
		checkResponse := mustGetString(cmd, "check-response")
		checkTopics := mustGetInt(cmd, "check-topics")
		processSession := mustGetBool(cmd, "process-session")
		outputFormat := mustGetString(cmd, "output")

		userID := getUserID(cmd)
		message := strings.Join(args, " ")

		// Create context with signal handling for graceful shutdown
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Send message
		result, err := tb.bot.SendTestMessage(ctx, userID, message, true)
		if err != nil {
			return fmt.Errorf("failed to send message: %w", err)
		}

		// Check assertions
		var failures []string

		if checkResponse != "" && !strings.Contains(strings.ToLower(result.Response), strings.ToLower(checkResponse)) {
			failures = append(failures, fmt.Sprintf("response does not contain %q (case-insensitive)", checkResponse))
		}

		// Check topics
		if checkTopics >= 0 {
			topics, err := tb.store.GetTopics(userID)
			if err != nil {
				return fmt.Errorf("failed to get topics: %w", err)
			}
			if len(topics) != checkTopics {
				failures = append(failures, fmt.Sprintf("expected %d topics, got %d", checkTopics, len(topics)))
			}
		}

		// Process session if requested - use existing ForceCloseSession method
		if processSession {
			count, err := tb.bot.ForceCloseSession(ctx, userID)
			if err != nil {
				return fmt.Errorf("failed to process session: %w", err)
			}
			if count > 0 {
				fmt.Fprintf(os.Stderr, "Processed session: %d messages archived\n", count)
			}
		}

		// Output results
		if outputFormat == "json" {
			return outputJSON(result, failures)
		}
		return outputText(result, failures)
	},
}

func init() {
	sendCmd.Flags().String("check-response", "", "Check response contains substring")
	sendCmd.Flags().Int("check-topics", -1, "Check topic count")
	sendCmd.Flags().Bool("process-session", false, "Force process session after message")
	sendCmd.Flags().String("output", "text", "Output format: text, json")

	rootCmd.AddCommand(sendCmd)
}
