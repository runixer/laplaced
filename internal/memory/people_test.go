package memory

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/archivist"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestApplyPeopleUpdates_AddNewPerson(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	peopleResult := archivist.PeopleResult{
		Added: []archivist.AddedPerson{
			{
				DisplayName: "John Doe",
				Circle:      "Friends",
				Bio:         "Software engineer",
				Aliases:     []string{"Johnny", "JD"},
				Reason:      "Mentioned in conversation",
			},
		},
	}

	// Mock: embedding generation
	mockOR.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req interface{}) bool {
		return true
	})).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: add person
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.UserID == userID && p.DisplayName == "John Doe" &&
			p.Circle == "Friends" && p.Bio == "Software engineer" &&
			len(p.Aliases) == 2 && p.MentionCount == 1
	})).Return(int64(1), nil).Once()

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, []storage.Person{}, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Added)
	assert.Equal(t, 0, stats.Updated)
	assert.Equal(t, 0, stats.Merged)
	assert.Equal(t, 10, stats.EmbeddingTokens) // From MockEmbeddingResponse
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

func TestApplyPeopleUpdates_SkipDuplicatePerson(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:          1,
			UserID:      userID,
			DisplayName: "John Doe",
			Circle:      "Friends",
		},
	}

	peopleResult := archivist.PeopleResult{
		Added: []archivist.AddedPerson{
			{
				DisplayName: "John Doe",
				Circle:      "Friends",
				Reason:      "Duplicate",
			},
		},
	}

	// No embedding or AddPerson calls expected (person already exists)

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Added)
	assert.Equal(t, 0, stats.Updated)
	assert.Equal(t, 0, stats.Merged)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
	mockOR.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

func TestApplyPeopleUpdates_NameVariation_AddBecomesUpdate(t *testing.T) {
	// Setup: test Russian naming pattern "Мария" -> "Мария Ивановна"
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:          1,
			UserID:      userID,
			DisplayName: "Мария",
			Circle:      "Family",
			Bio:         "Sister",
			Aliases:     []string{},
		},
	}

	peopleResult := archivist.PeopleResult{
		Added: []archivist.AddedPerson{
			{
				DisplayName: "Мария Ивановна",
				Circle:      "Family",
				Bio:         "Older sister, works as teacher",
				Reason:      "Full name revealed",
			},
		},
	}

	// Mock: embedding generation for update
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: update person (converting add to update due to name prefix)
	// Use .Maybe() to accept any UpdatePerson call with correct ID and display name
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.DisplayName == "Мария Ивановна" && p.Bio == "Older sister, works as teacher"
	})).Return(nil).Once()

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Added)
	assert.Equal(t, 1, stats.Updated) // Add became update
	assert.Equal(t, 0, stats.Merged)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

func TestApplyPeopleUpdates_AddPersonByAlias(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:          1,
			UserID:      userID,
			DisplayName: "John Smith",
			Aliases:     []string{"Johnny"},
			Circle:      "Friends",
		},
	}

	// Try to add person with alias that matches existing person's alias
	peopleResult := archivist.PeopleResult{
		Added: []archivist.AddedPerson{
			{
				DisplayName: "Johnny", // Matches existing alias
				Circle:      "Friends",
				Reason:      "Referred by alias",
			},
		},
	}

	// No calls expected (skipped as duplicate)

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Added)
	assert.Equal(t, 0, stats.Updated)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
	mockOR.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

func TestApplyPeopleUpdates_ExistingPerson(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:           1,
			UserID:       userID,
			DisplayName:  "Jane Doe",
			Circle:       "Work_Outer",
			Bio:          "Developer",
			Aliases:      []string{},
			FirstSeen:    referenceDate.Add(-24 * time.Hour),
			MentionCount: 3,
		},
	}

	peopleResult := archivist.PeopleResult{
		Updated: []archivist.UpdatedPerson{
			{
				DisplayName: "Jane Doe",
				Circle:      "Work_Inner",
				Bio:         "Senior developer, team lead",
				Aliases:     []string{"Jane"},
				Reason:      "Promoted",
			},
		},
	}

	// Mock: embedding generation (bio changed)
	mockOR.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req interface{}) bool {
		return true
	})).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: update person
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.DisplayName == "Jane Doe" &&
			p.Circle == "Work_Inner" && p.Bio == "Senior developer, team lead" &&
			p.MentionCount == 4 && len(p.Aliases) == 1
	})).Return(nil).Once()

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Added)
	assert.Equal(t, 1, stats.Updated)
	assert.Equal(t, 0, stats.Merged)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

func TestApplyPeopleUpdates_UpdatePersonWithRename(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:           1,
			UserID:       userID,
			DisplayName:  "Jane Smith",
			Circle:       "Friends",
			Aliases:      []string{},
			MentionCount: 2,
		},
	}

	peopleResult := archivist.PeopleResult{
		Updated: []archivist.UpdatedPerson{
			{
				DisplayName:    "Jane Smith",
				NewDisplayName: "Jane Johnson", // Married
				Reason:         "Changed surname after marriage",
			},
		},
	}

	// Mock: embedding generation (rename needs re-embedding)
	mockOR.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req interface{}) bool {
		return true
	})).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: update with rename
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.DisplayName == "Jane Johnson" &&
			len(p.Aliases) == 1 && p.Aliases[0] == "Jane Smith" // Old name added to aliases
	})).Return(nil).Once()

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Updated)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

func TestApplyPeopleUpdates_MergePeople(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:          1,
			UserID:      userID,
			DisplayName: "John Doe",
			Bio:         "Engineer",
			Aliases:     []string{"Johnny"},
		},
		{
			ID:          2,
			UserID:      userID,
			DisplayName: "J. Doe",
			Bio:         "Manager",
			Aliases:     []string{"JD"},
		},
	}

	peopleResult := archivist.PeopleResult{
		Merged: []archivist.MergedPerson{
			{
				TargetName: "John Doe",
				SourceName: "J. Doe",
				Reason:     "Same person",
			},
		},
	}

	// Mock: MergePeople call
	mockStore.On("MergePeople", userID, int64(1), int64(2),
		mock.MatchedBy(func(bio string) bool {
			return bio == "Engineer Manager" // Bios combined
		}),
		mock.MatchedBy(func(aliases []string) bool {
			// Should contain Johnny, JD, and "J. Doe" (source display name)
			return len(aliases) >= 2
		})).Return(nil).Once()

	// Mock: embedding generation for merged person
	mockOR.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req interface{}) bool {
		return true
	})).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: UpdatePerson call to update embedding after merge
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.DisplayName == "John Doe"
	})).Return(nil).Once()

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Added)
	assert.Equal(t, 0, stats.Updated)
	assert.Equal(t, 1, stats.Merged)
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}

func TestApplyPeopleUpdates_MergePersonNotFound(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:          1,
			UserID:      userID,
			DisplayName: "John Doe",
		},
	}

	peopleResult := archivist.PeopleResult{
		Merged: []archivist.MergedPerson{
			{
				TargetName: "John Doe",
				SourceName: "Unknown Person", // Doesn't exist
				Reason:     "Test",
			},
		},
	}

	// No MergePeople call expected (source not found)

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Merged)
	mockStore.AssertNotCalled(t, "MergePeople", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockOR.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

func TestApplyPeopleUpdates_UpdatePersonNotFound(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{} // Empty

	peopleResult := archivist.PeopleResult{
		Updated: []archivist.UpdatedPerson{
			{
				DisplayName: "Unknown Person",
				Bio:         "Some info",
				Reason:      "Test",
			},
		},
	}

	// No UpdatePerson call expected (person not found)

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 0, stats.Updated)
	mockStore.AssertNotCalled(t, "UpdatePerson", mock.Anything)
	mockOR.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

func TestApplyPeopleUpdates_Complex(t *testing.T) {
	// Setup: test multiple operations in one call
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)
	referenceDate := time.Now()

	existingPeople := []storage.Person{
		{
			ID:           1,
			UserID:       userID,
			DisplayName:  "Alice Smith",
			Circle:       "Friends",
			MentionCount: 5,
		},
		{
			ID:           2,
			UserID:       userID,
			DisplayName:  "Bob Jones",
			Circle:       "Work_Outer",
			MentionCount: 2,
		},
	}

	peopleResult := archivist.PeopleResult{
		Added: []archivist.AddedPerson{
			{
				DisplayName: "Charlie Brown",
				Circle:      "Other",
				Bio:         "New person",
				Reason:      "Just met",
			},
		},
		Updated: []archivist.UpdatedPerson{
			{
				DisplayName: "Alice Smith",
				Circle:      "Family",
				Reason:      "Now family",
			},
		},
		Merged: []archivist.MergedPerson{
			{
				TargetName: "Alice Smith",
				SourceName: "Bob Jones",
				Reason:     "Same person",
			},
		},
	}

	// Mock: embedding for add
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Times(3) // add + update + merge

	// Mock: add person
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "Charlie Brown"
	})).Return(int64(3), nil).Once()

	// Mock: update Alice (circle change)
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.Circle == "Family"
	})).Return(nil).Once()

	// Mock: merge Bob into Alice
	mockStore.On("MergePeople", userID, int64(1), int64(2), mock.Anything, mock.Anything).Return(nil).Once()

	// Mock: update embedding after merge
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1
	})).Return(nil).Once()

	// Execute
	stats, err := svc.applyPeopleUpdates(context.Background(), userID, peopleResult, existingPeople, referenceDate)

	// Verify
	assert.NoError(t, err)
	assert.Equal(t, 1, stats.Added)   // Charlie
	assert.Equal(t, 1, stats.Updated) // Alice circle change
	assert.Equal(t, 1, stats.Merged)  // Bob merged into Alice
	mockStore.AssertExpectations(t)
	mockOR.AssertExpectations(t)
}
