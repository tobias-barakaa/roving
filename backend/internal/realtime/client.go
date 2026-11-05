// internal/realtime/client.go
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"roving/backend/internal/chat"
)

const (
	// A call burst (an offer plus a trickle of ICE candidates to each of up to
	// 7 peers) needs room in both directions, hence the larger limits.
	sendBuffer      = 256
	maxMessageBytes = 32 * 1024 // must exceed maxSignalBytes plus envelope
	writeWait       = 10 * time.Second
	pongWait        = 60 * time.Second
	pingEvery       = 30 * time.Second

	rateWindow  = 10 * time.Second
	rateMax     = 400 // inbound frames per window before the client is dropped
	typingEvery = time.Second
)

// Client is one WebSocket connection of one user.
type Client struct {
	hub         *Hub
	conn        *websocket.Conn
	userID      string
	displayName string

	send chan []byte   // never closed; writePump stops on done
	done chan struct{} // closed by unregister

	doneOnce  sync.Once
	closeOnce sync.Once

	subs  map[string]struct{} // channel keys; guarded by hub.mu
	calls map[string]struct{} // channel keys of calls joined; guarded by hub.callMu

	// readPump-only state (no locking needed).
	typingAt    map[string]time.Time
	windowStart time.Time
	windowCount int
}

func newClient(h *Hub, conn *websocket.Conn, userID, displayName string) *Client {
	return &Client{
		hub:         h,
		conn:        conn,
		userID:      userID,
		displayName: displayName,
		send:        make(chan []byte, sendBuffer),
		done:        make(chan struct{}),
		subs:        make(map[string]struct{}),
		calls:       make(map[string]struct{}),
		typingAt:    make(map[string]time.Time),
		windowStart: time.Now(),
	}
}

func (c *Client) markDone() { c.doneOnce.Do(func() { close(c.done) }) }

// closeConn sends a close frame and drops the connection; safe to call from
// any goroutine, any number of times.
func (c *Client) closeConn(code int, reason string) {
	c.closeOnce.Do(func() {
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		_ = c.conn.Close()
	})
}

// sendJSON queues a message without blocking; a full buffer means the client
// cannot keep up and it is dropped.
func (c *Client) sendJSON(v outbound) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("realtime: marshal %s: %v", v.Type, err)
		return
	}
	select {
	case c.send <- b:
	default:
		c.closeConn(websocket.ClosePolicyViolation, "slow consumer")
	}
}

func (c *Client) sendError(ref, code, msg string) {
	c.sendJSON(outbound{Type: "error", Ref: ref, Code: code, Error: msg})
}

// ==================== pumps ====================

func (c *Client) writePump() {
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

// readPump runs on the HTTP handler's goroutine and returns when the
// connection ends, at which point everything the client held is released.
func (c *Client) readPump() {
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

		if !c.allow() {
			c.closeConn(websocket.ClosePolicyViolation, "rate limit exceeded")
			return
		}
		var in inbound
		if err := json.Unmarshal(data, &in); err != nil {
			c.sendError("", "bad_json", "frame is not valid JSON")
			continue
		}
		c.handle(in)
	}
}

func (c *Client) allow() bool {
	now := time.Now()
	if now.Sub(c.windowStart) > rateWindow {
		c.windowStart, c.windowCount = now, 0
	}
	c.windowCount++
	return c.windowCount <= rateMax
}

// ==================== inbound messages ====================

func (c *Client) handle(in inbound) {
	switch in.Type {
	case "ping":
		c.sendJSON(outbound{Type: "pong", Ref: in.Ref})

	case "subscribe":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		if !c.authorize(in, ref) {
			return
		}
		presence, err := c.hub.subscribe(c, ref)
		if err != nil {
			c.sendError(in.Ref, "limit", err.Error())
			return
		}
		r := ref
		c.sendJSON(outbound{Type: "subscribed", Ref: in.Ref, Channel: &r, Presence: presence})

	case "unsubscribe":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		c.hub.unsubscribe(c, ref.key(), "")

	case "typing":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		key := ref.key()
		if !c.hub.isSubscribed(c, key) {
			c.sendError(in.Ref, "not_subscribed", "subscribe to the channel first")
			return
		}
		if now := time.Now(); now.Sub(c.typingAt[key]) >= typingEvery {
			c.typingAt[key] = now
			r := ref
			c.hub.broadcastJSON(key, outbound{
				Type: "typing", Channel: &r, UserID: c.userID, DisplayName: c.displayName,
			}, c.userID)
		}

	case "status":
		if !validStatus(in.Status) {
			c.sendError(in.Ref, "bad_status", "status must be available, focusing or away")
			return
		}
		c.hub.setStatus(c, in.Status)

	case "call.join":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		audio := in.Audio == nil || *in.Audio // microphone on unless told otherwise
		video := in.Video != nil && *in.Video // camera off unless asked for
		parts, fresh, err := c.hub.joinCall(c, ref, audio, video)
		if err != nil {
			c.sendCallError(in.Ref, err)
			return
		}
		r := ref
		c.sendJSON(outbound{
			Type: "call.state", Ref: in.Ref, Channel: &r,
			Participants: parts, MaxParticipants: MaxCallParticipants,
		})
		if fresh {
			c.hub.announceJoin(c, ref, audio, video)
		}

	case "call.leave":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		c.hub.leaveCall(c, ref.key())

	case "call.signal":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		if err := c.hub.relaySignal(c, ref, in.To, in.Kind, in.Data); err != nil {
			c.sendCallError(in.Ref, err)
		}

	case "call.media":
		ref, ok := c.channelFrom(in)
		if !ok {
			return
		}
		if in.Audio == nil && in.Video == nil {
			c.sendError(in.Ref, "bad_media", "send audio and/or video")
			return
		}
		if err := c.hub.setMedia(c, ref, in.Audio, in.Video); err != nil {
			c.sendCallError(in.Ref, err)
		}

	default:
		c.sendError(in.Ref, "unknown_type", "unknown message type")
	}
}

func (c *Client) sendCallError(ref string, err error) {
	var ce *callError
	if errors.As(err, &ce) {
		c.sendError(ref, ce.code, ce.msg)
		return
	}
	log.Printf("realtime: call: %v", err)
	c.sendError(ref, "internal", "something went wrong")
}

func (c *Client) channelFrom(in inbound) (ChannelRef, bool) {
	if in.Channel == nil {
		c.sendError(in.Ref, "bad_channel", "channel is required")
		return ChannelRef{}, false
	}
	ref, ok := in.Channel.normalize()
	if !ok {
		c.sendError(in.Ref, "bad_channel", "malformed channel")
		return ChannelRef{}, false
	}
	return ref, true
}

// authorize applies the shared chat access rules. Not-a-member and
// hidden-room both answer "forbidden" so channels cannot be probed.
func (c *Client) authorize(in inbound, ref ChannelRef) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.hub.auth.CanAccessChannel(ctx, c.userID, ref.GroupID, ref.RoomID)
	switch {
	case err == nil:
		return true
	case errors.Is(err, chat.ErrNotFound), errors.Is(err, chat.ErrForbidden):
		c.sendError(in.Ref, "forbidden", "you cannot access this channel")
	case errors.Is(err, chat.ErrInvalid):
		c.sendError(in.Ref, "bad_channel", "malformed channel")
	default:
		log.Printf("realtime: authorize: %v", err)
		c.sendError(in.Ref, "internal", "something went wrong")
	}
	return false
}
// // internal/realtime/client.go
// package realtime

// import (
// 	"context"
// 	"encoding/json"
// 	"errors"
// 	"log"
// 	"sync"
// 	"time"

// 	"github.com/gorilla/websocket"

// 	"roving/backend/internal/chat"
// )

// const (
// 	sendBuffer      = 64
// 	maxMessageBytes = 4096
// 	writeWait       = 10 * time.Second
// 	pongWait        = 60 * time.Second
// 	pingEvery       = 30 * time.Second

// 	rateWindow  = 10 * time.Second
// 	rateMax     = 60 // inbound frames per window before the client is dropped
// 	typingEvery = time.Second
// )

// // Client is one WebSocket connection of one user.
// type Client struct {
// 	hub         *Hub
// 	conn        *websocket.Conn
// 	userID      string
// 	displayName string

// 	send chan []byte   // never closed; writePump stops on done
// 	done chan struct{} // closed by unregister

// 	doneOnce  sync.Once
// 	closeOnce sync.Once

// 	subs map[string]struct{} // channel keys; guarded by hub.mu

// 	// readPump-only state (no locking needed).
// 	typingAt    map[string]time.Time
// 	windowStart time.Time
// 	windowCount int
// }

// func newClient(h *Hub, conn *websocket.Conn, userID, displayName string) *Client {
// 	return &Client{
// 		hub:         h,
// 		conn:        conn,
// 		userID:      userID,
// 		displayName: displayName,
// 		send:        make(chan []byte, sendBuffer),
// 		done:        make(chan struct{}),
// 		subs:        make(map[string]struct{}),
// 		typingAt:    make(map[string]time.Time),
// 		windowStart: time.Now(),
// 	}
// }

// func (c *Client) markDone() { c.doneOnce.Do(func() { close(c.done) }) }

// // closeConn sends a close frame and drops the connection; safe to call from
// // any goroutine, any number of times.
// func (c *Client) closeConn(code int, reason string) {
// 	c.closeOnce.Do(func() {
// 		_ = c.conn.WriteControl(websocket.CloseMessage,
// 			websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
// 		_ = c.conn.Close()
// 	})
// }

// // sendJSON queues a message without blocking; a full buffer means the client
// // cannot keep up and it is dropped.
// func (c *Client) sendJSON(v outbound) {
// 	b, err := json.Marshal(v)
// 	if err != nil {
// 		log.Printf("realtime: marshal %s: %v", v.Type, err)
// 		return
// 	}
// 	select {
// 	case c.send <- b:
// 	default:
// 		c.closeConn(websocket.ClosePolicyViolation, "slow consumer")
// 	}
// }

// func (c *Client) sendError(ref, code, msg string) {
// 	c.sendJSON(outbound{Type: "error", Ref: ref, Code: code, Error: msg})
// }

// // ==================== pumps ====================

// func (c *Client) writePump() {
// 	ticker := time.NewTicker(pingEvery)
// 	defer ticker.Stop()
// 	defer c.conn.Close()
// 	for {
// 		select {
// 		case msg := <-c.send:
// 			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
// 			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
// 				return
// 			}
// 		case <-ticker.C:
// 			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
// 			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
// 				return
// 			}
// 		case <-c.done:
// 			return
// 		}
// 	}
// }

// // readPump runs on the HTTP handler's goroutine and returns when the
// // connection ends, at which point everything the client held is released.
// func (c *Client) readPump() {
// 	defer func() {
// 		c.hub.unregister(c)
// 		_ = c.conn.Close()
// 	}()

// 	c.conn.SetReadLimit(maxMessageBytes)
// 	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
// 	c.conn.SetPongHandler(func(string) error {
// 		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
// 	})

// 	for {
// 		_, data, err := c.conn.ReadMessage()
// 		if err != nil {
// 			return
// 		}
// 		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))

// 		if !c.allow() {
// 			c.closeConn(websocket.ClosePolicyViolation, "rate limit exceeded")
// 			return
// 		}
// 		var in inbound
// 		if err := json.Unmarshal(data, &in); err != nil {
// 			c.sendError("", "bad_json", "frame is not valid JSON")
// 			continue
// 		}
// 		c.handle(in)
// 	}
// }

// func (c *Client) allow() bool {
// 	now := time.Now()
// 	if now.Sub(c.windowStart) > rateWindow {
// 		c.windowStart, c.windowCount = now, 0
// 	}
// 	c.windowCount++
// 	return c.windowCount <= rateMax
// }

// // ==================== inbound messages ====================

// func (c *Client) handle(in inbound) {
// 	switch in.Type {
// 	case "ping":
// 		c.sendJSON(outbound{Type: "pong", Ref: in.Ref})

// 	case "subscribe":
// 		ref, ok := c.channelFrom(in)
// 		if !ok {
// 			return
// 		}
// 		if !c.authorize(in, ref) {
// 			return
// 		}
// 		presence, err := c.hub.subscribe(c, ref)
// 		if err != nil {
// 			c.sendError(in.Ref, "limit", err.Error())
// 			return
// 		}
// 		r := ref
// 		c.sendJSON(outbound{Type: "subscribed", Ref: in.Ref, Channel: &r, Presence: presence})

// 	case "unsubscribe":
// 		ref, ok := c.channelFrom(in)
// 		if !ok {
// 			return
// 		}
// 		c.hub.unsubscribe(c, ref.key(), "")

// 	case "typing":
// 		ref, ok := c.channelFrom(in)
// 		if !ok {
// 			return
// 		}
// 		key := ref.key()
// 		if !c.hub.isSubscribed(c, key) {
// 			c.sendError(in.Ref, "not_subscribed", "subscribe to the channel first")
// 			return
// 		}
// 		if now := time.Now(); now.Sub(c.typingAt[key]) >= typingEvery {
// 			c.typingAt[key] = now
// 			r := ref
// 			c.hub.broadcastJSON(key, outbound{
// 				Type: "typing", Channel: &r, UserID: c.userID, DisplayName: c.displayName,
// 			}, c.userID)
// 		}

// 	case "status":
// 		if !validStatus(in.Status) {
// 			c.sendError(in.Ref, "bad_status", "status must be available, focusing or away")
// 			return
// 		}
// 		c.hub.setStatus(c, in.Status)

// 	default:
// 		c.sendError(in.Ref, "unknown_type", "unknown message type")
// 	}
// }

// func (c *Client) channelFrom(in inbound) (ChannelRef, bool) {
// 	if in.Channel == nil {
// 		c.sendError(in.Ref, "bad_channel", "channel is required")
// 		return ChannelRef{}, false
// 	}
// 	ref, ok := in.Channel.normalize()
// 	if !ok {
// 		c.sendError(in.Ref, "bad_channel", "malformed channel")
// 		return ChannelRef{}, false
// 	}
// 	return ref, true
// }

// // authorize applies the shared chat access rules. Not-a-member and
// // hidden-room both answer "forbidden" so channels cannot be probed.
// func (c *Client) authorize(in inbound, ref ChannelRef) bool {
// 	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 	defer cancel()
// 	err := c.hub.auth.CanAccessChannel(ctx, c.userID, ref.GroupID, ref.RoomID)
// 	switch {
// 	case err == nil:
// 		return true
// 	case errors.Is(err, chat.ErrNotFound), errors.Is(err, chat.ErrForbidden):
// 		c.sendError(in.Ref, "forbidden", "you cannot access this channel")
// 	case errors.Is(err, chat.ErrInvalid):
// 		c.sendError(in.Ref, "bad_channel", "malformed channel")
// 	default:
// 		log.Printf("realtime: authorize: %v", err)
// 		c.sendError(in.Ref, "internal", "something went wrong")
// 	}
// 	return false
// }

// // // internal/realtime/client.go
// // package realtime

// // import (
// // 	"context"
// // 	"encoding/json"
// // 	"strings"
// // 	"time"

// // 	"github.com/gorilla/websocket"
// // 	"golang.org/x/time/rate"
// // )

// // const (
// // 	writeWait  = 10 * time.Second
// // 	pongWait   = 60 * time.Second
// // 	pingEvery  = 50 * time.Second
// // 	maxMsgSize = 32 << 10 // SDP offers/answers are several KB
// // 	sendBuffer = 64
// // )

// // type Client struct {
// // 	hub    *Hub
// // 	conn   *websocket.Conn
// // 	auth   Authorizer
// // 	userID string
// // 	name   string
// // 	send   chan []byte

// // 	// Touched only by the hub goroutine:
// // 	subs   map[string]struct{}
// // 	closed bool
// // }

// // func (c *Client) readPump() {
// // 	defer func() {
// // 		c.hub.control(func() { c.hub.removeClient(c) })
// // 		c.conn.Close()
// // 	}()
// // 	c.conn.SetReadLimit(maxMsgSize)
// // 	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
// // 	c.conn.SetPongHandler(func(string) error {
// // 		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
// // 	})
// // 	lim := rate.NewLimiter(50, 100) // ICE candidates arrive in bursts, one per peer

// // 	for {
// // 		_, raw, err := c.conn.ReadMessage()
// // 		if err != nil {
// // 			return
// // 		}
// // 		if !lim.Allow() {
// // 			_ = c.conn.WriteControl(websocket.CloseMessage,
// // 				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "rate limit"),
// // 				time.Now().Add(time.Second))
// // 			return
// // 		}
// // 		var m ClientMsg
// // 		if json.Unmarshal(raw, &m) != nil {
// // 			c.reply("error", "malformed message")
// // 			continue
// // 		}
// // 		c.handle(m)
// // 	}
// // }

// // func (c *Client) handle(m ClientMsg) {
// // 	switch m.Type {
// // 	case "subscribe", "unsubscribe", "typing":
// // 		key, groupID, ok := resolve(m)
// // 		if !ok {
// // 			c.reply("error", "invalid groupId/roomId")
// // 			return
// // 		}
// // 		switch m.Type {
// // 		case "subscribe":
// // 			// DB check happens here, on the client's own goroutine, never in the hub.
// // 			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// // 			err := c.auth.CanAccess(ctx, c.userID, groupID, m.RoomID)
// // 			cancel()
// // 			if err != nil {
// // 				c.reply("error", "cannot subscribe to that channel") // same answer for 404/403
// // 				return
// // 			}
// // 			c.hub.control(func() { c.hub.join(c, key, groupID, "online") })
// // 		case "unsubscribe":
// // 			c.hub.control(func() { c.hub.leave(c, key) })
// // 		case "typing":
// // 			c.hub.control(func() { c.hub.typing(c, key) })
// // 		}
// // 	case "call.signal":
// // 		key, _, ok := resolve(m)
// // 		if !ok || m.RoomID == nil || !uuidRe.MatchString(m.To) || !validSignal[m.Kind] || len(m.Data) == 0 {
// // 			c.reply("error", "invalid call signal")
// // 			return
// // 		}
// // 		// Pure in-memory check + relay; no DB on the hot path.
// // 		c.hub.control(func() { c.hub.relaySignal(c, key, m) })
// // 	case "presence.set":
// // 		if !validStatus[m.Status] {
// // 			c.reply("error", "invalid status")
// // 			return
// // 		}
// // 		c.hub.control(func() { c.hub.setStatus(c, m.Status) })
// // 	default:
// // 		c.reply("error", "unknown message type")
// // 	}
// // }

// // func resolve(m ClientMsg) (key, groupID string, ok bool) {
// // 	if !uuidRe.MatchString(m.GroupID) {
// // 		return "", "", false
// // 	}
// // 	groupID = strings.ToLower(m.GroupID)
// // 	if m.RoomID == nil || *m.RoomID == "" {
// // 		return GroupTopic(groupID), groupID, true
// // 	}
// // 	if !uuidRe.MatchString(*m.RoomID) {
// // 		return "", "", false
// // 	}
// // 	return RoomTopic(*m.RoomID), groupID, true
// // }

// // func (c *Client) reply(typ, msg string) {
// // 	b, _ := json.Marshal(ServerMsg{Type: typ, Data: map[string]string{"message": msg}})
// // 	c.hub.try(func() { c.hub.deliver(c, b) })
// // }

// // func (c *Client) writePump() {
// // 	t := time.NewTicker(pingEvery)
// // 	defer func() { t.Stop(); c.conn.Close() }()
// // 	for {
// // 		select {
// // 		case b, ok := <-c.send:
// // 			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
// // 			if !ok { // hub closed us
// // 				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
// // 				return
// // 			}
// // 			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
// // 				return
// // 			}
// // 		case <-t.C:
// // 			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
// // 			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
// // 				return
// // 			}
// // 		}
// // 	}
// // }