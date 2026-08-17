package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddMessageToHistoryReturningID_IsExact(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	first, err := store.AddMessageToHistoryReturningID("scope-a", Message{Role: "assistant", Content: "first"})
	require.NoError(t, err)
	second, err := store.AddMessageToHistoryReturningID("scope-b", Message{Role: "assistant", Content: "other scope"})
	require.NoError(t, err)
	third, err := store.AddMessageToHistoryReturningID("scope-a", Message{Role: "assistant", Content: "third"})
	require.NoError(t, err)

	assert.Positive(t, first)
	assert.Equal(t, first+1, second)
	assert.Equal(t, second+1, third)
}

func TestReplyTransportMessages_CompositeLookupAndLegacyFallback(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())
	userID := ScopeID("scope")

	historyA, err := store.AddMessageToHistoryReturningID(userID, Message{Role: "assistant", Content: "reply A"})
	require.NoError(t, err)
	require.NoError(t, store.LinkReplyTransportMessages(userID, historyA, []TransportMessage{
		{Transport: "telegram", ConversationID: "chat-a", MessageID: "7", Ordinal: 0, IsPrimary: true},
		{Transport: "telegram", ConversationID: "chat-a", MessageID: "8", Ordinal: 1},
	}))

	historyB, err := store.AddMessageToHistoryReturningID(userID, Message{Role: "assistant", Content: "reply B"})
	require.NoError(t, err)
	require.NoError(t, store.LinkReplyTransportMessages(userID, historyB, []TransportMessage{
		{Transport: "telegram", ConversationID: "chat-b", MessageID: "7", Ordinal: 0, IsPrimary: true},
	}))
	historyC, err := store.AddMessageToHistoryReturningID(userID, Message{Role: "assistant", Content: "reply C"})
	require.NoError(t, err)
	require.NoError(t, store.LinkReplyTransportMessages(userID, historyC, []TransportMessage{
		{Transport: "mattermost", ConversationID: "town-square", MessageID: "post-7", Ordinal: 0, IsPrimary: true},
	}))
	conflictHistory, err := store.AddMessageToHistoryReturningID(userID, Message{Role: "assistant", Content: "conflict"})
	require.NoError(t, err)
	assert.Error(t, store.LinkReplyTransportMessages(userID, conflictHistory, []TransportMessage{
		{Transport: "telegram", ConversationID: "chat-a", MessageID: "7", Ordinal: 0, IsPrimary: true},
	}), "one composite transport identity cannot be reassigned")

	for _, tc := range []struct {
		conversation string
		messageID    string
		wantHistory  int64
		wantContent  string
	}{
		{"chat-a", "7", historyA, "reply A"},
		{"chat-a", "8", historyA, "reply A"},
		{"chat-b", "7", historyB, "reply B"},
	} {
		got, err := store.GetReplyByTransportMessage(userID, "telegram", tc.conversation, tc.messageID)
		require.NoError(t, err)
		if assert.NotNil(t, got) {
			assert.Equal(t, tc.wantHistory, got.ID)
			assert.Equal(t, tc.wantContent, got.Content)
		}
	}
	got, err := store.GetReplyByTransportMessage(userID, "mattermost", "town-square", "post-7")
	require.NoError(t, err)
	if assert.NotNil(t, got, "normalized mappings remain transport-universal") {
		assert.Equal(t, historyC, got.ID)
	}
	got, err = store.GetReplyByTransportMessage(userID, "telegram", "wrong-chat", "7")
	require.NoError(t, err)
	assert.Nil(t, got, "a non-NULL mapping from another conversation must not collide")

	legacyID := "old-9"
	legacyConversation := "-1004242"
	legacyHistory, err := store.AddMessageToHistoryReturningID(userID, Message{
		Role: "assistant", Content: "legacy", MessageID: &legacyID, ConversationID: &legacyConversation,
	})
	require.NoError(t, err)
	got, err = store.GetReplyByTransportMessage(userID, "telegram", legacyConversation, legacyID)
	require.NoError(t, err)
	if assert.NotNil(t, got) {
		assert.Equal(t, legacyHistory, got.ID)
	}
	got, err = store.GetReplyByTransportMessage(userID, "mattermost", legacyConversation, legacyID)
	require.NoError(t, err)
	assert.Nil(t, got, "a cross-transport principal scope must not reuse Telegram's legacy attribution")
	got, err = store.GetReplyByTransportMessage(userID, "telegram", "legacy-chat", legacyID)
	require.NoError(t, err)
	assert.Nil(t, got, "non-numeric conversations cannot be proven to be legacy Telegram chats")
	got, err = store.GetReplyByTransportMessage(userID, "telegram", "other-chat", legacyID)
	require.NoError(t, err)
	assert.Nil(t, got)

	legacyNullID := "old-null"
	legacyNullHistory, err := store.AddMessageToHistoryReturningID(userID, Message{
		Role: "assistant", Content: "legacy null", MessageID: &legacyNullID,
	})
	require.NoError(t, err)
	got, err = store.GetReplyByTransportMessage(userID, "telegram", "998877", legacyNullID)
	require.NoError(t, err)
	if assert.NotNil(t, got) {
		assert.Equal(t, legacyNullHistory, got.ID)
	}

	assert.Error(t, store.LinkReplyTransportMessages(userID, historyA, []TransportMessage{
		{Transport: "telegram", ConversationID: "chat-a", MessageID: "10", Ordinal: 0},
	}), "exactly one primary is required")
	assert.Error(t, store.LinkReplyTransportMessages(userID, historyA, []TransportMessage{
		{Transport: "telegram", ConversationID: "", MessageID: "10", Ordinal: 0, IsPrimary: true},
	}), "conversation id is required")
}

func TestOutboundDeliveryLedger_StateMachineAndRecovery(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	create := func(operationCount int) int64 {
		operations := make([]OutboundDeliveryOperation, operationCount)
		for i := range operations {
			operations[i] = OutboundDeliveryOperation{Ordinal: i, Kind: DeliveryOperationRichText}
		}
		id, err := store.CreateOutboundDelivery(OutboundDelivery{
			UserID: "scope", Transport: "telegram", ConversationID: "chat",
		}, operations)
		require.NoError(t, err)
		return id
	}

	t.Run("confirmed aggregate", func(t *testing.T) {
		id := create(2)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		assert.Error(t, store.MarkOutboundDeliveryOperationSending(id, 0), "sending ownership is single-use")
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"100", "101"}))
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusSending, delivery.Status)
		assert.Equal(t, 1, delivery.ConfirmedCount)
		assert.Equal(t, []string{"100", "101"}, ops[0].TransportMessageIDs)

		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 1))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 1,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"102"}))
		delivery, _, err = store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusConfirmed, delivery.Status)
		assert.Equal(t, 2, delivery.ConfirmedCount)
	})

	t.Run("partial rejected", func(t *testing.T) {
		id := create(3)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"200"}))
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 1))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 1,
			DeliveryOperationStatusRejected, DeliveryErrorFormat, nil))
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusPartialRejected, delivery.Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[2].Status)
		assert.NotNil(t, ops[2].FinishedAt)
		assert.Error(t, store.MarkOutboundDeliveryOperationSending(id, 2),
			"a rejected branch must not leave an executable planned suffix")
	})

	t.Run("partial unknown seals planned suffix", func(t *testing.T) {
		id := create(3)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"unknown-prefix"}))
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 1))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 1,
			DeliveryOperationStatusUnknown, DeliveryErrorNetwork, nil))

		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusPartialUnknown, delivery.Status)
		assert.Equal(t, DeliveryOperationStatusUnknown, ops[1].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[2].Status)
		assert.NotNil(t, ops[2].FinishedAt)
		assert.Error(t, store.MarkOutboundDeliveryOperationSending(id, 2),
			"an unknown branch must not leave an executable planned suffix")
	})

	t.Run("terminal delivery cannot start an unsent tail", func(t *testing.T) {
		id := create(2)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusRejected, DeliveryErrorFormat, nil))
		assert.Error(t, store.MarkOutboundDeliveryOperationSending(id, 1))
	})

	t.Run("format rejection activates preflighted fallback branch", func(t *testing.T) {
		id := create(3)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusRejected, DeliveryErrorFormat, nil))
		beforeActivation, primaryOps, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusRejected, beforeActivation.Status)
		assert.Equal(t, DeliveryOperationStatusRejected, primaryOps[0].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, primaryOps[1].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, primaryOps[2].Status)
		assert.NotNil(t, primaryOps[1].FinishedAt)
		assert.NotNil(t, primaryOps[2].FinishedAt)

		ordinals, err := store.ActivateOutboundDeliveryFallback(id, 0, []OutboundDeliveryOperation{
			{Ordinal: 0, Kind: DeliveryOperationLegacyText},
			{Ordinal: 1, Kind: DeliveryOperationMedia},
		})
		require.NoError(t, err)
		assert.Equal(t, []int{3, 4}, ordinals)
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusSending, delivery.Status)
		assert.Equal(t, 5, delivery.OperationCount)
		assert.Equal(t, DeliveryOperationStatusFormatRejected, ops[0].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[1].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[2].Status)

		for i, ordinal := range ordinals {
			require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, ordinal))
			require.NoError(t, store.CompleteOutboundDeliveryOperation(id, ordinal,
				DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{fmt.Sprintf("fallback-%d", i)}))
		}
		delivery, _, err = store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusConfirmed, delivery.Status)
		assert.Equal(t, 2, delivery.ConfirmedCount)
	})

	t.Run("non-format failure cannot activate fallback", func(t *testing.T) {
		id := create(1)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusUnknown, DeliveryErrorNetwork, nil))
		_, err := store.ActivateOutboundDeliveryFallback(id, 0, []OutboundDeliveryOperation{
			{Ordinal: 0, Kind: DeliveryOperationLegacyText},
		})
		assert.Error(t, err)
	})

	t.Run("interrupted becomes partial unknown and is sealed once", func(t *testing.T) {
		id := create(2)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"300"}))
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 1))
		count, err := store.MarkInterruptedOutboundDeliveriesUnknown()
		require.NoError(t, err)
		assert.EqualValues(t, 1, count)
		count, err = store.MarkInterruptedOutboundDeliveriesUnknown()
		require.NoError(t, err)
		assert.Zero(t, count)
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusPartialUnknown, delivery.Status)
		assert.Equal(t, DeliveryOperationStatusUnknown, ops[1].Status)
		assert.Equal(t, DeliveryErrorInterrupted, ops[1].ErrorClass)
	})
}

func TestOutboundDeliveryLedger_RecoveryWindows(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	create := func(operationCount int) int64 {
		operations := make([]OutboundDeliveryOperation, operationCount)
		for i := range operations {
			operations[i] = OutboundDeliveryOperation{Ordinal: i, Kind: DeliveryOperationRichText}
		}
		id, err := store.CreateOutboundDelivery(OutboundDelivery{
			UserID: "recovery-scope", Transport: "telegram", ConversationID: "recovery-chat",
		}, operations)
		require.NoError(t, err)
		return id
	}
	recoverOne := func() {
		count, err := store.MarkInterruptedOutboundDeliveriesUnknown()
		require.NoError(t, err)
		assert.EqualValues(t, 1, count)
		count, err = store.MarkInterruptedOutboundDeliveriesUnknown()
		require.NoError(t, err)
		assert.Zero(t, count, "a recovered delivery must be terminal")
	}

	t.Run("before first mark", func(t *testing.T) {
		id := create(2)
		recoverOne()
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusRejected, delivery.Status)
		assert.Zero(t, delivery.ConfirmedCount)
		for _, op := range ops {
			assert.Equal(t, DeliveryOperationStatusSkipped, op.Status)
			assert.NotNil(t, op.FinishedAt)
			assert.Equal(t, DeliveryErrorNone, op.ErrorClass)
		}
	})

	t.Run("while sending", func(t *testing.T) {
		id := create(2)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		recoverOne()
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusUnknown, delivery.Status)
		assert.Equal(t, DeliveryOperationStatusUnknown, ops[0].Status)
		assert.Equal(t, DeliveryErrorInterrupted, ops[0].ErrorClass)
		assert.NotNil(t, ops[0].FinishedAt)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[1].Status)
		assert.NotNil(t, ops[1].FinishedAt)
	})

	t.Run("after confirmed operation before next mark", func(t *testing.T) {
		id := create(2)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"confirmed-before-crash"}))
		recoverOne()
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusPartialRejected, delivery.Status)
		assert.Equal(t, 1, delivery.ConfirmedCount)
		assert.Equal(t, DeliveryOperationStatusConfirmed, ops[0].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[1].Status)
		assert.NotNil(t, ops[1].FinishedAt)
	})

	t.Run("activated fallback before first fallback send", func(t *testing.T) {
		id := create(2)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusRejected, DeliveryErrorFormat, nil))
		ordinals, err := store.ActivateOutboundDeliveryFallback(id, 0, []OutboundDeliveryOperation{
			{Ordinal: 0, Kind: DeliveryOperationLegacyText},
		})
		require.NoError(t, err)
		require.Equal(t, []int{2}, ordinals)
		recoverOne()
		delivery, ops, err := store.GetOutboundDelivery(id)
		require.NoError(t, err)
		assert.Equal(t, DeliveryStatusRejected, delivery.Status)
		assert.Equal(t, DeliveryOperationStatusFormatRejected, ops[0].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[1].Status)
		assert.Equal(t, DeliveryOperationStatusSkipped, ops[2].Status)
		assert.NotNil(t, ops[2].FinishedAt)
	})
}

func TestOutboundDeliveryLookupByTransportMessage_ConfirmedWithoutHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	const (
		userID    = ScopeID("ledger-lookup-scope")
		transport = "telegram"
	)
	traceID := "0102030405060708090a0b0c0d0e0f10"
	create := func(conversationID string, operationCount int) int64 {
		operations := make([]OutboundDeliveryOperation, operationCount)
		for i := range operations {
			operations[i] = OutboundDeliveryOperation{Ordinal: i, Kind: DeliveryOperationRichText}
		}
		id, err := store.CreateOutboundDelivery(OutboundDelivery{
			UserID: userID, Transport: transport, ConversationID: conversationID, TraceID: &traceID,
		}, operations)
		require.NoError(t, err)
		return id
	}

	t.Run("fully confirmed before history persistence", func(t *testing.T) {
		const conversationID = "7001"
		id := create(conversationID, 1)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"confirmed-before-history"}))

		delivery, err := store.GetOutboundDeliveryByTransportMessage(
			userID, transport, conversationID, "confirmed-before-history")
		require.NoError(t, err)
		if assert.NotNil(t, delivery) {
			assert.Equal(t, id, delivery.ID)
			assert.Equal(t, DeliveryStatusConfirmed, delivery.Status)
			assert.Nil(t, delivery.HistoryID)
			require.NotNil(t, delivery.TraceID)
			assert.Equal(t, traceID, *delivery.TraceID)
		}

		missing, err := store.GetOutboundDeliveryByTransportMessage(
			userID, transport, "different-conversation", "confirmed-before-history")
		require.NoError(t, err)
		assert.Nil(t, missing, "lookup must include the exact conversation identity")
	})

	t.Run("confirmed prefix of partial unknown delivery", func(t *testing.T) {
		const conversationID = "7002"
		id := create(conversationID, 3)
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
			DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"confirmed-prefix"}))
		require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 1))
		require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 1,
			DeliveryOperationStatusUnknown, DeliveryErrorNetwork, nil))

		delivery, err := store.GetOutboundDeliveryByTransportMessage(
			userID, transport, conversationID, "confirmed-prefix")
		require.NoError(t, err)
		if assert.NotNil(t, delivery) {
			assert.Equal(t, id, delivery.ID)
			assert.Equal(t, DeliveryStatusPartialUnknown, delivery.Status)
			assert.Equal(t, 1, delivery.ConfirmedCount)
			assert.Nil(t, delivery.HistoryID)
		}
	})

	t.Run("duplicate exact identity fails closed", func(t *testing.T) {
		const conversationID = "7003"
		for range 2 {
			id := create(conversationID, 1)
			require.NoError(t, store.MarkOutboundDeliveryOperationSending(id, 0))
			require.NoError(t, store.CompleteOutboundDeliveryOperation(id, 0,
				DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"ambiguous-id"}))
		}
		delivery, err := store.GetOutboundDeliveryByTransportMessage(
			userID, transport, conversationID, "ambiguous-id")
		require.Error(t, err)
		assert.Nil(t, delivery)
	})
}

func TestPersistOutboundDeliveryReply_IsAtomic(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())
	userID := ScopeID("scope")

	artifactID, err := store.AddArtifact(Artifact{
		UserID: userID, MessageID: 0, FileType: "image", FilePath: "generated/a.png",
		FileSize: 12, MimeType: "image/png", OriginalName: "a.png", ContentHash: "hash-a", State: "ready",
	})
	require.NoError(t, err)
	deliveryID, err := store.CreateOutboundDelivery(OutboundDelivery{
		UserID: userID, Transport: "telegram", ConversationID: "chat-42",
	}, []OutboundDeliveryOperation{{Ordinal: 0, Kind: DeliveryOperationRichMedia}})
	require.NoError(t, err)
	require.NoError(t, store.MarkOutboundDeliveryOperationSending(deliveryID, 0))
	require.NoError(t, store.CompleteOutboundDeliveryOperation(deliveryID, 0,
		DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"501", "502"}))

	historyID, err := store.PersistOutboundDeliveryReply(userID, deliveryID,
		Message{Role: "assistant", Content: "delivered reply"}, []int64{artifactID})
	require.NoError(t, err)
	assert.Positive(t, historyID)
	delivery, _, err := store.GetOutboundDelivery(deliveryID)
	require.NoError(t, err)
	require.NotNil(t, delivery.HistoryID)
	assert.Equal(t, historyID, *delivery.HistoryID)
	artifact, err := store.GetArtifact(userID, artifactID)
	require.NoError(t, err)
	assert.Equal(t, historyID, artifact.MessageID)
	for _, messageID := range []string{"501", "502"} {
		reply, err := store.GetReplyByTransportMessage(userID, "telegram", "chat-42", messageID)
		require.NoError(t, err)
		if assert.NotNil(t, reply) {
			assert.Equal(t, historyID, reply.ID)
		}
	}

	// AddArtifact intentionally deduplicates by content hash. Reusing a generated
	// artifact that is already linked to an older reply must move its canonical
	// message link to the newly confirmed reply without rolling back history or
	// exact transport mappings.
	_, err = store.db.Exec("UPDATE history SET topic_id = ? WHERE id = ? AND user_id = ?", 77, historyID, userID)
	require.NoError(t, err)
	reusedArtifactID, err := store.AddArtifact(Artifact{
		UserID: userID, MessageID: 0, FileType: "image", FilePath: "generated/a-again.png",
		FileSize: 12, MimeType: "image/png", OriginalName: "a-again.png", ContentHash: "hash-a", State: "ready",
	})
	require.NoError(t, err)
	require.Equal(t, artifactID, reusedArtifactID, "content-hash deduplication must exercise the prelinked-artifact path")
	reusedDelivery, err := store.CreateOutboundDelivery(OutboundDelivery{
		UserID: userID, Transport: "telegram", ConversationID: "chat-42",
	}, []OutboundDeliveryOperation{{Ordinal: 0, Kind: DeliveryOperationRichMedia}})
	require.NoError(t, err)
	require.NoError(t, store.MarkOutboundDeliveryOperationSending(reusedDelivery, 0))
	require.NoError(t, store.CompleteOutboundDeliveryOperation(reusedDelivery, 0,
		DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"551", "552"}))

	reusedHistoryID, err := store.PersistOutboundDeliveryReply(userID, reusedDelivery,
		Message{Role: "assistant", Content: "reused delivered reply"}, []int64{reusedArtifactID})
	require.NoError(t, err)
	require.NotEqual(t, historyID, reusedHistoryID)
	reusedArtifact, err := store.GetArtifact(userID, artifactID)
	require.NoError(t, err)
	assert.Equal(t, reusedHistoryID, reusedArtifact.MessageID)
	for _, messageID := range []string{"551", "552"} {
		reply, lookupErr := store.GetReplyByTransportMessage(userID, "telegram", "chat-42", messageID)
		require.NoError(t, lookupErr)
		if assert.NotNil(t, reply) {
			assert.Equal(t, reusedHistoryID, reply.ID)
			assert.Equal(t, "reused delivered reply", reply.Content)
		}
	}
	oldHistory, err := store.GetMessagesByIDs(userID, []int64{historyID})
	require.NoError(t, err)
	assert.Len(t, oldHistory, 1, "moving the artifact link must not remove its older history row")
	sessionArtifacts, err := store.GetSessionArtifacts(context.Background(), userID, 10, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, sessionArtifacts, 1, "reused artifact must follow its newest reply into the active session")
	assert.Equal(t, artifactID, sessionArtifacts[0].ID)

	foreignUser := ScopeID("foreign-scope")
	foreignHistoryID, err := store.AddMessageToHistoryReturningID(foreignUser,
		Message{Role: "assistant", Content: "foreign reply"})
	require.NoError(t, err)
	foreignArtifactID, err := store.AddArtifact(Artifact{
		UserID: foreignUser, MessageID: foreignHistoryID, FileType: "image", FilePath: "generated/foreign.png",
		FileSize: 12, MimeType: "image/png", OriginalName: "foreign.png", ContentHash: "hash-foreign", State: "ready",
	})
	require.NoError(t, err)
	foreignDelivery, err := store.CreateOutboundDelivery(OutboundDelivery{
		UserID: userID, Transport: "telegram", ConversationID: "chat-42",
	}, []OutboundDeliveryOperation{{Ordinal: 0, Kind: DeliveryOperationRichMedia}})
	require.NoError(t, err)
	require.NoError(t, store.MarkOutboundDeliveryOperationSending(foreignDelivery, 0))
	require.NoError(t, store.CompleteOutboundDeliveryOperation(foreignDelivery, 0,
		DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"581"}))
	beforeForeign, err := store.GetRecentHistory(userID, 100)
	require.NoError(t, err)
	_, err = store.PersistOutboundDeliveryReply(userID, foreignDelivery,
		Message{Role: "assistant", Content: "must roll back foreign artifact"}, []int64{foreignArtifactID})
	require.Error(t, err)
	afterForeign, err := store.GetRecentHistory(userID, 100)
	require.NoError(t, err)
	assert.Len(t, afterForeign, len(beforeForeign), "foreign artifact must roll back history and mappings")
	foreignReply, err := store.GetReplyByTransportMessage(userID, "telegram", "chat-42", "581")
	require.NoError(t, err)
	assert.Nil(t, foreignReply)
	foreignDeliveryState, _, err := store.GetOutboundDelivery(foreignDelivery)
	require.NoError(t, err)
	assert.Nil(t, foreignDeliveryState.HistoryID)
	foreignArtifact, err := store.GetArtifact(foreignUser, foreignArtifactID)
	require.NoError(t, err)
	assert.Equal(t, foreignHistoryID, foreignArtifact.MessageID)

	badDelivery, err := store.CreateOutboundDelivery(OutboundDelivery{
		UserID: userID, Transport: "telegram", ConversationID: "chat-42",
	}, []OutboundDeliveryOperation{{Ordinal: 0, Kind: DeliveryOperationRichMedia}})
	require.NoError(t, err)
	require.NoError(t, store.MarkOutboundDeliveryOperationSending(badDelivery, 0))
	require.NoError(t, store.CompleteOutboundDeliveryOperation(badDelivery, 0,
		DeliveryOperationStatusConfirmed, DeliveryErrorNone, []string{"601"}))
	before, err := store.GetRecentHistory(userID, 100)
	require.NoError(t, err)
	_, err = store.PersistOutboundDeliveryReply(userID, badDelivery,
		Message{Role: "assistant", Content: "must roll back"}, []int64{999999})
	require.Error(t, err)
	after, err := store.GetRecentHistory(userID, 100)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "history insert and mappings must roll back with artifact failure")
	reply, err := store.GetReplyByTransportMessage(userID, "telegram", "chat-42", "601")
	require.NoError(t, err)
	assert.Nil(t, reply)

	require.NoError(t, store.ClearHistory(userID))
	delivery, _, err = store.GetOutboundDelivery(deliveryID)
	require.NoError(t, err)
	assert.Nil(t, delivery, "history clear must remove the content-free delivery ledger too")
	reply, err = store.GetReplyByTransportMessage(userID, "telegram", "chat-42", "501")
	require.NoError(t, err)
	assert.Nil(t, reply)
}
