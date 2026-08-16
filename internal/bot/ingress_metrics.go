package bot

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// incomingUpdatesTotal intentionally has only fixed, low-cardinality labels.
// In particular it never includes user ids, rich block discriminators, file
// names, or message content.
var incomingUpdatesTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "bot",
		Name:      "incoming_updates_total",
		Help:      "Telegram and transport-neutral incoming updates by coarse content kind and outcome",
	},
	[]string{"kind", "outcome"},
)

func recordIncomingUpdate(kind, outcome string) {
	incomingUpdatesTotal.WithLabelValues(kind, outcome).Inc()
}
