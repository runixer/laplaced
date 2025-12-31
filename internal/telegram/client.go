package telegram

import (
	"bytes"
	"context"
	"encoding/json"
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
	SetMyCommands(ctx context.Context, req SetMyCommandsRequest) error
	SetWebhook(ctx context.Context, req SetWebhookRequest) error
	SendChatAction(ctx context.Context, req SendChatActionRequest) error
	GetFile(ctx context.Context, req GetFileRequest) (*File, error)
	SetMessageReaction(ctx context.Context, req SetMessageReactionRequest) error
	GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error)
	GetToken() string
}

// Client is a client for the Telegram Bot API.
//
// АРХИТЕКТУРНОЕ РЕШЕНИЕ: Два отдельных HTTP-клиента
//
// Проблема: При использовании одного HTTP-клиента для всех запросов возникали
// спорадические ошибки "context deadline exceeded (Client.Timeout exceeded while
// awaiting headers)" для sendMessage, sendChatAction и getUpdates.
//
// Причина: Long polling (getUpdates с Timeout=25s) "занимал" соединения из общего
// пула на длительное время. При высокой нагрузке или сетевых задержках другие
// запросы не могли получить свободное соединение и таймаутились.
//
// Решение:
//  1. httpClient - для коротких API-вызовов (sendMessage, sendChatAction и т.д.)
//     с таймаутом 15 секунд и retry-логикой
//  2. longPollingClient - для getUpdates с бесконечным таймаутом (контролируется
//     через context), изолированный пул соединений
//
// Дополнительно отключён HTTP/2 (ForceAttemptHTTP2=false), так как мультиплексирование
// запросов через одно соединение усугубляло проблему с конкуренцией за ресурсы.
type Client struct {
	token             string
	httpClient        *http.Client // Для коротких API-вызовов с retry
	longPollingClient *http.Client // Для getUpdates с длительным ожиданием
	apiURL            string
}

// NewClient creates a new Telegram API client.
//
// Создаёт два изолированных HTTP-клиента с разными настройками:
// - httpClient: таймаут 30s, retry-логика, для sendMessage/sendChatAction
// - longPollingClient: без таймаута (контроль через context), для getUpdates
func NewClient(token, proxyURL string) (*Client, error) {
	// Transport для обычных API-вызовов (sendMessage, sendChatAction и т.д.)
	// DisableKeepAlives=true - каждый запрос создаёт новое соединение,
	// что исключает проблемы с "зависшими" keep-alive соединениями
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // Увеличен для нестабильных сетей
			KeepAlive: 0,                // Отключаем keep-alive на уровне TCP
		}).DialContext,
		// HTTP/2 отключён: мультиплексирование через одно соединение создавало
		// конкуренцию между короткими и длинными запросами
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          0, // Не храним idle соединения
		IdleConnTimeout:       0, // Не используется при DisableKeepAlives=true
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second, // Таймаут на получение заголовков ответа
		MaxIdleConnsPerHost:   0,
		DisableKeepAlives:     true, // Каждый запрос - новое соединение
	}

	// Отдельный transport для long polling - изолированный пул соединений
	// с увеличенными таймаутами для длительного ожидания updates
	// Здесь keep-alive нужен, чтобы не переустанавливать соединение каждые 25 секунд
	longPollingTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 60 * time.Second, // Дольше держим соединение живым
		}).DialContext,
		ForceAttemptHTTP2:     false, // Консистентность с основным transport
		MaxIdleConns:          2,     // Минимальный пул, нужно только одно соединение
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 0, // Без ограничения - контролируется через context
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
		// Таймаут 30s + retry-логика в makeRequest()
		// DisableKeepAlives гарантирует свежее соединение для каждого запроса
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		// Таймаут=0 означает "без ограничения" - контролируется через context
		// в методе GetUpdates() (Timeout + 10 секунд на сетевые задержки)
		longPollingClient: &http.Client{
			Timeout:   0,
			Transport: longPollingTransport,
		},
		apiURL: fmt.Sprintf("https://api.telegram.org/bot%s", token),
	}, nil
}

// makeRequest performs a request to the Telegram API with retry logic.
//
// Retry-стратегия: до 2 попыток с задержкой 2 секунды.
// Retry выполняется только для сетевых ошибок, НЕ для API-ошибок Telegram
// (например, "Bad Request" не будет повторяться).
//
// С таймаутом httpClient 30s и DisableKeepAlives=true:
// - Каждый запрос создаёт новое TCP-соединение (исключает проблемы с "зависшими" соединениями)
// - Общее время ожидания до ~62 секунд в худшем случае (30+2+30)
// - Retry помогает при временных DNS/сетевых проблемах
//
// Метрики: записывает время выполнения, количество retry и ошибки в Prometheus.
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
			// Записываем retry в метрики
			recordRetry(method)

			// Фиксированная задержка перед retry
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
			lastErr = fmt.Errorf("failed to perform request: %w", err)
			// Only retry on network errors, not on context cancellation
			if ctx.Err() != nil {
				duration := time.Since(startTime).Seconds()
				recordRequestDuration(method, statusTimeout, duration)
				recordError(method, errorTypeTimeout)
				return nil, lastErr
			}
			// Определяем тип ошибки для метрик
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

		// Успешный запрос
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusSuccess, duration)
		return &apiResp, nil
	}

	// Все retry исчерпаны
	duration := time.Since(startTime).Seconds()
	recordRequestDuration(method, statusError, duration)
	return nil, lastErr
}

// isTimeoutError проверяет, является ли ошибка таймаутом
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "context canceled")
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
// ВАЖНО: Использует отдельный longPollingClient с Timeout=0.
//
// Long polling работает так: Telegram держит соединение открытым до req.Timeout
// секунд, ожидая новые сообщения. Если сообщения приходят раньше - возвращает
// их сразу. Если нет - возвращает пустой массив по истечении таймаута.
//
// Почему отдельный клиент:
// - httpClient имеет таймаут 15s, что меньше типичного req.Timeout (25s)
// - Использование общего пула соединений создавало конкуренцию с короткими запросами
// - Изолированный transport гарантирует, что long polling не влияет на sendMessage
//
// Таймаут контролируется через context: req.Timeout + 10 секунд на сетевые задержки.
//
// Метрики: записывает время выполнения, статус long polling и количество updates.
func (c *Client) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	const method = "getUpdates"
	startTime := time.Now()

	// Отмечаем начало long polling
	setLongPollingActive(true)
	defer setLongPollingActive(false)

	jsonParams, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	apiURL := fmt.Sprintf("%s/%s", c.apiURL, method)

	// Context с таймаутом = Telegram timeout + запас на сетевые задержки
	// Например: req.Timeout=25 → context timeout=35 секунд
	timeout := time.Duration(req.Timeout+10) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, bytes.NewBuffer(jsonParams))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// longPollingClient имеет Timeout=0, реальный таймаут контролируется через reqCtx
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
		return nil, fmt.Errorf("failed to perform request: %w", err)
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

	// Успешный запрос
	duration := time.Since(startTime).Seconds()
	recordRequestDuration(method, statusSuccess, duration)

	// Записываем количество полученных updates
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
