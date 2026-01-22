package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersonCRUD(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)
	telegramID := int64(999888)
	username := "johndoe"

	person := Person{
		UserID:      userID,
		DisplayName: "John Doe",
		Aliases:     []string{"Johnny", "@johndoe"},
		TelegramID:  &telegramID,
		Username:    &username,
		Circle:      "Friends",
		Bio:         "A good friend from college.",
		Embedding:   []float32{0.1, 0.2, 0.3},
	}

	// 1. Add
	id, err := store.AddPerson(person)
	require.NoError(t, err)
	assert.NotZero(t, id)

	// 2. Get by ID
	got, err := store.GetPerson(userID, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, person.DisplayName, got.DisplayName)
	assert.Equal(t, person.Aliases, got.Aliases)
	assert.Equal(t, person.Circle, got.Circle)
	assert.Equal(t, person.Bio, got.Bio)
	assert.Equal(t, telegramID, *got.TelegramID)
	assert.Equal(t, username, *got.Username)
	assert.Equal(t, 1, got.MentionCount)
	assert.Len(t, got.Embedding, 3)

	// 3. Update
	got.Bio = "A very good friend from college. Works at TechCorp."
	got.Circle = "Work_Inner"
	got.MentionCount = 5
	got.Aliases = append(got.Aliases, "JD")
	err = store.UpdatePerson(*got)
	require.NoError(t, err)

	// Verify update
	updated, err := store.GetPerson(userID, id)
	require.NoError(t, err)
	assert.Equal(t, "A very good friend from college. Works at TechCorp.", updated.Bio)
	assert.Equal(t, "Work_Inner", updated.Circle)
	assert.Equal(t, 5, updated.MentionCount)
	assert.Contains(t, updated.Aliases, "JD")

	// 4. Delete
	err = store.DeletePerson(userID, id)
	require.NoError(t, err)

	// Verify delete
	deleted, err := store.GetPerson(userID, id)
	require.Error(t, err) // Should return sql.ErrNoRows
	assert.Nil(t, deleted)
}

func TestGetPeople(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	// Add multiple people
	people := []Person{
		{UserID: userID, DisplayName: "Alice", Circle: "Friends", Bio: "Friend Alice"},
		{UserID: userID, DisplayName: "Bob", Circle: "Work_Inner", Bio: "Colleague Bob"},
		{UserID: userID, DisplayName: "Charlie", Circle: "Family", Bio: "Brother Charlie"},
	}

	for _, p := range people {
		_, err := store.AddPerson(p)
		require.NoError(t, err)
	}

	// Get all for user
	result, err := store.GetPeople(userID)
	require.NoError(t, err)
	assert.Len(t, result, 3)

	// Should be ordered by display_name ASC
	assert.Equal(t, "Alice", result[0].DisplayName)
	assert.Equal(t, "Bob", result[1].DisplayName)
	assert.Equal(t, "Charlie", result[2].DisplayName)

	// Different user should get empty result
	other, err := store.GetPeople(999)
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestGetPeopleByIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	id1, err := store.AddPerson(Person{UserID: userID, DisplayName: "Alice"})
	require.NoError(t, err)
	id2, err := store.AddPerson(Person{UserID: userID, DisplayName: "Bob"})
	require.NoError(t, err)
	_, err = store.AddPerson(Person{UserID: userID, DisplayName: "Charlie"})
	require.NoError(t, err)

	// Get specific IDs
	result, err := store.GetPeopleByIDs(userID, []int64{id1, id2})
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Empty IDs
	empty, err := store.GetPeopleByIDs(userID, []int64{})
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestGetAllPeople(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	// Add people for different users
	_, err := store.AddPerson(Person{UserID: 1, DisplayName: "User1_Alice"})
	require.NoError(t, err)
	_, err = store.AddPerson(Person{UserID: 2, DisplayName: "User2_Bob"})
	require.NoError(t, err)

	// GetAllPeople returns all across all users
	all, err := store.GetAllPeople()
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestGetPeopleAfterID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	id1, err := store.AddPerson(Person{UserID: userID, DisplayName: "Alice"})
	require.NoError(t, err)
	_, err = store.AddPerson(Person{UserID: userID, DisplayName: "Bob"})
	require.NoError(t, err)
	_, err = store.AddPerson(Person{UserID: userID, DisplayName: "Charlie"})
	require.NoError(t, err)

	// Get people after first ID
	result, err := store.GetPeopleAfterID(id1)
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, "Bob", result[0].DisplayName)
	assert.Equal(t, "Charlie", result[1].DisplayName)
}

func TestFindPersonByTelegramID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)
	telegramID := int64(999888)

	_, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "John",
		TelegramID:  &telegramID,
	})
	require.NoError(t, err)

	// Find existing
	found, err := store.FindPersonByTelegramID(userID, telegramID)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "John", found.DisplayName)

	// Not found
	notFound, err := store.FindPersonByTelegramID(userID, 11111)
	require.NoError(t, err)
	assert.Nil(t, notFound)

	// Wrong user
	wrongUser, err := store.FindPersonByTelegramID(999, telegramID)
	require.NoError(t, err)
	assert.Nil(t, wrongUser)
}

func TestFindPersonByUsername(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)
	username := "johndoe"

	_, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "John",
		Username:    &username,
	})
	require.NoError(t, err)

	// Find existing (case insensitive)
	found, err := store.FindPersonByUsername(userID, "JohnDoe")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "John", found.DisplayName)

	// Not found
	notFound, err := store.FindPersonByUsername(userID, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

func TestFindPersonByName(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	_, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "John Doe",
		Bio:         "Test person",
	})
	require.NoError(t, err)

	// Find existing (case insensitive)
	found, err := store.FindPersonByName(userID, "john doe")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "John Doe", found.DisplayName)

	// Not found
	notFound, err := store.FindPersonByName(userID, "Jane")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

func TestFindPersonByAlias(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	_, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "John",
		Aliases:     []string{"Johnny", "JD", "@johndoe"},
	})
	require.NoError(t, err)

	// Find by alias (case insensitive)
	found, err := store.FindPersonByAlias(userID, "johnny")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "John", found[0].DisplayName)

	// Find by username-style alias
	foundAt, err := store.FindPersonByAlias(userID, "@johndoe")
	require.NoError(t, err)
	require.Len(t, foundAt, 1)

	// Not found
	notFound, err := store.FindPersonByAlias(userID, "unknown")
	require.NoError(t, err)
	assert.Empty(t, notFound)
}

func TestMergePeople(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	// Create target person
	targetID, err := store.AddPerson(Person{
		UserID:       userID,
		DisplayName:  "Oleg",
		Aliases:      []string{"@akaGelo"},
		Circle:       "Work_Inner",
		Bio:          "Colleague from X-team.",
		MentionCount: 5,
	})
	require.NoError(t, err)

	// Create source person (to be merged)
	sourceID, err := store.AddPerson(Person{
		UserID:       userID,
		DisplayName:  "Gelo",
		Aliases:      []string{"Гелёй"},
		Circle:       "Work_Inner",
		Bio:          "Backend developer.",
		MentionCount: 3,
	})
	require.NoError(t, err)

	// Merge source into target
	newBio := "Colleague from X-team, backend developer."
	newAliases := []string{"@akaGelo", "Гелёй", "Gelo"}
	// No username/telegram_id in this test (both nil)
	err = store.MergePeople(userID, targetID, sourceID, newBio, newAliases, nil, nil)
	require.NoError(t, err)

	// Verify target was updated
	target, err := store.GetPerson(userID, targetID)
	require.NoError(t, err)
	assert.Equal(t, newBio, target.Bio)
	assert.Equal(t, newAliases, target.Aliases)
	assert.Equal(t, 8, target.MentionCount) // 5 + 3

	// Verify source was deleted
	source, err := store.GetPerson(userID, sourceID)
	require.Error(t, err)
	assert.Nil(t, source)
}

func TestMergePeople_UsernameAndTelegramID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	username := "testuser"
	telegramID := int64(123456789)

	// Create target person WITHOUT username/telegram_id
	targetID, err := store.AddPerson(Person{
		UserID:       userID,
		DisplayName:  "Alice",
		Circle:       "Friends",
		Bio:          "Target person",
		MentionCount: 2,
	})
	require.NoError(t, err)

	// Create source person WITH username/telegram_id (the data we want to preserve)
	sourceID, err := store.AddPerson(Person{
		UserID:       userID,
		DisplayName:  "Alice Smith",
		Username:     &username,
		TelegramID:   &telegramID,
		Circle:       "Friends",
		Bio:          "Source person with contact info",
		MentionCount: 1,
	})
	require.NoError(t, err)

	// Merge source into target, passing source's username/telegram_id
	newBio := "Target person. Source person with contact info"
	newAliases := []string{"Alice Smith"}
	err = store.MergePeople(userID, targetID, sourceID, newBio, newAliases, &username, &telegramID)
	require.NoError(t, err)

	// Verify target now has username and telegram_id from source
	target, err := store.GetPerson(userID, targetID)
	require.NoError(t, err)
	assert.NotNil(t, target.Username)
	assert.Equal(t, username, *target.Username)
	assert.NotNil(t, target.TelegramID)
	assert.Equal(t, telegramID, *target.TelegramID)

	// Verify source was deleted
	source, err := store.GetPerson(userID, sourceID)
	require.Error(t, err)
	assert.Nil(t, source)
}

func TestGetPeopleExtended(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	// Add test people
	people := []Person{
		{UserID: userID, DisplayName: "Alice", Circle: "Friends", Bio: "Friend from school"},
		{UserID: userID, DisplayName: "Bob", Circle: "Work_Inner", Bio: "Colleague"},
		{UserID: userID, DisplayName: "Charlie", Circle: "Work_Inner", Bio: "Another colleague"},
		{UserID: userID, DisplayName: "Diana", Circle: "Family", Bio: "Sister"},
	}
	for _, p := range people {
		_, err := store.AddPerson(p)
		require.NoError(t, err)
	}

	t.Run("filter by circle", func(t *testing.T) {
		result, err := store.GetPeopleExtended(PersonFilter{
			UserID: userID,
			Circle: "Work_Inner",
		}, 10, 0, "display_name", "ASC")
		require.NoError(t, err)
		assert.Equal(t, 2, result.TotalCount)
		assert.Len(t, result.Data, 2)
		assert.Equal(t, "Bob", result.Data[0].DisplayName)
		assert.Equal(t, "Charlie", result.Data[1].DisplayName)
	})

	t.Run("search by name", func(t *testing.T) {
		result, err := store.GetPeopleExtended(PersonFilter{
			UserID: userID,
			Search: "ali",
		}, 10, 0, "display_name", "ASC")
		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalCount)
		assert.Equal(t, "Alice", result.Data[0].DisplayName)
	})

	t.Run("search by bio", func(t *testing.T) {
		result, err := store.GetPeopleExtended(PersonFilter{
			UserID: userID,
			Search: "colleague",
		}, 10, 0, "display_name", "ASC")
		require.NoError(t, err)
		assert.Equal(t, 2, result.TotalCount)
	})

	t.Run("pagination", func(t *testing.T) {
		result, err := store.GetPeopleExtended(PersonFilter{
			UserID: userID,
		}, 2, 0, "display_name", "ASC")
		require.NoError(t, err)
		assert.Equal(t, 4, result.TotalCount)
		assert.Len(t, result.Data, 2)
		assert.Equal(t, "Alice", result.Data[0].DisplayName)
		assert.Equal(t, "Bob", result.Data[1].DisplayName)

		// Second page
		result2, err := store.GetPeopleExtended(PersonFilter{
			UserID: userID,
		}, 2, 2, "display_name", "ASC")
		require.NoError(t, err)
		assert.Len(t, result2.Data, 2)
		assert.Equal(t, "Charlie", result2.Data[0].DisplayName)
		assert.Equal(t, "Diana", result2.Data[1].DisplayName)
	})

	t.Run("sort by mention_count DESC", func(t *testing.T) {
		// Update mention counts
		all, _ := store.GetPeople(userID)
		for i, p := range all {
			p.MentionCount = (i + 1) * 10
			err := store.UpdatePerson(p)
			require.NoError(t, err)
		}

		result, err := store.GetPeopleExtended(PersonFilter{
			UserID: userID,
		}, 10, 0, "mention_count", "DESC")
		require.NoError(t, err)
		assert.Equal(t, 40, result.Data[0].MentionCount)
	})
}

func TestPeopleWithoutEmbedding(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	// Add person with embedding
	_, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "Alice",
		Bio:         "Has embedding",
		Embedding:   []float32{0.1, 0.2, 0.3},
	})
	require.NoError(t, err)

	// Add person without embedding
	_, err = store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "Bob",
		Bio:         "No embedding",
		Embedding:   nil,
	})
	require.NoError(t, err)

	// Count without embedding
	count, err := store.CountPeopleWithoutEmbedding(userID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Get without embedding
	people, err := store.GetPeopleWithoutEmbedding(userID)
	require.NoError(t, err)
	assert.Len(t, people, 1)
	assert.Equal(t, "Bob", people[0].DisplayName)
}

func TestUniqueConstraint(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	// Add first person
	_, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "John",
	})
	require.NoError(t, err)

	// Adding same name should fail
	_, err = store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "John",
	})
	require.Error(t, err)

	// Different user, same name should succeed
	_, err = store.AddPerson(Person{
		UserID:      999,
		DisplayName: "John",
	})
	require.NoError(t, err)
}

func TestPersonDefaults(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)

	// Add person with minimal fields
	id, err := store.AddPerson(Person{
		UserID:      userID,
		DisplayName: "Minimal",
	})
	require.NoError(t, err)

	// Verify defaults
	person, err := store.GetPerson(userID, id)
	require.NoError(t, err)
	assert.Equal(t, "Other", person.Circle)                    // Default circle
	assert.Equal(t, 1, person.MentionCount)                    // Default mention count
	assert.NotNil(t, person.Aliases)                           // Should be empty slice, not nil
	assert.Empty(t, person.Aliases)                            // Empty aliases
	assert.False(t, person.FirstSeen.IsZero())                 // Should have timestamp
	assert.False(t, person.LastSeen.IsZero())                  // Should have timestamp
	assert.True(t, time.Since(person.FirstSeen) < time.Minute) // Should be recent
}
