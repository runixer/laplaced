package testutil

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// MetricsHelper provides helper functions for testing Prometheus metrics.
type MetricsHelper struct {
	metricsURL string
	client     *http.Client
}

// NewMetricsHelper creates a new metrics helper.
func NewMetricsHelper(host string, port int) *MetricsHelper {
	return &MetricsHelper{
		metricsURL: fmt.Sprintf("http://%s:%d/metrics", host, port),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// ScrapeMetrics scrapes metrics from the metrics endpoint.
func (mh *MetricsHelper) ScrapeMetrics() (string, error) {
	resp, err := mh.client.Get(mh.metricsURL)
	if err != nil {
		return "", fmt.Errorf("failed to scrape metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read metrics body: %w", err)
	}

	return string(body), nil
}

// ParseMetricValue parses a single metric value from the metrics output.
// Returns the value and labels (if any) for the first matching metric.
func ParseMetricValue(metrics, metricName string) (string, map[string]string, error) {
	lines := strings.Split(metrics, "\n")

	for _, line := range lines {
		// Skip comments and empty lines
		if strings.HasPrefix(strings.TrimSpace(line), "#") || line == "" {
			continue
		}

		// Check if this line starts with our metric name
		if strings.HasPrefix(line, metricName) {
			// Parse metric line
			// Format: metric_name{label1="value1",label2="value2"} value
			remaining := strings.TrimPrefix(line, metricName)

			// Extract labels if present
			labels := make(map[string]string)
			if strings.HasPrefix(remaining, "{") {
				// Find closing brace
				endBrace := strings.Index(remaining, "}")
				if endBrace == -1 {
					return "", nil, fmt.Errorf("invalid metric format: missing closing brace")
				}

				// Parse labels
				labelStr := remaining[1:endBrace]
				for _, labelPair := range strings.Split(labelStr, ",") {
					parts := strings.SplitN(labelPair, "=", 2)
					if len(parts) == 2 {
						key := strings.TrimSpace(parts[0])
						value := strings.Trim(parts[1], `"`)
						labels[key] = value
					}
				}

				remaining = remaining[endBrace+1:]
			}

			// Extract value
			value := strings.TrimSpace(remaining)
			return value, labels, nil
		}
	}

	return "", nil, fmt.Errorf("metric %q not found", metricName)
}

// GetMetricValue is a convenience function that scrapes and parses a metric value.
func (mh *MetricsHelper) GetMetricValue(metricName string) (string, map[string]string, error) {
	metrics, err := mh.ScrapeMetrics()
	if err != nil {
		return "", nil, err
	}
	return ParseMetricValue(metrics, metricName)
}

// AssertMetricExists asserts that a metric exists in the metrics output.
func AssertMetricExists(t *testing.T, metrics, metricName string) {
	t.Helper()
	_, _, err := ParseMetricValue(metrics, metricName)
	if err != nil {
		t.Fatalf("metric %q does not exist: %v", metricName, err)
	}
}

// AssertMetricValue asserts that a metric has a specific value (with optional label matching).
func AssertMetricValue(t *testing.T, metrics, metricName, expectedValue string, expectedLabels map[string]string) {
	t.Helper()
	value, labels, err := ParseMetricValue(metrics, metricName)
	if err != nil {
		t.Fatalf("metric %q does not exist: %v", metricName, err)
	}

	if value != expectedValue {
		t.Errorf("metric %q has value %q, want %q", metricName, value, expectedValue)
	}

	// Check labels if provided
	for key, expectedVal := range expectedLabels {
		if actualVal, ok := labels[key]; !ok {
			t.Errorf("metric %q missing label %q", metricName, key)
		} else if actualVal != expectedVal {
			t.Errorf("metric %q label %q has value %q, want %q", metricName, key, actualVal, expectedVal)
		}
	}
}

// AssertMetricGreaterThan asserts that a metric value is greater than a threshold.
func AssertMetricGreaterThan(t *testing.T, metrics, metricName string, threshold float64) {
	t.Helper()
	valueStr, _, err := ParseMetricValue(metrics, metricName)
	if err != nil {
		t.Fatalf("metric %q does not exist: %v", metricName, err)
	}

	var value float64
	_, err = fmt.Sscanf(valueStr, "%f", &value)
	if err != nil {
		t.Fatalf("failed to parse metric value %q: %v", valueStr, err)
	}

	if value <= threshold {
		t.Errorf("metric %q has value %v, want > %v", metricName, value, threshold)
	}
}

// AssertMetricIncremented asserts that a metric value increased between two scrapes.
// Useful for testing that a metric is incremented during an operation.
func AssertMetricIncremented(t *testing.T, before, after, metricName string) {
	t.Helper()
	beforeValue, _, err := ParseMetricValue(before, metricName)
	if err != nil {
		t.Fatalf("metric %q does not exist in before metrics: %v", metricName, err)
	}

	afterValue, _, err := ParseMetricValue(after, metricName)
	if err != nil {
		t.Fatalf("metric %q does not exist in after metrics: %v", metricName, err)
	}

	var beforeFloat, afterFloat float64
	_, err = fmt.Sscanf(beforeValue, "%f", &beforeFloat)
	if err != nil {
		t.Fatalf("failed to parse before metric value %q: %v", beforeValue, err)
	}

	_, err = fmt.Sscanf(afterValue, "%f", &afterFloat)
	if err != nil {
		t.Fatalf("failed to parse after metric value %q: %v", afterValue, err)
	}

	if afterFloat <= beforeFloat {
		t.Errorf("metric %q did not increment: before=%v, after=%v", metricName, beforeFloat, afterFloat)
	}
}
