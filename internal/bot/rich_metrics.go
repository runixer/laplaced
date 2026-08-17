package bot

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/runixer/laplaced/internal/telegram"
)

// Every value used below is selected by bot code, never copied from a user,
// model response, Telegram description, filename, or MIME type. The
// normalizers are intentionally defensive so a future caller cannot turn a
// delivery error into an unbounded Prometheus label.
const (
	richMetricContentText    = "text"
	richMetricContentPhoto   = "photo"
	richMetricContentUnknown = "unknown"

	richMetricPathNative            = "native"
	richMetricPathLocalFallback     = "local_fallback"
	richMetricPathAPIFallback       = "api_fallback"
	richMetricPathLegacyMedia       = "legacy_media"
	richMetricPathTextFallback      = "text_fallback"
	richMetricPathPreflightRejected = "preflight_rejected"
	richMetricPathUnknown           = "unknown"

	richMetricFallbackNone             = "none"
	richMetricFallbackRenderOrLimit    = "rich_render_or_limit"
	richMetricFallbackFormatRejected   = "rich_format_rejected"
	richMetricFallbackMediaIneligible  = "native_media_ineligible"
	richMetricFallbackMediaUnavailable = "generated_media_unavailable"
	richMetricFallbackHardPreflight    = "hard_preflight"
	richMetricFallbackUnknown          = "unknown"

	richMetricShadowNative        = "native"
	richMetricShadowLocalFallback = "local_fallback"
	richMetricShadowRejected      = "rejected"
	richMetricShadowUnknown       = "unknown"
)

var (
	richMessageFinalDeliveriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "rich_message_final_deliveries_total",
			Help:      "Logical Rich Message final deliveries by bounded content kind, delivery path, outcome and fallback reason",
		},
		[]string{"content_kind", "path", "outcome", "fallback_reason"},
	)

	richMessageRateLimitedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "rich_message_rate_limited_total",
			Help:      "Logical Rich Message final deliveries that ended on a Telegram 429 response",
		},
		[]string{"content_kind", "path"},
	)

	richMessageShadowEvaluationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "rich_message_shadow_evaluations_total",
			Help:      "Rich Message shadow preflight evaluations by bounded outcome and fallback reason",
		},
		[]string{"outcome", "fallback_reason"},
	)

	richMessageNativeAttachmentCount = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "rich_message_native_attachments",
			Help:      "Number of trusted local attachments in each attempted native Rich Message final",
			Buckets:   []float64{0, 1, 2, 4, 8, 10},
		},
	)

	richMessageNativeAttachmentBytes = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "rich_message_native_attachment_bytes",
			Help:      "Total trusted local attachment bytes in each attempted native Rich Message final",
			Buckets:   []float64{64 << 10, 256 << 10, 512 << 10, 1 << 20, 2 << 20, 5 << 20, 10 << 20, 20 << 20},
		},
	)
)

type richFinalMetric struct {
	contentKind       string
	path              string
	outcome           richDeliveryOutcome
	fallbackReason    string
	err               error
	nativeAttachments int
	nativeBytes       int
}

func recordRichFinalDelivery(m richFinalMetric) {
	contentKind := boundedRichContentKind(m.contentKind)
	path := boundedRichDeliveryPath(m.path)
	outcome := boundedRichDeliveryOutcome(m.outcome)
	fallbackReason := boundedRichFallbackReason(m.fallbackReason)

	richMessageFinalDeliveriesTotal.WithLabelValues(contentKind, path, outcome, fallbackReason).Inc()
	if telegramRateLimited(m.err) {
		richMessageRateLimitedTotal.WithLabelValues(contentKind, path).Inc()
	}
	if m.nativeAttachments > 0 {
		richMessageNativeAttachmentCount.Observe(float64(m.nativeAttachments))
		richMessageNativeAttachmentBytes.Observe(float64(max(m.nativeBytes, 0)))
	}
}

func recordRichShadowEvaluation(outcome, fallbackReason string) {
	richMessageShadowEvaluationsTotal.WithLabelValues(
		boundedRichShadowOutcome(outcome),
		boundedRichFallbackReason(fallbackReason),
	).Inc()
}

func telegramRateLimited(err error) bool {
	var apiErr *telegram.APIError
	return errors.As(err, &apiErr) && apiErr.Code == 429
}

func boundedRichContentKind(value string) string {
	switch value {
	case richMetricContentText, richMetricContentPhoto:
		return value
	default:
		return richMetricContentUnknown
	}
}

func boundedRichDeliveryPath(value string) string {
	switch value {
	case richMetricPathNative,
		richMetricPathLocalFallback,
		richMetricPathAPIFallback,
		richMetricPathLegacyMedia,
		richMetricPathTextFallback,
		richMetricPathPreflightRejected:
		return value
	default:
		return richMetricPathUnknown
	}
}

func boundedRichDeliveryOutcome(value richDeliveryOutcome) string {
	switch value {
	case richDeliveryConfirmed, richDeliveryRejected, richDeliveryPartialRejected,
		richDeliveryUnknown, richDeliveryPartialUnknown:
		return string(value)
	default:
		return string(richDeliveryUnknown)
	}
}

func boundedRichFallbackReason(value string) string {
	switch value {
	case richMetricFallbackNone,
		richMetricFallbackRenderOrLimit,
		richMetricFallbackFormatRejected,
		richMetricFallbackMediaIneligible,
		richMetricFallbackMediaUnavailable,
		richMetricFallbackHardPreflight:
		return value
	default:
		return richMetricFallbackUnknown
	}
}

func boundedRichShadowOutcome(value string) string {
	switch value {
	case richMetricShadowNative, richMetricShadowLocalFallback, richMetricShadowRejected:
		return value
	default:
		return richMetricShadowUnknown
	}
}
