// internal/realtime/calls.go
package realtime

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Audio/video calls are WebRTC mesh calls: every participant connects directly
// to every other one, so media never touches this server. The server only
//   - tracks who is in each call (in memory, like presence),
//   - relays signaling messages (offer / answer / ICE candidate) between two
//     participants of the same call, and
//   - broadcasts mute/camera state.
//
// A call lives on a channel: the group-wide channel or one room. The rule that
// avoids "glare" (both sides offering at once): the participant who JOINS
// sends the offers, to everyone already in the call.
//
// Mesh is fine for study-group sizes; MaxCallParticipants caps it, because
// each extra person adds an upload stream for everyone.

const (
	MaxCallParticipants = 8
	maxSignalBytes      = 24 * 1024
)

type callParticipant struct {
	client   *Client
	name     string
	audio    bool
	video    bool
	joinedAt time.Time
}

type call struct {
	ref   ChannelRef
	parts map[string]*callParticipant // by user id: one connection per user
}

// CallParticipant is the public view of someone in a call.
type CallParticipant struct {
	UserID      string    `json:"userId"`
	DisplayName string    `json:"displayName"`
	Audio       bool      `json:"audio"`
	Video       bool      `json:"video"`
	JoinedAt    time.Time `json:"joinedAt"`
}

// callError carries a protocol error code to the client.
type callError struct{ code, msg string }

func (e *callError) Error() string { return e.msg }

func errCall(code, msg string) error { return &callError{code: code, msg: msg} }

func snapshotLocked(cl *call) []CallParticipant {
	out := make([]CallParticipant, 0, len(cl.parts))
	for id, p := range cl.parts {
		out = append(out, CallParticipant{
			UserID: id, DisplayName: p.name, Audio: p.audio, Video: p.video, JoinedAt: p.joinedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].JoinedAt.Equal(out[j].JoinedAt) {
			return out[i].JoinedAt.Before(out[j].JoinedAt)
		}
		return out[i].UserID < out[j].UserID
	})
	return out
}

// CallFor lists who is in the call on a channel (used by REST).
func (h *Hub) CallFor(ref ChannelRef) []CallParticipant {
	h.callMu.Lock()
	defer h.callMu.Unlock()
	cl := h.calls[ref.key()]
	if cl == nil {
		return []CallParticipant{}
	}
	return snapshotLocked(cl)
}

// joinCall puts the connection into the channel's call. The connection must
// already be subscribed to the channel (which proves access). fresh is false
// when this same connection was already in the call.
func (h *Hub) joinCall(c *Client, ref ChannelRef, audio, video bool) (parts []CallParticipant, fresh bool, err error) {
	key := ref.key()
	if !h.isSubscribed(c, key) {
		return nil, false, errCall("not_subscribed", "subscribe to the channel first")
	}

	h.callMu.Lock()
	defer h.callMu.Unlock()

	cl := h.calls[key]
	if cl == nil {
		cl = &call{ref: ref, parts: make(map[string]*callParticipant)}
		h.calls[key] = cl
	}
	if p, ok := cl.parts[c.userID]; ok {
		if p.client == c {
			return snapshotLocked(cl), false, nil
		}
		return nil, false, errCall("already_in_call", "you are already in this call from another connection")
	}
	if len(cl.parts) >= MaxCallParticipants {
		return nil, false, errCall("call_full", "this call is full")
	}
	cl.parts[c.userID] = &callParticipant{
		client: c, name: c.displayName, audio: audio, video: video, joinedAt: time.Now().UTC(),
	}
	c.calls[key] = struct{}{}
	return snapshotLocked(cl), true, nil
}

func (h *Hub) announceJoin(c *Client, ref ChannelRef, audio, video bool) {
	r := ref
	h.broadcastJSON(ref.key(), outbound{
		Type: "call.joined", Channel: &r, UserID: c.userID, DisplayName: c.displayName,
		Audio: &audio, Video: &video,
	}, "")
}

// dropLocked removes c from the call on key (callMu must be held). It reports
// the call's channel, whether anything was removed, and whether the call is
// now empty (and so deleted).
func (h *Hub) dropLocked(key string, c *Client) (ref ChannelRef, removed, ended bool) {
	cl := h.calls[key]
	if cl == nil {
		return ref, false, false
	}
	p := cl.parts[c.userID]
	if p == nil || p.client != c {
		return ref, false, false
	}
	delete(cl.parts, c.userID)
	delete(c.calls, key)
	if len(cl.parts) == 0 {
		delete(h.calls, key)
		ended = true
	}
	return cl.ref, true, ended
}

func (h *Hub) announceLeave(key string, ref ChannelRef, userID string, ended bool) {
	r := ref
	h.broadcastJSON(key, outbound{Type: "call.left", Channel: &r, UserID: userID}, "")
	if ended {
		r2 := ref
		h.broadcastJSON(key, outbound{Type: "call.ended", Channel: &r2}, "")
	}
}

// leaveCall removes the connection from one channel's call, if it is in it.
func (h *Hub) leaveCall(c *Client, key string) {
	h.callMu.Lock()
	ref, removed, ended := h.dropLocked(key, c)
	h.callMu.Unlock()
	if removed {
		h.announceLeave(key, ref, c.userID, ended)
	}
}

// leaveAllCalls is used when a connection goes away.
func (h *Hub) leaveAllCalls(c *Client) {
	type left struct {
		key   string
		ref   ChannelRef
		ended bool
	}
	var lefts []left

	h.callMu.Lock()
	keys := make([]string, 0, len(c.calls))
	for key := range c.calls {
		keys = append(keys, key)
	}
	for _, key := range keys {
		if ref, removed, ended := h.dropLocked(key, c); removed {
			lefts = append(lefts, left{key, ref, ended})
		}
	}
	h.callMu.Unlock()

	for _, l := range lefts {
		h.announceLeave(l.key, l.ref, c.userID, l.ended)
	}
}

// relaySignal forwards an opaque WebRTC signaling payload from one call
// participant to another. The server never inspects the payload; it only
// checks that both ends are in the same call and that the payload is small.
func (h *Hub) relaySignal(c *Client, ref ChannelRef, to, kind string, data json.RawMessage) error {
	switch kind {
	case "offer", "answer", "candidate":
	default:
		return errCall("bad_kind", "kind must be offer, answer or candidate")
	}
	if len(data) == 0 || string(data) == "null" || len(data) > maxSignalBytes {
		return errCall("bad_signal", "signal data is missing or too large")
	}
	to = strings.ToLower(to)
	if !uuidRe.MatchString(to) || to == c.userID {
		return errCall("bad_peer", "invalid peer")
	}

	key := ref.key()
	h.callMu.Lock()
	var target *Client
	cl := h.calls[key]
	var me *callParticipant
	if cl != nil {
		me = cl.parts[c.userID]
	}
	if me == nil || me.client != c {
		h.callMu.Unlock()
		return errCall("not_in_call", "join the call first")
	}
	if tp := cl.parts[to]; tp != nil {
		target = tp.client
	}
	h.callMu.Unlock()

	if target == nil {
		return errCall("peer_not_in_call", "that user is not in the call")
	}
	r := ref
	target.sendJSON(outbound{
		Type: "call.signal", Channel: &r, From: c.userID, Kind: kind, Data: data,
	})
	return nil
}

// setMedia updates and broadcasts a participant's mute / camera state.
func (h *Hub) setMedia(c *Client, ref ChannelRef, audio, video *bool) error {
	key := ref.key()
	h.callMu.Lock()
	cl := h.calls[key]
	var p *callParticipant
	if cl != nil {
		p = cl.parts[c.userID]
	}
	if p == nil || p.client != c {
		h.callMu.Unlock()
		return errCall("not_in_call", "join the call first")
	}
	if audio != nil {
		p.audio = *audio
	}
	if video != nil {
		p.video = *video
	}
	a, v := p.audio, p.video
	h.callMu.Unlock()

	r := ref
	h.broadcastJSON(key, outbound{
		Type: "call.media", Channel: &r, UserID: c.userID, Audio: &a, Video: &v,
	}, "")
	return nil
}