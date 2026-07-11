package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFactKindMarker_InProfileFormatters(t *testing.T) {
	updated := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	facts := []Fact{
		{ID: 1, Category: "work", Type: "identity", Kind: FactKindSelfReport, LastUpdated: updated, Content: "Works as engineer"},
		{ID: 2, Category: "people", Type: "context", Kind: FactKindUserOpinion, LastUpdated: updated, Content: "Believes X manipulates her"},
		{ID: 3, Category: "preference", Type: "context", Kind: FactKindConstraint, LastUpdated: updated, Content: "Asks not to be harsh"},
		{ID: 4, Category: "bio", Type: "identity", LastUpdated: updated, Content: "Legacy fact without kind"},
	}

	t.Run("with IDs", func(t *testing.T) {
		out := FormatUserProfile(facts)
		assert.Contains(t, out, "- [Fact:1] [work/identity] (")
		assert.Contains(t, out, "- [Fact:2] [people/context/opinion] (")
		assert.Contains(t, out, "- [Fact:3] [preference/context/constraint] (")
		assert.Contains(t, out, "- [Fact:4] [bio/identity] (", "empty kind renders unmarked")
	})

	t.Run("compact", func(t *testing.T) {
		out := FormatUserProfileCompact(facts)
		assert.Contains(t, out, "- [work/identity] (")
		assert.Contains(t, out, "- [people/context/opinion] (")
		assert.Contains(t, out, "- [preference/context/constraint] (")
		assert.Equal(t, 1, strings.Count(out, "/opinion"))
	})
}

func TestFilterProfileFacts_IncludesConstraints(t *testing.T) {
	facts := []Fact{
		{ID: 1, Type: "context", Importance: 50, Kind: FactKindConstraint, Content: "Low-importance constraint"},
		{ID: 2, Type: "context", Importance: 50, Kind: FactKindUserOpinion, Content: "Low-importance opinion"},
		{ID: 3, Type: "identity", Importance: 95, Content: "Identity fact"},
	}

	got := FilterProfileFacts(facts)

	ids := make([]int64, 0, len(got))
	for _, f := range got {
		ids = append(ids, f.ID)
	}
	assert.Contains(t, ids, int64(1), "constraint must reach the prompt regardless of importance")
	assert.NotContains(t, ids, int64(2), "low-importance opinion stays filtered")
	assert.Contains(t, ids, int64(3))
}
