// Package testutil provides centralized test mocks, fixtures, and helpers.
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
