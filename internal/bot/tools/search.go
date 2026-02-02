package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// performHistorySearch searches the user's conversation history.
func (e *ToolExecutor) performHistorySearch(ctx context.Context, userID int64, args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query argument missing or not a string")
	}

	if e.ragService == nil {
		return "Search is not available", nil
	}

	opts := &rag.RetrievalOptions{
		SkipEnrichment: true,
		Source:         "tool",
	}
	result, _, err := e.ragService.Retrieve(ctx, userID, query, opts)
	if err != nil {
		return "", err
	}
	if result == nil || len(result.Topics) == 0 {
		return "No results found in memory.", nil
	}

	// Sort by weight ASC (lowest to highest) to match context injection behavior
	// Retrieve returns DESC (highest to lowest)
	topics := result.Topics
	for i, j := 0, len(topics)-1; i < j; i, j = i+1, j-1 {
		topics[i], topics[j] = topics[j], topics[i]
	}

	return e.formatRAGResults(topics, query), nil
}

// performSearchPeople searches the user's people database.
func (e *ToolExecutor) performSearchPeople(ctx context.Context, userID int64, args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query argument missing or not a string")
	}

	if e.peopleRepo == nil {
		return "People search is not available", nil
	}

	var people []storage.Person

	// Try username search first
	if strings.HasPrefix(query, "@") {
		person, err := e.peopleRepo.FindPersonByUsername(userID, strings.TrimPrefix(query, "@"))
		if err == nil && person != nil {
			people = append(people, *person)
		}
	}

	// Try exact name match
	if len(people) == 0 {
		person, err := e.peopleRepo.FindPersonByName(userID, query)
		if err == nil && person != nil {
			people = append(people, *person)
		}
	}

	// Try alias search
	if len(people) == 0 {
		aliasPeople, err := e.peopleRepo.FindPersonByAlias(userID, query)
		if err == nil {
			people = append(people, aliasPeople...)
		}
	}

	// If still no results, try vector search via RAG service
	if len(people) == 0 && e.ragService != nil {
		// Generate embedding for the query
		resp, err := e.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
			Model: e.cfg.Embedding.Model,
			Input: []string{query},
		})
		if err == nil && len(resp.Data) > 0 {
			results, err := e.ragService.SearchPeople(ctx, userID, resp.Data[0].Embedding, float32(e.cfg.Search.GetPeopleSimilarityThreshold()), e.cfg.Search.GetPeopleMaxResults(), nil) // nil = no circle exclusion
			if err == nil {
				for _, r := range results {
					people = append(people, r.Person)
				}
			}
		}
	}

	if len(people) == 0 {
		return fmt.Sprintf("No people found matching '%s'", query), nil
	}

	// Format results
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d people:\n\n", len(people)))
	for _, p := range people {
		sb.WriteString(fmt.Sprintf("**%s** [%s]\n", p.DisplayName, p.Circle))
		if p.Username != nil && *p.Username != "" {
			sb.WriteString(fmt.Sprintf("Username: @%s\n", *p.Username))
		}
		if len(p.Aliases) > 0 {
			sb.WriteString(fmt.Sprintf("Aliases: %s\n", strings.Join(p.Aliases, ", ")))
		}
		if p.Bio != "" {
			// Truncate long bios
			bio := p.Bio
			if len(bio) > 200 {
				bio = bio[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("Bio: %s\n", bio))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// formatRAGResults formats RAG search results for display.
func (e *ToolExecutor) formatRAGResults(topics []rag.TopicSearchResult, query string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d topics:\n\n", len(topics)))
	for i, t := range topics {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, t.Topic.Summary))
		sb.WriteString(fmt.Sprintf("   Date: %s\n", t.Topic.CreatedAt.Format("2006-01-02")))
		sb.WriteString(fmt.Sprintf("   Messages: %d\n", len(t.Messages)))
		if len(t.Messages) > 0 {
			// Show first message as preview
			preview := t.Messages[0].Content
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			sb.WriteString(fmt.Sprintf("   Preview: %s\n", preview))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
