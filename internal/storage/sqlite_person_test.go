package storage

import (
	"fmt"
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

func TestDeleteAllPeople(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := int64(123)
	otherUserID := int64(456)

	t.Run("delete all people for user", func(t *testing.T) {
		// Create people for user1
		for i := 0; i < 3; i++ {
			_, err := store.AddPerson(Person{
				UserID:      userID,
				DisplayName: fmt.Sprintf("Person %d", i),
			})
			require.NoError(t, err)
		}

		// Create person for user2
		_, err := store.AddPerson(Person{
			UserID:      otherUserID,
			DisplayName: "Other Person",
		})
		require.NoError(t, err)

		// Verify initial state
		people1, _ := store.GetPeople(userID)
		assert.Len(t, people1, 3)
		people2, _ := store.GetPeople(otherUserID)
		assert.Len(t, people2, 1)

		// Delete all for user1
		err = store.DeleteAllPeople(userID)
		require.NoError(t, err)

		// Verify user1's people are deleted
		people1, _ = store.GetPeople(userID)
		assert.Empty(t, people1, "user1's people should be deleted")

		// Verify user2's people are intact
		people2, _ = store.GetPeople(otherUserID)
		assert.Len(t, people2, 1, "user2's people should remain")
	})

	t.Run("delete all when none exist", func(t *testing.T) {
		// Should not error even if no people exist for the user
		err := store.DeleteAllPeople(999)
		require.NoError(t, err)
	})

	t.Run("delete all removes all circles", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		userID3 := int64(789)

		// Create people in different circles
		circles := []string{"Friends", "Work_Inner", "Family", "Other"}
		for _, circle := range circles {
			_, err := store2.AddPerson(Person{
				UserID:      userID3,
				DisplayName: fmt.Sprintf("%s Person", circle),
				Circle:      circle,
			})
			require.NoError(t, err)
		}

		// Verify all circles exist
		all, _ := store2.GetPeople(userID3)
		assert.Len(t, all, 4)

		// Delete all
		err := store2.DeleteAllPeople(userID3)
		require.NoError(t, err)

		// Verify all are gone
		all, _ = store2.GetPeople(userID3)
		assert.Empty(t, all)
	})
}

// TestPersonUserIsolation tests comprehensive user isolation for people.
func TestPersonUserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	user1ID := int64(100)
	user2ID := int64(200)

	// Create people for user1
	user1People := make([]int64, 3)
	for i := 0; i < 3; i++ {
		person := Person{
			UserID:      user1ID,
			DisplayName: fmt.Sprintf("User1Person%d", i),
			Circle:      "Friends",
			Bio:         fmt.Sprintf("Friend %d", i),
		}
		id, err := store.AddPerson(person)
		require.NoError(t, err)
		user1People[i] = id
	}

	// Create people for user2
	user2People := make([]int64, 2)
	for i := 0; i < 2; i++ {
		person := Person{
			UserID:      user2ID,
			DisplayName: fmt.Sprintf("User2Person%d", i),
			Circle:      "Work_Inner",
			Bio:         fmt.Sprintf("Colleague %d", i),
		}
		id, err := store.AddPerson(person)
		require.NoError(t, err)
		user2People[i] = id
	}

	t.Run("GetPerson - user cannot get other user's person by ID", func(t *testing.T) {
		// User1 tries to get user2's person
		person, err := store.GetPerson(user1ID, user2People[0])
		require.Error(t, err, "user1 should not be able to get user2's person")
		assert.Nil(t, person)

		// User1 can get their own person
		person, err = store.GetPerson(user1ID, user1People[0])
		require.NoError(t, err)
		assert.Equal(t, user1ID, person.UserID)
		assert.Equal(t, user1People[0], person.ID)
	})

	t.Run("GetPeople - only returns own people", func(t *testing.T) {
		// User1 should only see their own people
		people1, err := store.GetPeople(user1ID)
		require.NoError(t, err)
		assert.Len(t, people1, 3, "user1 should only see their 3 people")
		for _, p := range people1 {
			assert.Equal(t, user1ID, p.UserID, "all people should belong to user1")
		}

		// User2 should only see their own people
		people2, err := store.GetPeople(user2ID)
		require.NoError(t, err)
		assert.Len(t, people2, 2, "user2 should only see their 2 people")
		for _, p := range people2 {
			assert.Equal(t, user2ID, p.UserID, "all people should belong to user2")
		}
	})

	t.Run("GetPeopleByIDs - cannot access other users' people by ID", func(t *testing.T) {
		// User1 tries to get user2's person by ID
		people, err := store.GetPeopleByIDs(user1ID, []int64{user2People[0]})
		require.NoError(t, err)
		assert.Empty(t, people, "user1 should not be able to get user2's person by ID")

		// User1 can only get their own people
		people, err = store.GetPeopleByIDs(user1ID, user1People)
		require.NoError(t, err)
		assert.Len(t, people, 3)

		// User1 tries to get mixed IDs - should only get their own
		mixedIDs := append(user1People[:1], user2People[0])
		people, err = store.GetPeopleByIDs(user1ID, mixedIDs)
		require.NoError(t, err)
		assert.Len(t, people, 1, "should only return own person, not other user's")
		assert.Equal(t, user1ID, people[0].UserID)
	})

	t.Run("UpdatePerson - user cannot update other user's person", func(t *testing.T) {
		// Get user1's first person
		person1, _ := store.GetPerson(user1ID, user1People[0])
		originalBio := person1.Bio

		// User2 tries to update user1's person
		person1.UserID = user2ID // Spoof!
		person1.Bio = "Hacked by user2"

		err := store.UpdatePerson(*person1)
		require.NoError(t, err) // Update succeeds but WHERE clause filters by user_id

		// Verify user1's person was NOT modified (need to fetch with user1ID)
		person1After, err := store.GetPerson(user1ID, user1People[0])
		require.NoError(t, err)
		assert.Equal(t, originalBio, person1After.Bio, "user1's person should not be modified by user2")

		// User1 can update their own person
		person1After.UserID = user1ID
		person1After.Bio = "Updated by owner"
		err = store.UpdatePerson(*person1After)
		require.NoError(t, err)

		person1Updated, _ := store.GetPerson(user1ID, user1People[0])
		assert.Equal(t, "Updated by owner", person1Updated.Bio, "person should be updated by owner")
	})

	t.Run("DeletePerson - user cannot delete other user's person", func(t *testing.T) {
		// Get initial counts
		people1Before, _ := store.GetPeople(user1ID)
		count1Before := len(people1Before)

		// User2 tries to delete user1's person
		err := store.DeletePerson(user2ID, user1People[0])
		require.NoError(t, err) // No error, but WHERE clause filters by user_id

		// Verify user1's person was NOT deleted
		people1After, _ := store.GetPeople(user1ID)
		assert.Len(t, people1After, count1Before, "user1's person count should not change")

		// User1 can delete their own person
		err = store.DeletePerson(user1ID, user1People[0])
		require.NoError(t, err)

		people1After, _ = store.GetPeople(user1ID)
		assert.Len(t, people1After, count1Before-1, "user1's person should be deleted by user1")

		// Verify user2's people are intact
		people2, _ := store.GetPeople(user2ID)
		assert.Len(t, people2, 2, "user2's people should remain")
	})

	t.Run("MergePeople - only merges within same user", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		// Create target and source for user1
		target1, _ := store2.AddPerson(Person{UserID: user1ID, DisplayName: "Target1", MentionCount: 5})
		source1, _ := store2.AddPerson(Person{UserID: user1ID, DisplayName: "Source1", MentionCount: 3})

		// Create target and source for user2
		target2, _ := store2.AddPerson(Person{UserID: user2ID, DisplayName: "Target2", MentionCount: 2})
		source2, _ := store2.AddPerson(Person{UserID: user2ID, DisplayName: "Source2", MentionCount: 4})

		// User1 tries to merge user2's people - WHERE clause prevents access
		// Note: MergePeople doesn't return error for cross-user, just no-ops
		err := store2.MergePeople(user1ID, target2, source2, "merged bio", []string{"alias"}, nil, nil)
		require.NoError(t, err, "no error for cross-user merge (no-op)")

		// Verify user2's people are still intact (merge did NOT happen)
		target2After, err := store2.GetPerson(user2ID, target2)
		require.NoError(t, err, "user2's target person should still exist")
		assert.Equal(t, 2, target2After.MentionCount, "mention count should not change")

		// Verify source still exists
		_, err = store2.GetPerson(user2ID, source2)
		require.NoError(t, err, "user2's source person should still exist")

		// User1 can merge their own people
		newBio := "Merged person"
		newAliases := []string{"merged"}
		err = store2.MergePeople(user1ID, target1, source1, newBio, newAliases, nil, nil)
		require.NoError(t, err)

		// Verify merge happened for user1
		target1After, _ := store2.GetPerson(user1ID, target1)
		assert.Equal(t, newBio, target1After.Bio)
		assert.Equal(t, 8, target1After.MentionCount) // 5 + 3

		// Source should be deleted
		_, err = store2.GetPerson(user1ID, source1)
		require.Error(t, err, "source person should be deleted after merge")
	})

	t.Run("FindPersonByTelegramID - only finds own people", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		telegramID := int64(999888)

		// Both users have person with same telegram_id (possible in real world)
		id1, _ := store2.AddPerson(Person{
			UserID:      user1ID,
			DisplayName: "User1 John",
			TelegramID:  &telegramID,
		})

		id2, _ := store2.AddPerson(Person{
			UserID:      user2ID,
			DisplayName: "User2 John",
			TelegramID:  &telegramID,
		})

		// User1 should find their own person
		found1, err := store2.FindPersonByTelegramID(user1ID, telegramID)
		require.NoError(t, err)
		assert.NotNil(t, found1)
		assert.Equal(t, id1, found1.ID)
		assert.Equal(t, user1ID, found1.UserID)

		// User2 should find their own person
		found2, err := store2.FindPersonByTelegramID(user2ID, telegramID)
		require.NoError(t, err)
		assert.NotNil(t, found2)
		assert.Equal(t, id2, found2.ID)
		assert.Equal(t, user2ID, found2.UserID)
	})

	t.Run("FindPersonByUsername - only finds own people", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		username := "johndoe"

		// Both users have person with same username (possible in real world)
		_, _ = store2.AddPerson(Person{
			UserID:      user1ID,
			DisplayName: "User1 John",
			Username:    &username,
		})

		_, _ = store2.AddPerson(Person{
			UserID:      user2ID,
			DisplayName: "User2 John",
			Username:    &username,
		})

		// User1 should find their own person
		found1, err := store2.FindPersonByUsername(user1ID, username)
		require.NoError(t, err)
		assert.NotNil(t, found1)
		assert.Equal(t, user1ID, found1.UserID)

		// User2 should find their own person
		found2, err := store2.FindPersonByUsername(user2ID, username)
		require.NoError(t, err)
		assert.NotNil(t, found2)
		assert.Equal(t, user2ID, found2.UserID)
	})

	t.Run("FindPersonByName - only finds own people", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		name := "John Doe"

		// Both users have person with same name
		_, _ = store2.AddPerson(Person{
			UserID:      user1ID,
			DisplayName: name,
		})

		_, _ = store2.AddPerson(Person{
			UserID:      user2ID,
			DisplayName: name,
		})

		// User1 should find their own person
		found1, err := store2.FindPersonByName(user1ID, name)
		require.NoError(t, err)
		assert.NotNil(t, found1)
		assert.Equal(t, user1ID, found1.UserID)

		// User2 should find their own person
		found2, err := store2.FindPersonByName(user2ID, name)
		require.NoError(t, err)
		assert.NotNil(t, found2)
		assert.Equal(t, user2ID, found2.UserID)
	})

	t.Run("FindPersonByAlias - only finds own people", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		// Both users have person with same alias
		_, _ = store2.AddPerson(Person{
			UserID:      user1ID,
			DisplayName: "User1 Johnny",
			Aliases:     []string{"Johnny"},
		})

		_, _ = store2.AddPerson(Person{
			UserID:      user2ID,
			DisplayName: "User2 Johnny",
			Aliases:     []string{"Johnny"},
		})

		// User1 should find their own person
		found1, err := store2.FindPersonByAlias(user1ID, "Johnny")
		require.NoError(t, err)
		assert.Len(t, found1, 1)
		assert.Equal(t, user1ID, found1[0].UserID)

		// User2 should find their own person
		found2, err := store2.FindPersonByAlias(user2ID, "Johnny")
		require.NoError(t, err)
		assert.Len(t, found2, 1)
		assert.Equal(t, user2ID, found2[0].UserID)
	})

	t.Run("GetPeopleWithoutEmbedding - only returns own people", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		// User1 has person without embedding
		_, _ = store2.AddPerson(Person{
			UserID:      user1ID,
			DisplayName: "User1 No Embed",
			Embedding:   nil,
		})

		// User2 has person without embedding
		_, _ = store2.AddPerson(Person{
			UserID:      user2ID,
			DisplayName: "User2 No Embed",
			Embedding:   nil,
		})

		// Count for user1
		count1, err := store2.CountPeopleWithoutEmbedding(user1ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count1, "user1 should have 1 person without embedding")

		// Count for user2
		count2, err := store2.CountPeopleWithoutEmbedding(user2ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count2, "user2 should have 1 person without embedding")

		// Get for user1
		people1, err := store2.GetPeopleWithoutEmbedding(user1ID)
		require.NoError(t, err)
		assert.Len(t, people1, 1)
		assert.Equal(t, user1ID, people1[0].UserID)
	})

	t.Run("GetPeopleExtended - only returns own people", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		require.NoError(t, store2.Init())

		// Add people for both users in different circles
		_, _ = store2.AddPerson(Person{
			UserID:      user1ID,
			DisplayName: "User1 Alice",
			Circle:      "Friends",
		})

		_, _ = store2.AddPerson(Person{
			UserID:      user2ID,
			DisplayName: "User2 Bob",
			Circle:      "Friends",
		})

		// Get Friends for user1
		result1, err := store2.GetPeopleExtended(PersonFilter{
			UserID: user1ID,
			Circle: "Friends",
		}, 10, 0, "display_name", "ASC")
		require.NoError(t, err)
		assert.Equal(t, 1, result1.TotalCount, "user1 should have 1 friend")
		assert.Equal(t, user1ID, result1.Data[0].UserID)

		// Get Friends for user2
		result2, err := store2.GetPeopleExtended(PersonFilter{
			UserID: user2ID,
			Circle: "Friends",
		}, 10, 0, "display_name", "ASC")
		require.NoError(t, err)
		assert.Equal(t, 1, result2.TotalCount, "user2 should have 1 friend")
		assert.Equal(t, user2ID, result2.Data[0].UserID)
	})
}
