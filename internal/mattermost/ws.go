package mattermost

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsInitialBackoff = 1 * time.Second
	wsMaxBackoff     = 30 * time.Second
	wsWriteTimeout   = 10 * time.Second
)

// wsEnvelope is the outer frame the server sends for every WebSocket event.
type wsEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	Seq   int64           `json:"seq"`
}

// postedData is the data payload of a "posted" event. Per the MM canon, post is
// a JSON *string* that must be re-parsed into a Post.
type postedData struct {
	Post        string `json:"post"`
	ChannelType string `json:"channel_type"`
}

// wsAction is a client→server WebSocket request frame (auth, presence, …).
type wsAction struct {
	Seq    int64                  `json:"seq"`
	Action string                 `json:"action"`
	Data   map[string]interface{} `json:"data,omitempty"`
}

// Run connects, authenticates, and pumps "posted" events onto Events() until the
// context is cancelled. The WebSocket is live-only (no replay): on any drop it
// reconnects with exponential backoff. It closes the events channel exactly once,
// when ctx is done, so the ingestion consumer can range over it and exit cleanly.
func (c *Client) Run(ctx context.Context) {
	defer close(c.events)

	backoff := wsInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := c.dialAndAuth(ctx)
		if err != nil {
			c.logger.Warn("websocket connect failed, retrying", "error", err, "backoff", backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		c.logger.Info("websocket connected")
		backoff = wsInitialBackoff
		c.readLoop(ctx, conn)
		// readLoop returned: connection lost or ctx cancelled. Loop reconnects
		// unless ctx is done (checked at the top).
	}
}

// dialAndAuth dials the websocket (via the per-client proxy) and sends the
// authentication_challenge frame as the first message.
func (c *Client) dialAndAuth(ctx context.Context) (*websocket.Conn, error) {
	dialer := &websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}
	if c.proxyURL != nil {
		dialer.Proxy = http.ProxyURL(c.proxyURL)
	}

	conn, resp, err := dialer.DialContext(ctx, c.wsURL, nil)
	if resp != nil && resp.Body != nil {
		// gorilla already drains/closes this on success, but close defensively
		// so the linter (and the error path) are satisfied.
		defer resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}

	auth := wsAction{Seq: 1, Action: "authentication_challenge", Data: map[string]interface{}{"token": c.cfg.BotToken}}
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if err := conn.WriteJSON(auth); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetWriteDeadline(time.Time{})

	// Announce presence so the server doesn't fan out every typing event to us.
	presence := wsAction{Seq: 2, Action: "update_presence", Data: map[string]interface{}{"channel_id": "", "thread_id": ""}}
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	_ = conn.WriteJSON(presence)
	_ = conn.SetWriteDeadline(time.Time{})

	return conn, nil
}

// readLoop reads frames until an error or context cancellation. A watcher
// goroutine closes the connection on ctx.Done() to unblock the blocking read.
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	defer conn.Close()

	for {
		var env wsEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			if ctx.Err() == nil {
				c.logger.Warn("websocket read error, will reconnect", "error", err)
			}
			return
		}
		c.logger.Debug("websocket frame", "event", env.Event, "seq", env.Seq, "data_len", len(env.Data))
		if env.Event != "posted" {
			continue
		}
		ev, ok := c.parsePosted(env.Data)
		if !ok {
			continue
		}
		select {
		case c.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// parsePosted decodes a "posted" event payload, re-parsing the inner post which
// the server sends as a JSON string.
func (c *Client) parsePosted(data json.RawMessage) (PostedEvent, bool) {
	var pd postedData
	if err := json.Unmarshal(data, &pd); err != nil {
		c.logger.Warn("failed to parse posted event data", "error", err)
		return PostedEvent{}, false
	}
	var post Post
	if err := json.Unmarshal([]byte(pd.Post), &post); err != nil {
		c.logger.Warn("failed to parse inner post json", "error", err)
		return PostedEvent{}, false
	}
	return PostedEvent{Post: post, ChannelType: pd.ChannelType}, true
}

// sleepCtx sleeps for d or until ctx is cancelled; returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextBackoff doubles the backoff up to the cap, then adds up to 20% jitter to
// avoid reconnect storms when many clients drop at once.
func nextBackoff(d time.Duration) time.Duration {
	next := d * 2
	if next > wsMaxBackoff {
		next = wsMaxBackoff
	}
	// #nosec G404 -- reconnect backoff jitter is timing, not a security primitive
	jitter := time.Duration(rand.Int64N(int64(next)/5 + 1))
	return next + jitter
}
