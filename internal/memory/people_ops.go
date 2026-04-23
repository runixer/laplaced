package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent/archivist"
	"github.com/runixer/laplaced/internal/storage"
)

// applyPeopleAdded handles creation of new people with deduplication.
// Converts to UPDATE if duplicate detected via prefix matching.
// Returns stats, list of newly added people (for subsequent lookups), and error.
func (s *Service) applyPeopleAdded(ctx context.Context, userID int64, added []archivist.AddedPerson, currentPeople []storage.Person, referenceDate time.Time) (PeopleStats, []storage.Person, error) {
	var stats PeopleStats
	var addedPeople []storage.Person

	for _, added := range added {
		// Check if person already exists by name or alias
		existingPerson, matchType := matchPersonByName(added.DisplayName, added.Circle, currentPeople)

		if existingPerson != nil {
			if matchType == "prefix" || matchType == "prefix_reverse" {
				// Convert add to update: rename existing person to fuller name and update bio
				s.logger.Info("Detected name variation, converting add to update",
					"new_name", added.DisplayName,
					"existing_name", existingPerson.DisplayName,
					"existing_id", existingPerson.ID,
					"match_type", matchType)

				// Use the fuller name as display_name
				newDisplayName := added.DisplayName
				if matchType == "prefix_reverse" {
					newDisplayName = existingPerson.DisplayName // Keep existing fuller name
				}

				// Add old name to aliases if not already there
				oldName := existingPerson.DisplayName
				if matchType == "prefix_reverse" {
					oldName = added.DisplayName
				}

				// Merge aliases from both sources
				newAliases := mergePersonAliases(existingPerson.Aliases, oldName, added.Aliases, newDisplayName)

				// Update the person with fuller name and merged bio
				updatedPerson := *existingPerson
				updatedPerson.DisplayName = newDisplayName
				updatedPerson.Aliases = newAliases
				if added.Bio != "" {
					updatedPerson.Bio = added.Bio // Use new bio (it's likely more complete)
				}
				updatedPerson.LastSeen = referenceDate
				updatedPerson.MentionCount++

				// Regenerate embedding with new data
				if err := s.regeneratePersonEmbedding(ctx, &updatedPerson, &stats); err != nil {
					s.logger.Error("failed to regenerate embedding for person update", "name", updatedPerson.DisplayName, "error", err)
				}

				if err := s.peopleRepo.UpdatePerson(updatedPerson); err != nil {
					s.logger.Error("failed to update person (name variation)", "id", updatedPerson.ID, "error", err)
				} else {
					s.logger.Info("Person updated (name variation)", "id", updatedPerson.ID, "name", updatedPerson.DisplayName)
					stats.Updated++
				}
				continue
			}

			s.logger.Info("Person already exists, skipping add", "name", added.DisplayName, "existing_id", existingPerson.ID, "match_type", matchType)
			continue
		}

		// Get embedding for person (includes display_name, username, aliases, bio)
		embText := buildPersonEmbeddingText(added.DisplayName, nil, added.Aliases, added.Bio)
		emb, embUsage, err := s.getEmbedding(ctx, embText)
		addEmbeddingStats(&stats, embUsage)
		if err != nil {
			s.logger.Error("failed to get embedding for person", "name", added.DisplayName, "error", err)
			// Continue without embedding
		}

		person := storage.Person{
			UserID:       userID,
			DisplayName:  added.DisplayName,
			Aliases:      added.Aliases,
			Circle:       added.Circle,
			Bio:          added.Bio,
			Embedding:    emb,
			FirstSeen:    referenceDate,
			LastSeen:     referenceDate,
			MentionCount: 1,
		}

		if person.Circle == "" {
			person.Circle = "Other"
		}
		if person.Aliases == nil {
			person.Aliases = []string{}
		}

		personID, err := s.peopleRepo.AddPerson(person)
		if err != nil {
			// Check if this is a UNIQUE constraint error (person was added by concurrent process)
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				// Find the existing person and update them instead
				existing, findErr := s.peopleRepo.FindPersonByName(userID, added.DisplayName)
				if findErr != nil || existing == nil {
					s.logger.Error("failed to find existing person after UNIQUE error", "name", added.DisplayName, "error", findErr)
					continue
				}

				// Merge bio if needed
				mergedBio := existing.Bio
				if added.Bio != "" && added.Bio != existing.Bio {
					if existing.Bio != "" {
						mergedBio = existing.Bio + " " + added.Bio
					} else {
						mergedBio = added.Bio
					}
				}

				// Update the existing person
				existing.Bio = mergedBio
				if added.Circle != "" && added.Circle != "Other" {
					existing.Circle = added.Circle
				}
				existing.LastSeen = referenceDate
				existing.MentionCount++
				existing.Embedding = emb // Use the new embedding

				if updateErr := s.peopleRepo.UpdatePerson(*existing); updateErr != nil {
					s.logger.Error("failed to update existing person", "name", added.DisplayName, "error", updateErr)
					continue
				}

				stats.Updated++
				s.logger.Info("Person updated (was UNIQUE conflict)", "id", existing.ID, "name", added.DisplayName, "reason", added.Reason)
				continue
			}

			s.logger.Error("failed to add person", "name", added.DisplayName, "error", err)
			continue
		}

		person.ID = personID
		addedPeople = append(addedPeople, person)
		stats.Added++
		s.logger.Info("Person added", "id", personID, "name", added.DisplayName, "circle", added.Circle, "reason", added.Reason)
	}

	return stats, addedPeople, nil
}

// applyPeopleUpdated applies field changes to existing people.
// Handles ID/name lookup with fallbacks, conditional re-embedding.
func (s *Service) applyPeopleUpdated(ctx context.Context, userID int64, updated []archivist.UpdatedPerson, currentPeople []storage.Person, referenceDate time.Time) (PeopleStats, error) {
	var stats PeopleStats

	for _, upd := range updated {
		// Try to extract person_id first (new format)
		personIDStr, hasPersonID := upd.GetPersonID()

		// Find existing person
		existingPerson := findPersonByIDOrName(personIDStr, hasPersonID, upd.DisplayName, currentPeople, s.logger)

		if existingPerson == nil {
			switch {
			case hasPersonID:
				s.logger.Warn("Person to update not found", "person_id", personIDStr)
			case upd.DisplayName != "":
				s.logger.Warn("Person to update not found", "name", upd.DisplayName)
			default:
				s.logger.Warn("Person to update not found", "person_id", "", "name", "")
			}
			continue
		}

		// Prepare updated person
		updatedPerson, needsReembed := preparePersonUpdate(*existingPerson, upd, referenceDate)

		// Regenerate embedding if any searchable field changed, or if person had no embedding
		if needsReembed || len(existingPerson.Embedding) == 0 {
			if err := s.regeneratePersonEmbedding(ctx, &updatedPerson, &stats); err != nil {
				s.logger.Error("failed to regenerate embedding for person update", "name", upd.DisplayName, "error", err)
				updatedPerson.Embedding = existingPerson.Embedding // Keep old embedding on error
			}
		} else {
			updatedPerson.Embedding = existingPerson.Embedding
		}

		if err := s.peopleRepo.UpdatePerson(updatedPerson); err != nil {
			s.logger.Error("failed to update person", "name", upd.DisplayName, "error", err)
			continue
		}

		stats.Updated++
		s.logger.Info("Person updated", "id", updatedPerson.ID, "name", updatedPerson.DisplayName, "reason", upd.Reason)
	}

	return stats, nil
}

// applyPeopleMerged consolidates duplicate people records.
// Merges bio, aliases, username, telegram_id; deletes source.
func (s *Service) applyPeopleMerged(ctx context.Context, userID int64, merged []archivist.MergedPerson, currentPeople []storage.Person) (PeopleStats, error) {
	var stats PeopleStats

	for _, m := range merged {
		targetPerson, sourcePerson := findMergeTargetAndSource(m, currentPeople, s.logger)

		if targetPerson == nil {
			targetIDStr, _ := m.GetTargetID()
			if targetIDStr != "" {
				s.logger.Warn("Target person for merge not found", "target_id", targetIDStr)
			} else {
				s.logger.Warn("Target person for merge not found", "target_name", m.TargetName)
			}
			continue
		}
		if sourcePerson == nil {
			sourceIDStr, _ := m.GetSourceID()
			if sourceIDStr != "" {
				s.logger.Warn("Source person for merge not found", "source_id", sourceIDStr)
			} else {
				s.logger.Warn("Source person for merge not found", "source_name", m.SourceName)
			}
			continue
		}

		// Prepare merged data
		newBio, uniqueAliases, newUsername, newTelegramID := prepareMergedData(targetPerson, sourcePerson)

		if err := s.peopleRepo.MergePeople(userID, targetPerson.ID, sourcePerson.ID, newBio, uniqueAliases, newUsername, newTelegramID); err != nil {
			s.logger.Error("failed to merge people", "target", m.TargetName, "source", m.SourceName, "error", err)
			continue
		}

		// Update embedding after merge
		if err := s.updateMergedPersonEmbedding(ctx, targetPerson, newBio, uniqueAliases, newUsername, newTelegramID, &stats); err != nil {
			s.logger.Error("failed to update merged person embedding", "name", m.TargetName, "error", err)
		}

		stats.Merged++
		s.logger.Info("People merged", "target", m.TargetName, "source", m.SourceName, "reason", m.Reason)
	}

	return stats, nil
}

// findMergeTargetAndSource locates target and source persons for merge operation.
// Checks by ID first (preferred), then falls back to name matching.
func findMergeTargetAndSource(m archivist.MergedPerson, people []storage.Person, logger *slog.Logger) (target, source *storage.Person) {
	targetIDStr, hasTargetID := m.GetTargetID()
	sourceIDStr, hasSourceID := m.GetSourceID()

	// Priority 1: Use person_id if provided
	if hasTargetID {
		targetID, _ := strconv.ParseInt(targetIDStr, 10, 64)
		for _, p := range people {
			if p.ID == targetID {
				target = &p
				break
			}
		}
	}
	if hasSourceID {
		sourceID, _ := strconv.ParseInt(sourceIDStr, 10, 64)
		for _, p := range people {
			if p.ID == sourceID {
				source = &p
				break
			}
		}
	}

	// Priority 2: Fallback to name matching (for backward compatibility)
	if target == nil && m.TargetName != "" {
		for _, p := range people {
			if p.DisplayName == m.TargetName {
				target = &p
				break
			}
		}
	}
	if source == nil && m.SourceName != "" {
		for _, p := range people {
			if p.DisplayName == m.SourceName {
				source = &p
				break
			}
		}
	}

	return target, source
}

// prepareMergedData combines data from target and source persons for merge.
// Returns merged bio, deduplicated aliases, and merged username/telegram_id.
func prepareMergedData(target, source *storage.Person) (bio string, aliases []string, username *string, telegramID *int64) {
	// Combine bios
	bio = target.Bio
	if source.Bio != "" {
		if bio != "" {
			bio += " " + source.Bio
		} else {
			bio = source.Bio
		}
	}

	// Combine and deduplicate aliases
	newAliases := append([]string{}, target.Aliases...)
	newAliases = append(newAliases, source.DisplayName)
	newAliases = append(newAliases, source.Aliases...)

	seen := make(map[string]bool)
	uniqueAliases := []string{}
	for _, alias := range newAliases {
		if !seen[alias] && alias != target.DisplayName {
			seen[alias] = true
			uniqueAliases = append(uniqueAliases, alias)
		}
	}

	// Merge username: prefer target's value if exists, otherwise use source's
	var newUsername *string
	if target.Username != nil && *target.Username != "" {
		newUsername = target.Username
	} else if source.Username != nil && *source.Username != "" {
		newUsername = source.Username
	}

	// Merge telegram_id: prefer target's value if exists, otherwise use source's
	var newTelegramID *int64
	if target.TelegramID != nil && *target.TelegramID != 0 {
		newTelegramID = target.TelegramID
	} else if source.TelegramID != nil && *source.TelegramID != 0 {
		newTelegramID = source.TelegramID
	}

	return bio, uniqueAliases, newUsername, newTelegramID
}

// updateMergedPersonEmbedding regenerates and updates embedding for merged person.
func (s *Service) updateMergedPersonEmbedding(ctx context.Context, target *storage.Person, bio string, aliases []string, username *string, telegramID *int64, stats *PeopleStats) error {
	embUsername := username
	if embUsername == nil {
		embUsername = target.Username // Fallback (will be nil if both are nil)
	}
	embText := buildPersonEmbeddingText(target.DisplayName, embUsername, aliases, bio)
	emb, embUsage, err := s.getEmbedding(ctx, embText)
	addEmbeddingStats(stats, embUsage)
	if err != nil {
		return err
	}

	// Update merged person with new embedding and data
	mergedPerson := *target
	mergedPerson.Bio = bio
	mergedPerson.Aliases = aliases
	mergedPerson.Username = username
	mergedPerson.TelegramID = telegramID
	mergedPerson.Embedding = emb
	if err := s.peopleRepo.UpdatePerson(mergedPerson); err != nil {
		return fmt.Errorf("failed to update person: %w", err)
	}

	return nil
}

// matchPersonByName finds existing person by exact name, alias, or prefix.
// Returns match and matchType ("exact", "alias", "prefix", "prefix_reverse").
func matchPersonByName(name string, circle string, people []storage.Person) (*storage.Person, string) {
	for _, p := range people {
		if p.DisplayName == name {
			return &p, "exact"
		}
		// Check aliases
		for _, alias := range p.Aliases {
			if alias == name {
				return &p, "alias"
			}
		}
	}

	// Check for name prefix pattern (e.g., "Мария" vs "Мария Ивановна Петрова")
	// This catches Russian naming patterns: first name → +patronymic → +surname
	for i := range people {
		p := &people[i]
		// New name starts with existing name (e.g., "Мария Ивановна" starts with "Мария")
		if strings.HasPrefix(name, p.DisplayName+" ") && p.Circle == circle {
			return p, "prefix"
		}
		// Existing name starts with new name (e.g., existing "Мария Ивановна", adding "Мария")
		if strings.HasPrefix(p.DisplayName, name+" ") && p.Circle == circle {
			return p, "prefix_reverse"
		}
	}

	return nil, ""
}

// mergePersonAliases combines aliases from two sources without duplicates.
// Excludes aliases that equal targetDisplayName.
func mergePersonAliases(targetAliases []string, sourceName string, sourceAliases []string, targetDisplayName string) []string {
	newAliases := append([]string{}, targetAliases...)

	// Add old name if not already there and not equal to display name
	aliasExists := false
	for _, a := range targetAliases {
		if a == sourceName {
			aliasExists = true
			break
		}
	}
	if !aliasExists && sourceName != targetDisplayName {
		newAliases = append(newAliases, sourceName)
	}

	// Add any new aliases from source
	for _, a := range sourceAliases {
		found := false
		for _, ea := range newAliases {
			if ea == a {
				found = true
				break
			}
		}
		if !found && a != targetDisplayName {
			newAliases = append(newAliases, a)
		}
	}

	return newAliases
}

// findPersonByIDOrName locates person using ID (preferred) or name (fallback).
// Handles "Person:5" format and alias matching.
func findPersonByIDOrName(personIDStr string, hasPersonID bool, displayName string, people []storage.Person, logger *slog.Logger) *storage.Person {
	var existingPerson *storage.Person

	// Priority 1: Use person_id if provided
	if hasPersonID {
		personID, _ := strconv.ParseInt(personIDStr, 10, 64)
		for i := range people {
			if people[i].ID == personID {
				existingPerson = &people[i]
				logger.Debug("Found person by person_id", "person_id", personIDStr, "name", people[i].DisplayName)
				break
			}
		}
	}

	// Priority 2: Try exact match on DisplayName (fallback)
	if existingPerson == nil && displayName != "" {
		for i := range people {
			if people[i].DisplayName == displayName {
				existingPerson = &people[i]
				break
			}
		}

		// Fallback: strip aliases in parentheses (e.g., "John Smith (@john, Johnny)" -> "John Smith")
		if existingPerson == nil {
			cleanName := displayName
			if idx := strings.Index(cleanName, " ("); idx > 0 {
				cleanName = strings.TrimSpace(cleanName[:idx])
			}
			if cleanName != displayName {
				for i := range people {
					if people[i].DisplayName == cleanName {
						existingPerson = &people[i]
						logger.Debug("Found person by stripped name", "original", displayName, "clean", cleanName)
						break
					}
				}
			}
		}

		// Fallback: try matching by alias
		if existingPerson == nil {
			for i := range people {
				for _, alias := range people[i].Aliases {
					if strings.EqualFold(alias, displayName) || strings.Contains(displayName, alias) {
						existingPerson = &people[i]
						logger.Debug("Found person by alias", "requested", displayName, "found", people[i].DisplayName)
						break
					}
				}
				if existingPerson != nil {
					break
				}
			}
		}
	}

	return existingPerson
}

// preparePersonUpdate applies changes from UpdatedPerson to existing person.
// Returns updated person and whether re-embedding is needed.
func preparePersonUpdate(existing storage.Person, upd archivist.UpdatedPerson, referenceDate time.Time) (storage.Person, bool) {
	person := storage.Person{
		ID:           existing.ID,
		UserID:       existing.UserID,
		DisplayName:  existing.DisplayName,
		Aliases:      existing.Aliases,
		TelegramID:   existing.TelegramID,
		Username:     existing.Username,
		Circle:       existing.Circle,
		Bio:          existing.Bio,
		FirstSeen:    existing.FirstSeen,
		LastSeen:     referenceDate,
		MentionCount: existing.MentionCount + 1,
	}

	needsReembed := false

	// Apply updates
	if upd.Bio != "" && upd.Bio != existing.Bio {
		person.Bio = upd.Bio
		needsReembed = true
	}
	if upd.Circle != "" {
		person.Circle = upd.Circle
	}
	// Add new aliases from Archivist
	if len(upd.Aliases) > 0 {
		existingAliasSet := make(map[string]bool)
		for _, a := range person.Aliases {
			existingAliasSet[a] = true
		}
		for _, newAlias := range upd.Aliases {
			if !existingAliasSet[newAlias] && newAlias != person.DisplayName {
				person.Aliases = append(person.Aliases, newAlias)
				needsReembed = true
			}
		}
	}
	// Handle rename: new_display_name moves old name to aliases
	if upd.NewDisplayName != "" && upd.NewDisplayName != person.DisplayName {
		oldName := person.DisplayName
		person.DisplayName = upd.NewDisplayName
		// Add old name to aliases if not already there
		hasOldName := false
		for _, alias := range person.Aliases {
			if alias == oldName {
				hasOldName = true
				break
			}
		}
		if !hasOldName {
			person.Aliases = append(person.Aliases, oldName)
		}
		needsReembed = true
	}

	return person, needsReembed
}

// regeneratePersonEmbedding creates new embedding for person's searchable fields.
// Updates stats with token usage and cost.
func (s *Service) regeneratePersonEmbedding(ctx context.Context, person *storage.Person, stats *PeopleStats) error {
	embText := buildPersonEmbeddingText(person.DisplayName, person.Username, person.Aliases, person.Bio)
	emb, embUsage, err := s.getEmbedding(ctx, embText)
	addEmbeddingStats(stats, embUsage)
	if err != nil {
		return fmt.Errorf("failed to get embedding: %w", err)
	}
	person.Embedding = emb
	return nil
}

// addEmbeddingStats adds embedding usage to stats.
func addEmbeddingStats(stats *PeopleStats, usage embeddingUsage) {
	stats.EmbeddingTokens += usage.Tokens
	if usage.Cost != nil {
		if stats.EmbeddingCost == nil {
			stats.EmbeddingCost = new(float64)
		}
		*stats.EmbeddingCost += *usage.Cost
	}
}

// add merges another PeopleStats into this one.
func (s *PeopleStats) add(other PeopleStats) {
	s.Added += other.Added
	s.Updated += other.Updated
	s.Merged += other.Merged
	s.EmbeddingTokens += other.EmbeddingTokens
	if other.EmbeddingCost != nil {
		if s.EmbeddingCost == nil {
			s.EmbeddingCost = new(float64)
		}
		*s.EmbeddingCost += *other.EmbeddingCost
	}
}
