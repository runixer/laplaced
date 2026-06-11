package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BotAPI defines the interface for the Telegram Bot API methods we use.
// This allows for easier mocking in tests.
type BotAPI interface {
	SendMessage(ctx context.Context, req SendMessageRequest) (*Message, error)
	EditMessageText(ctx context.Context, req EditMessageTextRequest) (*Message, error)
	SendPhoto(ctx context.Context, req SendPhotoRequest) (*Message, error)
	SendDocument(ctx context.Context, req SendDocumentRequest) (*Message, error)
	SendMediaGroup(ctx context.Context, req SendMediaGroupRequest) ([]Message, error)
	SendMediaGroupDocuments(ctx context.Context, req SendMediaGroupDocumentsRequest) ([]Message, error)
	SetMyCommands(ctx context.Context, req SetMyCommandsRequest) error
	SetWebhook(ctx context.Context, req SetWebhookRequest) error
	SendChatAction(ctx context.Context, req SendChatActionRequest) error
	GetFile(ctx context.Context, req GetFileRequest) (*File, error)
	SetMessageReaction(ctx context.Context, req SetMessageReactionRequest) error
	GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error)
	GetToken() string
}

// ErrMessageNotModified is returned by EditMessageText when Telegram rejects an
// edit because the new text and entities are identical to the current message.
// Callers should treat this as a successful no-op.
var ErrMessageNotModified = errors.New("telegram: message is not modified")

// Client is a client for the Telegram Bot API.
//
// ARCHITECTURAL DECISION: Two separate HTTP clients
//
// Problem: Using a single HTTP client for all requests caused sporadic
// "context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
// errors for sendMessage, sendChatAction, and getUpdates.
//
// Cause: Long polling (getUpdates with Timeout=25s) held connections from the
// shared pool for long periods. Under high load or network delays, other
// requests could not get a free connection and timed out.
//
// Solution:
//  1. httpClient - for short API calls (sendMessage, sendChatAction, etc.)
//     with a 15-second timeout and retry logic
//  2. longPollingClient - for getUpdates with no timeout (controlled via
//     context), isolated connection pool
//
// Additionally, HTTP/2 is disabled (ForceAttemptHTTP2=false), since multiplexing
// requests over a single connection aggravated the resource contention problem.
type Client struct {
	token             string
	httpClient        *http.Client // For short API calls with retry
	longPollingClient *http.Client // For getUpdates with long waits
	apiURL            string
}

// NewClient creates a new Telegram API client.
//
// Creates two isolated HTTP clients with different settings:
// - httpClient: 30s timeout, retry logic, for sendMessage/sendChatAction
// - longPollingClient: no timeout (controlled via context), for getUpdates
func NewClient(token, proxyURL string) (*Client, error) {
	// Transport for regular API calls (sendMessage, sendChatAction, etc.)
	// DisableKeepAlives=true - each request creates a new connection,
	// which eliminates problems with "stuck" keep-alive connections
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // Increased for unstable networks
			KeepAlive: 0,                // Disable keep-alive at the TCP level
		}).DialContext,
		// HTTP/2 disabled: multiplexing over a single connection created
		// contention between short and long requests
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          0, // Don't keep idle connections
		IdleConnTimeout:       0, // Not used with DisableKeepAlives=true
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second, // Timeout for receiving response headers
		MaxIdleConnsPerHost:   0,
		DisableKeepAlives:     true, // Each request gets a new connection
	}

	// Separate transport for long polling - isolated connection pool
	// with increased timeouts for long waits on updates.
	// Keep-alive is needed here to avoid re-establishing the connection every 25 seconds
	longPollingTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 60 * time.Second, // Keep the connection alive longer
		}).DialContext,
		ForceAttemptHTTP2:     false, // Consistency with the main transport
		MaxIdleConns:          2,     // Minimal pool, only one connection needed
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 0, // No limit - controlled via context
		MaxIdleConnsPerHost:   1,
		DisableKeepAlives:     false,
	}

	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxy)
		longPollingTransport.Proxy = http.ProxyURL(proxy)
	}

	return &Client{
		token: token,
		// 30s timeout + retry logic in makeRequest()
		// DisableKeepAlives guarantees a fresh connection for each request
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		// Timeout=0 means "no limit" - controlled via context
		// in the GetUpdates() method (Timeout + 10 seconds for network delays)
		longPollingClient: &http.Client{
			Timeout:   0,
			Transport: longPollingTransport,
		},
		apiURL: fmt.Sprintf("https://api.telegram.org/bot%s", token),
	}, nil
}

// makeRequest performs a request to the Telegram API with retry logic.
//
// Retry strategy: up to 2 attempts with a 2-second delay.
// Retries happen only for network errors, NOT for Telegram API errors
// (e.g., "Bad Request" will not be retried).
//
// With the 30s httpClient timeout and DisableKeepAlives=true:
// - Each request creates a new TCP connection (eliminates problems with "stuck" connections)
// - Total wait time up to ~62 seconds in the worst case (30+2+30)
// - Retry helps with transient DNS/network problems
//
// Metrics: records request duration, retry count, and errors in Prometheus.
func (c *Client) makeRequest(ctx context.Context, method string, params interface{}) (*APIResponse, error) {
	startTime := time.Now()

	jsonParams, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	apiURL := fmt.Sprintf("%s/%s", c.apiURL, method)

	var lastErr error
	maxRetries := 2
	retryDelay := 2 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Record the retry in metrics
			recordRetry(method)

			// Fixed delay before retry
			select {
			case <-ctx.Done():
				duration := time.Since(startTime).Seconds()
				recordRequestDuration(method, statusTimeout, duration)
				recordError(method, errorTypeTimeout)
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewBuffer(jsonParams))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Sanitize error to remove bot token from URL in error messages
			lastErr = fmt.Errorf("failed to perform request: %w", sanitizeError(err, c.token))
			// Only retry on network errors, not on context cancellation
			if ctx.Err() != nil {
				duration := time.Since(startTime).Seconds()
				recordRequestDuration(method, statusTimeout, duration)
				recordError(method, errorTypeTimeout)
				return nil, lastErr
			}
			// Determine the error type for metrics
			if isTimeoutError(err) {
				recordError(method, errorTypeTimeout)
			} else {
				recordError(method, errorTypeNetwork)
			}
			continue
		}

		var apiResp APIResponse
		if decodeErr := json.NewDecoder(resp.Body).Decode(&apiResp); decodeErr != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("failed to decode response: %w", decodeErr)
			recordError(method, errorTypeDecode)
			continue
		}
		resp.Body.Close()

		if !apiResp.Ok {
			// Don't retry on API errors (like "Bad Request")
			duration := time.Since(startTime).Seconds()
			recordRequestDuration(method, statusError, duration)
			recordError(method, errorTypeAPI)
			return nil, fmt.Errorf("telegram api error: %s", apiResp.Description)
		}

		// Successful request
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusSuccess, duration)
		return &apiResp, nil
	}

	// All retries exhausted
	duration := time.Since(startTime).Seconds()
	recordRequestDuration(method, statusError, duration)
	return nil, lastErr
}

// isTimeoutError reports whether the error is a timeout
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "context canceled")
}

// sanitizeError removes sensitive information (bot token) from error messages.
// The token appears in URLs like: https://api.telegram.org/bot<TOKEN>/method
func sanitizeError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	sanitized := strings.ReplaceAll(err.Error(), token, "[REDACTED]")
	return fmt.Errorf("%s", sanitized)
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (*Message, error) {
	resp, err := c.makeRequest(ctx, "sendMessage", req)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}

	return &msg, nil
}

// EditMessageText edits the text of a message. Used by the streaming sink to
// progressively reveal the bot's reply.
//
// Special-case error: when Telegram rejects the edit because the new text
// equals the existing one (HTTP 400 "message is not modified"), this method
// returns the typed sentinel ErrMessageNotModified so callers can ignore it
// without string-matching API descriptions.
func (c *Client) EditMessageText(ctx context.Context, req EditMessageTextRequest) (*Message, error) {
	resp, err := c.makeRequest(ctx, "editMessageText", req)
	if err != nil {
		// Telegram returns "Bad Request: message is not modified" when the new
		// content matches the existing message exactly. Surface as a typed error.
		if strings.Contains(err.Error(), "message is not modified") {
			return nil, ErrMessageNotModified
		}
		return nil, err
	}

	// editMessageText returns the edited Message on success when the bot
	// authored the message (it does for our use case). Decode as Message.
	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return &msg, nil
}

// SetMyCommands changes the list of the bot's commands.
func (c *Client) SetMyCommands(ctx context.Context, req SetMyCommandsRequest) error {
	_, err := c.makeRequest(ctx, "setMyCommands", req)
	return err
}

// SetWebhook specifies a URL and receives incoming updates via an outgoing webhook.
func (c *Client) SetWebhook(ctx context.Context, req SetWebhookRequest) error {
	_, err := c.makeRequest(ctx, "setWebhook", req)
	return err
}

// SendChatAction tells the user that something is happening on the bot's side.
func (c *Client) SendChatAction(ctx context.Context, req SendChatActionRequest) error {
	_, err := c.makeRequest(ctx, "sendChatAction", req)
	return err
}

// GetFile returns a File object with a file_path that can be used to download the file.
func (c *Client) GetFile(ctx context.Context, req GetFileRequest) (*File, error) {
	resp, err := c.makeRequest(ctx, "getFile", req)
	if err != nil {
		return nil, err
	}

	var file File
	if err := json.Unmarshal(resp.Result, &file); err != nil {
		return nil, fmt.Errorf("failed to unmarshal file: %w", err)
	}

	return &file, nil
}

// SetMessageReaction sets a reaction on a message.
func (c *Client) SetMessageReaction(ctx context.Context, req SetMessageReactionRequest) error {
	_, err := c.makeRequest(ctx, "setMessageReaction", req)
	return err
}

// GetUpdates receives incoming updates using long polling.
//
// IMPORTANT: Uses the dedicated longPollingClient with Timeout=0.
//
// Long polling works like this: Telegram keeps the connection open for up to
// req.Timeout seconds, waiting for new messages. If messages arrive earlier,
// it returns them immediately. If not, it returns an empty array on timeout.
//
// Why a separate client:
// - httpClient has a 15s timeout, which is less than the typical req.Timeout (25s)
// - Using a shared connection pool created contention with short requests
// - An isolated transport guarantees long polling does not affect sendMessage
//
// Timeout is controlled via context: req.Timeout + 10 seconds for network delays.
//
// Metrics: records request duration, long polling status, and number of updates.
func (c *Client) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	const method = "getUpdates"
	startTime := time.Now()

	// Mark the start of long polling
	setLongPollingActive(true)
	defer setLongPollingActive(false)

	jsonParams, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	apiURL := fmt.Sprintf("%s/%s", c.apiURL, method)

	// Context timeout = Telegram timeout + margin for network delays
	// For example: req.Timeout=25 → context timeout=35 seconds
	timeout := time.Duration(req.Timeout+10) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, bytes.NewBuffer(jsonParams))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// longPollingClient has Timeout=0, the actual timeout is controlled via reqCtx
	resp, err := c.longPollingClient.Do(httpReq)
	if err != nil {
		duration := time.Since(startTime).Seconds()
		if isTimeoutError(err) {
			recordRequestDuration(method, statusTimeout, duration)
			recordError(method, errorTypeTimeout)
		} else {
			recordRequestDuration(method, statusError, duration)
			recordError(method, errorTypeNetwork)
		}
		// Sanitize error to remove bot token from URL in error messages
		return nil, fmt.Errorf("failed to perform request: %w", sanitizeError(err, c.token))
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusError, duration)
		recordError(method, errorTypeDecode)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !apiResp.Ok {
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusError, duration)
		recordError(method, errorTypeAPI)
		return nil, fmt.Errorf("telegram api error: %s", apiResp.Description)
	}

	var updates []Update
	if err := json.Unmarshal(apiResp.Result, &updates); err != nil {
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusError, duration)
		recordError(method, errorTypeDecode)
		return nil, fmt.Errorf("failed to unmarshal updates: %w", err)
	}

	// Successful request
	duration := time.Since(startTime).Seconds()
	recordRequestDuration(method, statusSuccess, duration)

	// Record the number of updates received
	if len(updates) > 0 {
		recordLongPollingUpdates(len(updates))
	}

	return updates, nil
}

// Wrapper for telegram.Client to implement BotAPI interface
type ExtendedClient struct {
	*Client
}

func NewExtendedClient(token, proxyURL string) (BotAPI, error) {
	client, err := NewClient(token, proxyURL)
	if err != nil {
		return nil, err
	}
	return &ExtendedClient{
		Client: client,
	}, nil
}

func (c *ExtendedClient) GetToken() string {
	return c.token
}
