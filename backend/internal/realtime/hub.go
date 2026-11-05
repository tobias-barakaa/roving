// internal/realtime/hub.go
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"roving/backend/internal/chat"
)

const (
	MaxConnAge  = time.Hour       // sessions can expire; force periodic reconnect
	reauthEvery = 5 * time.Minute // re-check every live subscription
	maxSubs     = 50              // subscriptions per connection
)

// Authorizer decides whether a user may enter a channel. *chat.Service
// implements it, which keeps WebSocket access identical to the HTTP rules.
type Authorizer interface {
	CanAccessChannel(ctx context.Context, userID, groupID string, roomID *string) error
}

type userState struct {
	name    string
	status  string
	clients map[*Client]struct{}
}

// Hub tracks connections, channel subscriptions and presence. It implements
// chat.Publisher, so attaching it to the chat service makes chat live.
//
// Presence is per user, not per connection: a user is "in" a channel while at
// least one of their connections is subscribed to it, so extra browser tabs
// never produce duplicate joins or premature leaves.
type Hub struct {
	auth Authorizer

	mu       sync.RWMutex
	channels map[string]map[*Client]struct{}
	meta     map[string]ChannelRef
	users    map[string]*userState
	closed   bool

	// Calls have their own lock (see calls.go). It is never held together
	// with mu, so the two cannot deadlock.
	callMu sync.Mutex
	calls  map[string]*call
}

func NewHub(auth Authorizer) *Hub {
	return &Hub{
		auth:     auth,
		channels: make(map[string]map[*Client]struct{}),
		meta:     make(map[string]ChannelRef),
		users:    make(map[string]*userState),
		calls:    make(map[string]*call),
	}
}

// ==================== connection lifecycle ====================

func (h *Hub) register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	st := h.users[c.userID]
	if st == nil {
		st = &userState{name: c.displayName, status: StatusAvailable, clients: make(map[*Client]struct{})}
		h.users[c.userID] = st
	}
	st.clients[c] = struct{}{}
	return true
}

func (h *Hub) unregister(c *Client) {
	type leave struct {
		key string
		ref ChannelRef
	}
	var leaves []leave

	h.mu.Lock()
	for key := range c.subs {
		ref, userLeft := h.removeLocked(c, key)
		if userLeft {
			leaves = append(leaves, leave{key, ref})
		}
	}
	if st := h.users[c.userID]; st != nil {
		delete(st.clients, c)
		if len(st.clients) == 0 {
			delete(h.users, c.userID)
		}
	}
	h.mu.Unlock()

	h.leaveAllCalls(c)
	c.markDone()
	for _, l := range leaves {
		ref := l.ref
		h.broadcastJSON(l.key, outbound{Type: "presence.leave", Channel: &ref, UserID: c.userID}, "")
	}
}

// Shutdown closes every connection; call before http.Server.Shutdown, which
// does not track hijacked (WebSocket) connections.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	h.closed = true
	var all []*Client
	for _, st := range h.users {
		for c := range st.clients {
			all = append(all, c)
		}
	}
	h.mu.Unlock()
	for _, c := range all {
		c.closeConn(websocket.CloseGoingAway, "server shutting down")
	}
}

// ==================== subscriptions ====================

// subscribe adds c to a channel (the caller has already authorized it) and
// returns the presence snapshot. It is idempotent.
func (h *Hub) subscribe(c *Client, ref ChannelRef) ([]PresenceEntry, error) {
	key := ref.key()

	h.mu.Lock()
	if _, already := c.subs[key]; already {
		p := h.presenceLocked(key)
		h.mu.Unlock()
		return p, nil
	}
	if len(c.subs) >= maxSubs {
		h.mu.Unlock()
		return nil, errors.New("too many subscriptions")
	}
	set := h.channels[key]
	if set == nil {
		set = make(map[*Client]struct{})
		h.channels[key] = set
		h.meta[key] = ref
	}
	first := !userInSetLocked(set, c.userID)
	set[c] = struct{}{}
	c.subs[key] = struct{}{}
	st := h.users[c.userID]
	presence := h.presenceLocked(key)
	name, status := c.displayName, StatusAvailable
	if st != nil {
		name, status = st.name, st.status
	}
	h.mu.Unlock()

	if first {
		r := ref
		h.broadcastJSON(key, outbound{
			Type: "presence.join", Channel: &r,
			UserID: c.userID, DisplayName: name, Status: status,
		}, c.userID)
	}
	return presence, nil
}

// unsubscribe removes c from a channel. A non-empty reason is also sent to the
// client (used when access is revoked server-side).
func (h *Hub) unsubscribe(c *Client, key, reason string) {
	h.mu.Lock()
	if _, ok := c.subs[key]; !ok {
		h.mu.Unlock()
		return
	}
	ref, userLeft := h.removeLocked(c, key)
	h.mu.Unlock()

	// Leaving a channel (or losing access to it) also ends this connection's
	// part in that channel's call.
	h.leaveCall(c, key)

	if userLeft {
		r := ref
		h.broadcastJSON(key, outbound{Type: "presence.leave", Channel: &r, UserID: c.userID}, "")
	}
	if reason != "" {
		r := ref
		c.sendJSON(outbound{Type: "unsubscribed", Channel: &r, Reason: reason})
	}
}

// removeLocked drops c from one channel. It reports the channel ref and
// whether the user has no other connection left there (i.e. they "left").
func (h *Hub) removeLocked(c *Client, key string) (ChannelRef, bool) {
	ref := h.meta[key]
	set := h.channels[key]
	delete(set, c)
	delete(c.subs, key)
	userLeft := !userInSetLocked(set, c.userID)
	if len(set) == 0 {
		delete(h.channels, key)
		delete(h.meta, key)
	}
	return ref, userLeft
}

func userInSetLocked(set map[*Client]struct{}, userID string) bool {
	for other := range set {
		if other.userID == userID {
			return true
		}
	}
	return false
}

func (h *Hub) isSubscribed(c *Client, key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := c.subs[key]
	return ok
}

// ==================== presence ====================

func (h *Hub) presenceLocked(key string) []PresenceEntry {
	seen := make(map[string]bool)
	var out []PresenceEntry
	for c := range h.channels[key] {
		if seen[c.userID] {
			continue
		}
		seen[c.userID] = true
		e := PresenceEntry{UserID: c.userID, DisplayName: c.displayName, Status: StatusAvailable}
		if st := h.users[c.userID]; st != nil {
			e.DisplayName, e.Status = st.name, st.status
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].UserID < out[j].UserID
	})
	return out
}

// PresenceFor lists who is currently online in a channel (used by REST).
func (h *Hub) PresenceFor(ref ChannelRef) []PresenceEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := h.presenceLocked(ref.key())
	if out == nil {
		out = []PresenceEntry{}
	}
	return out
}

// setStatus updates a user's status and tells every channel they are in.
func (h *Hub) setStatus(c *Client, status string) {
	type target struct {
		key string
		ref ChannelRef
	}
	var targets []target

	h.mu.Lock()
	st := h.users[c.userID]
	if st == nil {
		h.mu.Unlock()
		return
	}
	st.status = status
	seen := make(map[string]bool)
	for cl := range st.clients {
		for key := range cl.subs {
			if !seen[key] {
				seen[key] = true
				targets = append(targets, target{key, h.meta[key]})
			}
		}
	}
	h.mu.Unlock()

	for _, t := range targets {
		ref := t.ref
		h.broadcastJSON(t.key, outbound{
			Type: "presence.status", Channel: &ref, UserID: c.userID, Status: status,
		}, "")
	}
}

// ==================== fan-out ====================

func (h *Hub) broadcastJSON(key string, v outbound, exceptUser string) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("realtime: marshal %s: %v", v.Type, err)
		return
	}
	h.broadcast(key, b, exceptUser)
}

// broadcast never blocks: a client whose buffer is full is dropped rather
// than allowed to stall everyone else.
func (h *Hub) broadcast(key string, msg []byte, exceptUser string) {
	var slow []*Client
	h.mu.RLock()
	for c := range h.channels[key] {
		if exceptUser != "" && c.userID == exceptUser {
			continue
		}
		select {
		case c.send <- msg:
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range slow {
		c.closeConn(websocket.ClosePolicyViolation, "slow consumer")
	}
}

// Publish implements chat.Publisher: chat changes reach everyone subscribed
// to that channel the moment the database commit succeeds.
func (h *Hub) Publish(ev chat.Event) {
	if ev.Message == nil {
		return
	}
	ref := ChannelRef{GroupID: strings.ToLower(ev.GroupID)}
	if ev.RoomID != nil {
		r := strings.ToLower(*ev.RoomID)
		ref.RoomID = &r
	}
	m := chat.PublicResponse(ev.Message)
	h.broadcastJSON(ref.key(), outbound{Type: ev.Type, Channel: &ref, Message: &m}, "")
}

// ==================== access re-checks ====================

// RunReauth periodically re-authorizes every live subscription, so removing a
// member or revoking room access also cuts their live feed, not just new
// HTTP calls. Transient errors (database down) never evict anyone.
func (h *Hub) RunReauth(ctx context.Context) {
	tick := time.NewTicker(reauthEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.reauthOnce(ctx)
		}
	}
}

func (h *Hub) reauthOnce(ctx context.Context) {
	type sub struct {
		c   *Client
		key string
		ref ChannelRef
	}
	var subs []sub
	h.mu.RLock()
	for key, set := range h.channels {
		for c := range set {
			subs = append(subs, sub{c, key, h.meta[key]})
		}
	}
	h.mu.RUnlock()

	for _, s := range subs {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := h.auth.CanAccessChannel(cctx, s.c.userID, s.ref.GroupID, s.ref.RoomID)
		cancel()
		if errors.Is(err, chat.ErrNotFound) || errors.Is(err, chat.ErrForbidden) || errors.Is(err, chat.ErrInvalid) {
			h.unsubscribe(s.c, s.key, "access_revoked")
		}
	}
}


// // internal/realtime/hub.go
// package realtime

// import (
// 	"context"
// 	"encoding/json"
// 	"errors"
// 	"log"
// 	"sort"
// 	"strings"
// 	"sync"
// 	"time"

// 	"github.com/gorilla/websocket"

// 	"roving/backend/internal/chat"
// )

// const (
// 	MaxConnAge  = time.Hour       // sessions can expire; force periodic reconnect
// 	reauthEvery = 5 * time.Minute // re-check every live subscription
// 	maxSubs     = 50              // subscriptions per connection
// )

// // Authorizer decides whether a user may enter a channel. *chat.Service
// // implements it, which keeps WebSocket access identical to the HTTP rules.
// type Authorizer interface {
// 	CanAccessChannel(ctx context.Context, userID, groupID string, roomID *string) error
// }

// type userState struct {
// 	name    string
// 	status  string
// 	clients map[*Client]struct{}
// }

// // Hub tracks connections, channel subscriptions and presence. It implements
// // chat.Publisher, so attaching it to the chat service makes chat live.
// //
// // Presence is per user, not per connection: a user is "in" a channel while at
// // least one of their connections is subscribed to it, so extra browser tabs
// // never produce duplicate joins or premature leaves.
// type Hub struct {
// 	auth Authorizer

// 	mu       sync.RWMutex
// 	channels map[string]map[*Client]struct{}
// 	meta     map[string]ChannelRef
// 	users    map[string]*userState
// 	closed   bool
// }

// func NewHub(auth Authorizer) *Hub {
// 	return &Hub{
// 		auth:     auth,
// 		channels: make(map[string]map[*Client]struct{}),
// 		meta:     make(map[string]ChannelRef),
// 		users:    make(map[string]*userState),
// 	}
// }

// // ==================== connection lifecycle ====================

// func (h *Hub) register(c *Client) bool {
// 	h.mu.Lock()
// 	defer h.mu.Unlock()
// 	if h.closed {
// 		return false
// 	}
// 	st := h.users[c.userID]
// 	if st == nil {
// 		st = &userState{name: c.displayName, status: StatusAvailable, clients: make(map[*Client]struct{})}
// 		h.users[c.userID] = st
// 	}
// 	st.clients[c] = struct{}{}
// 	return true
// }

// func (h *Hub) unregister(c *Client) {
// 	type leave struct {
// 		key string
// 		ref ChannelRef
// 	}
// 	var leaves []leave

// 	h.mu.Lock()
// 	for key := range c.subs {
// 		ref, userLeft := h.removeLocked(c, key)
// 		if userLeft {
// 			leaves = append(leaves, leave{key, ref})
// 		}
// 	}
// 	if st := h.users[c.userID]; st != nil {
// 		delete(st.clients, c)
// 		if len(st.clients) == 0 {
// 			delete(h.users, c.userID)
// 		}
// 	}
// 	h.mu.Unlock()

// 	c.markDone()
// 	for _, l := range leaves {
// 		ref := l.ref
// 		h.broadcastJSON(l.key, outbound{Type: "presence.leave", Channel: &ref, UserID: c.userID}, "")
// 	}
// }

// // Shutdown closes every connection; call before http.Server.Shutdown, which
// // does not track hijacked (WebSocket) connections.
// func (h *Hub) Shutdown() {
// 	h.mu.Lock()
// 	h.closed = true
// 	var all []*Client
// 	for _, st := range h.users {
// 		for c := range st.clients {
// 			all = append(all, c)
// 		}
// 	}
// 	h.mu.Unlock()
// 	for _, c := range all {
// 		c.closeConn(websocket.CloseGoingAway, "server shutting down")
// 	}
// }

// // ==================== subscriptions ====================

// // subscribe adds c to a channel (the caller has already authorized it) and
// // returns the presence snapshot. It is idempotent.
// func (h *Hub) subscribe(c *Client, ref ChannelRef) ([]PresenceEntry, error) {
// 	key := ref.key()

// 	h.mu.Lock()
// 	if _, already := c.subs[key]; already {
// 		p := h.presenceLocked(key)
// 		h.mu.Unlock()
// 		return p, nil
// 	}
// 	if len(c.subs) >= maxSubs {
// 		h.mu.Unlock()
// 		return nil, errors.New("too many subscriptions")
// 	}
// 	set := h.channels[key]
// 	if set == nil {
// 		set = make(map[*Client]struct{})
// 		h.channels[key] = set
// 		h.meta[key] = ref
// 	}
// 	first := !userInSetLocked(set, c.userID)
// 	set[c] = struct{}{}
// 	c.subs[key] = struct{}{}
// 	st := h.users[c.userID]
// 	presence := h.presenceLocked(key)
// 	name, status := c.displayName, StatusAvailable
// 	if st != nil {
// 		name, status = st.name, st.status
// 	}
// 	h.mu.Unlock()

// 	if first {
// 		r := ref
// 		h.broadcastJSON(key, outbound{
// 			Type: "presence.join", Channel: &r,
// 			UserID: c.userID, DisplayName: name, Status: status,
// 		}, c.userID)
// 	}
// 	return presence, nil
// }

// // unsubscribe removes c from a channel. A non-empty reason is also sent to the
// // client (used when access is revoked server-side).
// func (h *Hub) unsubscribe(c *Client, key, reason string) {
// 	h.mu.Lock()
// 	if _, ok := c.subs[key]; !ok {
// 		h.mu.Unlock()
// 		return
// 	}
// 	ref, userLeft := h.removeLocked(c, key)
// 	h.mu.Unlock()

// 	if userLeft {
// 		r := ref
// 		h.broadcastJSON(key, outbound{Type: "presence.leave", Channel: &r, UserID: c.userID}, "")
// 	}
// 	if reason != "" {
// 		r := ref
// 		c.sendJSON(outbound{Type: "unsubscribed", Channel: &r, Reason: reason})
// 	}
// }

// // removeLocked drops c from one channel. It reports the channel ref and
// // whether the user has no other connection left there (i.e. they "left").
// func (h *Hub) removeLocked(c *Client, key string) (ChannelRef, bool) {
// 	ref := h.meta[key]
// 	set := h.channels[key]
// 	delete(set, c)
// 	delete(c.subs, key)
// 	userLeft := !userInSetLocked(set, c.userID)
// 	if len(set) == 0 {
// 		delete(h.channels, key)
// 		delete(h.meta, key)
// 	}
// 	return ref, userLeft
// }

// func userInSetLocked(set map[*Client]struct{}, userID string) bool {
// 	for other := range set {
// 		if other.userID == userID {
// 			return true
// 		}
// 	}
// 	return false
// }

// func (h *Hub) isSubscribed(c *Client, key string) bool {
// 	h.mu.RLock()
// 	defer h.mu.RUnlock()
// 	_, ok := c.subs[key]
// 	return ok
// }

// // ==================== presence ====================

// func (h *Hub) presenceLocked(key string) []PresenceEntry {
// 	seen := make(map[string]bool)
// 	var out []PresenceEntry
// 	for c := range h.channels[key] {
// 		if seen[c.userID] {
// 			continue
// 		}
// 		seen[c.userID] = true
// 		e := PresenceEntry{UserID: c.userID, DisplayName: c.displayName, Status: StatusAvailable}
// 		if st := h.users[c.userID]; st != nil {
// 			e.DisplayName, e.Status = st.name, st.status
// 		}
// 		out = append(out, e)
// 	}
// 	sort.Slice(out, func(i, j int) bool {
// 		if out[i].DisplayName != out[j].DisplayName {
// 			return out[i].DisplayName < out[j].DisplayName
// 		}
// 		return out[i].UserID < out[j].UserID
// 	})
// 	return out
// }

// // PresenceFor lists who is currently online in a channel (used by REST).
// func (h *Hub) PresenceFor(ref ChannelRef) []PresenceEntry {
// 	h.mu.RLock()
// 	defer h.mu.RUnlock()
// 	out := h.presenceLocked(ref.key())
// 	if out == nil {
// 		out = []PresenceEntry{}
// 	}
// 	return out
// }

// // setStatus updates a user's status and tells every channel they are in.
// func (h *Hub) setStatus(c *Client, status string) {
// 	type target struct {
// 		key string
// 		ref ChannelRef
// 	}
// 	var targets []target

// 	h.mu.Lock()
// 	st := h.users[c.userID]
// 	if st == nil {
// 		h.mu.Unlock()
// 		return
// 	}
// 	st.status = status
// 	seen := make(map[string]bool)
// 	for cl := range st.clients {
// 		for key := range cl.subs {
// 			if !seen[key] {
// 				seen[key] = true
// 				targets = append(targets, target{key, h.meta[key]})
// 			}
// 		}
// 	}
// 	h.mu.Unlock()

// 	for _, t := range targets {
// 		ref := t.ref
// 		h.broadcastJSON(t.key, outbound{
// 			Type: "presence.status", Channel: &ref, UserID: c.userID, Status: status,
// 		}, "")
// 	}
// }

// // ==================== fan-out ====================

// func (h *Hub) broadcastJSON(key string, v outbound, exceptUser string) {
// 	b, err := json.Marshal(v)
// 	if err != nil {
// 		log.Printf("realtime: marshal %s: %v", v.Type, err)
// 		return
// 	}
// 	h.broadcast(key, b, exceptUser)
// }

// // broadcast never blocks: a client whose buffer is full is dropped rather
// // than allowed to stall everyone else.
// func (h *Hub) broadcast(key string, msg []byte, exceptUser string) {
// 	var slow []*Client
// 	h.mu.RLock()
// 	for c := range h.channels[key] {
// 		if exceptUser != "" && c.userID == exceptUser {
// 			continue
// 		}
// 		select {
// 		case c.send <- msg:
// 		default:
// 			slow = append(slow, c)
// 		}
// 	}
// 	h.mu.RUnlock()
// 	for _, c := range slow {
// 		c.closeConn(websocket.ClosePolicyViolation, "slow consumer")
// 	}
// }

// // Publish implements chat.Publisher: chat changes reach everyone subscribed
// // to that channel the moment the database commit succeeds.
// func (h *Hub) Publish(ev chat.Event) {
// 	if ev.Message == nil {
// 		return
// 	}
// 	ref := ChannelRef{GroupID: strings.ToLower(ev.GroupID)}
// 	if ev.RoomID != nil {
// 		r := strings.ToLower(*ev.RoomID)
// 		ref.RoomID = &r
// 	}
// 	m := chat.PublicResponse(ev.Message)
// 	h.broadcastJSON(ref.key(), outbound{Type: ev.Type, Channel: &ref, Message: &m}, "")
// }

// // ==================== access re-checks ====================

// // RunReauth periodically re-authorizes every live subscription, so removing a
// // member or revoking room access also cuts their live feed, not just new
// // HTTP calls. Transient errors (database down) never evict anyone.
// func (h *Hub) RunReauth(ctx context.Context) {
// 	tick := time.NewTicker(reauthEvery)
// 	defer tick.Stop()
// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		case <-tick.C:
// 			h.reauthOnce(ctx)
// 		}
// 	}
// }

// func (h *Hub) reauthOnce(ctx context.Context) {
// 	type sub struct {
// 		c   *Client
// 		key string
// 		ref ChannelRef
// 	}
// 	var subs []sub
// 	h.mu.RLock()
// 	for key, set := range h.channels {
// 		for c := range set {
// 			subs = append(subs, sub{c, key, h.meta[key]})
// 		}
// 	}
// 	h.mu.RUnlock()

// 	for _, s := range subs {
// 		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
// 		err := h.auth.CanAccessChannel(cctx, s.c.userID, s.ref.GroupID, s.ref.RoomID)
// 		cancel()
// 		if errors.Is(err, chat.ErrNotFound) || errors.Is(err, chat.ErrForbidden) || errors.Is(err, chat.ErrInvalid) {
// 			h.unsubscribe(s.c, s.key, "access_revoked")
// 		}
// 	}
// }

// // // internal/realtime/hub.go
// // package realtime

// // import (
// // 	"context"
// // 	"encoding/json"
// // 	"log"
// // 	"strings"
// // 	"time"
// // )

// // // Hub owns ALL mutable state (topics, members, subscriptions) in one goroutine.
// // // Everything else talks to it by posting closures, so there are no locks.
// // // Anything slow (DB checks) happens OUTSIDE the hub goroutine.
// // type Hub struct {
// // 	cmds   chan func()
// // 	done   <-chan struct{}
// // 	grace  time.Duration
// // 	topics map[string]*topic

// // 	// calls: room topic key -> users currently in that room's call. Written by
// // 	// the calls module (SetInCall); read to authorise signal relaying.
// // 	calls map[string]map[string]struct{}

// // 	// onGone fires (in its own goroutine) when a user has been absent from a
// // 	// topic for the whole grace period.
// // 	onGone func(topicKey, userID string)
// // }

// // type topic struct {
// // 	groupID string
// // 	clients map[*Client]struct{}
// // 	members map[string]*member // by userID; one entry however many tabs
// // }

// // type member struct {
// // 	name   string
// // 	status string
// // 	conns  map[*Client]struct{}
// // 	timer  *time.Timer // pending "left" announcement (reconnect grace)
// // 	gen    int
// // }

// // // grace: how long a user with zero connections still counts as present, so a
// // // page refresh or flaky network doesn't flap join/leave events.
// // func NewHub(ctx context.Context, grace time.Duration) *Hub {
// // 	h := &Hub{
// // 		cmds:   make(chan func(), 1024),
// // 		done:   ctx.Done(),
// // 		grace:  grace,
// // 		topics: make(map[string]*topic),
// // 		calls:  make(map[string]map[string]struct{}),
// // 	}
// // 	go h.run()
// // 	return h
// // }

// // func (h *Hub) run() {
// // 	for {
// // 		select {
// // 		case fn := <-h.cmds:
// // 			fn()
// // 		case <-h.done:
// // 			all := map[*Client]struct{}{}
// // 			for _, t := range h.topics {
// // 				for c := range t.clients {
// // 					all[c] = struct{}{}
// // 				}
// // 			}
// // 			for c := range all {
// // 				h.removeClient(c)
// // 			}
// // 			return
// // 		}
// // 	}
// // }

// // // control: reliable post (blocks if the queue is full). Used for join/leave.
// // func (h *Hub) control(fn func()) {
// // 	select {
// // 	case h.cmds <- fn:
// // 	case <-h.done:
// // 	}
// // }

// // // try: best-effort post. Used for fan-out, so a stalled hub can never block
// // // an HTTP request that is publishing an event.
// // func (h *Hub) try(fn func()) {
// // 	select {
// // 	case h.cmds <- fn:
// // 	default:
// // 		log.Println("realtime: hub queue full, event dropped")
// // 	}
// // }

// // // ---------- public API for other modules ----------

// // // Broadcast sends an arbitrary event to everyone subscribed to a topic.
// // func (h *Hub) Broadcast(topicKey, typ string, data any) {
// // 	h.try(func() {
// // 		if t := h.topics[topicKey]; t != nil {
// // 			h.fanout(t, topicKey, typ, data, nil)
// // 		}
// // 	})
// // }

// // // EvictUser removes a user from every topic of a group (call it when a member
// // // is removed/suspended or a room membership is revoked).
// // func (h *Hub) EvictUser(groupID, userID string) {
// // 	h.control(func() {
// // 		for key, t := range h.topics {
// // 			if t.groupID != groupID {
// // 				continue
// // 			}
// // 			for c := range t.clients {
// // 				if c.userID == userID {
// // 					h.leave(c, key)
// // 					h.sendTo(c, ServerMsg{Type: "evicted", Topic: key})
// // 				}
// // 			}
// // 		}
// // 	})
// // }

// // // SetLeaveHook registers a callback for "user fully left this topic". Call it
// // // once at startup, before serving. The calls module uses it to drop people
// // // from a call when their connection disappears.
// // func (h *Hub) SetLeaveHook(fn func(topicKey, userID string)) { h.onGone = fn }

// // // SetInCall marks a user as a participant of a room's call. Only these users
// // // may send or receive call.signal messages for that room.
// // func (h *Hub) SetInCall(roomID, userID string, in bool) {
// // 	key, uid := RoomTopic(roomID), strings.ToLower(userID)
// // 	h.control(func() {
// // 		set := h.calls[key]
// // 		if in {
// // 			if set == nil {
// // 				set = map[string]struct{}{}
// // 				h.calls[key] = set
// // 			}
// // 			set[uid] = struct{}{}
// // 			return
// // 		}
// // 		delete(set, uid)
// // 		if len(set) == 0 {
// // 			delete(h.calls, key)
// // 		}
// // 	})
// // }

// // // IsPresent reports whether the user currently holds a live subscription to
// // // the topic (synchronous round-trip through the hub goroutine).
// // func (h *Hub) IsPresent(topicKey, userID string) bool {
// // 	res := make(chan bool, 1)
// // 	h.control(func() {
// // 		var m *member
// // 		if t := h.topics[topicKey]; t != nil {
// // 			m = t.members[userID]
// // 		}
// // 		res <- m != nil && len(m.conns) > 0
// // 	})
// // 	select {
// // 	case v := <-res:
// // 		return v
// // 	case <-h.done:
// // 		return false
// // 	}
// // }

// // // ---------- internals (hub goroutine only) ----------

// // // relaySignal forwards one WebRTC signal (offer/answer/ICE candidate) to a
// // // single peer. Both ends must be in the room's call and the sender must hold
// // // a subscription to the room, so signals can't be aimed at outsiders.
// // func (h *Hub) relaySignal(c *Client, key string, m ClientMsg) {
// // 	if _, ok := c.subs[key]; !ok {
// // 		return
// // 	}
// // 	inCall := h.calls[key]
// // 	to := strings.ToLower(m.To)
// // 	if _, ok := inCall[c.userID]; !ok {
// // 		return
// // 	}
// // 	if _, ok := inCall[to]; !ok || to == c.userID {
// // 		return
// // 	}
// // 	t := h.topics[key]
// // 	if t == nil {
// // 		return
// // 	}
// // 	peer := t.members[to]
// // 	if peer == nil {
// // 		return
// // 	}
// // 	b, err := json.Marshal(ServerMsg{Type: "call.signal", Topic: key, Data: map[string]any{
// // 		"from": c.userID, "kind": m.Kind, "data": m.Data,
// // 	}})
// // 	if err != nil {
// // 		return
// // 	}
// // 	for pc := range peer.conns {
// // 		h.deliver(pc, b)
// // 	}
// // }


// // func (h *Hub) join(c *Client, key, groupID, status string) {
// // 	if c.closed {
// // 		return
// // 	}
// // 	t := h.topics[key]
// // 	if t == nil {
// // 		t = &topic{groupID: groupID, clients: map[*Client]struct{}{}, members: map[string]*member{}}
// // 		h.topics[key] = t
// // 	}
// // 	if _, already := t.clients[c]; already {
// // 		return
// // 	}
// // 	t.clients[c] = struct{}{}
// // 	c.subs[key] = struct{}{}

// // 	m, existed := t.members[c.userID]
// // 	if !existed {
// // 		m = &member{name: c.name, status: status, conns: map[*Client]struct{}{}}
// // 		t.members[c.userID] = m
// // 	}
// // 	if m.timer != nil { // reconnected within grace: cancel the pending "left"
// // 		m.timer.Stop()
// // 		m.timer = nil
// // 	}
// // 	m.gen++
// // 	m.conns[c] = struct{}{}

// // 	snapshot := make([]PresenceUser, 0, len(t.members))
// // 	for uid, mm := range t.members {
// // 		snapshot = append(snapshot, PresenceUser{UserID: uid, Name: mm.name, Status: mm.status})
// // 	}
// // 	h.sendTo(c, ServerMsg{Type: "subscribed", Topic: key})
// // 	h.sendTo(c, ServerMsg{Type: "presence.snapshot", Topic: key, Data: snapshot})
// // 	if !existed {
// // 		h.fanout(t, key, "presence.joined",
// // 			PresenceUser{UserID: c.userID, Name: m.name, Status: m.status}, c)
// // 	}
// // }

// // func (h *Hub) leave(c *Client, key string) {
// // 	t := h.topics[key]
// // 	delete(c.subs, key)
// // 	if t == nil {
// // 		return
// // 	}
// // 	delete(t.clients, c)
// // 	m := t.members[c.userID]
// // 	if m == nil {
// // 		return
// // 	}
// // 	delete(m.conns, c)
// // 	if len(m.conns) > 0 {
// // 		return // still connected from another tab/device
// // 	}
// // 	m.gen++
// // 	gen, uid := m.gen, c.userID
// // 	m.timer = time.AfterFunc(h.grace, func() {
// // 		h.control(func() { h.expire(key, uid, gen) })
// // 	})
// // }

// // func (h *Hub) expire(key, userID string, gen int) {
// // 	t := h.topics[key]
// // 	if t == nil {
// // 		return
// // 	}
// // 	m := t.members[userID]
// // 	if m == nil || m.gen != gen || len(m.conns) > 0 {
// // 		return // user came back
// // 	}
// // 	delete(t.members, userID)
// // 	h.fanout(t, key, "presence.left", PresenceUser{UserID: userID, Name: m.name}, nil)
// // 	if h.onGone != nil {
// // 		go h.onGone(key, userID)
// // 	}
// // 	if len(t.clients) == 0 && len(t.members) == 0 {
// // 		delete(h.topics, key)
// // 	}
// // }

// // func (h *Hub) setStatus(c *Client, status string) {
// // 	for key := range c.subs {
// // 		t := h.topics[key]
// // 		if t == nil {
// // 			continue
// // 		}
// // 		if m := t.members[c.userID]; m != nil {
// // 			m.status = status
// // 			h.fanout(t, key, "presence.updated",
// // 				PresenceUser{UserID: c.userID, Name: m.name, Status: status}, nil)
// // 		}
// // 	}
// // }

// // func (h *Hub) typing(c *Client, key string) {
// // 	if _, ok := c.subs[key]; !ok {
// // 		return // can only type in channels you're subscribed to (and were authorized for)
// // 	}
// // 	if t := h.topics[key]; t != nil {
// // 		h.fanout(t, key, "typing", map[string]string{"userId": c.userID, "name": c.name}, c)
// // 	}
// // }

// // func (h *Hub) removeClient(c *Client) {
// // 	if c.closed {
// // 		return
// // 	}
// // 	c.closed = true
// // 	for key := range c.subs {
// // 		h.leave(c, key)
// // 	}
// // 	close(c.send) // only ever closed here, so no send-on-closed panics
// // }

// // func (h *Hub) fanout(t *topic, key, typ string, data any, except *Client) {
// // 	b, err := json.Marshal(ServerMsg{Type: typ, Topic: key, Data: data})
// // 	if err != nil {
// // 		log.Printf("realtime: marshal %s: %v", typ, err)
// // 		return
// // 	}
// // 	for c := range t.clients {
// // 		if c != except {
// // 			h.deliver(c, b)
// // 		}
// // 	}
// // }

// // func (h *Hub) sendTo(c *Client, m ServerMsg) {
// // 	if b, err := json.Marshal(m); err == nil {
// // 		h.deliver(c, b)
// // 	}
// // }

// // // A client whose buffer is full is too slow: drop it rather than stall everyone.
// // // It will reconnect and catch up through the REST history (?after=<id>).
// // func (h *Hub) deliver(c *Client, b []byte) {
// // 	if c.closed {
// // 		return
// // 	}
// // 	select {
// // 	case c.send <- b:
// // 	default:
// // 		h.removeClient(c)
// // 	}
// // }