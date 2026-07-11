package memory

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/runixer/laplaced/internal/agent/archivist"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestConvertArchivistResult_PropagatesKind(t *testing.T) {
	result := &archivist.Result{
		Facts: archivist.FactsResult{
			Added: []archivist.AddedFact{
				{Relation: "believes", Content: "Believes X manipulates her", Category: "people", Type: "context", Kind: storage.FactKindUserOpinion, Importance: 80, Reason: "one-sided verdict"},
				{Relation: "works_as", Content: "Software Engineer", Category: "work", Type: "identity", Importance: 90, Reason: "self-report, kind omitted"},
			},
			Updated: []archivist.UpdatedFact{
				{ID: 5, Content: "Asks not to analyze X psychologically", Kind: storage.FactKindConstraint, Importance: 85, Reason: "behavioral instruction"},
			},
		},
	}

	update := convertArchivistResult(result)

	assert.Len(t, update.Added, 2)
	assert.Equal(t, storage.FactKindUserOpinion, update.Added[0].Kind)
	assert.Empty(t, update.Added[1].Kind, "omitted kind stays empty until apply normalizes it")
	assert.Len(t, update.Updated, 1)
	assert.Equal(t, storage.FactKindConstraint, update.Updated[0].Kind)
}

func TestApplyUpdate_NormalizesKindOnAdd(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockOR := new(testutil.MockLLMClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	translator := testutil.TestTranslator(t)

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	update := &MemoryUpdate{}
	update.Added = append(update.Added,
		struct {
			Relation   string `json:"relation"`
			Content    string `json:"content"`
			Category   string `json:"category"`
			Type       string `json:"type"`
			Kind       string `json:"kind,omitempty"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{Relation: "believes", Content: "Believes Y is lazy", Category: "people", Type: "context", Kind: "user_opinion", Importance: 70, Reason: "verdict"},
		struct {
			Relation   string `json:"relation"`
			Content    string `json:"content"`
			Category   string `json:"category"`
			Type       string `json:"type"`
			Kind       string `json:"kind,omitempty"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{Relation: "is", Content: "Loves hiking", Category: "hobby", Type: "context", Kind: "made_up_kind", Importance: 50, Reason: "hobby"},
	)

	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil)
	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.Content == "Believes Y is lazy" && f.Kind == storage.FactKindUserOpinion
	})).Return(int64(1), nil).Once()
	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.Content == "Loves hiking" && f.Kind == storage.FactKindSelfReport
	})).Return(int64(2), nil).Once()

	stats, err := svc.applyUpdateWithStats(context.Background(), "123", update, nil, time.Now(), 0, "")

	assert.NoError(t, err)
	assert.Equal(t, 2, stats.Created)
	mockStore.AssertExpectations(t)
}
