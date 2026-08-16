package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestResponsePath_RichFinalDeliveryMetricRecordsOneLogicalTextResult(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("metric-user"),
		convID: "123", replyTo: "42", richMode: config.TelegramRichMessagesSend,
	}

	beforeTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total")
	beforeSeries := promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentText,
		richMetricPathNative,
		string(richDeliveryConfirmed),
		richMetricFallbackNone,
	))

	ctx := context.Background()
	ok := path.sendFinal(ctx, trace.SpanFromContext(ctx), "# Metric probe")

	require.True(t, ok)
	assert.Equal(t, beforeTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total"))
	assert.Equal(t, beforeSeries+1, promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentText,
		richMetricPathNative,
		string(richDeliveryConfirmed),
		richMetricFallbackNone,
	)))
}

func TestGeneratedMedia_RichFinalDeliveryMetricRecordsOnePhotoAndAttachmentAttempt(t *testing.T) {
	transport := &recordingTransport{
		richMediaErr: &telegram.APIError{Code: 429, Description: "Too Many Requests"},
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)

	beforeTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total")
	beforeSeries := promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentPhoto,
		richMetricPathNative,
		string(richDeliveryRejected),
		richMetricFallbackNone,
	))
	beforeRateLimit := promtest.ToFloat64(richMessageRateLimitedTotal.WithLabelValues(
		richMetricContentPhoto,
		richMetricPathNative,
	))
	beforeAttachmentCount, beforeAttachmentCountSum := richHistogramSnapshot(t, richMessageNativeAttachmentCount)
	beforeAttachmentBytesCount, beforeAttachmentBytesSum := richHistogramSnapshot(t, richMessageNativeAttachmentBytes)

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Generated metric probe", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryRejected, result.outcome)
	assert.Equal(t, beforeTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total"))
	assert.Equal(t, beforeSeries+1, promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentPhoto,
		richMetricPathNative,
		string(richDeliveryRejected),
		richMetricFallbackNone,
	)))
	assert.Equal(t, beforeRateLimit+1, promtest.ToFloat64(richMessageRateLimitedTotal.WithLabelValues(
		richMetricContentPhoto,
		richMetricPathNative,
	)))
	afterAttachmentCount, afterAttachmentCountSum := richHistogramSnapshot(t, richMessageNativeAttachmentCount)
	afterAttachmentBytesCount, afterAttachmentBytesSum := richHistogramSnapshot(t, richMessageNativeAttachmentBytes)
	assert.Equal(t, beforeAttachmentCount+1, afterAttachmentCount)
	assert.Equal(t, beforeAttachmentCountSum+1, afterAttachmentCountSum)
	assert.Equal(t, beforeAttachmentBytesCount+1, afterAttachmentBytesCount)
	assert.Equal(t, beforeAttachmentBytesSum+float64(len("png-bytes")), afterAttachmentBytesSum)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichFinalDeliveryMetricRecordsConfirmedAPIFallback(t *testing.T) {
	transport := &recordingTransport{
		richMediaErr: fmt.Errorf("%w: invalid media binding", ErrRichMessageRejected),
		mediaID:      "legacy-media-7",
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "legacy-media-7").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

	beforeTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total")
	beforeSeries := promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentPhoto,
		richMetricPathAPIFallback,
		string(richDeliveryConfirmed),
		richMetricFallbackFormatRejected,
	))

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Generated fallback metric probe", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, beforeTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total"))
	assert.Equal(t, beforeSeries+1, promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentPhoto,
		richMetricPathAPIFallback,
		string(richDeliveryConfirmed),
		richMetricFallbackFormatRejected,
	)))
	store.AssertExpectations(t)
}

func TestResponsePath_RichShadowMetricRecordsOneBoundedEvaluation(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("metric-user"),
		convID: "123", richMode: config.TelegramRichMessagesShadow,
	}
	beforeTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total")
	beforeSeries := promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
		richMetricShadowNative,
		richMetricFallbackNone,
	))

	ctx := context.Background()
	path.shadowRichRender(ctx, trace.SpanFromContext(ctx), "# Shadow metric probe")

	assert.Equal(t, beforeTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total"))
	assert.Equal(t, beforeSeries+1, promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
		richMetricShadowNative,
		richMetricFallbackNone,
	)))
}

func TestGeneratedMedia_RichShadowEvaluatesNativePhotoWithoutSendingIt(t *testing.T) {
	transport := &recordingTransport{mediaID: "legacy-media-shadow"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesShadow
	expectGeneratedArtifact(store, userID)
	expectGeneratedMetricPersistence(store, userID, "legacy-media-shadow", 42)

	beforeShadowTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total")
	beforeShadowNative := promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
		richMetricShadowNative,
		richMetricFallbackNone,
	))
	beforeFinalTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total")
	beforeAttachmentCount, beforeAttachmentSum := richHistogramSnapshot(t, richMessageNativeAttachmentCount)
	beforeAttachmentBytesCount, beforeAttachmentBytesSum := richHistogramSnapshot(t, richMessageNativeAttachmentBytes)

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Shadow photo\n\nFormula $x^2$.", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Empty(t, transport.richMedia, "shadow must never perform a native rich send")
	require.Len(t, transport.media, 1, "shadow keeps the persistent legacy delivery")
	assert.Equal(t, beforeShadowTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total"))
	assert.Equal(t, beforeShadowNative+1, promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
		richMetricShadowNative,
		richMetricFallbackNone,
	)))
	assert.Equal(t, beforeFinalTotal, richCounterFamilyTotal(t, "laplaced_bot_rich_message_final_deliveries_total"))
	afterAttachmentCount, afterAttachmentSum := richHistogramSnapshot(t, richMessageNativeAttachmentCount)
	afterAttachmentBytesCount, afterAttachmentBytesSum := richHistogramSnapshot(t, richMessageNativeAttachmentBytes)
	assert.Equal(t, beforeAttachmentCount, afterAttachmentCount)
	assert.Equal(t, beforeAttachmentSum, afterAttachmentSum)
	assert.Equal(t, beforeAttachmentBytesCount, afterAttachmentBytesCount)
	assert.Equal(t, beforeAttachmentBytesSum, afterAttachmentBytesSum)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichShadowRecordsOneLocalFallbackDecision(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		artifactIDs []int64
		threshold   int
		wantReason  string
	}{
		{
			name:        "two generated images",
			response:    "# Album",
			artifactIDs: []int64{42, 43},
			wantReason:  richMetricFallbackMediaIneligible,
		},
		{
			name:        "explicit split",
			response:    "# First\n\n###SPLIT###\n\n## Second",
			artifactIDs: []int64{42},
			wantReason:  richMetricFallbackMediaIneligible,
		},
		{
			name:        "document quality image",
			response:    "# Document",
			artifactIDs: []int64{42},
			threshold:   4,
			wantReason:  richMetricFallbackMediaIneligible,
		},
		{
			name:        "injected photo exceeds block budget",
			response:    strings.TrimSuffix(strings.Repeat("x\n\n", richMessageSafeBlockLimit), "\n\n"),
			artifactIDs: []int64{42},
			wantReason:  richMetricFallbackRenderOrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{mediaID: "legacy-media-shadow"}
			bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
			path := generatedPath(bot, userID)
			path.richMode = config.TelegramRichMessagesShadow
			if tt.threshold > 0 {
				bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = tt.threshold
			}
			expectGeneratedArtifact(store, userID)
			if len(tt.artifactIDs) == 2 {
				bot.fileStorage.(*fakeFileStorage).blobs["gen/dog.png"] = []byte("dog-bytes")
				store.On("GetArtifact", userID, int64(43)).Return(&storage.Artifact{
					ID: 43, UserID: userID, FilePath: "gen/dog.png",
					OriginalName: "dog.png", MimeType: "image/png",
				}, nil).Once()
			}
			expectGeneratedMetricPersistence(store, userID, "legacy-media-shadow", tt.artifactIDs...)

			beforeTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total")
			beforeSeries := promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
				richMetricShadowLocalFallback,
				tt.wantReason,
			))

			result := bot.sendResponseWithGeneratedImages(
				context.Background(), path, nil, tt.response, tt.artifactIDs, bot.logger,
			)

			require.Equal(t, richDeliveryConfirmed, result.outcome)
			assert.Empty(t, transport.richMedia)
			require.Len(t, transport.media, 1)
			assert.Equal(t, beforeTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total"))
			assert.Equal(t, beforeSeries+1, promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
				richMetricShadowLocalFallback,
				tt.wantReason,
			)))
			store.AssertExpectations(t)
		})
	}
}

func TestGeneratedMedia_RichShadowRecordsUnavailableArtifactOnce(t *testing.T) {
	transport := &recordingTransport{}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesShadow
	store.On("GetArtifact", userID, int64(42)).Return(nil, nil).Once()
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "text-message-1").Return(nil).Once()

	beforeTotal := richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total")
	beforeSeries := promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
		richMetricShadowLocalFallback,
		richMetricFallbackMediaUnavailable,
	))

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Missing photo", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Empty(t, transport.richMedia)
	assert.Empty(t, transport.media)
	require.Len(t, transport.text, 1)
	assert.Equal(t, beforeTotal+1, richCounterFamilyTotal(t, "laplaced_bot_rich_message_shadow_evaluations_total"))
	assert.Equal(t, beforeSeries+1, promtest.ToFloat64(richMessageShadowEvaluationsTotal.WithLabelValues(
		richMetricShadowLocalFallback,
		richMetricFallbackMediaUnavailable,
	)))
	store.AssertExpectations(t)
}

func TestRichMetrics_NormalizeEveryLabelAndFindWrapped429(t *testing.T) {
	before := promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentUnknown,
		richMetricPathUnknown,
		string(richDeliveryUnknown),
		richMetricFallbackUnknown,
	))
	beforeRateLimit := promtest.ToFloat64(richMessageRateLimitedTotal.WithLabelValues(
		richMetricContentUnknown,
		richMetricPathUnknown,
	))

	recordRichFinalDelivery(richFinalMetric{
		contentKind:    "user-controlled-kind",
		path:           "telegram-description-as-path",
		outcome:        richDeliveryOutcome("arbitrary"),
		fallbackReason: "unbounded-error-text",
		err:            errors.Join(errors.New("wrapped"), &telegram.APIError{Code: 429}),
	})

	assert.Equal(t, before+1, promtest.ToFloat64(richMessageFinalDeliveriesTotal.WithLabelValues(
		richMetricContentUnknown,
		richMetricPathUnknown,
		string(richDeliveryUnknown),
		richMetricFallbackUnknown,
	)))
	assert.Equal(t, beforeRateLimit+1, promtest.ToFloat64(richMessageRateLimitedTotal.WithLabelValues(
		richMetricContentUnknown,
		richMetricPathUnknown,
	)))
}

func richCounterFamilyTotal(t *testing.T, name string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		var total float64
		for _, metric := range family.GetMetric() {
			total += metric.GetCounter().GetValue()
		}
		return total
	}
	return 0
}

func richHistogramSnapshot(t *testing.T, histogram prometheus.Histogram) (uint64, float64) {
	t.Helper()
	metric := &dto.Metric{}
	require.NoError(t, histogram.Write(metric))
	return metric.GetHistogram().GetSampleCount(), metric.GetHistogram().GetSampleSum()
}

func expectGeneratedMetricPersistence(store *testutil.MockStorage, userID storage.ScopeID, transportID string, artifactIDs ...int64) {
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, transportID).Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	for _, artifactID := range artifactIDs {
		store.On("UpdateMessageID", userID, artifactID, int64(9)).Return(nil).Once()
	}
}
