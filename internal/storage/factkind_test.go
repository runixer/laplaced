package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeFactKind(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"self_report passes", FactKindSelfReport, FactKindSelfReport},
		{"user_opinion passes", FactKindUserOpinion, FactKindUserOpinion},
		{"verified passes", FactKindVerified, FactKindVerified},
		{"constraint passes", FactKindConstraint, FactKindConstraint},
		{"empty defaults", "", FactKindSelfReport},
		{"arbitrary defaults", "hallucinated_kind", FactKindSelfReport},
		{"wrong case defaults", "User_Opinion", FactKindSelfReport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeFactKind(tt.input))
		})
	}
}

func TestFactKind_RoundTrip(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := ScopeID("123")

	t.Run("add persists kind", func(t *testing.T) {
		id, err := store.AddFact(Fact{
			UserID: userID, Relation: "thinks", Content: "X is manipulative",
			Category: "people", Type: "context", Kind: FactKindUserOpinion, Importance: 90,
		})
		assert.NoError(t, err)

		facts, err := store.GetFactsByIDs(userID, []int64{id})
		assert.NoError(t, err)
		assert.Len(t, facts, 1)
		assert.Equal(t, FactKindUserOpinion, facts[0].Kind)
	})

	t.Run("empty kind defaults to self_report", func(t *testing.T) {
		id, err := store.AddFact(Fact{
			UserID: userID, Relation: "is", Content: "Loves photography",
			Category: "hobby", Type: "context", Importance: 60,
		})
		assert.NoError(t, err)

		facts, err := store.GetFactsByIDs(userID, []int64{id})
		assert.NoError(t, err)
		assert.Len(t, facts, 1)
		assert.Equal(t, FactKindSelfReport, facts[0].Kind)
	})

	t.Run("update without kind keeps existing", func(t *testing.T) {
		id, err := store.AddFact(Fact{
			UserID: userID, Relation: "asks", Content: "No harsh feedback",
			Category: "preference", Type: "context", Kind: FactKindConstraint, Importance: 85,
		})
		assert.NoError(t, err)

		err = store.UpdateFact(Fact{
			ID: id, UserID: userID, Content: "No harsh feedback",
			Type: "context", Importance: 95,
		})
		assert.NoError(t, err)

		facts, err := store.GetFactsByIDs(userID, []int64{id})
		assert.NoError(t, err)
		assert.Len(t, facts, 1)
		assert.Equal(t, FactKindConstraint, facts[0].Kind, "empty kind on update must not downgrade provenance")
		assert.Equal(t, 95, facts[0].Importance)
	})

	t.Run("update with kind overwrites", func(t *testing.T) {
		id, err := store.AddFact(Fact{
			UserID: userID, Relation: "states", Content: "Salary is 50k",
			Category: "work", Type: "context", Importance: 70,
		})
		assert.NoError(t, err)

		err = store.UpdateFact(Fact{
			ID: id, UserID: userID, Content: "Salary is 50k",
			Type: "context", Kind: FactKindUserOpinion, Importance: 70,
		})
		assert.NoError(t, err)

		facts, err := store.GetFactsByIDs(userID, []int64{id})
		assert.NoError(t, err)
		assert.Len(t, facts, 1)
		assert.Equal(t, FactKindUserOpinion, facts[0].Kind)
	})

	t.Run("conflict re-add updates kind", func(t *testing.T) {
		id1, err := store.AddFact(Fact{
			UserID: userID, Relation: "believes", Content: "Y ignores her",
			Category: "people", Type: "context", Importance: 80,
		})
		assert.NoError(t, err)

		id2, err := store.AddFact(Fact{
			UserID: userID, Relation: "believes", Content: "Y ignores her",
			Category: "people", Type: "context", Kind: FactKindUserOpinion, Importance: 85,
		})
		assert.NoError(t, err)
		assert.Equal(t, id1, id2)

		facts, err := store.GetFactsByIDs(userID, []int64{id1})
		assert.NoError(t, err)
		assert.Len(t, facts, 1)
		assert.Equal(t, FactKindUserOpinion, facts[0].Kind)
	})
}
