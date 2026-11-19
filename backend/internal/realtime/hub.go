package realtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Config tunes the hub. Zero values are replaced by the defaults noted below.
type Config struct {
	MaxConnsPerUser int           // open sockets per user (tabs/devices). Default 8.
	MaxMessageBytes int64         // largest accepted message. Default 64 KiB.
	SendBuffer      int           // queued outgoing messages per connection. Default 64.
	PingEvery       time.Duration // protocol-level ping. Keeps proxies and Cloudflare from idling the socket out. Default 25s.
	PongTimeout     time.Duration // how long to wait for the pong. Default 10s.
	WriteTimeout    time.Duration // per-message write deadline. Default 10s.
	RatePerSecond   float64       // sustained incoming messages per second per connection. Default 20.
	RateBurst       float64       // short burst allowance. Default 40.

	// Reauth re-checks the session behind a connection every ReauthEvery
	// (default 2m). It receives the session cookie value captured at connect
	// time and returns the user it belongs to. Return ErrSessionInvalid when
	// the session is gone. Nil disables re-checking.
	Reauth      func(ctx context.Context, sessionToken string) (userID string, err error)
	ReauthEvery time.Duration

	// Logf receives lifecycle logs. Default is log.Printf.
	Logf func(format string, args ...any)
}

func (c Config) withDefaults() Config {
	if c.MaxConnsPerUser <= 0 {
		c.MaxConnsPerUser = 8
	}
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = 64 << 10
	}
	if c.SendBuffer <= 0 {
		c.SendBuffer = 64
	}
	if c.PingEvery <= 0 {
		c.PingEvery = 25 * time.Second
	}
	if c.PongTimeout <= 0 {
		c.PongTimeout = 10 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.RatePerSecond <= 0 {
		c.RatePerSecond = 20
	}
	if c.RateBurst <= 0 {
		c.RateBurst = 40
	}
	if c.ReauthEvery <= 0 {
		c.ReauthEvery = 2 * time.Minute
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return c
}

// HandlerFunc handles one incoming message. It runs on the connection's read
// goroutine, so it must be quick: do slow work in another goroutine.
type HandlerFunc func(ctx context.Context, c *Client, env Envelope)

var (
	errHubClosed   = errors.New("realtime: hub is shutting down")
	errTooManyConn = errors.New("realtime: too many open connections for this user")
)

// Stats is a snapshot for health checks and tests.
type Stats struct {
	Connections int `json:"connections"`
	Users       int `json:"users"`
}

// Hub is the registry of live connections and the message router.
// It is safe for concurrent use.
type Hub struct {
	cfg Config

	mu       sync.RWMutex
	closed   bool
	byID     map[string]*Client
	byUser   map[string]map[string]*Client
	handlers map[string]HandlerFunc

	wg sync.WaitGroup // one per registered connection
}

func NewHub(cfg Config) *Hub {
	h := &Hub{
		cfg:      cfg.withDefaults(),
		byID:     make(map[string]*Client),
		byUser:   make(map[string]map[string]*Client),
		handlers: make(map[string]HandlerFunc),
	}
	h.registerBuiltins()
	return h
}

// Handle registers the handler for a message type. Later stages call this to
// add "room.join", "rtc.offer" and so on. Registering the same type twice
// replaces the earlier handler.
func (h *Hub) Handle(msgType string, fn HandlerFunc) {
	h.mu.Lock()
	h.handlers[msgType] = fn
	h.mu.Unlock()
}

func (h *Hub) register(c *Client) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errHubClosed
	}
	if len(h.byUser[c.UserID]) >= h.cfg.MaxConnsPerUser {
		return errTooManyConn
	}
	h.byID[c.ID] = c
	if h.byUser[c.UserID] == nil {
		h.byUser[c.UserID] = make(map[string]*Client)
	}
	h.byUser[c.UserID][c.ID] = c
	h.wg.Add(1)
	return nil
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	if _, ok := h.byID[c.ID]; ok {
		delete(h.byID, c.ID)
		if m := h.byUser[c.UserID]; m != nil {
			delete(m, c.ID)
			if len(m) == 0 {
				delete(h.byUser, c.UserID)
			}
		}
		h.wg.Done()
	}
	h.mu.Unlock()
}

func (h *Hub) isClosed() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.closed
}

// UserConnections returns how many sockets the user has open right now.
func (h *Hub) UserConnections(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byUser[userID])
}

func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return Stats{Connections: len(h.byID), Users: len(h.byUser)}
}

// SendToUser delivers env to every open connection of the user, except the one
// whose ID is exceptConnID (pass "" to include all). It returns how many
// connections accepted the message. Delivery is best effort: a full send
// buffer drops that connection instead of blocking the caller.
func (h *Hub) SendToUser(userID string, env Envelope, exceptConnID string) int {
	b, err := json.Marshal(env)
	if err != nil {
		return 0
	}
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.byUser[userID]))
	for id, c := range h.byUser[userID] {
		if id != exceptConnID {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	n := 0
	for _, c := range targets {
		if c.enqueue(b) {
			n++
		}
	}
	return n
}

// DisconnectUser closes every connection of the user. Call it when a user logs
// out, is removed, or is banned, so their sockets don't linger.
func (h *Hub) DisconnectUser(userID string, code websocket.StatusCode, reason string) {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.byUser[userID]))
	for _, c := range h.byUser[userID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.closeWith(code, reason)
	}
}

// Shutdown stops accepting connections, asks every client to reconnect
// elsewhere (close code 1001), and waits until all of them are gone or ctx ends.
func (h *Hub) Shutdown(ctx context.Context) {
	h.mu.Lock()
	h.closed = true
	targets := make([]*Client, 0, len(h.byID))
	for _, c := range h.byID {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	for _, c := range targets {
		c.closeWith(websocket.StatusGoingAway, "server restarting")
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		h.cfg.Logf("[realtime] shutdown: gave up waiting for %d connection(s)", h.Stats().Connections)
	}
}

// dispatch routes one decoded message to its handler.
func (h *Hub) dispatch(ctx context.Context, c *Client, env Envelope) {
	h.mu.RLock()
	fn := h.handlers[env.Type]
	h.mu.RUnlock()
	if fn == nil {
		c.sendError(env.ID, CodeUnknownType, "unknown message type: "+truncate(env.Type, 40))
		return
	}
	defer func() {
		if r := recover(); r != nil {
			h.cfg.Logf("[realtime] handler %q panicked (conn %s): %v", env.Type, c.ID, r)
			c.sendError(env.ID, CodeInternal, "something went wrong")
		}
	}()
	fn(ctx, c, env)
}

func newConnID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "c_" + hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
