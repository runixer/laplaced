package files

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const metricsNamespace = "laplaced"

var (
	// fileDownloadDuration measures time spent downloading files from Telegram.
	// Labels:
	//   - user_id: Telegram user ID
	//   - file_type: type of file (photo, image, pdf, voice, document)
	fileDownloadDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "file",
			Name:      "download_duration_seconds",
			Help:      "Duration of Telegram file downloads in seconds",
			// Buckets optimized for file downloads:
			// - Small files (voice, photos): 0.1-1s
			// - Medium files (images, PDFs): 1-5s
			// - Large files: 5-30s
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 20, 30},
		},
		[]string{"user_id", "file_type"},
	)

	// fileDownloadsTotal counts total file download attempts.
	// Labels:
	//   - user_id: Telegram user ID
	//   - file_type: type of file
	//   - status: success or error
	fileDownloadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "file",
			Name:      "downloads_total",
			Help:      "Total number of file download attempts",
		},
		[]string{"user_id", "file_type", "status"},
	)

	// fileSizeBytes measures file sizes.
	// Labels:
	//   - file_type: type of file
	fileSizeBytes = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "file",
			Name:      "size_bytes",
			Help:      "Size of downloaded files in bytes",
			// Buckets for file sizes:
			// 10KB, 50KB, 100KB, 500KB, 1MB, 5MB, 10MB, 20MB
			Buckets: []float64{10240, 51200, 102400, 512000, 1048576, 5242880, 10485760, 20971520},
		},
		[]string{"file_type"},
	)
)

const (
	statusSuccess = "success"
	statusError   = "error"
)

// RecordFileDownload records metrics for a file download operation.
func RecordFileDownload(userID int64, fileType FileType, durationSeconds float64, sizeBytes int64, success bool) {
	userIDStr := strconv.FormatInt(userID, 10)
	fileTypeStr := string(fileType)

	// Record duration
	fileDownloadDuration.WithLabelValues(userIDStr, fileTypeStr).Observe(durationSeconds)

	// Record count
	status := statusSuccess
	if !success {
		status = statusError
	}
	fileDownloadsTotal.WithLabelValues(userIDStr, fileTypeStr, status).Inc()

	// Record size (only for successful downloads with known size)
	if success && sizeBytes > 0 {
		fileSizeBytes.WithLabelValues(fileTypeStr).Observe(float64(sizeBytes))
	}
}
