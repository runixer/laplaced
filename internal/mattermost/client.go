// Package mattermost is a thin, hand-written client for the Mattermost API v4
// (Time = Mattermost v4). It deliberately avoids the official MM SDK to keep the
// dependency surface small and to tolerate Time's extensions/divergences.
//
// The package is a leaf: it depends only on the stdlib and gorilla/websocket and
// MUST NOT import internal/bot, internal/config, or internal/storage. The
// transport adapter that maps wire types onto the bot's neutral envelope lives in
// internal/bot/transport_mattermost.go.
package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config holds the connection settings for a Mattermost/Time server.
type Config struct {
	ServerURL string // e.g. https://time.example.com
	BotToken  string // bot account token; sent as "Authorization: Bearer <token>"
	ProxyURL  string // explicit per-client proxy (corp proxy); "" = direct
}

// Client talks to the Mattermost v4 REST API over a dedicated, proxy-aware
// HTTP client. The same proxy is reused for the WebSocket dialer (ws.go).
type Client struct {
	cfg        Config
	httpClient *http.Client
	proxyURL   *url.URL // parsed once; reused by the WS dialer
	baseURL    string   // ServerURL + "/api/v4"
	wsURL      string   // ws(s)://host/api/v4/websocket
	logger     *slog.Logger

	botID       string
	maxPostSize int

	userCache   map[string]*User // id -> profile; populated lazily by GetUser
	userCacheMu sync.RWMutex

	events chan PostedEvent
}

// Wire types (subset of the MM v4 schema we use).

// User is the subset of /users/{id} we read (identity + display name).
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Nickname  string `json:"nickname"`
	IsBot     bool   `json:"is_bot"`
}

// Post is the subset of a Mattermost post we read/write.
type Post struct {
	ID        string       `json:"id"`
	UserID    string       `json:"user_id"`
	ChannelID string       `json:"channel_id"`
	RootID    string       `json:"root_id"`
	Message   string       `json:"message"`
	Type      string       `json:"type"` // "" for user posts; non-empty for system posts
	CreateAt  int64        `json:"create_at"`
	FileIDs   []string     `json:"file_ids,omitempty"` // attached file ids
	Metadata  PostMetadata `json:"metadata,omitempty"` // carries embedded FileInfo for attachments
}

// PostMetadata carries enriched post data; we only read attached file info.
type PostMetadata struct {
	Files []FileInfo `json:"files,omitempty"`
}

// FileInfo is the subset of a Mattermost FileInfo we use for inbound attachments.
type FileInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mime_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// createPostReq is the body for POST /posts. idempotency_key and peer are Time
// extensions (idempotency_key dedupes retries with a 30s TTL).
type createPostReq struct {
	ChannelID      string   `json:"channel_id,omitempty"`
	Message        string   `json:"message"`
	RootID         string   `json:"root_id,omitempty"`
	FileIDs        []string `json:"file_ids,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

// CreatePostReq is the caller-facing post-creation request.
type CreatePostReq struct {
	ChannelID      string
	Message        string
	RootID         string
	FileIDs        []string
	IdempotencyKey string
}

// PostedEvent is a parsed "posted" WebSocket event: the inner post plus the
// channel type (D/O/P/G) the event carried alongside it.
type PostedEvent struct {
	Post        Post
	ChannelType string // "D" (DM) | "O" (open) | "P" (private) | "G" (group)
}

// clientConfig is the subset of GET /config/client we read.
type clientConfig struct {
	MaxPostSize string `json:"MaxPostSize"`
}

// defaultMaxPostSize is the fallback when the server's client-config does not
// report MaxPostSize (never use 0 — the splitter would misbehave).
const defaultMaxPostSize = 16383

// NewClient builds the client and bootstraps the bot identity and message-size
// limit from the live server. It fails fast if /users/me is unreachable.
func NewClient(ctx context.Context, cfg Config, logger *slog.Logger) (*Client, error) {
	server := strings.TrimRight(cfg.ServerURL, "/")
	if server == "" {
		return nil, fmt.Errorf("mattermost: server_url is required")
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	var proxyURL *url.URL
	if cfg.ProxyURL != "" {
		p, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("mattermost: failed to parse proxy URL: %w", err)
		}
		proxyURL = p
		// Explicit per-client proxy ONLY — never a process-wide HTTP_PROXY, which
		// would also route the litellm/openrouter client through the corp proxy.
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	c := &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
		proxyURL:   proxyURL,
		baseURL:    server + "/api/v4",
		wsURL:      wsURLFromServer(server),
		logger:     logger.With("component", "mattermost"),
		userCache:  make(map[string]*User),
		events:     make(chan PostedEvent),
	}

	me, err := c.getMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("mattermost: failed to fetch bot identity: %w", err)
	}
	c.botID = me.ID
	c.maxPostSize = c.fetchMaxPostSize(ctx)
	c.logger.Info("mattermost client ready", "bot_id", c.botID, "bot_username", me.Username, "max_post_size", c.maxPostSize)
	return c, nil
}

// BotID returns the bot account id (used to ignore the bot's own posts).
func (c *Client) BotID() string { return c.botID }

// MaxPostSize returns the server's per-post size limit (read at startup).
func (c *Client) MaxPostSize() int { return c.maxPostSize }

// Events returns the channel of incoming "posted" events produced by Run.
func (c *Client) Events() <-chan PostedEvent { return c.events }

func (c *Client) getMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/users/me", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// fetchMaxPostSize reads MaxPostSize from the client config, falling back to a
// safe default on any error so the renderer's splitter always has a valid limit.
func (c *Client) fetchMaxPostSize(ctx context.Context) int {
	var cc clientConfig
	if err := c.do(ctx, http.MethodGet, "/config/client?format=old", nil, &cc); err != nil {
		c.logger.Warn("failed to read client config, using default max post size", "error", err, "default", defaultMaxPostSize)
		return defaultMaxPostSize
	}
	var size int
	if _, err := fmt.Sscanf(cc.MaxPostSize, "%d", &size); err != nil || size <= 0 {
		return defaultMaxPostSize
	}
	return size
}

// CreatePost sends a post and returns the created post (with its server id).
func (c *Client) CreatePost(ctx context.Context, r CreatePostReq) (*Post, error) {
	body := createPostReq{
		ChannelID:      r.ChannelID,
		Message:        r.Message,
		RootID:         r.RootID,
		FileIDs:        r.FileIDs,
		IdempotencyKey: r.IdempotencyKey,
	}
	var post Post
	if err := c.do(ctx, http.MethodPost, "/posts", body, &post); err != nil {
		return nil, err
	}
	return &post, nil
}

// SetReaction adds an emoji reaction (by shortcode name) to a post.
func (c *Client) SetReaction(ctx context.Context, postID, emojiName string) error {
	body := map[string]string{
		"user_id":    c.botID,
		"post_id":    postID,
		"emoji_name": emojiName,
	}
	return c.do(ctx, http.MethodPost, "/reactions", body, nil)
}

// SendTyping posts a best-effort typing indicator to a channel.
func (c *Client) SendTyping(ctx context.Context, channelID string) error {
	body := map[string]string{"channel_id": channelID}
	return c.do(ctx, http.MethodPost, "/users/"+c.botID+"/typing", body, nil)
}

// GetUser fetches a user's profile by id, caching the result — profiles change
// rarely and the WS ingestion looks them up once per inbound post. Concurrency-
// safe (the parallel tool path never calls this, but the cache is guarded anyway).
func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	c.userCacheMu.RLock()
	cached, ok := c.userCache[userID]
	c.userCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	var u User
	if err := c.do(ctx, http.MethodGet, "/users/"+userID, nil, &u); err != nil {
		return nil, err
	}
	c.userCacheMu.Lock()
	c.userCache[userID] = &u
	c.userCacheMu.Unlock()
	return &u, nil
}

// GetFileInfo fetches metadata for an attached file (name, mime, size, …).
func (c *Client) GetFileInfo(ctx context.Context, fileID string) (*FileInfo, error) {
	var fi FileInfo
	if err := c.do(ctx, http.MethodGet, "/files/"+fileID+"/info", nil, &fi); err != nil {
		return nil, err
	}
	return &fi, nil
}

// GetFile downloads the raw bytes of an attached file.
func (c *Client) GetFile(ctx context.Context, fileID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/files/"+fileID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.BotToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to /files/%s failed: %w", fileID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mattermost get file %s: status %d: %s", fileID, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return io.ReadAll(resp.Body)
}

// UploadFile uploads one file to a channel via POST /api/v4/files (multipart),
// returning the new file id to attach to a post. The MIME type is set on the
// form part when known so the server records the right content type.
func (c *Client) UploadFile(ctx context.Context, channelID, filename, mimeType string, data []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("channel_id", channelID); err != nil {
		return "", fmt.Errorf("failed to write channel_id field: %w", err)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename=%q`, filename))
	if mimeType != "" {
		h.Set("Content-Type", mimeType)
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("failed to create form file part: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("failed to write file bytes: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files", &buf)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.BotToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request to /files failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("mattermost upload file: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		FileInfos []FileInfo `json:"file_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to decode upload response: %w", err)
	}
	if len(out.FileInfos) == 0 {
		return "", fmt.Errorf("mattermost upload file: empty file_infos in response")
	}
	return out.FileInfos[0].ID, nil
}

// do performs a JSON request against the API, decoding the response into out
// (out may be nil to discard the body). MM error bodies are surfaced verbatim.
func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.BotToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("mattermost api %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode response from %s: %w", path, err)
	}
	return nil
}

// wsURLFromServer maps an http(s) server URL to the ws(s) websocket endpoint.
func wsURLFromServer(server string) string {
	ws := server
	switch {
	case strings.HasPrefix(ws, "https://"):
		ws = "wss://" + strings.TrimPrefix(ws, "https://")
	case strings.HasPrefix(ws, "http://"):
		ws = "ws://" + strings.TrimPrefix(ws, "http://")
	}
	return ws + "/api/v4/websocket"
}
