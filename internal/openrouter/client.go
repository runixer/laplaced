package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"time"
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

// filterReasoningForLog filters out encrypted reasoning entries from reasoning_details.
// Keeps only "reasoning.text" entries which are human-readable and useful for debugging.
// This prevents massive base64 blobs from polluting logs.
func filterReasoningForLog(details interface{}) interface{} {
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

type ChatCompletionRequest struct {
	Model          string      `json:"model"`
	Messages       []Message   `json:"messages"`
	Plugins        []Plugin    `json:"plugins,omitempty"`
	Tools          []Tool      `json:"tools,omitempty"`
	ToolChoice     any         `json:"tool_choice,omitempty"`
	ResponseFormat interface{} `json:"response_format,omitempty"`
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
			ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
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
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
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
			Timeout:   120 * time.Second, // Global timeout for requests
		},
		apiKey:      apiKey,
		apiEndpoint: baseURL,
		logger:      clientLogger,
	}, nil
}

// loggableMessage is a version of the Message struct for logging, omitting large data fields.
type loggableMessage struct {
	Role       string         `json:"role"`
	Content    []loggablePart `json:"content"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// loggablePart represents a part of a message for logging purposes.
type loggablePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	FileName string `json:"filename,omitempty"`
}

func (c *clientImpl) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	// Create a loggable version of the request
	loggableMessages := make([]loggableMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		logMsg := loggableMessage{
			Role:       msg.Role,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
		}
		if msg.Content != nil {
			if contentParts, ok := msg.Content.([]interface{}); ok {
				for _, part := range contentParts {
					switch p := part.(type) {
					case TextPart:
						const maxLoggableTextLength = 256
						logText := p.Text
						if len(logText) > maxLoggableTextLength {
							logText = logText[:maxLoggableTextLength] + "... (truncated)"
						}
						logMsg.Content = append(logMsg.Content, loggablePart{Type: p.Type, Text: logText})
					case ImagePart:
						logMsg.Content = append(logMsg.Content, loggablePart{Type: p.Type, FileName: "image.jpg"}) // Placeholder
					case FilePart:
						logMsg.Content = append(logMsg.Content, loggablePart{Type: p.Type, FileName: p.File.FileName})
					}
				}
			} else if contentStr, ok := msg.Content.(string); ok {
				// Handle string content (which might happen if we reuse response messages)
				const maxLoggableTextLength = 256
				logText := contentStr
				if len(logText) > maxLoggableTextLength {
					logText = logText[:maxLoggableTextLength] + "... (truncated)"
				}
				logMsg.Content = append(logMsg.Content, loggablePart{Type: "text", Text: logText})
			}
		}
		loggableMessages = append(loggableMessages, logMsg)
	}

	loggableReq := struct {
		Model          string            `json:"model"`
		Messages       []loggableMessage `json:"messages"`
		Plugins        []Plugin          `json:"plugins,omitempty"`
		Tools          []Tool            `json:"tools,omitempty"`
		ToolChoice     any               `json:"tool_choice,omitempty"`
		ResponseFormat interface{}       `json:"response_format,omitempty"`
	}{
		Model:          req.Model,
		Messages:       loggableMessages,
		Plugins:        req.Plugins,
		Tools:          req.Tools,
		ToolChoice:     req.ToolChoice,
		ResponseFormat: req.ResponseFormat,
	}

	c.logger.Debug("Sending request to OpenRouter",
		"model", req.Model,
		"message_count", len(req.Messages),
		"plugins", req.Plugins,
		"tools_count", len(req.Tools),
		"request_structure", loggableReq,
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
		return ChatCompletionResponse{}, fmt.Errorf("openrouter API error: %s", resp.Status)
	}

	var chatResp ChatCompletionResponse
	if err := json.NewDecoder(bytes.NewBuffer(responseBody)).Decode(&chatResp); err != nil {
		c.logger.Error("Failed to decode OpenRouter response", "error", err, "body_length", len(responseBody))
		return ChatCompletionResponse{}, err
	}

	if len(chatResp.Choices) > 0 {
		msg := chatResp.Choices[0].Message
		c.logger.Debug("OpenRouter response content",
			"content", msg.Content,
			"tool_calls_count", len(msg.ToolCalls),
			"reasoning_details", filterReasoningForLog(msg.ReasoningDetails),
		)
	}

	c.logger.Info("OpenRouter response parsed successfully",
		"model", chatResp.Model,
		"choices", len(chatResp.Choices),
		"prompt_tokens", chatResp.Usage.PromptTokens,
		"completion_tokens", chatResp.Usage.CompletionTokens,
		"total_tokens", chatResp.Usage.TotalTokens,
	)

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
