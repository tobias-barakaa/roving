package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Client is one open WebSocket connection (one browser tab). A user with three
// tabs open has three Clients. Connections are not rooms, calls or sessions.
type Client struct {
	ID     string
	UserID string

	hub     *Hub
	conn    *websocket.Conn
	token   string // session cookie value, used only for periodic re-checks
	send    chan []byte
	cancel  context.CancelFunc
	closing sync.Once
}

func newClient(h *Hub, conn *websocket.Conn, userID, token string) *Client {
	return &Client{
		ID:     newConnID(),
		UserID: userID,
		hub:    h,
		conn:   conn,
		token:  token,
		send:   make(chan []byte, h.cfg.SendBuffer),
	}
}

// Send queues a message for this connection. It never blocks. If the browser
// is too slow and the buffer is full, the connection is closed (it reconnects
// and re-syncs) instead of stalling the rest of the server.
func (c *Client) Send(env Envelope) bool {
	b, err := json.Marshal(env)
	if err != nil {
		return false
	}
	return c.enqueue(b)
}

func (c *Client) enqueue(b []byte) bool {
	select {
	case c.send <- b:
		return true
	default:
		c.closeWith(websocket.StatusTryAgainLater, "slow consumer")
		return false
	}
}

func (c *Client) sendError(id, code, msg string) {
	c.Send(Envelope{Type: TypeError, ID: id, Payload: payloadOf(ErrorPayload{Code: code, Message: msg})})
}

// closeWith performs the WebSocket close handshake once, in the background.
func (c *Client) closeWith(code websocket.StatusCode, reason string) {
	c.closing.Do(func() {
		go func() { _ = c.conn.Close(code, reason) }()
	})
}

// closeFromReader is closeWith for the read goroutine itself: it finishes the
// close handshake before returning, so the deferred cleanup in run does not cut
// the connection before the close frame is delivered.
func (c *Client) closeFromReader(code websocket.StatusCode, reason string) {
	c.closing.Do(func() { _ = c.conn.Close(code, reason) })
}

// run owns the connection until it ends. It blocks, then cleans up.
func (c *Client) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	defer func() {
		cancel()
		c.hub.unregister(c)
		_ = c.conn.CloseNow()
		c.hub.fireDisconnect(c)
		c.hub.cfg.Logf("[realtime] disconnected conn=%s user=%s", c.ID, c.UserID)
	}()

	c.conn.SetReadLimit(c.hub.cfg.MaxMessageBytes)

	go c.writeLoop(ctx)
	go c.pingLoop(ctx)
	go c.reauthLoop(ctx)
	c.readLoop(ctx)
}

func (c *Client) readLoop(ctx context.Context) {
	cfg := c.hub.cfg
	bucket := newBucket(cfg.RatePerSecond, cfg.RateBurst)
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return // closed by peer, by us, or by ctx
		}
		if typ != websocket.MessageText {
			c.closeFromReader(websocket.StatusUnsupportedData, "text messages only")
			return
		}
		if !bucket.allow() {
			c.closeFromReader(websocket.StatusPolicyViolation, "rate limit exceeded")
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
			c.sendError("", CodeBadMessage, `messages look like {"type":"…","payload":{…}}`)
			continue
		}
		c.hub.dispatch(ctx, c, env)
	}
}

func (c *Client) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, c.hub.cfg.WriteTimeout)
			err := c.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

// pingLoop sends protocol-level pings. A missed pong means the peer is gone
// (laptop lid closed, network dropped) and the connection is cleaned up.
func (c *Client) pingLoop(ctx context.Context) {
	t := time.NewTicker(c.hub.cfg.PingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.hub.cfg.PongTimeout)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					c.hub.cfg.Logf("[realtime] ping failed conn=%s: %v", c.ID, err)
					c.cancel()
				}
				return
			}
		}
	}
}

// reauthLoop makes sure a logged-out or expired session cannot keep a socket.
func (c *Client) reauthLoop(ctx context.Context) {
	cfg := c.hub.cfg
	if cfg.Reauth == nil {
		return
	}
	t := time.NewTicker(cfg.ReauthEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			uid, err := cfg.Reauth(rctx, c.token)
			cancel()
			switch {
			case err == nil && uid == c.UserID:
			case err == nil, errors.Is(err, ErrSessionInvalid):
				c.closeWith(websocket.StatusPolicyViolation, "session ended")
				return
			default:
				cfg.Logf("[realtime] reauth check failed (keeping conn %s): %v", c.ID, err)
			}
		}
	}
}

// bucket is a small token-bucket rate limiter (single goroutine use).
type bucket struct {
	rate, burst, tokens float64
	last                time.Time
}

func newBucket(rate, burst float64) *bucket {
	return &bucket{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

func (b *bucket) allow() bool {
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}


// // backend/internal/realtime/client.go

// package realtime

// import (
// 	"context"
// 	"encoding/json"
// 	"errors"
// 	"sync"
// 	"time"

// 	"github.com/coder/websocket"
// )

// // Client is one open WebSocket connection (one browser tab). A user with three
// // tabs open has three Clients. Connections are not rooms, calls or sessions.
// type Client struct {
// 	ID     string
// 	UserID string

// 	hub     *Hub
// 	conn    *websocket.Conn
// 	token   string // session cookie value, used only for periodic re-checks
// 	send    chan []byte
// 	cancel  context.CancelFunc
// 	closing sync.Once
// }

// func newClient(h *Hub, conn *websocket.Conn, userID, token string) *Client {
// 	return &Client{
// 		ID:     newConnID(),
// 		UserID: userID,
// 		hub:    h,
// 		conn:   conn,
// 		token:  token,
// 		send:   make(chan []byte, h.cfg.SendBuffer),
// 	}
// }

// // Send queues a message for this connection. It never blocks. If the browser
// // is too slow and the buffer is full, the connection is closed (it reconnects
// // and re-syncs) instead of stalling the rest of the server.
// func (c *Client) Send(env Envelope) bool {
// 	b, err := json.Marshal(env)
// 	if err != nil {
// 		return false
// 	}
// 	return c.enqueue(b)
// }

// func (c *Client) enqueue(b []byte) bool {
// 	select {
// 	case c.send <- b:
// 		return true
// 	default:
// 		c.closeWith(websocket.StatusTryAgainLater, "slow consumer")
// 		return false
// 	}
// }

// func (c *Client) sendError(id, code, msg string) {
// 	c.Send(Envelope{Type: TypeError, ID: id, Payload: payloadOf(ErrorPayload{Code: code, Message: msg})})
// }

// // closeWith performs the WebSocket close handshake once, in the background.
// func (c *Client) closeWith(code websocket.StatusCode, reason string) {
// 	c.closing.Do(func() {
// 		go func() { _ = c.conn.Close(code, reason) }()
// 	})
// }

// // closeFromReader is closeWith for the read goroutine itself: it finishes the
// // close handshake before returning, so the deferred cleanup in run does not cut
// // the connection before the close frame is delivered.
// func (c *Client) closeFromReader(code websocket.StatusCode, reason string) {
// 	c.closing.Do(func() { _ = c.conn.Close(code, reason) })
// }

// // run owns the connection until it ends. It blocks, then cleans up.
// func (c *Client) run(parent context.Context) {
// 	ctx, cancel := context.WithCancel(parent)
// 	c.cancel = cancel
// 	defer func() {
// 		cancel()
// 		c.hub.unregister(c)
// 		_ = c.conn.CloseNow()
// 		c.hub.cfg.Logf("[realtime] disconnected conn=%s user=%s", c.ID, c.UserID)
// 	}()

// 	c.conn.SetReadLimit(c.hub.cfg.MaxMessageBytes)

// 	go c.writeLoop(ctx)
// 	go c.pingLoop(ctx)
// 	go c.reauthLoop(ctx)
// 	c.readLoop(ctx)
// }

// func (c *Client) readLoop(ctx context.Context) {
// 	cfg := c.hub.cfg
// 	bucket := newBucket(cfg.RatePerSecond, cfg.RateBurst)
// 	for {
// 		typ, data, err := c.conn.Read(ctx)
// 		if err != nil {
// 			return // closed by peer, by us, or by ctx
// 		}
// 		if typ != websocket.MessageText {
// 			c.closeFromReader(websocket.StatusUnsupportedData, "text messages only")
// 			return
// 		}
// 		if !bucket.allow() {
// 			c.closeFromReader(websocket.StatusPolicyViolation, "rate limit exceeded")
// 			return
// 		}
// 		var env Envelope
// 		if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
// 			c.sendError("", CodeBadMessage, `messages look like {"type":"…","payload":{…}}`)
// 			continue
// 		}
// 		c.hub.dispatch(ctx, c, env)
// 	}
// }

// func (c *Client) writeLoop(ctx context.Context) {
// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		case b := <-c.send:
// 			wctx, cancel := context.WithTimeout(ctx, c.hub.cfg.WriteTimeout)
// 			err := c.conn.Write(wctx, websocket.MessageText, b)
// 			cancel()
// 			if err != nil {
// 				c.cancel()
// 				return
// 			}
// 		}
// 	}
// }

// // pingLoop sends protocol-level pings. A missed pong means the peer is gone
// // (laptop lid closed, network dropped) and the connection is cleaned up.
// func (c *Client) pingLoop(ctx context.Context) {
// 	t := time.NewTicker(c.hub.cfg.PingEvery)
// 	defer t.Stop()
// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		case <-t.C:
// 			pctx, cancel := context.WithTimeout(ctx, c.hub.cfg.PongTimeout)
// 			err := c.conn.Ping(pctx)
// 			cancel()
// 			if err != nil {
// 				if ctx.Err() == nil {
// 					c.hub.cfg.Logf("[realtime] ping failed conn=%s: %v", c.ID, err)
// 					c.cancel()
// 				}
// 				return
// 			}
// 		}
// 	}
// }

// // reauthLoop makes sure a logged-out or expired session cannot keep a socket.
// func (c *Client) reauthLoop(ctx context.Context) {
// 	cfg := c.hub.cfg
// 	if cfg.Reauth == nil {
// 		return
// 	}
// 	t := time.NewTicker(cfg.ReauthEvery)
// 	defer t.Stop()
// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		case <-t.C:
// 			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
// 			uid, err := cfg.Reauth(rctx, c.token)
// 			cancel()
// 			switch {
// 			case err == nil && uid == c.UserID:
// 			case err == nil, errors.Is(err, ErrSessionInvalid):
// 				c.closeWith(websocket.StatusPolicyViolation, "session ended")
// 				return
// 			default:
// 				cfg.Logf("[realtime] reauth check failed (keeping conn %s): %v", c.ID, err)
// 			}
// 		}
// 	}
// }

// // bucket is a small token-bucket rate limiter (single goroutine use).
// type bucket struct {
// 	rate, burst, tokens float64
// 	last                time.Time
// }

// func newBucket(rate, burst float64) *bucket {
// 	return &bucket{rate: rate, burst: burst, tokens: burst, last: time.Now()}
// }

// func (b *bucket) allow() bool {
// 	now := time.Now()
// 	b.tokens += now.Sub(b.last).Seconds() * b.rate
// 	if b.tokens > b.burst {
// 		b.tokens = b.burst
// 	}
// 	b.last = now
// 	if b.tokens < 1 {
// 		return false
// 	}
// 	b.tokens--
// 	return true
// }
