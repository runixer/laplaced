package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var clearFactsCmd = &cobra.Command{
	Use:   "clear-facts",
	Short: "Clear all facts for user",
	Long: `Delete all facts stored for the user. This is useful for testing fact extraction
from scratch.

Example:
  testbot clear-facts`,
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		userID := getUserID(cmd)

		// Get count first for reporting
		facts, err := tb.store.GetFacts(userID)
		if err != nil {
			return fmt.Errorf("failed to get facts: %w", err)
		}

		// Use bulk delete
		if err := tb.store.DeleteAllFacts(userID); err != nil {
			return fmt.Errorf("failed to delete facts: %w", err)
		}

		fmt.Printf("Cleared %d facts for user %d\n", len(facts), userID)
		return nil
	},
}

var clearTopicsCmd = &cobra.Command{
	Use:   "clear-topics",
	Short: "Clear all topics for user",
	Long: `Delete all topics stored for the user. This is useful for testing topic creation
and RAG retrieval from scratch.

Example:
  testbot clear-topics`,
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		userID := getUserID(cmd)

		// Get count first for reporting
		topics, err := tb.store.GetTopics(userID)
		if err != nil {
			return fmt.Errorf("failed to get topics: %w", err)
		}

		// Use bulk delete
		if err := tb.store.DeleteAllTopics(userID); err != nil {
			return fmt.Errorf("failed to delete topics: %w", err)
		}

		fmt.Printf("Cleared %d topics for user %d\n", len(topics), userID)
		return nil
	},
}

var clearPeopleCmd = &cobra.Command{
	Use:   "clear-people",
	Short: "Clear all people for user",
	Long: `Delete all people stored for the user. This is useful for testing people extraction
from scratch.

Example:
  testbot clear-people`,
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		userID := getUserID(cmd)

		// Get count first for reporting
		people, err := tb.store.GetPeople(userID)
		if err != nil {
			return fmt.Errorf("failed to get people: %w", err)
		}

		// Use bulk delete
		if err := tb.store.DeleteAllPeople(userID); err != nil {
			return fmt.Errorf("failed to delete people: %w", err)
		}

		fmt.Printf("Cleared %d people for user %d\n", len(people), userID)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(clearFactsCmd)
	rootCmd.AddCommand(clearTopicsCmd)
	rootCmd.AddCommand(clearPeopleCmd)
}
