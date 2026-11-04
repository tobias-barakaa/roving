// internal/realtime/hub.go
package realtime

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// Hub owns ALL mutable state (topics, members, subscriptions) in one goroutine.
// Everything else talks to it by posting closures, so there are no locks.
// Anything slow (DB checks) happens OUTSIDE the hub goroutine.
type Hub struct {
	cmds   chan func()
	done   <-chan struct{}
	grace  time.Duration
	topics map[string]*topic

	// calls: room topic key -> users currently in that room's call. Written by
	// the calls module (SetInCall); read to authorise signal relaying.
	calls map[string]map[string]struct{}

	// onGone fires (in its own goroutine) when a user has been absent from a
	// topic for the whole grace period.
	onGone func(topicKey, userID string)
}

type topic struct {
	groupID string
	clients map[*Client]struct{}
	members map[string]*member // by userID; one entry however many tabs
}

type member struct {
	name   string
	status string
	conns  map[*Client]struct{}
	timer  *time.Timer // pending "left" announcement (reconnect grace)
	gen    int
}

// grace: how long a user with zero connections still counts as present, so a
// page refresh or flaky network doesn't flap join/leave events.
func NewHub(ctx context.Context, grace time.Duration) *Hub {
	h := &Hub{
		cmds:   make(chan func(), 1024),
		done:   ctx.Done(),
		grace:  grace,
		topics: make(map[string]*topic),
		calls:  make(map[string]map[string]struct{}),
	}
	go h.run()
	return h
}

func (h *Hub) run() {
	for {
		select {
		case fn := <-h.cmds:
			fn()
		case <-h.done:
			all := map[*Client]struct{}{}
			for _, t := range h.topics {
				for c := range t.clients {
					all[c] = struct{}{}
				}
			}
			for c := range all {
				h.removeClient(c)
			}
			return
		}
	}
}

// control: reliable post (blocks if the queue is full). Used for join/leave.
func (h *Hub) control(fn func()) {
	select {
	case h.cmds <- fn:
	case <-h.done:
	}
}

// try: best-effort post. Used for fan-out, so a stalled hub can never block
// an HTTP request that is publishing an event.
func (h *Hub) try(fn func()) {
	select {
	case h.cmds <- fn:
	default:
		log.Println("realtime: hub queue full, event dropped")
	}
}

// ---------- public API for other modules ----------

// Broadcast sends an arbitrary event to everyone subscribed to a topic.
func (h *Hub) Broadcast(topicKey, typ string, data any) {
	h.try(func() {
		if t := h.topics[topicKey]; t != nil {
			h.fanout(t, topicKey, typ, data, nil)
		}
	})
}

// EvictUser removes a user from every topic of a group (call it when a member
// is removed/suspended or a room membership is revoked).
func (h *Hub) EvictUser(groupID, userID string) {
	h.control(func() {
		for key, t := range h.topics {
			if t.groupID != groupID {
				continue
			}
			for c := range t.clients {
				if c.userID == userID {
					h.leave(c, key)
					h.sendTo(c, ServerMsg{Type: "evicted", Topic: key})
				}
			}
		}
	})
}

// SetLeaveHook registers a callback for "user fully left this topic". Call it
// once at startup, before serving. The calls module uses it to drop people
// from a call when their connection disappears.
func (h *Hub) SetLeaveHook(fn func(topicKey, userID string)) { h.onGone = fn }

// SetInCall marks a user as a participant of a room's call. Only these users
// may send or receive call.signal messages for that room.
func (h *Hub) SetInCall(roomID, userID string, in bool) {
	key, uid := RoomTopic(roomID), strings.ToLower(userID)
	h.control(func() {
		set := h.calls[key]
		if in {
			if set == nil {
				set = map[string]struct{}{}
				h.calls[key] = set
			}
			set[uid] = struct{}{}
			return
		}
		delete(set, uid)
		if len(set) == 0 {
			delete(h.calls, key)
		}
	})
}

// IsPresent reports whether the user currently holds a live subscription to
// the topic (synchronous round-trip through the hub goroutine).
func (h *Hub) IsPresent(topicKey, userID string) bool {
	res := make(chan bool, 1)
	h.control(func() {
		var m *member
		if t := h.topics[topicKey]; t != nil {
			m = t.members[userID]
		}
		res <- m != nil && len(m.conns) > 0
	})
	select {
	case v := <-res:
		return v
	case <-h.done:
		return false
	}
}

// ---------- internals (hub goroutine only) ----------

// relaySignal forwards one WebRTC signal (offer/answer/ICE candidate) to a
// single peer. Both ends must be in the room's call and the sender must hold
// a subscription to the room, so signals can't be aimed at outsiders.
func (h *Hub) relaySignal(c *Client, key string, m ClientMsg) {
	if _, ok := c.subs[key]; !ok {
		return
	}
	inCall := h.calls[key]
	to := strings.ToLower(m.To)
	if _, ok := inCall[c.userID]; !ok {
		return
	}
	if _, ok := inCall[to]; !ok || to == c.userID {
		return
	}
	t := h.topics[key]
	if t == nil {
		return
	}
	peer := t.members[to]
	if peer == nil {
		return
	}
	b, err := json.Marshal(ServerMsg{Type: "call.signal", Topic: key, Data: map[string]any{
		"from": c.userID, "kind": m.Kind, "data": m.Data,
	}})
	if err != nil {
		return
	}
	for pc := range peer.conns {
		h.deliver(pc, b)
	}
}


func (h *Hub) join(c *Client, key, groupID, status string) {
	if c.closed {
		return
	}
	t := h.topics[key]
	if t == nil {
		t = &topic{groupID: groupID, clients: map[*Client]struct{}{}, members: map[string]*member{}}
		h.topics[key] = t
	}
	if _, already := t.clients[c]; already {
		return
	}
	t.clients[c] = struct{}{}
	c.subs[key] = struct{}{}

	m, existed := t.members[c.userID]
	if !existed {
		m = &member{name: c.name, status: status, conns: map[*Client]struct{}{}}
		t.members[c.userID] = m
	}
	if m.timer != nil { // reconnected within grace: cancel the pending "left"
		m.timer.Stop()
		m.timer = nil
	}
	m.gen++
	m.conns[c] = struct{}{}

	snapshot := make([]PresenceUser, 0, len(t.members))
	for uid, mm := range t.members {
		snapshot = append(snapshot, PresenceUser{UserID: uid, Name: mm.name, Status: mm.status})
	}
	h.sendTo(c, ServerMsg{Type: "subscribed", Topic: key})
	h.sendTo(c, ServerMsg{Type: "presence.snapshot", Topic: key, Data: snapshot})
	if !existed {
		h.fanout(t, key, "presence.joined",
			PresenceUser{UserID: c.userID, Name: m.name, Status: m.status}, c)
	}
}

func (h *Hub) leave(c *Client, key string) {
	t := h.topics[key]
	delete(c.subs, key)
	if t == nil {
		return
	}
	delete(t.clients, c)
	m := t.members[c.userID]
	if m == nil {
		return
	}
	delete(m.conns, c)
	if len(m.conns) > 0 {
		return // still connected from another tab/device
	}
	m.gen++
	gen, uid := m.gen, c.userID
	m.timer = time.AfterFunc(h.grace, func() {
		h.control(func() { h.expire(key, uid, gen) })
	})
}

func (h *Hub) expire(key, userID string, gen int) {
	t := h.topics[key]
	if t == nil {
		return
	}
	m := t.members[userID]
	if m == nil || m.gen != gen || len(m.conns) > 0 {
		return // user came back
	}
	delete(t.members, userID)
	h.fanout(t, key, "presence.left", PresenceUser{UserID: userID, Name: m.name}, nil)
	if h.onGone != nil {
		go h.onGone(key, userID)
	}
	if len(t.clients) == 0 && len(t.members) == 0 {
		delete(h.topics, key)
	}
}

func (h *Hub) setStatus(c *Client, status string) {
	for key := range c.subs {
		t := h.topics[key]
		if t == nil {
			continue
		}
		if m := t.members[c.userID]; m != nil {
			m.status = status
			h.fanout(t, key, "presence.updated",
				PresenceUser{UserID: c.userID, Name: m.name, Status: status}, nil)
		}
	}
}

func (h *Hub) typing(c *Client, key string) {
	if _, ok := c.subs[key]; !ok {
		return // can only type in channels you're subscribed to (and were authorized for)
	}
	if t := h.topics[key]; t != nil {
		h.fanout(t, key, "typing", map[string]string{"userId": c.userID, "name": c.name}, c)
	}
}

func (h *Hub) removeClient(c *Client) {
	if c.closed {
		return
	}
	c.closed = true
	for key := range c.subs {
		h.leave(c, key)
	}
	close(c.send) // only ever closed here, so no send-on-closed panics
}

func (h *Hub) fanout(t *topic, key, typ string, data any, except *Client) {
	b, err := json.Marshal(ServerMsg{Type: typ, Topic: key, Data: data})
	if err != nil {
		log.Printf("realtime: marshal %s: %v", typ, err)
		return
	}
	for c := range t.clients {
		if c != except {
			h.deliver(c, b)
		}
	}
}

func (h *Hub) sendTo(c *Client, m ServerMsg) {
	if b, err := json.Marshal(m); err == nil {
		h.deliver(c, b)
	}
}

// A client whose buffer is full is too slow: drop it rather than stall everyone.
// It will reconnect and catch up through the REST history (?after=<id>).
func (h *Hub) deliver(c *Client, b []byte) {
	if c.closed {
		return
	}
	select {
	case c.send <- b:
	default:
		h.removeClient(c)
	}
}