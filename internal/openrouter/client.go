package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/jobtype"
)

// Retry configuration
const (
	maxRetries   = 3
	baseDelay    = 1 * time.Second
	maxDelay     = 30 * time.Second
	jitterFactor = 0.2 // 20% jitter
)

type Client interface {
	CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error)
	CreateEmbeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// FilterReasoningForLog filters out encrypted reasoning entries from reasoning_details.
// Keeps only "reasoning.text" entries which are human-readable and useful for debugging.
// This prevents massive base64 blobs from polluting logs.
func FilterReasoningForLog(details interface{}) interface{} {
	if details == nil {
		return nil
	}

	// reasoning_details is typically []interface{} of maps
	arr, ok := details.([]interface{})
	if !ok {
		return details
	}

	var filtered []interface{}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Keep only reasoning.text entries, skip reasoning.encrypted
		if t, ok := m["type"].(string); ok && t == "reasoning.text" {
			filtered = append(filtered, m)
		}
	}

	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// truncateForLog truncates a string to maxLen characters for logging.
// Adds "... (truncated)" suffix if truncation occurred.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

type clientImpl struct {
	httpClient  *http.Client
	apiKey      string
	apiEndpoint string
	logger      *slog.Logger
}

// isRetryableStatusCode returns true if the HTTP status code indicates a retryable error.
func isRetryableStatusCode(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// isRetryableError returns true if the error is a network/timeout error that should be retried.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for timeout errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Check for connection errors
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// calculateBackoff returns the delay for the given attempt using exponential backoff with jitter.
func calculateBackoff(attempt int) time.Duration {
	// Limit attempt to avoid overflow (2^5 = 32 seconds is already > maxDelay)
	if attempt > 5 {
		attempt = 5
	}
	// Exponential backoff: baseDelay * 2^attempt
	delay := baseDelay * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter: ±20%
	jitter := time.Duration(float64(delay) * jitterFactor * (2*rand.Float64() - 1))
	return delay + jitter
}

type ImageURL struct {
	URL string `json:"url"`
}

type ImagePart struct {
	Type     string   `json:"type"`
	ImageURL ImageURL `json:"image_url"`
}

type File struct {
	FileName string `json:"filename"`
	FileData string `json:"file_data"`
}

type FilePart struct {
	Type string `json:"type"`
	File File   `json:"file"`
}

type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ExtraContent interface{} `json:"extra_content,omitempty"`
}

type Message struct {
	Role             string      `json:"role"`
	Content          interface{} `json:"content"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string      `json:"tool_call_id,omitempty"`
	ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
}

type PDFConfig struct {
	Engine string `json:"engine"`
}

type Plugin struct {
	ID  string    `json:"id"`
	PDF PDFConfig `json:"pdf,omitempty"`
}

// ReasoningConfig controls the model's internal reasoning behavior.
// For Gemini 3: effort levels "minimal", "low", "medium", "high".
// For other models: max_tokens (1024-128000) controls reasoning depth.
//
// NOTE: response-healing plugin breaks reasoning visibility when combined with json_object format.
type ReasoningConfig struct {
	Effort    string `json:"effort,omitempty"`     // Gemini 3: "minimal", "low", "medium", "high"
	MaxTokens int    `json:"max_tokens,omitempty"` // Other models: token budget for reasoning
	Exclude   bool   `json:"exclude,omitempty"`    // Suppress reasoning from response
}

type ChatCompletionRequest struct {
	Model          string           `json:"model"`
	Messages       []Message        `json:"messages"`
	Plugins        []Plugin         `json:"plugins,omitempty"`
	Tools          []Tool           `json:"tools,omitempty"`
	ToolChoice     any              `json:"tool_choice,omitempty"`
	ResponseFormat interface{}      `json:"response_format,omitempty"`
	Reasoning      *ReasoningConfig `json:"reasoning,omitempty"`

	// UserID is used for metrics tracking only, not sent to API
	UserID int64 `json:"-"`
}

type JSONSchema struct {
	Name   string                 `json:"name"`
	Strict bool                   `json:"strict,omitempty"`
	Schema map[string]interface{} `json:"schema"`
}

type ResponseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

type ResponseFormatJSONSchema struct {
	Type       string     `json:"type"` // "json_schema"
	JSONSchema JSONSchema `json:"json_schema"`
}

type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role             string      `json:"role"`
			Content          string      `json:"content"`
			ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
			Reasoning        string      `json:"reasoning,omitempty"`         // Gemini 3: raw reasoning text
			ReasoningDetails interface{} `json:"reasoning_details,omitempty"` // Structured reasoning details
		} `json:"message"`
		FinishReason string `json:"finish_reason,omitempty"`
		Index        int    `json:"index"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		TotalTokens      int      `json:"total_tokens"`
		Cost             *float64 `json:"cost,omitempty"` // Cost in USD from OpenRouter
	} `json:"usage"`

	// DebugRequestBody contains the raw JSON request body sent to OpenRouter.
	// Not part of API response - populated by client for debugging purposes.
	DebugRequestBody string `json:"-"`

	// DebugResponseBody contains the raw JSON response body from OpenRouter.
	// Not part of API response - populated by client for debugging purposes.
	DebugResponseBody string `json:"-"`
}

type EmbeddingRequest struct {
	Model   string                 `json:"model"`
	Input   []string               `json:"input"`
	LogMeta map[string]interface{} `json:"-"`
}

type EmbeddingObject struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type EmbeddingResponse struct {
	Object string            `json:"object"`
	Data   []EmbeddingObject `json:"data"`
	Model  string            `json:"model"`
	Usage  struct {
		PromptTokens int      `json:"prompt_tokens"`
		TotalTokens  int      `json:"total_tokens"`
		Cost         *float64 `json:"cost,omitempty"` // Cost in USD from OpenRouter
	} `json:"usage"`
}

func NewClient(logger *slog.Logger, apiKey, proxyURL string) (Client, error) {
	return NewClientWithBaseURL(logger, apiKey, proxyURL, "https://openrouter.ai/api/v1")
}

func NewClientWithBaseURL(logger *slog.Logger, apiKey, proxyURL, baseURL string) (Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConnsPerHost:   10, // Increased from default 2 to allow more concurrent requests
	}

	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}

	clientLogger := logger.With("component", "openrouter_client")

	if proxyURL != "" {
		safeProxyURL := proxyURL
		if u, err := url.Parse(proxyURL); err == nil {
			if u.User != nil {
				u.User = url.UserPassword(u.User.Username(), "*****")
				safeProxyURL = u.String()
			}
		}
		clientLogger.Info("Using proxy for OpenRouter", "proxy_url", safeProxyURL)
	}

	return &clientImpl{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   300 * time.Second, // Global timeout for requests (5 min for large contexts)
		},
		apiKey:      apiKey,
		apiEndpoint: baseURL,
		logger:      clientLogger,
	}, nil
}

func (c *clientImpl) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	startTime := time.Now()
	jt := jobtype.FromContext(ctx).String()

	// Calculate context size for logging (text content only, not images/files)
	contextChars := 0
	for _, msg := range req.Messages {
		switch content := msg.Content.(type) {
		case string:
			contextChars += len(content)
		case []interface{}:
			// Multi-modal content (text + images)
			for _, part := range content {
				if partMap, ok := part.(map[string]interface{}); ok {
					if partType, ok := partMap["type"].(string); ok && partType == "text" {
						if text, ok := partMap["text"].(string); ok {
							contextChars += len(text)
						}
					}
				}
			}
		}
	}
	// Rough estimate: ~4 chars per token for Gemini
	estimatedTokens := contextChars / 4

	// Log request summary (full details available in Agent Debug UI)
	c.logger.Info("Sending request to OpenRouter",
		"model", req.Model,
		"message_count", len(req.Messages),
		"tools_count", len(req.Tools),
		"context_chars", contextChars,
		"estimated_tokens", estimatedTokens,
	)

	body, err := json.Marshal(req)
	if err != nil {
		return ChatCompletionResponse{}, err
	}

	endpoint, err := url.JoinPath(c.apiEndpoint, "chat/completions")
	if err != nil {
		return ChatCompletionResponse{}, err
	}

	var responseBody []byte
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			RecordLLMRetry(req.Model)
			delay := calculateBackoff(attempt - 1)
			c.logger.Warn("Retrying OpenRouter request",
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)

			select {
			case <-ctx.Done():
				return ChatCompletionResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(body))
		if err != nil {
			return ChatCompletionResponse{}, err
		}

		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "laplaced/1.0")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return ChatCompletionResponse{}, err
		}

		responseBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return ChatCompletionResponse{}, err
		}

		c.logger.Debug("OpenRouter response received", "status", resp.Status, "attempt", attempt)

		if resp.StatusCode == http.StatusOK {
			break // Success
		}

		// Check if we should retry
		if isRetryableStatusCode(resp.StatusCode) && attempt < maxRetries {
			lastErr = fmt.Errorf("openrouter API error: %s", resp.Status)
			continue
		}

		// Non-retryable error or max retries reached
		c.logger.Error("OpenRouter returned non-OK status", "status", resp.Status, "body", string(responseBody))
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		return ChatCompletionResponse{}, fmt.Errorf("openrouter API error: %s", resp.Status)
	}

	var chatResp ChatCompletionResponse
	if err := json.NewDecoder(bytes.NewBuffer(responseBody)).Decode(&chatResp); err != nil {
		c.logger.Error("Failed to decode OpenRouter response", "error", err, "body_length", len(responseBody))
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		return ChatCompletionResponse{}, err
	}

	// Store raw request/response bodies for debugging
	chatResp.DebugRequestBody = string(body)
	chatResp.DebugResponseBody = string(responseBody)

	if len(chatResp.Choices) > 0 {
		msg := chatResp.Choices[0].Message
		c.logger.Debug("OpenRouter response content",
			"content", msg.Content,
			"tool_calls_count", len(msg.ToolCalls),
			"reasoning_details", FilterReasoningForLog(msg.ReasoningDetails),
		)

		// Log warning if model outputs tool call as text instead of proper tool_calls
		// This is a known Gemini issue where it hallucinates the internal tool format
		if strings.Contains(msg.Content, "default_api:") {
			c.logger.Warn("Model leaked internal tool call format in content (Gemini hallucination)",
				"content_length", len(msg.Content),
				"content_preview", truncateForLog(msg.Content, 500),
				"tool_calls_count", len(msg.ToolCalls),
				"finish_reason", chatResp.Choices[0].FinishReason,
				"completion_tokens", chatResp.Usage.CompletionTokens,
			)
		}
	}

	c.logger.Info("OpenRouter response parsed successfully",
		"model", chatResp.Model,
		"choices", len(chatResp.Choices),
		"prompt_tokens", chatResp.Usage.PromptTokens,
		"completion_tokens", chatResp.Usage.CompletionTokens,
		"total_tokens", chatResp.Usage.TotalTokens,
		"cost", chatResp.Usage.Cost,
	)

	// Record success metrics
	duration := time.Since(startTime).Seconds()
	RecordLLMRequest(req.UserID, req.Model, duration, true, chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, chatResp.Usage.Cost, jt)

	return chatResp, nil
}

func (c *clientImpl) CreateEmbeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error) {
	embeddingsURL, err := url.JoinPath(c.apiEndpoint, "embeddings")
	if err != nil {
		return EmbeddingResponse{}, err
	}

	c.logger.Debug("Sending embedding request to OpenRouter",
		"model", req.Model,
		"input_count", len(req.Input),
		"meta", req.LogMeta,
	)

	body, err := json.Marshal(req)
	if err != nil {
		return EmbeddingResponse{}, err
	}

	var responseBody []byte
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := calculateBackoff(attempt - 1)
			c.logger.Warn("Retrying OpenRouter embeddings request",
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)

			select {
			case <-ctx.Done():
				return EmbeddingResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", embeddingsURL, bytes.NewBuffer(body))
		if err != nil {
			return EmbeddingResponse{}, err
		}

		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "laplaced/1.0")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return EmbeddingResponse{}, err
		}

		responseBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return EmbeddingResponse{}, err
		}

		if resp.StatusCode == http.StatusOK {
			break // Success
		}

		// Check if we should retry
		if isRetryableStatusCode(resp.StatusCode) && attempt < maxRetries {
			lastErr = fmt.Errorf("openrouter API error: %s", resp.Status)
			continue
		}

		// Non-retryable error or max retries reached
		c.logger.Error("OpenRouter embeddings returned non-OK status", "status", resp.Status, "body", string(responseBody))
		return EmbeddingResponse{}, fmt.Errorf("openrouter API error: %s", resp.Status)
	}

	var embeddingResp EmbeddingResponse
	if err := json.Unmarshal(responseBody, &embeddingResp); err != nil {
		c.logger.Error("Failed to decode embedding response", "error", err, "body", string(responseBody))
		return EmbeddingResponse{}, err
	}

	if len(embeddingResp.Data) == 0 {
		c.logger.Warn("OpenRouter embeddings received NO DATA", "body", string(responseBody))
	} else {
		c.logger.Debug("OpenRouter embeddings received",
			"object", embeddingResp.Object,
			"count", len(embeddingResp.Data),
			"usage", embeddingResp.Usage,
		)
	}

	return embeddingResp, nil
}
