package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// CODE DUPLICATION DECISION
//
// The four check commands (check-facts, check-topics, check-people, check-messages)
// intentionally duplicate some boilerplate code (~13 lines per command):
//   - format flag retrieval and JSON check
//   - testbot initialization
//   - JSON output logic
//
// WHY THIS IS ACCEPTABLE:
// 1. This is an internal developer tool, not a public library
// 2. Each command is self-contained and readable without jumping between functions
// 3. The duplication is limited to boilerplate (error handling, JSON vs text branching)
// 4. Type-specific logic (fetch, formatting) would require interface{} and type assertions
// 5. A generic factory approach would increase complexity more than reduce duplication
//
// FUTURE: If more check commands are added (6+), consider introducing a helper function
// that takes type-specific fetch/formatter callbacks. Until then, explicit > clever.

// outputCheckError wraps an error in JSON if JSON output is requested.
func outputCheckError(message string, isJSON bool) error {
	if isJSON {
		return outputCheckJSON(map[string]interface{}{
			"status": "error",
			"error":  message,
		})
	}
	return fmt.Errorf("%s", message)
}

var checkFactsCmd = &cobra.Command{
	Use:   "check-facts",
	Short: "Check facts in database",
	Long: `Display all facts stored for the user. Facts are extracted from conversations
by the archivist agent and used for profile information.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format := mustGetString(cmd, "format")
		isJSON := format == "json"

		tb := getTestBot(cmd)
		if tb == nil {
			return outputCheckError("testbot not initialized", isJSON)
		}

		facts, err := tb.store.GetFacts(getUserID(cmd))
		if err != nil {
			return outputCheckError(fmt.Sprintf("failed to get facts: %v", err), isJSON)
		}

		if isJSON {
			return outputCheckJSON(map[string]interface{}{
				"type":  "facts",
				"count": len(facts),
				"data":  facts,
			})
		}

		userID := getUserID(cmd)
		fmt.Printf("Facts for user %d: %d\n", userID, len(facts))
		for i, fact := range facts {
			fmt.Printf("%2d. [%s] %s\n", i+1, fact.Type, fact.Content)
		}
		return nil
	},
}

var checkTopicsCmd = &cobra.Command{
	Use:   "check-topics",
	Short: "Check topics in database",
	Long: `Display all topics stored for the user. Topics are conversation summaries
created from sessions and used for RAG retrieval.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format := mustGetString(cmd, "format")
		isJSON := format == "json"

		tb := getTestBot(cmd)
		if tb == nil {
			return outputCheckError("testbot not initialized", isJSON)
		}

		topics, err := tb.store.GetTopics(getUserID(cmd))
		if err != nil {
			return outputCheckError(fmt.Sprintf("failed to get topics: %v", err), isJSON)
		}

		if isJSON {
			return outputCheckJSON(map[string]interface{}{
				"type":  "topics",
				"count": len(topics),
				"data":  topics,
			})
		}

		userID := getUserID(cmd)
		fmt.Printf("Topics for user %d: %d\n", userID, len(topics))
		for i, topic := range topics {
			fmt.Printf("%2d. [%d] %s (msg %d-%d, %d chars)\n",
				i+1, topic.ID, topic.Summary, topic.StartMsgID, topic.EndMsgID, topic.SizeChars)
		}
		return nil
	},
}

var checkPeopleCmd = &cobra.Command{
	Use:   "check-people",
	Short: "Check people in database",
	Long: `Display all people stored for the user. People are extracted from conversations
and include contacts, family, friends, etc.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format := mustGetString(cmd, "format")
		isJSON := format == "json"

		tb := getTestBot(cmd)
		if tb == nil {
			return outputCheckError("testbot not initialized", isJSON)
		}

		people, err := tb.store.GetPeople(getUserID(cmd))
		if err != nil {
			return outputCheckError(fmt.Sprintf("failed to get people: %v", err), isJSON)
		}

		if isJSON {
			return outputCheckJSON(map[string]interface{}{
				"type":  "people",
				"count": len(people),
				"data":  people,
			})
		}

		userID := getUserID(cmd)
		fmt.Printf("People for user %d: %d\n", userID, len(people))
		for i, person := range people {
			username := "none"
			if person.Username != nil {
				username = *person.Username
			}
			aliases := ""
			if len(person.Aliases) > 0 {
				aliases = fmt.Sprintf(" (aliases: %s)", strings.Join(person.Aliases, ", "))
			}
			fmt.Printf("%2d. [%d] %s (@%s)%s\n", i+1, person.ID, person.DisplayName, username, aliases)
		}
		return nil
	},
}

var checkMessagesCmd = &cobra.Command{
	Use:   "check-messages",
	Short: "Check recent messages in database",
	Long:  `Display recent messages stored for the user. Shows the last 100 messages by default.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format := mustGetString(cmd, "format")
		isJSON := format == "json"

		tb := getTestBot(cmd)
		if tb == nil {
			return outputCheckError("testbot not initialized", isJSON)
		}

		messages, err := tb.store.GetRecentHistory(getUserID(cmd), defaultMessageLimit)
		if err != nil {
			return outputCheckError(fmt.Sprintf("failed to get messages: %v", err), isJSON)
		}

		if isJSON {
			return outputCheckJSON(map[string]interface{}{
				"type":  "messages",
				"count": len(messages),
				"data":  messages,
			})
		}

		userID := getUserID(cmd)
		fmt.Printf("Messages for user %d: %d (showing last %d)\n", userID, len(messages), defaultMessageLimit)
		for i, msg := range messages {
			topicInfo := ""
			if msg.TopicID != nil {
				topicInfo = fmt.Sprintf(" [topic:%d]", *msg.TopicID)
			}
			fmt.Printf("%2d. [%d]%s %s\n", i+1, msg.ID, topicInfo, msg.Content)
		}
		return nil
	},
}

func init() {
	// Add shared format flag to all check commands
	checkFactsCmd.Flags().StringP("format", "f", "text", "Output format: text, json")
	checkTopicsCmd.Flags().StringP("format", "f", "text", "Output format: text, json")
	checkPeopleCmd.Flags().StringP("format", "f", "text", "Output format: text, json")
	checkMessagesCmd.Flags().StringP("format", "f", "text", "Output format: text, json")

	rootCmd.AddCommand(checkFactsCmd)
	rootCmd.AddCommand(checkTopicsCmd)
	rootCmd.AddCommand(checkPeopleCmd)
	rootCmd.AddCommand(checkMessagesCmd)
}
