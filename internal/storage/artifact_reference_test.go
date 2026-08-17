package storage

import (
	"testing"

	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistOutboundDeliveryReplyWithArtifacts_PreservesProvenanceAndOrder(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := ScopeID("artifact-ref-scope")
	creatorHistoryID, err := store.AddMessageToHistoryReturningID(userID,
		Message{Role: "user", Content: "original upload"})
	require.NoError(t, err)
	storedID, err := store.AddArtifact(Artifact{
		UserID: userID, MessageID: creatorHistoryID, FileType: "image", FilePath: "stored/photo.png",
		FileSize: 21, MimeType: "image/png", OriginalName: "photo.png", ContentHash: "stored-photo", State: "ready",
	})
	require.NoError(t, err)
	generatedID, err := store.AddArtifact(Artifact{
		UserID: userID, MessageID: 0, FileType: "image", FilePath: "generated/new.png",
		FileSize: 34, MimeType: "image/png", OriginalName: "new.png", ContentHash: "generated-new", State: "ready",
	})
	require.NoError(t, err)

	deliveryID := createConfirmedArtifactDelivery(t, store, userID, "artifact-order", "701")
	historyID, err := store.PersistOutboundDeliveryReplyWithArtifacts(userID, deliveryID,
		Message{Role: "assistant", Content: "ordered artifacts"}, PersistOutboundArtifacts{
			OwnedArtifactIDs: []int64{generatedID},
			// Deliberately insert out of order. Reads must use the explicit ordinal,
			// and repeating storedID must support preview + original as two refs.
			References: []OutboundArtifactReference{
				{ArtifactID: storedID, Ordinal: 2, Mode: artifactdelivery.ModeOriginal, Source: ArtifactReferenceSourceStored},
				{ArtifactID: generatedID, Ordinal: 0, Mode: artifactdelivery.ModePreview, Source: ArtifactReferenceSourceGenerated},
				{ArtifactID: storedID, Ordinal: 1, Mode: artifactdelivery.ModePreview, Source: ArtifactReferenceSourceStored},
			},
		})
	require.NoError(t, err)

	generated, err := store.GetArtifact(userID, generatedID)
	require.NoError(t, err)
	assert.Equal(t, historyID, generated.MessageID, "new artifact must acquire its creator history")
	stored, err := store.GetArtifact(userID, storedID)
	require.NoError(t, err)
	assert.Equal(t, creatorHistoryID, stored.MessageID, "stored resend must preserve creator provenance")

	references, err := store.GetHistoryArtifactReferences(userID, historyID)
	require.NoError(t, err)
	require.Len(t, references, 3)
	assert.Equal(t, []int{0, 1, 2}, []int{references[0].Ordinal, references[1].Ordinal, references[2].Ordinal})
	assert.Equal(t, []int64{generatedID, storedID, storedID}, []int64{references[0].ArtifactID, references[1].ArtifactID, references[2].ArtifactID})
	assert.Equal(t, []artifactdelivery.Mode{
		artifactdelivery.ModePreview, artifactdelivery.ModePreview, artifactdelivery.ModeOriginal,
	}, []artifactdelivery.Mode{references[0].Mode, references[1].Mode, references[2].Mode})
	assert.Equal(t, []ArtifactReferenceSource{
		ArtifactReferenceSourceGenerated, ArtifactReferenceSourceStored, ArtifactReferenceSourceStored,
	}, []ArtifactReferenceSource{references[0].Source, references[1].Source, references[2].Source})

	foreignRefs, err := store.GetHistoryArtifactReferences(ScopeID("other-scope"), historyID)
	require.NoError(t, err)
	assert.Empty(t, foreignRefs)

	require.NoError(t, store.ClearHistory(userID))
	clearedRefs, err := store.GetHistoryArtifactReferences(userID, historyID)
	require.NoError(t, err)
	assert.Empty(t, clearedRefs)
}

func TestPersistOutboundDeliveryReplyWithArtifacts_ForeignReferenceRollsBackEverything(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := ScopeID("artifact-ref-local")
	foreignUserID := ScopeID("artifact-ref-foreign")
	foreignHistoryID, err := store.AddMessageToHistoryReturningID(foreignUserID,
		Message{Role: "user", Content: "foreign upload"})
	require.NoError(t, err)
	foreignID, err := store.AddArtifact(Artifact{
		UserID: foreignUserID, MessageID: foreignHistoryID, FileType: "pdf", FilePath: "foreign/report.pdf",
		FileSize: 55, MimeType: "application/pdf", OriginalName: "report.pdf", ContentHash: "foreign-report", State: "ready",
	})
	require.NoError(t, err)
	newID, err := store.AddArtifact(Artifact{
		UserID: userID, MessageID: 0, FileType: "image", FilePath: "generated/local.png",
		FileSize: 89, MimeType: "image/png", OriginalName: "local.png", ContentHash: "local-new", State: "ready",
	})
	require.NoError(t, err)

	deliveryID := createConfirmedArtifactDelivery(t, store, userID, "artifact-rollback", "801")
	beforeHistory, err := store.GetRecentHistory(userID, 100)
	require.NoError(t, err)
	_, err = store.PersistOutboundDeliveryReplyWithArtifacts(userID, deliveryID,
		Message{Role: "assistant", Content: "must roll back"}, PersistOutboundArtifacts{
			OwnedArtifactIDs: []int64{newID},
			References: []OutboundArtifactReference{
				{ArtifactID: newID, Ordinal: 0, Mode: artifactdelivery.ModePreview, Source: ArtifactReferenceSourceGenerated},
				{ArtifactID: foreignID, Ordinal: 1, Mode: artifactdelivery.ModeOriginal, Source: ArtifactReferenceSourceStored},
			},
		})
	require.Error(t, err)

	afterHistory, err := store.GetRecentHistory(userID, 100)
	require.NoError(t, err)
	assert.Len(t, afterHistory, len(beforeHistory), "history insert must roll back")
	localArtifact, err := store.GetArtifact(userID, newID)
	require.NoError(t, err)
	assert.Zero(t, localArtifact.MessageID, "creator assignment must roll back")
	foreignArtifact, err := store.GetArtifact(foreignUserID, foreignID)
	require.NoError(t, err)
	assert.Equal(t, foreignHistoryID, foreignArtifact.MessageID)
	delivery, _, err := store.GetOutboundDelivery(deliveryID)
	require.NoError(t, err)
	assert.Nil(t, delivery.HistoryID, "delivery linkage must roll back")
	reply, err := store.GetReplyByTransportMessage(userID, "telegram", "artifact-rollback", "801")
	require.NoError(t, err)
	assert.Nil(t, reply, "exact transport mapping must roll back")
	var refCount int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM history_artifact_refs WHERE user_id = ?", userID).Scan(&refCount))
	assert.Zero(t, refCount)
}

func TestPersistOutboundDeliveryReplyWithArtifacts_RejectsOwnedReuseAndDuplicateOrdinal(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID := ScopeID("artifact-ref-validation")
	creatorHistoryID, err := store.AddMessageToHistoryReturningID(userID,
		Message{Role: "user", Content: "creator"})
	require.NoError(t, err)
	artifactID, err := store.AddArtifact(Artifact{
		UserID: userID, MessageID: creatorHistoryID, FileType: "image", FilePath: "stored/existing.png",
		FileSize: 13, MimeType: "image/png", OriginalName: "existing.png", ContentHash: "existing", State: "ready",
	})
	require.NoError(t, err)

	t.Run("already-owned artifact cannot be rebound", func(t *testing.T) {
		deliveryID := createConfirmedArtifactDelivery(t, store, userID, "artifact-owned", "901")
		_, err := store.PersistOutboundDeliveryReplyWithArtifacts(userID, deliveryID,
			Message{Role: "assistant", Content: "invalid owner"}, PersistOutboundArtifacts{
				OwnedArtifactIDs: []int64{artifactID},
				References: []OutboundArtifactReference{{
					ArtifactID: artifactID, Ordinal: 0, Mode: artifactdelivery.ModePreview, Source: ArtifactReferenceSourceGenerated,
				}},
			})
		require.Error(t, err)
		artifact, getErr := store.GetArtifact(userID, artifactID)
		require.NoError(t, getErr)
		assert.Equal(t, creatorHistoryID, artifact.MessageID)
		delivery, _, getErr := store.GetOutboundDelivery(deliveryID)
		require.NoError(t, getErr)
		assert.Nil(t, delivery.HistoryID)
	})

	t.Run("duplicate ordinal fails before persistence", func(t *testing.T) {
		deliveryID := createConfirmedArtifactDelivery(t, store, userID, "artifact-duplicate", "902")
		before, err := store.GetRecentHistory(userID, 100)
		require.NoError(t, err)
		_, err = store.PersistOutboundDeliveryReplyWithArtifacts(userID, deliveryID,
			Message{Role: "assistant", Content: "duplicate ordinal"}, PersistOutboundArtifacts{
				References: []OutboundArtifactReference{
					{ArtifactID: artifactID, Ordinal: 0, Mode: artifactdelivery.ModePreview, Source: ArtifactReferenceSourceStored},
					{ArtifactID: artifactID, Ordinal: 0, Mode: artifactdelivery.ModeOriginal, Source: ArtifactReferenceSourceStored},
				},
			})
		require.Error(t, err)
		after, getErr := store.GetRecentHistory(userID, 100)
		require.NoError(t, getErr)
		assert.Len(t, after, len(before))
	})
}

func createConfirmedArtifactDelivery(t *testing.T, store *Store, userID ScopeID, conversationID, messageID string) int64 {
	t.Helper()
	deliveryID, err := store.CreateOutboundDelivery(OutboundDelivery{
		UserID: userID, Transport: "telegram", ConversationID: conversationID,
	}, []OutboundDeliveryOperation{{Ordinal: 0, Kind: DeliveryOperationRichMedia}})
	require.NoError(t, err)
	require.NoError(t, store.MarkOutboundDeliveryOperationSending(deliveryID, 0))
	require.NoError(t, store.CompleteOutboundDeliveryOperation(deliveryID, 0,
		DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{messageID}))
	return deliveryID
}
