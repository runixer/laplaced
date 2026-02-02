package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// performAddFact adds a new fact to memory.
func (e *ToolExecutor) performAddFact(ctx context.Context, userID int64, p MemoryOpParams) (string, error) {
	if p.Content == "" {
		return "Error: Content is required for adding a fact.", fmt.Errorf("missing content")
	}

	resp, err := e.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: e.cfg.Embedding.Model,
		Input: []string{p.Content},
	})
	if err != nil || len(resp.Data) == 0 {
		return "Error generating embedding.", err
	}

	// Apply defaults
	category := p.Category
	if category == "" {
		category = "other"
	}
	factType := p.FactType
	if factType == "" {
		factType = "context"
	}
	importance := p.Importance
	if importance == 0 {
		importance = 50
	}

	fact := storage.Fact{
		UserID:     userID,
		Content:    p.Content,
		Type:       factType,
		Importance: importance,
		Embedding:  resp.Data[0].Embedding,
		Relation:   "related_to",
		Category:   category,
		TopicID:    nil,
	}

	id, err := e.factRepo.AddFact(fact)
	if err != nil {
		return "Error adding fact.", err
	}

	if err := e.factHistoryRepo.AddFactHistory(storage.FactHistory{
		FactID:     id,
		UserID:     userID,
		Action:     "add",
		NewContent: p.Content,
		Reason:     p.Reason,
		Category:   category,
		Relation:   "related_to",
		Importance: importance,
		TopicID:    nil,
	}); err != nil {
		e.logger.Warn("failed to add fact history", "fact_id", id, "error", err)
	}

	return "Fact added successfully.", nil
}

// performDeleteFact deletes a fact from memory.
func (e *ToolExecutor) performDeleteFact(ctx context.Context, userID int64, p MemoryOpParams) (string, error) {
	if p.FactID == 0 {
		return "Error: Fact ID is required for deletion.", fmt.Errorf("missing fact_id")
	}

	// Fetch old fact for history
	oldFacts, err := e.factRepo.GetFactsByIDs(userID, []int64{p.FactID})
	if err != nil {
		e.logger.Warn("failed to fetch old fact for history", "fact_id", p.FactID, "error", err)
	}
	var oldContent, category, relation string
	var importance int
	if len(oldFacts) > 0 {
		oldContent = oldFacts[0].Content
		category = oldFacts[0].Category
		relation = oldFacts[0].Relation
		importance = oldFacts[0].Importance
	}

	if err := e.factRepo.DeleteFact(userID, p.FactID); err != nil {
		return "Error deleting fact.", err
	}

	if err := e.factHistoryRepo.AddFactHistory(storage.FactHistory{
		FactID:     p.FactID,
		UserID:     userID,
		Action:     "delete",
		OldContent: oldContent,
		Reason:     p.Reason,
		Category:   category,
		Relation:   relation,
		Importance: importance,
		TopicID:    nil,
	}); err != nil {
		e.logger.Warn("failed to add fact history", "fact_id", p.FactID, "error", err)
	}

	return "Fact deleted successfully.", nil
}

// performUpdateFact updates an existing fact in memory.
func (e *ToolExecutor) performUpdateFact(ctx context.Context, userID int64, p MemoryOpParams) (string, error) {
	if p.FactID == 0 {
		return "Error: Fact ID is required for update.", fmt.Errorf("missing fact_id")
	}

	resp, err := e.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: e.cfg.Embedding.Model,
		Input: []string{p.Content},
	})
	if err != nil || len(resp.Data) == 0 {
		return "Error generating embedding.", err
	}

	// Apply defaults
	factType := p.FactType
	if factType == "" {
		factType = "context"
	}
	importance := p.Importance
	if importance == 0 {
		importance = 50
	}

	// Fetch old fact for history
	oldFacts, err := e.factRepo.GetFactsByIDs(userID, []int64{p.FactID})
	if err != nil {
		e.logger.Warn("failed to fetch old fact for history", "fact_id", p.FactID, "error", err)
	}
	var oldContent, category, relation string
	if len(oldFacts) > 0 {
		oldContent = oldFacts[0].Content
		category = oldFacts[0].Category
		relation = oldFacts[0].Relation
	}

	fact := storage.Fact{
		ID:         p.FactID,
		UserID:     userID,
		Content:    p.Content,
		Type:       factType,
		Importance: importance,
		Embedding:  resp.Data[0].Embedding,
	}

	if err := e.factRepo.UpdateFact(fact); err != nil {
		return "Error updating fact.", err
	}

	if err := e.factHistoryRepo.AddFactHistory(storage.FactHistory{
		FactID:     p.FactID,
		UserID:     userID,
		Action:     "update",
		OldContent: oldContent,
		NewContent: p.Content,
		Reason:     p.Reason,
		Category:   category,
		Relation:   relation,
		Importance: importance,
		TopicID:    nil,
	}); err != nil {
		e.logger.Warn("failed to add fact history", "fact_id", p.FactID, "error", err)
	}

	return "Fact updated successfully.", nil
}

// performManageMemory handles batch memory operations.
func (e *ToolExecutor) performManageMemory(ctx context.Context, userID int64, args map[string]interface{}) (string, error) {
	// The tool receives a JSON string inside the `query` argument
	query, ok := args["query"].(string)
	if !ok {
		return "Error: query argument is missing", nil
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(query), &root); err != nil {
		return fmt.Sprintf("Error parsing query JSON: %v", err), nil
	}

	var operations []map[string]interface{}

	// Check if it's a batch operation
	if ops, ok := root["operations"].([]interface{}); ok {
		for _, op := range ops {
			if opMap, ok := op.(map[string]interface{}); ok {
				operations = append(operations, opMap)
			}
		}
	} else {
		// Single operation (legacy or simple call)
		operations = append(operations, root)
	}

	var results []string
	var errorCount int

	for i, params := range operations {
		p, err := ParseMemoryOpParams(params)
		if err != nil {
			errorCount++
			results = append(results, fmt.Sprintf("Op %d: Failed - %v", i+1, err))
			continue
		}

		// Log warning if LLM used numeric fact_id format instead of "Fact:N" format
		if v, ok := params["fact_id"]; ok && p.FactID > 0 {
			if _, isNumeric := v.(float64); isNumeric && e.logger != nil {
				e.logger.Warn("LLM used numeric fact_id, expected 'Fact:N' format",
					"fact_id", p.FactID, "user_id", userID)
			}
		}

		var result string
		var opErr error

		switch p.Action {
		case "add":
			result, opErr = e.performAddFact(ctx, userID, p)
		case "delete":
			result, opErr = e.performDeleteFact(ctx, userID, p)
		case "update":
			result, opErr = e.performUpdateFact(ctx, userID, p)
		default:
			result = fmt.Sprintf("Unknown action: %s", p.Action)
			opErr = fmt.Errorf("unknown action")
		}

		if opErr != nil {
			errorCount++
			results = append(results, fmt.Sprintf("Op %d (%s): Failed - %s (%v)", i+1, p.Action, result, opErr))
		} else {
			results = append(results, fmt.Sprintf("Op %d (%s): Success", i+1, p.Action))
		}
	}

	finalResult := strings.Join(results, "\n")
	if errorCount > 0 {
		return fmt.Sprintf("Completed with %d errors:\n%s", errorCount, finalResult), nil
	}
	return fmt.Sprintf("Successfully processed %d operations:\n%s", len(operations), finalResult), nil
}
