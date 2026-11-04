// internal/realtime/client.go
package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingEvery  = 50 * time.Second
	maxMsgSize = 32 << 10 // SDP offers/answers are several KB
	sendBuffer = 64
)

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	auth   Authorizer
	userID string
	name   string
	send   chan []byte

	// Touched only by the hub goroutine:
	subs   map[string]struct{}
	closed bool
}

func (c *Client) readPump() {
	defer func() {
		c.hub.control(func() { c.hub.removeClient(c) })
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMsgSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	lim := rate.NewLimiter(50, 100) // ICE candidates arrive in bursts, one per peer

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if !lim.Allow() {
			_ = c.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "rate limit"),
				time.Now().Add(time.Second))
			return
		}
		var m ClientMsg
		if json.Unmarshal(raw, &m) != nil {
			c.reply("error", "malformed message")
			continue
		}
		c.handle(m)
	}
}

func (c *Client) handle(m ClientMsg) {
	switch m.Type {
	case "subscribe", "unsubscribe", "typing":
		key, groupID, ok := resolve(m)
		if !ok {
			c.reply("error", "invalid groupId/roomId")
			return
		}
		switch m.Type {
		case "subscribe":
			// DB check happens here, on the client's own goroutine, never in the hub.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.auth.CanAccess(ctx, c.userID, groupID, m.RoomID)
			cancel()
			if err != nil {
				c.reply("error", "cannot subscribe to that channel") // same answer for 404/403
				return
			}
			c.hub.control(func() { c.hub.join(c, key, groupID, "online") })
		case "unsubscribe":
			c.hub.control(func() { c.hub.leave(c, key) })
		case "typing":
			c.hub.control(func() { c.hub.typing(c, key) })
		}
	case "call.signal":
		key, _, ok := resolve(m)
		if !ok || m.RoomID == nil || !uuidRe.MatchString(m.To) || !validSignal[m.Kind] || len(m.Data) == 0 {
			c.reply("error", "invalid call signal")
			return
		}
		// Pure in-memory check + relay; no DB on the hot path.
		c.hub.control(func() { c.hub.relaySignal(c, key, m) })
	case "presence.set":
		if !validStatus[m.Status] {
			c.reply("error", "invalid status")
			return
		}
		c.hub.control(func() { c.hub.setStatus(c, m.Status) })
	default:
		c.reply("error", "unknown message type")
	}
}

func resolve(m ClientMsg) (key, groupID string, ok bool) {
	if !uuidRe.MatchString(m.GroupID) {
		return "", "", false
	}
	groupID = strings.ToLower(m.GroupID)
	if m.RoomID == nil || *m.RoomID == "" {
		return GroupTopic(groupID), groupID, true
	}
	if !uuidRe.MatchString(*m.RoomID) {
		return "", "", false
	}
	return RoomTopic(*m.RoomID), groupID, true
}

func (c *Client) reply(typ, msg string) {
	b, _ := json.Marshal(ServerMsg{Type: typ, Data: map[string]string{"message": msg}})
	c.hub.try(func() { c.hub.deliver(c, b) })
}

func (c *Client) writePump() {
	t := time.NewTicker(pingEvery)
	defer func() { t.Stop(); c.conn.Close() }()
	for {
		select {
		case b, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok { // hub closed us
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-t.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}