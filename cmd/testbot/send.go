package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send [--voice <file>] [message]",
	Short: "Send a test message to the bot",
	Long: `Send a test message through the bot pipeline without Telegram integration.
This is useful for testing prompts, RAG retrieval, and fact extraction.

With --voice flag, sends a voice/audio file for transcription testing.

Examples:
  testbot send "What is 2+2?"
  testbot send "My name is Alice" --check-response "Alice"
  testbot send --voice data/sample/short_voice_message.ogg`,
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
		voicePath := mustGetString(cmd, "voice")

		opts := getOptions(cmd)
		userID := opts.userID

		// Create context with signal handling for graceful shutdown
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		var resultText string
		var resultErr error

		if voicePath != "" {
			// Send voice message
			resultText, resultErr = sendVoiceMessage(ctx, tb, userID, voicePath, args)
		} else {
			// Send text message
			if len(args) == 0 {
				return fmt.Errorf("message text required when --voice not provided")
			}
			message := strings.Join(args, " ")
			result, err := tb.bot.SendTestMessage(ctx, userID, message, true)
			if err != nil {
				return fmt.Errorf("failed to send message: %w", err)
			}
			resultText = result.Response
			resultErr = err
		}

		if resultErr != nil {
			return fmt.Errorf("failed to send message: %w", resultErr)
		}

		// Check assertions
		var failures []string

		if checkResponse != "" && !strings.Contains(strings.ToLower(resultText), strings.ToLower(checkResponse)) {
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

		// Process session if requested
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
			// For voice messages, create a minimal result
			if voicePath != "" {
				result := &rag.TestMessageResult{
					Response:    resultText,
					TimingTotal: time.Since(time.Now()),
				}
				return outputJSON(result, failures)
			}
			// For text messages, create result from response text
			result := &rag.TestMessageResult{
				Response: resultText,
			}
			return outputJSON(result, failures)
		}
		// Text output
		fmt.Printf("Response:\n%s\n", resultText)
		if len(failures) > 0 {
			fmt.Printf("\nFailures:\n")
			for _, f := range failures {
				fmt.Printf("  - %s\n", f)
			}
		}
		return nil
	},
}

// sendVoiceMessage sends a voice message through the bot pipeline.
func sendVoiceMessage(ctx context.Context, tb *testBot, userID int64, voicePath string, textArgs []string) (string, error) {
	// Read voice file to get size
	fileInfo, err := os.Stat(voicePath)
	if err != nil {
		return "", fmt.Errorf("failed to stat voice file: %w", err)
	}

	// Build message text (optional caption from args)
	messageText := ""
	if len(textArgs) > 0 {
		messageText = strings.Join(textArgs, " ")
	}

	// Create mock downloader for this test
	downloader := &mockFileDownloader{voiceFilePath: voicePath}

	// Create fileProcessor with mock downloader
	translator, err := i18n.NewTranslator(tb.cfg.Bot.Language)
	if err != nil {
		return "", fmt.Errorf("failed to create translator: %w", err)
	}
	fileProcessor := files.NewProcessor(downloader, translator, tb.cfg.Bot.Language, tb.logger)
	fileProcessor.SetMinVoiceDurationSec(tb.cfg.Artifacts.MinVoiceDurationSeconds)

	// Set custom file processor
	tb.bot.SetFileProcessor(fileProcessor)

	// Create mock voice message
	voiceMsg := &telegram.Message{
		MessageID: 1,
		From: &telegram.User{
			ID:        userID,
			IsBot:     false,
			FirstName: "Test",
			Username:  "testuser",
		},
		Chat: &telegram.Chat{
			ID:   userID,
			Type: "private",
		},
		Date: int(time.Now().Unix()),
		Voice: &telegram.Voice{
			FileID:   "voice_test123", // Special ID for mock downloader
			Duration: 3,               // Will be updated if we can parse the file
			MimeType: "audio/ogg",
			FileSize: int(fileInfo.Size()),
		},
		Text:    messageText,
		Caption: messageText,
	}

	// Create update
	update := &telegram.Update{
		UpdateID: 1,
		Message:  voiceMsg,
	}

	// Process the update
	tb.bot.ProcessUpdateAsync(ctx, update, "testbot")

	// Wait for response - this is tricky because ProcessUpdateAsync is async
	// For now, we'll wait a bit and check history
	time.Sleep(15 * time.Second) // Give time for processing

	// Get last response from history
	messages, err := tb.store.GetRecentHistory(userID, 10)
	if err != nil {
		return "", fmt.Errorf("failed to get history: %w", err)
	}

	// Find the last assistant message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Content, nil
		}
	}

	return "", fmt.Errorf("no response found in history")
}

func init() {
	sendCmd.Flags().String("check-response", "", "Check response contains substring")
	sendCmd.Flags().Int("check-topics", -1, "Check topic count")
	sendCmd.Flags().Bool("process-session", false, "Force process session after message")
	sendCmd.Flags().String("output", "text", "Output format: text, json")
	sendCmd.Flags().String("voice", "", "Path to voice/audio file to send (for testing audio transcription)")

	rootCmd.AddCommand(sendCmd)
}
