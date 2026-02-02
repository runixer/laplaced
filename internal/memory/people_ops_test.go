package memory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/archivist"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestMatchPersonByName(t *testing.T) {
	tests := []struct {
		name          string
		searchName    string
		circle        string
		people        []storage.Person
		wantMatch     bool
		wantMatchType string
	}{
		{
			name:       "exact match",
			searchName: "John Doe",
			circle:     "Friends",
			people: []storage.Person{
				{ID: 1, DisplayName: "John Doe", Circle: "Friends"},
			},
			wantMatch:     true,
			wantMatchType: "exact",
		},
		{
			name:       "alias match",
			searchName: "Johnny",
			circle:     "Friends",
			people: []storage.Person{
				{ID: 1, DisplayName: "John Doe", Circle: "Friends", Aliases: []string{"Johnny", "JD"}},
			},
			wantMatch:     true,
			wantMatchType: "alias",
		},
		{
			name:       "prefix match - new name starts with existing",
			searchName: "Мария Ивановна Петрова",
			circle:     "Family",
			people: []storage.Person{
				{ID: 1, DisplayName: "Мария", Circle: "Family"},
			},
			wantMatch:     true,
			wantMatchType: "prefix",
		},
		{
			name:       "prefix reverse - existing name starts with new",
			searchName: "Мария",
			circle:     "Family",
			people: []storage.Person{
				{ID: 1, DisplayName: "Мария Ивановна", Circle: "Family"},
			},
			wantMatch:     true,
			wantMatchType: "prefix_reverse",
		},
		{
			name:       "no match - different circle but exact name match",
			searchName: "Мария",
			circle:     "Work",
			people: []storage.Person{
				{ID: 1, DisplayName: "Мария", Circle: "Family"},
			},
			// Exact name match has priority over circle check
			wantMatch:     true,
			wantMatchType: "exact",
		},
		{
			name:       "no match - completely different name",
			searchName: "Alice Smith",
			circle:     "Friends",
			people: []storage.Person{
				{ID: 1, DisplayName: "John Doe", Circle: "Friends"},
			},
			wantMatch:     false,
			wantMatchType: "",
		},
		{
			name:       "prefix match requires space after existing name",
			searchName: "МарияИвановна",
			circle:     "Family",
			people: []storage.Person{
				{ID: 1, DisplayName: "Мария", Circle: "Family"},
			},
			wantMatch:     false,
			wantMatchType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotType := matchPersonByName(tt.searchName, tt.circle, tt.people)

			if tt.wantMatch {
				assert.NotNil(t, got)
				assert.Equal(t, tt.wantMatchType, gotType)
			} else {
				assert.Nil(t, got)
				assert.Equal(t, "", gotType)
			}
		})
	}
}

func TestMergePersonAliases(t *testing.T) {
	tests := []struct {
		name              string
		targetAliases     []string
		sourceName        string
		sourceAliases     []string
		targetDisplayName string
		wantCount         int
		containsAliases   []string
	}{
		{
			name:              "merge new aliases",
			targetAliases:     []string{"JD"},
			sourceName:        "Johnny",
			sourceAliases:     []string{"John", "Jon"},
			targetDisplayName: "John Doe",
			wantCount:         4,
			containsAliases:   []string{"JD", "Johnny", "John", "Jon"},
		},
		{
			name:              "deduplicate existing alias",
			targetAliases:     []string{"JD", "John"},
			sourceName:        "Johnny",
			sourceAliases:     []string{"John"},
			targetDisplayName: "John Doe",
			wantCount:         3,
			containsAliases:   []string{"JD", "John", "Johnny"},
		},
		{
			name:              "exclude display name from aliases",
			targetAliases:     []string{},
			sourceName:        "John Doe",
			sourceAliases:     []string{"JD"},
			targetDisplayName: "John Doe",
			wantCount:         1,
			containsAliases:   []string{"JD"},
		},
		{
			name:              "empty source aliases",
			targetAliases:     []string{"JD"},
			sourceName:        "Johnny",
			sourceAliases:     []string{},
			targetDisplayName: "John Doe",
			wantCount:         2,
			containsAliases:   []string{"JD", "Johnny"},
		},
		{
			name:              "empty target aliases",
			targetAliases:     []string{},
			sourceName:        "Johnny",
			sourceAliases:     []string{"John", "Jon"},
			targetDisplayName: "John Doe",
			wantCount:         3,
			containsAliases:   []string{"Johnny", "John", "Jon"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergePersonAliases(tt.targetAliases, tt.sourceName, tt.sourceAliases, tt.targetDisplayName)

			assert.Equal(t, tt.wantCount, len(got))
			for _, alias := range tt.containsAliases {
				assert.Contains(t, got, alias)
			}
			// Verify display name is not in aliases
			assert.NotContains(t, got, tt.targetDisplayName)
		})
	}
}

func TestFindPersonByIDOrName(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	people := []storage.Person{
		{ID: 1, DisplayName: "John Doe", Aliases: []string{"Johnny", "JD"}},
		{ID: 2, DisplayName: "Alice Smith", Aliases: []string{"Ali"}},
		{ID: 3, DisplayName: "Bob Johnson", Aliases: []string{}},
	}

	tests := []struct {
		name         string
		personIDStr  string
		hasPersonID  bool
		displayName  string
		wantID       int64
		wantNotFound bool
	}{
		{
			name:        "find by ID",
			personIDStr: "1",
			hasPersonID: true,
			displayName: "",
			wantID:      1,
		},
		{
			name:        "find by exact name",
			personIDStr: "",
			hasPersonID: false,
			displayName: "Alice Smith",
			wantID:      2,
		},
		{
			name:        "find by alias",
			personIDStr: "",
			hasPersonID: false,
			displayName: "Johnny",
			wantID:      1,
		},
		{
			name:        "find by stripped name (with alias suffix)",
			personIDStr: "",
			hasPersonID: false,
			displayName: "Bob Johnson (Bobby)",
			wantID:      3,
		},
		{
			name:         "not found",
			personIDStr:  "",
			hasPersonID:  false,
			displayName:  "Unknown Person",
			wantNotFound: true,
		},
		{
			name:         "ID not found",
			personIDStr:  "999",
			hasPersonID:  true,
			displayName:  "",
			wantNotFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findPersonByIDOrName(tt.personIDStr, tt.hasPersonID, tt.displayName, people, logger)

			if tt.wantNotFound {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
				assert.Equal(t, tt.wantID, got.ID)
			}
		})
	}
}

func TestPreparePersonUpdate(t *testing.T) {
	referenceDate := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	existing := storage.Person{
		ID:           1,
		DisplayName:  "John Doe",
		Aliases:      []string{"JD"},
		Circle:       "Friends",
		Bio:          "Old bio",
		FirstSeen:    referenceDate,
		LastSeen:     referenceDate,
		MentionCount: 5,
	}

	tests := []struct {
		name             string
		existing         storage.Person
		update           archivist.UpdatedPerson
		wantBio          string
		wantCircle       string
		wantAliases      []string
		wantReembed      bool
		wantMentionCount int
	}{
		{
			name:     "update bio triggers reembed",
			existing: existing,
			update: archivist.UpdatedPerson{
				Bio: "New bio",
			},
			wantBio:          "New bio",
			wantCircle:       "Friends",
			wantAliases:      []string{"JD"},
			wantReembed:      true,
			wantMentionCount: 6,
		},
		{
			name:     "update circle",
			existing: existing,
			update: archivist.UpdatedPerson{
				Circle: "Family",
			},
			wantBio:          "Old bio",
			wantCircle:       "Family",
			wantAliases:      []string{"JD"},
			wantReembed:      false,
			wantMentionCount: 6,
		},
		{
			name:     "add new aliases triggers reembed",
			existing: existing,
			update: archivist.UpdatedPerson{
				Aliases: []string{"Johnny", "Jon"},
			},
			wantBio:          "Old bio",
			wantCircle:       "Friends",
			wantAliases:      []string{"JD", "Johnny", "Jon"},
			wantReembed:      true,
			wantMentionCount: 6,
		},
		{
			name:     "rename person moves old name to aliases",
			existing: existing,
			update: archivist.UpdatedPerson{
				NewDisplayName: "Johnny Doe",
			},
			wantBio:          "Old bio",
			wantCircle:       "Friends",
			wantAliases:      []string{"JD", "John Doe"},
			wantReembed:      true,
			wantMentionCount: 6,
		},
		{
			name:     "deduplicate aliases on add",
			existing: existing,
			update: archivist.UpdatedPerson{
				Aliases: []string{"JD", "NewAlias"},
			},
			wantBio:          "Old bio",
			wantCircle:       "Friends",
			wantAliases:      []string{"JD", "NewAlias"},
			wantReembed:      true,
			wantMentionCount: 6,
		},
		{
			name:     "exclude display name from aliases",
			existing: existing,
			update: archivist.UpdatedPerson{
				NewDisplayName: "Johnny Doe",
				Aliases:        []string{"Johnny Doe", "NewAlias"},
			},
			wantBio:    "Old bio",
			wantCircle: "Friends",
			// Note: aliases are added before rename, so "Johnny Doe" is added
			// Then "John Doe" (old name) is added during rename
			wantAliases:      []string{"JD", "Johnny Doe", "NewAlias", "John Doe"},
			wantReembed:      true,
			wantMentionCount: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotReembed := preparePersonUpdate(tt.existing, tt.update, referenceDate)

			assert.Equal(t, tt.wantBio, got.Bio)
			assert.Equal(t, tt.wantCircle, got.Circle)
			assert.Equal(t, tt.wantAliases, got.Aliases)
			assert.Equal(t, tt.wantReembed, gotReembed)
			assert.Equal(t, tt.wantMentionCount, got.MentionCount)
		})
	}
}

func TestPrepareMergedData(t *testing.T) {
	username := "johndoe"
	telegramID := int64(123456)

	tests := []struct {
		name              string
		target            storage.Person
		source            storage.Person
		wantBio           string
		wantAliasesCount  int
		wantHasUsername   bool
		wantHasTelegramID bool
	}{
		{
			name: "merge all fields",
			target: storage.Person{
				DisplayName: "John Doe",
				Bio:         "Target bio",
				Aliases:     []string{"JD"},
				Username:    &username,
				TelegramID:  &telegramID,
			},
			source: storage.Person{
				DisplayName: "Johnny",
				Bio:         "Source bio",
				Aliases:     []string{"Jon"},
			},
			wantBio:           "Target bio Source bio",
			wantAliasesCount:  3, // JD + Johnny (source display) + Jon
			wantHasUsername:   true,
			wantHasTelegramID: true,
		},
		{
			name: "inherit username from source",
			target: storage.Person{
				DisplayName: "John Doe",
				Bio:         "Target bio",
				Aliases:     []string{"JD"},
			},
			source: storage.Person{
				DisplayName: "Johnny",
				Bio:         "Source bio",
				Username:    &username,
			},
			wantBio:           "Target bio Source bio",
			wantAliasesCount:  2,
			wantHasUsername:   true,
			wantHasTelegramID: false,
		},
		{
			name: "prefer target username over source",
			target: storage.Person{
				DisplayName: "John Doe",
				Bio:         "Target bio",
				Username:    &username,
			},
			source: storage.Person{
				DisplayName: "Johnny",
				Bio:         "Source bio",
				Username:    ptrStr("other"),
			},
			wantBio:           "Target bio Source bio",
			wantAliasesCount:  1,
			wantHasUsername:   true,
			wantHasTelegramID: false,
		},
		{
			name: "deduplicate aliases including target display name",
			target: storage.Person{
				DisplayName: "John Doe",
				Bio:         "Target bio",
				Aliases:     []string{"JD", "John Doe"}, // John Doe should be excluded
			},
			source: storage.Person{
				DisplayName: "Johnny",
				Bio:         "Source bio",
				Aliases:     []string{"JD", "Jon"},
			},
			wantBio:           "Target bio Source bio",
			wantAliasesCount:  3, // JD + Johnny + Jon (John Doe is excluded)
			wantHasUsername:   false,
			wantHasTelegramID: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBio, gotAliases, gotUsername, gotTelegramID := prepareMergedData(&tt.target, &tt.source)

			assert.Equal(t, tt.wantBio, gotBio)
			assert.Equal(t, tt.wantAliasesCount, len(gotAliases))
			assert.Equal(t, tt.wantHasUsername, gotUsername != nil)
			assert.Equal(t, tt.wantHasTelegramID, gotTelegramID != nil)
			// Verify target display name is not in aliases
			assert.NotContains(t, gotAliases, tt.target.DisplayName)
		})
	}
}

func TestPeopleStatsAdd(t *testing.T) {
	cost1 := 0.001
	cost2 := 0.002

	stats1 := PeopleStats{
		Added:           5,
		Updated:         3,
		Merged:          1,
		EmbeddingTokens: 100,
		EmbeddingCost:   &cost1,
	}

	stats2 := PeopleStats{
		Added:           2,
		Updated:         4,
		Merged:          2,
		EmbeddingTokens: 200,
		EmbeddingCost:   &cost2,
	}

	stats1.add(stats2)

	assert.Equal(t, 7, stats1.Added)
	assert.Equal(t, 7, stats1.Updated)
	assert.Equal(t, 3, stats1.Merged)
	assert.Equal(t, 300, stats1.EmbeddingTokens)
	assert.InDelta(t, 0.003, *stats1.EmbeddingCost, 0.0001)
}

func TestPeopleStatsAdd_NilCost(t *testing.T) {
	cost := 0.001

	statsWithCost := PeopleStats{
		Added:           1,
		EmbeddingTokens: 100,
		EmbeddingCost:   &cost,
	}

	statsWithoutCost := PeopleStats{
		Added:           2,
		EmbeddingTokens: 200,
		EmbeddingCost:   nil,
	}

	statsWithCost.add(statsWithoutCost)

	assert.Equal(t, 3, statsWithCost.Added)
	assert.Equal(t, 300, statsWithCost.EmbeddingTokens)
	assert.InDelta(t, 0.001, *statsWithCost.EmbeddingCost, 0.0001)
}

func TestAddEmbeddingStats(t *testing.T) {
	tests := []struct {
		name           string
		initialCost    *float64
		additionalCost *float64
		tokens         int
		wantFinalCost  float64
	}{
		{
			name:           "add to existing cost",
			initialCost:    ptrFloat64(0.001),
			additionalCost: ptrFloat64(0.002),
			tokens:         100,
			wantFinalCost:  0.003,
		},
		{
			name:           "add to nil cost initializes it",
			initialCost:    nil,
			additionalCost: ptrFloat64(0.002),
			tokens:         100,
			wantFinalCost:  0.002,
		},
		{
			name:           "add nil cost to existing keeps existing",
			initialCost:    ptrFloat64(0.001),
			additionalCost: nil,
			tokens:         100,
			wantFinalCost:  0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := &PeopleStats{
				EmbeddingCost: tt.initialCost,
			}

			usage := embeddingUsage{
				Tokens: tt.tokens,
				Cost:   tt.additionalCost,
			}

			addEmbeddingStats(stats, usage)

			assert.Equal(t, tt.tokens, stats.EmbeddingTokens)
			if tt.wantFinalCost > 0 {
				assert.NotNil(t, stats.EmbeddingCost)
				assert.InDelta(t, tt.wantFinalCost, *stats.EmbeddingCost, 0.0001)
			} else {
				assert.Nil(t, stats.EmbeddingCost)
			}
		})
	}
}

// Helper functions for tests

func ptrStr(s string) *string {
	return &s
}

func ptrFloat64(f float64) *float64 {
	return &f
}

// TestApplyPeopleAdded_EmbeddingFailureContinues tests that embedding failures don't stop person creation.
func TestApplyPeopleAdded_EmbeddingFailureContinues(t *testing.T) {
	ts := setupTestServices(t)
	svc := ts.Service
	mockStore := ts.Store
	mockOR := ts.ORClient

	userID := testutil.TestUserID
	referenceDate := time.Now()

	added := []archivist.AddedPerson{
		{DisplayName: "John Doe", Circle: "Friends", Bio: "Engineer", Reason: "new person"},
		{DisplayName: "Jane Smith", Circle: "Work", Bio: "Manager", Reason: "another"},
	}

	// First embedding fails, second succeeds
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(nil, assert.AnError).Once()
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Both people should be added (even if one has no embedding)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "John Doe" || p.DisplayName == "Jane Smith"
	})).Return(int64(1), nil).Times(2)

	stats, _, err := svc.applyPeopleAdded(context.Background(), userID, added, []storage.Person{}, referenceDate)

	assert.NoError(t, err)
	assert.Equal(t, 2, stats.Added)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

// TestApplyPeopleAdded_UNIQUEConstraintFallback tests handling of UNIQUE constraint errors.
func TestApplyPeopleAdded_UNIQUEConstraintFallback(t *testing.T) {
	ts := setupTestServices(t)
	svc := ts.Service
	mockStore := ts.Store
	mockOR := ts.ORClient

	userID := testutil.TestUserID
	referenceDate := time.Now()

	existingPerson := storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
		Bio:         "Old bio",
		Aliases:     []string{},
	}

	added := []archivist.AddedPerson{
		{DisplayName: "John Doe", Circle: "Friends", Bio: "New bio", Reason: "duplicate"},
	}

	// Embedding succeeds
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// UNIQUE constraint error (simulating concurrent insert)
	// The error must contain "UNIQUE constraint" for the fallback to work
	uniqueErr := fmt.Errorf("UNIQUE constraint failed: people.user_id_display_name")
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "John Doe"
	})).Return(int64(0), uniqueErr).Once()

	// FindPersonByName returns existing person
	mockStore.On("FindPersonByName", userID, "John Doe").Return(&existingPerson, nil).Once()

	// UpdatePerson succeeds - bio should be merged
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.Bio == "Old bio New bio"
	})).Return(nil).Once()

	stats, _, err := svc.applyPeopleAdded(context.Background(), userID, added, []storage.Person{}, referenceDate)

	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Added)
	assert.Equal(t, 1, stats.Updated)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

// TestApplyPeopleUpdated_UpdateByDisplayName tests update by display name fallback.
func TestApplyPeopleUpdated_UpdateByDisplayName(t *testing.T) {
	ts := setupTestServices(t)
	svc := ts.Service
	mockStore := ts.Store

	userID := testutil.TestUserID
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{ID: 1, UserID: userID, DisplayName: "John Doe", Circle: "Friends", MentionCount: 1, Embedding: []float32{0.1, 0.2}},
	}

	updated := []archivist.UpdatedPerson{
		{DisplayName: "John Doe", Circle: "Family", Reason: "circle change"},
	}

	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.Circle == "Family" && p.MentionCount == 2
	})).Return(nil).Once()

	stats, err := svc.applyPeopleUpdated(context.Background(), userID, updated, existingPeople, referenceDate)

	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Updated)
	mockStore.AssertExpectations(t)
}

// TestApplyPeopleUpdated_UpdateByAlias tests update by alias fallback.
func TestApplyPeopleUpdated_UpdateByAlias(t *testing.T) {
	ts := setupTestServices(t)
	svc := ts.Service
	mockStore := ts.Store

	userID := testutil.TestUserID
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{ID: 1, UserID: userID, DisplayName: "John Doe", Aliases: []string{"Johnny"}, MentionCount: 1, Embedding: []float32{0.1}},
	}

	updated := []archivist.UpdatedPerson{
		{DisplayName: "Johnny", Circle: "Family", Reason: "update via alias"},
	}

	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.Circle == "Family"
	})).Return(nil).Once()

	stats, err := svc.applyPeopleUpdated(context.Background(), userID, updated, existingPeople, referenceDate)

	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Updated)
	mockStore.AssertExpectations(t)
}

// TestApplyPeopleMerged_EmbeddingRegeneration tests embedding regeneration after merge.
func TestApplyPeopleMerged_EmbeddingRegeneration(t *testing.T) {
	ts := setupTestServices(t)
	svc := ts.Service
	mockStore := ts.Store
	mockOR := ts.ORClient

	userID := testutil.TestUserID

	targetPerson := storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
		Bio:         "Engineer",
		Aliases:     []string{"Johnny"},
	}
	sourcePerson := storage.Person{
		ID:          2,
		UserID:      userID,
		DisplayName: "J. Doe",
		Bio:         "Manager",
		Aliases:     []string{"JD"},
	}

	merged := []archivist.MergedPerson{
		{TargetName: "John Doe", SourceName: "J. Doe", Reason: "same person"},
	}

	// MergePeople succeeds
	mockStore.On("MergePeople", userID, int64(1), int64(2), mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	// Embedding regeneration after merge
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// UpdatePerson with new embedding
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.DisplayName == "John Doe"
	})).Return(nil).Once()

	stats, err := svc.applyPeopleMerged(context.Background(), userID, merged, []storage.Person{targetPerson, sourcePerson})

	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Merged)
	assert.Equal(t, 10, stats.EmbeddingTokens) // From MockEmbeddingResponse
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

// TestApplyPeopleMerged_UsernameTelegramIDInheritance tests username and telegram_id inheritance.
func TestApplyPeopleMerged_UsernameTelegramIDInheritance(t *testing.T) {
	ts := setupTestServices(t)
	svc := ts.Service
	mockStore := ts.Store
	mockOR := ts.ORClient

	userID := testutil.TestUserID

	username := "johndoe"
	telegramID := int64(123456789)

	targetPerson := storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
		Bio:         "Engineer",
		Aliases:     []string{}, // Empty aliases
		// No username/telegram_id
	}
	sourcePerson := storage.Person{
		ID:          2,
		UserID:      userID,
		DisplayName: "J. Doe",
		Bio:         "Manager",
		Aliases:     []string{}, // Empty aliases
		Username:    &username,
		TelegramID:  &telegramID,
	}

	merged := []archivist.MergedPerson{
		{TargetName: "John Doe", SourceName: "J. Doe", Reason: "same person"},
	}

	// MergePeople should be called with source's username/telegram_id
	// Aliases should contain source display name "J. Doe"
	mockStore.On("MergePeople", userID, int64(1), int64(2),
		mock.MatchedBy(func(bio string) bool {
			return bio == "Engineer Manager"
		}),
		mock.MatchedBy(func(aliases []string) bool {
			// Should contain "J. Doe" (source display name)
			return len(aliases) == 1 && aliases[0] == "J. Doe"
		}),
		mock.MatchedBy(func(newUsername *string) bool {
			return newUsername != nil && *newUsername == username
		}),
		mock.MatchedBy(func(newTelegramID *int64) bool {
			return newTelegramID != nil && *newTelegramID == telegramID
		}),
	).Return(nil).Once()

	// Embedding update after merge
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// UpdatePerson with new embedding
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.Username != nil && *p.Username == username &&
			p.TelegramID != nil && *p.TelegramID == telegramID
	})).Return(nil).Once()

	stats, err := svc.applyPeopleMerged(context.Background(), userID, merged, []storage.Person{targetPerson, sourcePerson})

	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Merged)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}
