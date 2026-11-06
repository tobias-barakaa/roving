// internal/chess/websocket.go
package chess

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsEvent is the JSON envelope pushed to a game's connections. The client
// switches on "type"; every state-changing event also carries the full
// authoritative state, so both players stay in lock-step.
type wsEvent struct {
	Type         string             `json:"type"`
	State        *GameStateResponse `json:"state,omitempty"`
	Move         *MoveResponse      `json:"move,omitempty"`
	UserID       string             `json:"userId,omitempty"`
	Color        string             `json:"color,omitempty"`
	Result       string             `json:"result,omitempty"`
	ResultMethod string             `json:"resultMethod,omitempty"`
}

// Publisher is the seam the service uses to fan out events after a commit. The
// hub implements it; the service treats it as best-effort (nil-safe).
type Publisher interface {
	Broadcast(gameID string, ev wsEvent)
}

const (
	sendBuffer      = 256
	maxMessageBytes = 32 * 1024
	writeWait       = 10 * time.Second
	pongWait        = 60 * time.Second
	pingEvery       = 30 * time.Second
)

// Hub maps game ids to the connected clients of their two players. A game has
// at most two players, so the whole thing is guarded by one mutex.
type Hub struct {
	mu     sync.Mutex
	rooms  map[string]map[string]*client // gameID -> userID -> client
	closed bool
}

func NewHub() *Hub {
	return &Hub{rooms: map[string]map[string]*client{}}
}

// Broadcast sends an event to every connected client of a game. It never
// blocks; a client whose buffer is full is dropped rather than stalling others.
func (h *Hub) Broadcast(gameID string, ev wsEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		log.Printf("chess: marshal %s: %v", ev.Type, err)
		return
	}
	h.mu.Lock()
	var slow []*client
	for _, cl := range h.rooms[gameID] {
		select {
		case cl.send <- b:
		default:
			slow = append(slow, cl)
		}
	}
	h.mu.Unlock()
	for _, cl := range slow {
		cl.closeConn(websocket.ClosePolicyViolation, "slow consumer")
	}
}

func (h *Hub) broadcastExcept(gameID string, ev wsEvent, exceptUser string) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	var slow []*client
	for uid, cl := range h.rooms[gameID] {
		if uid == exceptUser {
			continue
		}
		select {
		case cl.send <- b:
		default:
			slow = append(slow, cl)
		}
	}
	h.mu.Unlock()
	for _, cl := range slow {
		cl.closeConn(websocket.ClosePolicyViolation, "slow consumer")
	}
}

// register adds a connection (replacing any previous one for the same user),
// sends the initial state and notifies the opponent. It returns nil when the
// hub is shutting down; the caller then closes the connection.
func (h *Hub) register(userID, gameID string, conn *websocket.Conn, state *GameStateResponse) *client {
	cl := &client{
		hub:    h,
		conn:   conn,
		userID: userID,
		gameID: gameID,
		send:   make(chan []byte, sendBuffer),
		done:   make(chan struct{}),
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	room := h.rooms[gameID]
	if room == nil {
		room = map[string]*client{}
		h.rooms[gameID] = room
	}
	var old *client
	if room[userID] != nil {
		old = room[userID]
		delete(room, userID)
	}
	room[userID] = cl
	h.mu.Unlock()

	if old != nil {
		old.closeConn(websocket.CloseGoingAway, "replaced by a new connection")
	}

	go cl.writePump()

	cl.sendJSON(wsEvent{Type: "game.state", State: state})
	h.broadcastExcept(gameID, wsEvent{Type: "opponent.connected", UserID: userID}, userID)
	return cl
}

func (h *Hub) unregister(cl *client) {
	h.mu.Lock()
	room := h.rooms[cl.gameID]
	if room != nil && room[cl.userID] == cl {
		delete(room, cl.userID)
		if len(room) == 0 {
			delete(h.rooms, cl.gameID)
		}
	}
	h.mu.Unlock()

	cl.markDone()
	h.Broadcast(cl.gameID, wsEvent{Type: "opponent.disconnected", UserID: cl.userID})
}

// Shutdown closes every connection; call before http.Server.Shutdown, which
// does not track hijacked (WebSocket) connections.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	h.closed = true
	var all []*client
	for _, room := range h.rooms {
		for _, cl := range room {
			all = append(all, cl)
		}
	}
	h.mu.Unlock()
	for _, cl := range all {
		cl.closeConn(websocket.CloseGoingAway, "server shutting down")
	}
}

// client is one WebSocket connection of one user to one game.
type client struct {
	hub    *Hub
	conn   *websocket.Conn
	userID string
	gameID string

	send chan []byte   // never closed; writePump stops on done
	done chan struct{} // closed by unregister

	doneOnce  sync.Once
	closeOnce sync.Once
}

func (c *client) markDone() { c.doneOnce.Do(func() { close(c.done) }) }

// closeConn sends a close frame and drops the connection; safe to call from
// any goroutine, any number of times.
func (c *client) closeConn(code int, reason string) {
	c.closeOnce.Do(func() {
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		_ = c.conn.Close()
	})
}

func (c *client) sendJSON(ev wsEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	default:
		c.closeConn(websocket.ClosePolicyViolation, "slow consumer")
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	defer c.conn.Close()
	for {
		select {
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// readPump runs on the HTTP handler's goroutine and returns when the connection
// ends, at which point the client is unregistered and the opponent is notified.
func (c *client) readPump() {
	defer func() {
		c.hub.unregister(c)
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))

		// Moves are submitted over HTTP; the socket is push-only except for a
		// trivial keepalive ping.
		var in struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		if in.Type == "ping" {
			c.sendJSON(wsEvent{Type: "pong"})
		}
	}
}
