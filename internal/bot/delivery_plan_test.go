package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

func textDeliveryOp(kind persistentOperationKind, text, replyTo string) deliveryOperation {
	format := ResponseFormatDefault
	if kind == persistentOperationRichText {
		format = ResponseFormatRichHTML
	}
	return deliveryOperation{
		Kind: kind,
		Text: &OutgoingResponse{
			ConversationID: "123",
			Text:           text,
			ReplyTo:        replyTo,
			Format:         format,
		},
	}
}

type recordingRichMediaDeliveryTransport struct {
	recordingRichTransport
	richMedia []OutgoingRichMedia
}

func (t *recordingRichMediaDeliveryTransport) SendRichMedia(_ context.Context, media OutgoingRichMedia) (string, error) {
	t.richMedia = append(t.richMedia, media)
	return "rich-media-1", nil
}

func validRichMediaItem() OutgoingMediaItem {
	return OutgoingMediaItem{
		Data: append([]byte(nil), generatedTestPNG...), Filename: "generated.png", MIME: "image/png", SourceOrdinal: 4,
	}
}

func TestExecuteDeliveryPlan_ConfirmedIDsAndSingleReplyAnchor(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{
		textDeliveryOp(persistentOperationRichText, "<p>one</p>", "42"),
		textDeliveryOp(persistentOperationRichText, "<p>two</p>", "42"),
	}})

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, []string{"message-1", "message-2"}, result.confirmedIDs)
	assert.Equal(t, "42", transport.responses[0].ReplyTo)
	assert.Empty(t, transport.responses[1].ReplyTo)
}

func TestExecuteDeliveryPlan_UsesOnlyPreparedFallbackAfterFormatRejection(t *testing.T) {
	transport := &recordingRichTransport{
		errors: map[int]error{1: errors.Join(ErrRichMessageRejected, &telegram.APIError{Code: 400, Description: "RICH_MESSAGE_INVALID"})},
	}
	bot := newRichDeliveryTestBot(t, transport)
	second := textDeliveryOp(persistentOperationRichText, "<p>two</p>", "42")
	second.formatFallback = []deliveryOperation{
		textDeliveryOp(persistentOperationLegacyText, "two safe", "42"),
		textDeliveryOp(persistentOperationLegacyText, "three safe", ""),
	}

	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{
		textDeliveryOp(persistentOperationRichText, "<p>one</p>", "42"),
		second,
		textDeliveryOp(persistentOperationRichText, "<p>must not send</p>", ""),
	}})

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, []string{"message-1", "message-3", "message-4"}, result.confirmedIDs)
	require.Len(t, transport.responses, 4)
	assert.Empty(t, transport.responses[2].ReplyTo, "fallback follows a confirmed persistent message")
	assert.Equal(t, "two safe", transport.responses[2].Text)
	assert.Equal(t, "three safe", transport.responses[3].Text)
}

func TestExecuteDeliveryPlan_StopsOnUnknownAndReportsPartial(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{1: context.DeadlineExceeded}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{
		textDeliveryOp(persistentOperationRichText, "<p>one</p>", "42"),
		textDeliveryOp(persistentOperationRichText, "<p>two</p>", ""),
		textDeliveryOp(persistentOperationRichText, "<p>three</p>", ""),
	}})

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryPartialUnknown, result.outcome)
	assert.Equal(t, []string{"message-1"}, result.confirmedIDs)
	assert.Len(t, transport.responses, 2, "unknown outcome must stop the plan without retry")
}

func TestExecuteDeliveryPlan_RejectsMalformedPlanBeforeNetwork(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{{
		Kind: persistentOperationRichText,
		Text: &OutgoingResponse{ConversationID: "123", Text: "not marked as rich"},
	}}})

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryRejected, result.outcome)
	assert.Zero(t, result.attempts)
	assert.Empty(t, transport.responses)
}

func TestExecuteDeliveryPlan_RejectsInvalidRichMediaGraphBeforeNetwork(t *testing.T) {
	transport := &recordingRichMediaDeliveryTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	plan := deliveryPlan{Operations: []deliveryOperation{{
		Kind: persistentOperationRichMedia,
		RichMedia: &OutgoingRichMedia{
			ConversationID:  "123",
			HTMLParts:       []string{`<img src="tg://photo?id=untrusted"/>`, ""},
			MediaGroupSizes: []int{1},
			Items:           []OutgoingMediaItem{validRichMediaItem()},
		},
	}}}

	require.NoError(t, plan.validate(), "shape validation should pass so the full graph preflight is exercised")
	result := bot.executeDeliveryPlan(context.Background(), plan)

	require.ErrorContains(t, result.err, `photo id "untrusted" has no rich message media entry`)
	assert.Equal(t, richDeliveryRejected, result.outcome)
	assert.Zero(t, result.attempts)
	assert.Empty(t, transport.richMedia)
}

func TestDeliveryPlanValidateRichMediaTopology(t *testing.T) {
	t.Run("media-only explicit topology", func(t *testing.T) {
		plan := deliveryPlan{Operations: []deliveryOperation{{
			Kind: persistentOperationRichMedia,
			RichMedia: &OutgoingRichMedia{
				ConversationID:  "123",
				HTMLParts:       []string{"", ""},
				MediaGroupSizes: []int{1},
				Items:           []OutgoingMediaItem{validRichMediaItem()},
			},
		}}}

		require.NoError(t, plan.validate())
	})

	t.Run("topology must consume all items", func(t *testing.T) {
		plan := deliveryPlan{Operations: []deliveryOperation{{
			Kind: persistentOperationRichMedia,
			RichMedia: &OutgoingRichMedia{
				ConversationID:  "123",
				HTMLParts:       []string{"", ""},
				MediaGroupSizes: []int{1},
				Items:           []OutgoingMediaItem{validRichMediaItem(), validRichMediaItem()},
			},
		}}}

		require.ErrorContains(t, plan.validate(), "consumes 1 items, payload has 2")
	})
}

func TestWithoutReply_ClonesRichMediaTopology(t *testing.T) {
	original := &OutgoingRichMedia{
		ConversationID:  "123",
		ReplyTo:         "42",
		HTMLParts:       []string{"<p>before</p>", "<p>after</p>"},
		MediaGroupSizes: []int{1},
		Items:           []OutgoingMediaItem{validRichMediaItem()},
	}

	clonedOp := withoutReply(deliveryOperation{Kind: persistentOperationRichMedia, RichMedia: original})
	require.NotSame(t, original, clonedOp.RichMedia)
	assert.Empty(t, clonedOp.RichMedia.ReplyTo)
	assert.Equal(t, "42", original.ReplyTo)
	assert.Equal(t, 4, clonedOp.RichMedia.Items[0].SourceOrdinal)

	clonedOp.RichMedia.HTMLParts[0] = "changed"
	clonedOp.RichMedia.MediaGroupSizes[0] = 2
	clonedOp.RichMedia.Items[0].Filename = "changed.png"
	assert.Equal(t, "<p>before</p>", original.HTMLParts[0])
	assert.Equal(t, 1, original.MediaGroupSizes[0])
	assert.Equal(t, "generated.png", original.Items[0].Filename)
}

func TestExecuteDeliveryPlan_ExplicitLedgerContextRequiresRepositoryBeforeNetwork(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{
		textDeliveryOp(persistentOperationRichText, "<p>one</p>", "42"),
	}}, deliveryLedgerContext{
		UserID:         storage.ScopeID("user"),
		Transport:      transportTelegram,
		ConversationID: "123",
	})

	require.ErrorContains(t, result.err, "no delivery repository")
	assert.Equal(t, richDeliveryRejected, result.outcome)
	assert.Zero(t, result.attempts)
	assert.Empty(t, transport.responses)
}

func TestExecuteDeliveryPlan_DuplicateStableIDIsPartialUnknown(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: "same", 1: "same"}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{
		textDeliveryOp(persistentOperationRichText, "<p>one</p>", "42"),
		textDeliveryOp(persistentOperationRichText, "<p>two</p>", ""),
	}})

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryPartialUnknown, result.outcome)
	assert.Equal(t, []string{"same"}, result.confirmedIDs)
}
