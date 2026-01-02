package telegram

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus метрики для Telegram HTTP-клиента
//
// Метрики позволяют отслеживать:
// - Время выполнения запросов к Telegram API (гистограмма)
// - Количество ошибок по типам (счётчик)
// - Количество retry-попыток (счётчик)
// - Текущее состояние long polling (gauge)

const metricsNamespace = "laplaced"

var (
	// telegramRequestDuration измеряет время выполнения запросов к Telegram API.
	// Labels:
	//   - method: название API-метода (sendMessage, sendChatAction, getUpdates и т.д.)
	//   - status: результат запроса (success, error, timeout, retry)
	telegramRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "request_duration_seconds",
			Help:      "Duration of Telegram API requests in seconds",
			// Buckets оптимизированы для типичных времён ответа Telegram API:
			// - Короткие запросы (sendMessage): 0.1-1s
			// - Long polling (getUpdates): 25-35s
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 35},
		},
		[]string{"method", "status"},
	)

	// telegramRequestsTotal считает общее количество запросов к Telegram API.
	// Labels:
	//   - method: название API-метода
	//   - status: результат (success, error, timeout)
	telegramRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "requests_total",
			Help:      "Total number of Telegram API requests",
		},
		[]string{"method", "status"},
	)

	// telegramRetriesTotal считает количество retry-попыток.
	// Labels:
	//   - method: название API-метода
	telegramRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "retries_total",
			Help:      "Total number of retry attempts for Telegram API requests",
		},
		[]string{"method"},
	)

	// telegramErrorsTotal считает ошибки по типам.
	// Labels:
	//   - method: название API-метода
	//   - error_type: тип ошибки (network, timeout, api_error, decode_error)
	telegramErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "errors_total",
			Help:      "Total number of errors by type",
		},
		[]string{"method", "error_type"},
	)

	// telegramLongPollingActive показывает, активен ли long polling.
	// Значение 1 = активен, 0 = неактивен
	telegramLongPollingActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "long_polling_active",
			Help:      "Whether long polling is currently active (1 = active, 0 = inactive)",
		},
	)

	// telegramLongPollingUpdates считает количество полученных updates.
	telegramLongPollingUpdates = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "long_polling_updates_total",
			Help:      "Total number of updates received via long polling",
		},
	)
)

// Константы для статусов метрик
const (
	statusSuccess = "success"
	statusError   = "error"
	statusTimeout = "timeout"
	statusRetry   = "retry"
)

// Константы для типов ошибок
const (
	errorTypeNetwork = "network"
	errorTypeTimeout = "timeout"
	errorTypeAPI     = "api_error"
	errorTypeDecode  = "decode_error"
)

// recordRequestDuration записывает время выполнения запроса
func recordRequestDuration(method, status string, durationSeconds float64) {
	telegramRequestDuration.WithLabelValues(method, status).Observe(durationSeconds)
	telegramRequestsTotal.WithLabelValues(method, status).Inc()
}

// recordRetry записывает retry-попытку
func recordRetry(method string) {
	telegramRetriesTotal.WithLabelValues(method).Inc()
}

// recordError записывает ошибку определённого типа
func recordError(method, errorType string) {
	telegramErrorsTotal.WithLabelValues(method, errorType).Inc()
}

// setLongPollingActive устанавливает статус long polling
func setLongPollingActive(active bool) {
	if active {
		telegramLongPollingActive.Set(1)
	} else {
		telegramLongPollingActive.Set(0)
	}
}

// recordLongPollingUpdates записывает количество полученных updates
func recordLongPollingUpdates(count int) {
	telegramLongPollingUpdates.Add(float64(count))
}
