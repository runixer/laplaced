package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// performManagePeople handles batch people management operations.
func (e *ToolExecutor) performManagePeople(ctx context.Context, userID int64, args map[string]interface{}) (string, error) {
	if e.peopleRepo == nil {
		return "", fmt.Errorf("people management is not available")
	}

	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query argument is missing")
	}

	var params map[string]interface{}
	if err := json.Unmarshal([]byte(query), &params); err != nil {
		return "", fmt.Errorf("parsing query JSON: %w", err)
	}

	operation, _ := params["operation"].(string)
	name, _ := params["name"].(string)

	switch operation {
	case "create":
		if name == "" {
			return "", fmt.Errorf("name is required for create operation")
		}
		return e.performCreatePerson(ctx, userID, name, params)
	case "update":
		// person_id is preferred, name is fallback
		if _, hasPersonID := params["person_id"]; !hasPersonID && name == "" {
			return "", fmt.Errorf("person_id or name is required for update operation")
		}
		return e.performUpdatePerson(ctx, userID, name, params)
	case "delete":
		// person_id is preferred, name is fallback
		if _, hasPersonID := params["person_id"]; !hasPersonID && name == "" {
			return "", fmt.Errorf("person_id or name is required for delete operation")
		}
		return e.performDeletePerson(ctx, userID, name, params)
	case "merge":
		// target_id/source_id are preferred (Person:123 format), target/source are fallback names
		targetID, hasTargetID := params["target_id"]
		sourceID, hasSourceID := params["source_id"]
		target, _ := params["target"].(string)
		source, _ := params["source"].(string)
		if !hasTargetID && target == "" {
			return "", fmt.Errorf("target_id or target is required for merge operation")
		}
		if !hasSourceID && source == "" {
			return "", fmt.Errorf("source_id or source is required for merge operation")
		}
		return e.performMergePeople(ctx, userID, target, source, targetID, sourceID, params)
	default:
		return "", fmt.Errorf("unknown operation '%s'. Valid operations: create, update, delete, merge", operation)
	}
}

// performCreatePerson creates a new person in the social graph.
func (e *ToolExecutor) performCreatePerson(ctx context.Context, userID int64, name string, params map[string]interface{}) (string, error) {
	// Check if person already exists
	existing, err := e.peopleRepo.FindPersonByName(userID, name)
	if err != nil {
		return "", fmt.Errorf("checking existing person: %w", err)
	}
	if existing != nil {
		return "", fmt.Errorf("person '%s' already exists (ID: %d). Use 'update' operation to modify", name, existing.ID)
	}

	// Also check aliases
	aliasMatches, err := e.peopleRepo.FindPersonByAlias(userID, name)
	if err != nil {
		return "", fmt.Errorf("checking aliases: %w", err)
	}
	if len(aliasMatches) > 0 {
		return "", fmt.Errorf("person with alias '%s' already exists: %s (ID: %d). Use 'update' operation to modify", name, aliasMatches[0].DisplayName, aliasMatches[0].ID)
	}

	// Build the person
	person := storage.Person{
		UserID:       userID,
		DisplayName:  name,
		Circle:       "Other", // default
		MentionCount: 1,
		FirstSeen:    time.Now(),
		LastSeen:     time.Now(),
	}

	// Apply optional fields
	if circle, ok := params["circle"].(string); ok && circle != "" {
		person.Circle = circle
	}
	if bio, ok := params["bio"].(string); ok {
		person.Bio = bio
	}
	if aliases, ok := params["aliases"].([]interface{}); ok {
		for _, alias := range aliases {
			if a, ok := alias.(string); ok {
				person.Aliases = append(person.Aliases, a)
			}
		}
	}
	if username, ok := params["username"].(string); ok && username != "" {
		// Strip @ if present
		username = strings.TrimPrefix(username, "@")
		person.Username = &username
	}

	// Generate embedding for searchable fields (name, aliases, bio)
	embText := name
	if len(person.Aliases) > 0 {
		embText += " " + strings.Join(person.Aliases, " ")
	}
	if person.Bio != "" {
		embText += " " + person.Bio
	}
	resp, err := e.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model:      e.cfg.Embedding.Model,
		Dimensions: e.cfg.Embedding.Dimensions,
		Input:      []string{embText},
	})
	if err != nil {
		e.logger.Warn("failed to generate person embedding", "error", err, "person", name)
	} else if len(resp.Data) > 0 {
		person.Embedding = resp.Data[0].Embedding
	}

	// Create the person
	personID, err := e.peopleRepo.AddPerson(person)
	if err != nil {
		return "", fmt.Errorf("creating person: %w", err)
	}

	e.logger.Info("Person created via manage_people tool",
		"user_id", userID,
		"person_id", personID,
		"person_name", name,
		"circle", person.Circle,
	)

	return fmt.Sprintf("Successfully created person '%s' (ID: %d, Circle: %s)", name, personID, person.Circle), nil
}

// performUpdatePerson updates a person's information.
// Supports both person_id (preferred "Person:123" format) and name (fallback) for lookup.
func (e *ToolExecutor) performUpdatePerson(ctx context.Context, userID int64, name string, params map[string]interface{}) (string, error) {
	var person *storage.Person
	var err error

	// Try person_id first (from <relevant_people> context) - accepts "Person:123" or numeric
	if v, ok := params["person_id"]; ok {
		personID, parseErr := ParsePersonID(v)
		if parseErr == nil && personID > 0 {
			// Log warning if LLM used numeric format instead of "Person:N" format
			if _, isNumeric := v.(float64); isNumeric && e.logger != nil {
				e.logger.Warn("LLM used numeric person_id, expected 'Person:N' format",
					"person_id", personID, "user_id", userID)
			}
			person, err = e.peopleRepo.GetPerson(userID, personID)
			if err != nil {
				return "", fmt.Errorf("finding person by ID %d: %w", personID, err)
			}
		}
	}

	// Fallback to name search
	if person == nil && name != "" {
		person, err = lookupPersonByName(e.peopleRepo, userID, name)
		if err != nil {
			return "", err
		}
	}

	if person == nil {
		return "", fmt.Errorf("person not found (no ID or name provided)")
	}

	updates, _ := params["updates"].(map[string]interface{})
	if updates == nil {
		return "", fmt.Errorf("updates object is required")
	}

	// Track if embedding-relevant fields changed
	needsReembed := false

	// Apply updates
	if circle, ok := updates["circle"].(string); ok {
		person.Circle = circle
	}
	if bioAppend, ok := updates["bio_append"].(string); ok && bioAppend != "" {
		if person.Bio != "" {
			person.Bio = person.Bio + " " + bioAppend
		} else {
			person.Bio = bioAppend
		}
		needsReembed = true
	}
	if aliasesAdd, ok := updates["aliases_add"].([]interface{}); ok && len(aliasesAdd) > 0 {
		for _, alias := range aliasesAdd {
			if a, ok := alias.(string); ok {
				person.Aliases = append(person.Aliases, a)
			}
		}
		needsReembed = true
	}

	// Regenerate embedding if searchable fields changed
	if needsReembed {
		embText := person.DisplayName
		if len(person.Aliases) > 0 {
			embText += " " + strings.Join(person.Aliases, " ")
		}
		if person.Bio != "" {
			embText += " " + person.Bio
		}
		resp, err := e.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
			Model:      e.cfg.Embedding.Model,
			Dimensions: e.cfg.Embedding.Dimensions,
			Input:      []string{embText},
		})
		if err != nil {
			e.logger.Warn("failed to regenerate person embedding", "error", err, "person", name)
		} else if len(resp.Data) > 0 {
			person.Embedding = resp.Data[0].Embedding
		}
	}

	// Update the person
	if err := e.peopleRepo.UpdatePerson(*person); err != nil {
		return "", fmt.Errorf("updating person: %w", err)
	}

	e.logger.Info("Person updated via manage_people tool",
		"user_id", userID,
		"person_id", person.ID,
		"person_name", person.DisplayName,
		"reembedded", needsReembed,
	)

	return fmt.Sprintf("Successfully updated person '%s'", name), nil
}

// performDeletePerson deletes a person from memory.
// Supports both person_id (preferred "Person:123" format) and name (fallback) for lookup.
func (e *ToolExecutor) performDeletePerson(ctx context.Context, userID int64, name string, params map[string]interface{}) (string, error) {
	var person *storage.Person
	var err error

	// Try person_id first (from <relevant_people> context) - accepts "Person:123" or numeric
	if v, ok := params["person_id"]; ok {
		personID, parseErr := ParsePersonID(v)
		if parseErr == nil && personID > 0 {
			// Log warning if LLM used numeric format instead of "Person:N" format
			if _, isNumeric := v.(float64); isNumeric && e.logger != nil {
				e.logger.Warn("LLM used numeric person_id, expected 'Person:N' format",
					"person_id", personID, "user_id", userID)
			}
			person, err = e.peopleRepo.GetPerson(userID, personID)
			if err != nil {
				return "", fmt.Errorf("finding person by ID %d: %w", personID, err)
			}
		}
	}

	// Fallback to name search
	if person == nil && name != "" {
		person, err = lookupPersonByName(e.peopleRepo, userID, name)
		if err != nil {
			return "", err
		}
	}

	if person == nil {
		return "", fmt.Errorf("person not found (no ID or name provided)")
	}

	reason, _ := params["reason"].(string)

	// Delete the person
	if err := e.peopleRepo.DeletePerson(userID, person.ID); err != nil {
		return "", fmt.Errorf("deleting person: %w", err)
	}

	e.logger.Info("Person deleted via manage_people tool",
		"user_id", userID,
		"person_id", person.ID,
		"person_name", person.DisplayName,
		"reason", reason,
	)

	return fmt.Sprintf("Successfully deleted person '%s'", name), nil
}

// performMergePeople merges two people records.
//
// Complexity: MEDIUM-HIGH (CC=43) - handles ID parsing, name/alias fallbacks, validation
// Dependencies: peopleRepo
// Side effects: Merges people in DB, regenerates embeddings
// Error handling: Returns formatted error messages for LLM consumption
//
// Supports both target_id/source_id (preferred "Person:123" format) and target/source names (fallback).
func (e *ToolExecutor) performMergePeople(ctx context.Context, userID int64, targetName, sourceName string, targetID, sourceID interface{}, params map[string]interface{}) (string, error) {
	var target, source *storage.Person
	var err error

	// Try target_id first (Person:123 format or numeric)
	if targetID != nil {
		personID, parseErr := ParsePersonID(targetID)
		if parseErr == nil && personID > 0 {
			if _, isNumeric := targetID.(float64); isNumeric && e.logger != nil {
				e.logger.Warn("LLM used numeric target_id, expected 'Person:N' format",
					"target_id", personID, "user_id", userID)
			}
			target, err = e.peopleRepo.GetPerson(userID, personID)
			if err != nil {
				return "", fmt.Errorf("finding target person by ID %d: %w", personID, err)
			}
		}
	}
	// Fallback to name search for target
	if target == nil && targetName != "" {
		target, err = e.peopleRepo.FindPersonByName(userID, targetName)
		if err != nil {
			return "", fmt.Errorf("finding target person: %w", err)
		}
		if target == nil {
			// Try alias search
			aliasPeople, err := e.peopleRepo.FindPersonByAlias(userID, targetName)
			if err != nil {
				return "", fmt.Errorf("finding target person: %w", err)
			}
			if len(aliasPeople) > 0 {
				target = &aliasPeople[0]
			}
		}
	}
	if target == nil {
		return "", fmt.Errorf("target person not found (no ID or name provided)")
	}

	// Try source_id first (Person:123 format or numeric)
	if sourceID != nil {
		personID, parseErr := ParsePersonID(sourceID)
		if parseErr == nil && personID > 0 {
			if _, isNumeric := sourceID.(float64); isNumeric && e.logger != nil {
				e.logger.Warn("LLM used numeric source_id, expected 'Person:N' format",
					"source_id", personID, "user_id", userID)
			}
			source, err = e.peopleRepo.GetPerson(userID, personID)
			if err != nil {
				return "", fmt.Errorf("finding source person by ID %d: %w", personID, err)
			}
		}
	}
	// Fallback to name search for source
	if source == nil && sourceName != "" {
		source, err = e.peopleRepo.FindPersonByName(userID, sourceName)
		if err != nil {
			return "", fmt.Errorf("finding source person: %w", err)
		}
		if source == nil {
			// Try alias search
			aliasPeople, err := e.peopleRepo.FindPersonByAlias(userID, sourceName)
			if err != nil {
				return "", fmt.Errorf("finding source person: %w", err)
			}
			if len(aliasPeople) > 0 {
				source = &aliasPeople[0]
			}
		}
	}
	if source == nil {
		return "", fmt.Errorf("source person not found (no ID or name provided)")
	}

	// Prevent self-merge
	if target.ID == source.ID {
		return "", fmt.Errorf("cannot merge person with itself (ID: %d)", target.ID)
	}

	reason, _ := params["reason"].(string)

	// Build merged bio: combine both bios
	newBio := target.Bio
	if source.Bio != "" {
		if newBio != "" {
			newBio = newBio + "\n" + source.Bio
		} else {
			newBio = source.Bio
		}
	}

	// Build merged aliases: combine all unique aliases
	aliasSet := make(map[string]struct{})
	for _, a := range target.Aliases {
		aliasSet[a] = struct{}{}
	}
	for _, a := range source.Aliases {
		aliasSet[a] = struct{}{}
	}
	// Add source name as alias if different from target
	if source.DisplayName != target.DisplayName {
		aliasSet[source.DisplayName] = struct{}{}
	}
	newAliases := make([]string, 0, len(aliasSet))
	for a := range aliasSet {
		newAliases = append(newAliases, a)
	}

	// Determine username and telegram_id to use (prefer target's if they exist)
	var newUsername *string
	if target.Username != nil && *target.Username != "" {
		newUsername = target.Username // Keep target's username
	} else if source.Username != nil && *source.Username != "" {
		newUsername = source.Username // Inherit from source
	}

	var newTelegramID *int64
	if target.TelegramID != nil && *target.TelegramID != 0 {
		newTelegramID = target.TelegramID // Keep target's telegram_id
	} else if source.TelegramID != nil && *source.TelegramID != 0 {
		newTelegramID = source.TelegramID // Inherit from source
	}

	// Merge source into target
	if err := e.peopleRepo.MergePeople(userID, target.ID, source.ID, newBio, newAliases, newUsername, newTelegramID); err != nil {
		return "", fmt.Errorf("merging people: %w", err)
	}

	e.logger.Info("People merged via manage_people tool",
		"user_id", userID,
		"target_id", target.ID,
		"target_name", target.DisplayName,
		"source_id", source.ID,
		"source_name", source.DisplayName,
		"reason", reason,
	)

	return fmt.Sprintf("Successfully merged '%s' into '%s'", sourceName, targetName), nil
}

// lookupPersonByName resolves a person given a name the LLM passed in. Tries an
// exact display_name match, then an alias lookup, and finally — if the name
// looks like the composite "Name (@handle)" rendering the LLM sees in
// <relevant_people> context — retries the display_name match with the handle
// suffix stripped. Returns (nil, nil) when no match is found so the caller can
// emit a "person 'X' not found" error in the same shape as before.
func lookupPersonByName(repo storage.PeopleRepository, userID int64, name string) (*storage.Person, error) {
	person, err := repo.FindPersonByName(userID, name)
	if err != nil {
		return nil, fmt.Errorf("finding person: %w", err)
	}
	if person != nil {
		return person, nil
	}

	aliasPeople, err := repo.FindPersonByAlias(userID, name)
	if err == nil && len(aliasPeople) > 0 {
		return &aliasPeople[0], nil
	}

	if stripped := StripHandleSuffix(name); stripped != "" {
		person, err = repo.FindPersonByName(userID, stripped)
		if err != nil {
			return nil, fmt.Errorf("finding person: %w", err)
		}
		if person != nil {
			return person, nil
		}
	}

	return nil, fmt.Errorf("person '%s' not found", name)
}
