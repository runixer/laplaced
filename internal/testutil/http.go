// Package testutil provides centralized test mocks, fixtures, and helpers.
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewTestRequest creates an HTTP request for testing handlers.
// For GET requests, body can be nil.
// For POST/PUT requests, body will be JSON-encoded if it's not nil and not already io.Reader.
//
// Example usage:
//
//	req := testutil.NewTestRequest(t, "GET", "/api/stats?user_id=123", nil)
//	req := testutil.NewTestRequest(t, "POST", "/api/webhook", map[string]any{"key": "value"})
func NewTestRequest(t *testing.T, method, path string, body interface{}) *http.Request {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		switch v := body.(type) {
		case io.Reader:
			bodyReader = v
		case []byte:
			bodyReader = bytes.NewReader(v)
		case string:
			bodyReader = bytes.NewBufferString(v)
		default:
			// JSON encode the body
			jsonData, err := json.Marshal(body)
			require.NoError(t, err, "failed to marshal request body")
			bodyReader = bytes.NewReader(jsonData)
		}
	}

	req, err := http.NewRequest(method, path, bodyReader)
	require.NoError(t, err, "failed to create test request")

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req
}

// NewTestRequestWithAuth creates an HTTP request with Basic Auth credentials.
func NewTestRequestWithAuth(t *testing.T, method, path string, body interface{}, username, password string) *http.Request {
	t.Helper()
	req := NewTestRequest(t, method, path, body)
	req.SetBasicAuth(username, password)
	return req
}

// ExecuteRequest executes an HTTP request against a handler and returns the recorder.
// This is useful for testing individual handlers without a full server.
//
// Example usage:
//
//	handler := http.HandlerFunc(myHandler)
//	req := testutil.NewTestRequest(t, "GET", "/api/stats", nil)
//	rr := testutil.ExecuteRequest(t, handler, req)
//	testutil.AssertStatusCode(t, rr, http.StatusOK)
func ExecuteRequest(t *testing.T, handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// AssertStatusCode asserts that the response has the expected status code.
func AssertStatusCode(t *testing.T, rr *httptest.ResponseRecorder, expected int) {
	t.Helper()
	assert.Equal(t, expected, rr.Code, "unexpected status code: got %d, want %d\nBody: %s",
		rr.Code, expected, rr.Body.String())
}

// AssertJSONResponse asserts that the response has the expected status code
// and parses the JSON body into the target struct.
//
// Example usage:
//
//	var resp struct { Success bool `json:"success"` }
//	testutil.AssertJSONResponse(t, rr, http.StatusOK, &resp)
//	assert.True(t, resp.Success)
func AssertJSONResponse(t *testing.T, rr *httptest.ResponseRecorder, expectedStatus int, target interface{}) {
	t.Helper()
	AssertStatusCode(t, rr, expectedStatus)

	contentType := rr.Header().Get("Content-Type")
	assert.Contains(t, contentType, "application/json", "expected JSON content type")

	err := json.Unmarshal(rr.Body.Bytes(), target)
	require.NoError(t, err, "failed to unmarshal response body: %s", rr.Body.String())
}

// AssertErrorResponse asserts that the response is an error with the expected status code
// and optionally checks the error message contains the expected string.
func AssertErrorResponse(t *testing.T, rr *httptest.ResponseRecorder, expectedStatus int, expectedContains string) {
	t.Helper()
	AssertStatusCode(t, rr, expectedStatus)

	if expectedContains != "" {
		body := rr.Body.String()
		assert.Contains(t, body, expectedContains, "error response should contain: %s", expectedContains)
	}
}

// AssertContentType asserts that the response has the expected Content-Type header.
func AssertContentType(t *testing.T, rr *httptest.ResponseRecorder, expected string) {
	t.Helper()
	contentType := rr.Header().Get("Content-Type")
	assert.Contains(t, contentType, expected, "unexpected content type")
}

// AssertRedirect asserts that the response is a redirect to the expected location.
func AssertRedirect(t *testing.T, rr *httptest.ResponseRecorder, expectedStatus int, expectedLocation string) {
	t.Helper()
	AssertStatusCode(t, rr, expectedStatus)

	location := rr.Header().Get("Location")
	assert.Equal(t, expectedLocation, location, "unexpected redirect location")
}

// ReadJSONResponse reads the response body as JSON into the target struct.
// Unlike AssertJSONResponse, this doesn't make assertions about status or content type.
func ReadJSONResponse(t *testing.T, rr *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	err := json.Unmarshal(rr.Body.Bytes(), target)
	require.NoError(t, err, "failed to unmarshal response body: %s", rr.Body.String())
}

// ResponseBodyContains checks if the response body contains the expected string.
func ResponseBodyContains(t *testing.T, rr *httptest.ResponseRecorder, expected string) {
	t.Helper()
	body := rr.Body.String()
	assert.Contains(t, body, expected, "response body should contain: %s", expected)
}

// ResponseBodyEqual checks if the response body exactly equals the expected string.
func ResponseBodyEqual(t *testing.T, rr *httptest.ResponseRecorder, expected string) {
	t.Helper()
	body := rr.Body.String()
	assert.Equal(t, expected, body)
}

// SSERecorder is a test response recorder that supports Server-Sent Events (SSE)
// by implementing the http.Flusher interface. Use this for testing handlers that
// stream responses via SSE.
//
// Example usage:
//
//	mockBot.On("ForceCloseSessionWithProgress", ...).Run(func(args mock.Arguments) {
//	    onProgress := args.Get(2).(rag.ProgressCallback)
//	    onProgress(rag.ProgressEvent{Stage: "processing", Complete: false})
//	    onProgress(rag.ProgressEvent{Stage: "done", Complete: true})
//	}).Return(&rag.ProcessingStats{}, nil)
//
//	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
//	sseRecorder := testutil.NewSSERecorder()
//	handler(sseRecorder, req)
//
//	assert.Equal(t, http.StatusOK, sseRecorder.Code)
//	events := testutil.ParseSSEEvents(t, sseRecorder.Body.String())
//	assert.Len(t, events, 2)
type SSERecorder struct {
	*httptest.ResponseRecorder
	flushCount int
}

// NewSSERecorder creates a new SSE-capable response recorder.
func NewSSERecorder() *SSERecorder {
	return &SSERecorder{
		ResponseRecorder: httptest.NewRecorder(),
	}
}

// Flush implements http.Flusher for SSE support.
func (r *SSERecorder) Flush() {
	r.flushCount++
}

// FlushCount returns the number of times Flush was called.
func (r *SSERecorder) FlushCount() int {
	return r.flushCount
}

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Data string
}

// ParseSSEEvents parses SSE formatted response body into individual events.
// Each SSE event has the format "data: <json>\n\n".
func ParseSSEEvents(t *testing.T, body string) []SSEEvent {
	t.Helper()
	var events []SSEEvent

	// Split by double newline to get individual events
	parts := splitSSEParts(body)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Extract data after "data: " prefix
		if strings.HasPrefix(part, "data: ") {
			data := strings.TrimPrefix(part, "data: ")
			events = append(events, SSEEvent{Data: data})
		}
	}

	return events
}

// splitSSEParts splits SSE body by double newline while preserving order.
func splitSSEParts(s string) []string {
	var parts []string
	var current strings.Builder

	for _, line := range strings.Split(s, "\n") {
		if line == "" && current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		} else {
			current.WriteString(line)
			current.WriteString("\n")
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// SSEHeadersGetter is an interface for getting HTTP headers.
type SSEHeadersGetter interface {
	Header() http.Header
}

// AssertSSEHeaders asserts that the response has proper SSE headers.
// Accepts both *httptest.ResponseRecorder and *SSERecorder.
func AssertSSEHeaders(t *testing.T, h SSEHeadersGetter) {
	t.Helper()
	assert.Equal(t, "text/event-stream", h.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", h.Header().Get("Cache-Control"))
	assert.Equal(t, "keep-alive", h.Header().Get("Connection"))
}

// CountSSEEvents counts the number of SSE events in the response body.
func CountSSEEvents(body string) int {
	count := 0
	dataIdx := 0
	for {
		idx := strings.Index(body[dataIdx:], "data: ")
		if idx == -1 {
			break
		}
		count++
		dataIdx += idx + 6 // Move past "data: "
	}
	return count
}
