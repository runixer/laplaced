package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMakeMultipartRequest_DecodeFailureAccountsRequestAndDuration(t *testing.T) {
	const method = "multipartDecodeMetricProbe"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	client := &Client{
		token:      "metric-token",
		httpClient: server.Client(),
		apiURL:     server.URL,
	}

	beforeTotal := promtest.ToFloat64(telegramRequestsTotal.WithLabelValues(method, statusError))
	beforeDurationCount := telegramHistogramCount(t, telegramRequestDuration.WithLabelValues(method, statusError))
	beforeDecodeErrors := promtest.ToFloat64(telegramErrorsTotal.WithLabelValues(method, errorTypeDecode))

	resp, err := client.makeMultipartRequest(
		context.Background(), method, map[string]string{"chat_id": "1"},
		[]multipartFile{{FieldName: "photo", Filename: "probe.png", Data: []byte("png")}},
	)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorContains(t, err, "failed to decode response")
	assert.Equal(t, beforeTotal+1, promtest.ToFloat64(telegramRequestsTotal.WithLabelValues(method, statusError)))
	assert.Equal(t, beforeDurationCount+1, telegramHistogramCount(t, telegramRequestDuration.WithLabelValues(method, statusError)))
	assert.Equal(t, beforeDecodeErrors+1, promtest.ToFloat64(telegramErrorsTotal.WithLabelValues(method, errorTypeDecode)))
}

func telegramHistogramCount(t *testing.T, observer prometheus.Observer) uint64 {
	t.Helper()
	writable, ok := observer.(prometheus.Metric)
	require.True(t, ok)
	metric := &dto.Metric{}
	require.NoError(t, writable.Write(metric))
	return metric.GetHistogram().GetSampleCount()
}
