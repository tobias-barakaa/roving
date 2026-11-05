// internal/realtime/protocol.go
package realtime

import (
	"encoding/json"
	"regexp"
	"strings"

	"roving/backend/internal/chat"
)

// ---------------------------------------------------------------------------
// Wire protocol (JSON text frames over one WebSocket at GET /api/v1/ws)
//
// client → server
//   {"type":"subscribe",   "channel":{"groupId":"…","roomId":"…"?}, "ref":"…"?}
//   {"type":"unsubscribe", "channel":{…}}
//   {"type":"typing",      "channel":{…}}
//   {"type":"status",      "status":"available|focusing|away"}
//   {"type":"ping"}
//
//   Calls (WebRTC mesh; subscribe to the channel first):
//   {"type":"call.join",   "channel":{…}, "audio":true?, "video":false?}
//   {"type":"call.leave",  "channel":{…}}
//   {"type":"call.signal", "channel":{…}, "to":"<userId>",
//                          "kind":"offer|answer|candidate", "data":{…}}
//   {"type":"call.media",  "channel":{…}, "audio":bool?, "video":bool?}
//
//   Whoever JOINS sends an offer to every participant already in the call;
//   existing participants only answer. "data" is opaque to the server: the
//   RTCSessionDescriptionInit or RTCIceCandidateInit, at most 24 KB.
//
// server → client
//   hello            {userId, displayName, maxAgeSeconds}
//   subscribed       {channel, presence:[{userId,displayName,status}], ref}
//   unsubscribed     {channel, reason}            (e.g. "access_revoked")
//   presence.join    {channel, userId, displayName, status}
//   presence.leave   {channel, userId}
//   presence.status  {channel, userId, status}
//   typing           {channel, userId, displayName}
//   message.created | message.updated | message.deleted   {channel, message}
//   pong, error      {code, error, ref}
//
//   call.state       {channel, participants:[{userId,displayName,audio,video,joinedAt}],
//                     maxParticipants, ref}          (reply to call.join)
//   call.joined      {channel, userId, displayName, audio, video}   (to the channel;
//                     ignore your own)
//   call.left        {channel, userId}
//   call.ended       {channel}                      (last participant left)
//   call.media       {channel, userId, audio, video}
//   call.signal      {channel, from, kind, data}    (only to the addressed peer)
//
//   Call error codes: not_subscribed, already_in_call, call_full, not_in_call,
//   peer_not_in_call, bad_kind, bad_signal, bad_peer, bad_media.
//
// A channel is the group-wide channel (no roomId) or one room. The
// connection is closed after MaxConnAge; clients reconnect and re-subscribe.
// ---------------------------------------------------------------------------

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type ChannelRef struct {
	GroupID string  `json:"groupId"`
	RoomID  *string `json:"roomId,omitempty"`
}

// normalize validates the ids and lowercases them so keys are canonical.
func (r ChannelRef) normalize() (ChannelRef, bool) {
	if !uuidRe.MatchString(r.GroupID) {
		return r, false
	}
	out := ChannelRef{GroupID: strings.ToLower(r.GroupID)}
	if r.RoomID != nil {
		if !uuidRe.MatchString(*r.RoomID) {
			return r, false
		}
		id := strings.ToLower(*r.RoomID)
		out.RoomID = &id
	}
	return out, true
}

// key identifies the channel inside the hub. A room id is globally unique, so
// room keys do not repeat the group (the authorizer already proved the room
// belongs to the claimed group).
func (r ChannelRef) key() string {
	if r.RoomID != nil {
		return "r:" + *r.RoomID
	}
	return "g:" + r.GroupID
}

const (
	StatusAvailable = "available"
	StatusFocusing  = "focusing"
	StatusAway      = "away"
)

func validStatus(s string) bool {
	return s == StatusAvailable || s == StatusFocusing || s == StatusAway
}

type inbound struct {
	Type    string          `json:"type"`
	Ref     string          `json:"ref"`
	Channel *ChannelRef     `json:"channel"`
	Status  string          `json:"status"`
	To      string          `json:"to"`
	Kind    string          `json:"kind"`
	Data    json.RawMessage `json:"data"`
	Audio   *bool           `json:"audio"`
	Video   *bool           `json:"video"`
}

type PresenceEntry struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

type outbound struct {
	Type          string                `json:"type"`
	Ref           string                `json:"ref,omitempty"`
	Channel       *ChannelRef           `json:"channel,omitempty"`
	UserID        string                `json:"userId,omitempty"`
	DisplayName   string                `json:"displayName,omitempty"`
	Status        string                `json:"status,omitempty"`
	Presence      []PresenceEntry       `json:"presence,omitempty"`
	Message       *chat.MessageResponse `json:"message,omitempty"`
	Reason        string                `json:"reason,omitempty"`
	MaxAgeSeconds int                   `json:"maxAgeSeconds,omitempty"`
	Code          string                `json:"code,omitempty"`
	Error         string                `json:"error,omitempty"`

	// calls
	Participants    []CallParticipant `json:"participants,omitempty"`
	MaxParticipants int               `json:"maxParticipants,omitempty"`
	From            string            `json:"from,omitempty"`
	Kind            string            `json:"kind,omitempty"`
	Data            json.RawMessage   `json:"data,omitempty"`
	Audio           *bool             `json:"audio,omitempty"` // pointer so false is sent
	Video           *bool             `json:"video,omitempty"`
}

// // internal/realtime/protocol.go
// package realtime

// import (
// 	"regexp"
// 	"strings"

// 	"roving/backend/internal/chat"
// )

// // ---------------------------------------------------------------------------
// // Wire protocol (JSON text frames over one WebSocket at GET /api/v1/ws)
// //
// // client → server
// //   {"type":"subscribe",   "channel":{"groupId":"…","roomId":"…"?}, "ref":"…"?}
// //   {"type":"unsubscribe", "channel":{…}}
// //   {"type":"typing",      "channel":{…}}
// //   {"type":"status",      "status":"available|focusing|away"}
// //   {"type":"ping"}
// //
// // server → client
// //   hello            {userId, displayName, maxAgeSeconds}
// //   subscribed       {channel, presence:[{userId,displayName,status}], ref}
// //   unsubscribed     {channel, reason}            (e.g. "access_revoked")
// //   presence.join    {channel, userId, displayName, status}
// //   presence.leave   {channel, userId}
// //   presence.status  {channel, userId, status}
// //   typing           {channel, userId, displayName}
// //   message.created | message.updated | message.deleted   {channel, message}
// //   pong, error      {code, error, ref}
// //
// // A channel is the group-wide channel (no roomId) or one room. The
// // connection is closed after MaxConnAge; clients reconnect and re-subscribe.
// // ---------------------------------------------------------------------------

// var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// type ChannelRef struct {
// 	GroupID string  `json:"groupId"`
// 	RoomID  *string `json:"roomId,omitempty"`
// }

// // normalize validates the ids and lowercases them so keys are canonical.
// func (r ChannelRef) normalize() (ChannelRef, bool) {
// 	if !uuidRe.MatchString(r.GroupID) {
// 		return r, false
// 	}
// 	out := ChannelRef{GroupID: strings.ToLower(r.GroupID)}
// 	if r.RoomID != nil {
// 		if !uuidRe.MatchString(*r.RoomID) {
// 			return r, false
// 		}
// 		id := strings.ToLower(*r.RoomID)
// 		out.RoomID = &id
// 	}
// 	return out, true
// }

// // key identifies the channel inside the hub. A room id is globally unique, so
// // room keys do not repeat the group (the authorizer already proved the room
// // belongs to the claimed group).
// func (r ChannelRef) key() string {
// 	if r.RoomID != nil {
// 		return "r:" + *r.RoomID
// 	}
// 	return "g:" + r.GroupID
// }

// const (
// 	StatusAvailable = "available"
// 	StatusFocusing  = "focusing"
// 	StatusAway      = "away"
// )

// func validStatus(s string) bool {
// 	return s == StatusAvailable || s == StatusFocusing || s == StatusAway
// }

// type inbound struct {
// 	Type    string      `json:"type"`
// 	Ref     string      `json:"ref"`
// 	Channel *ChannelRef `json:"channel"`
// 	Status  string      `json:"status"`
// }

// type PresenceEntry struct {
// 	UserID      string `json:"userId"`
// 	DisplayName string `json:"displayName"`
// 	Status      string `json:"status"`
// }

// type outbound struct {
// 	Type          string                `json:"type"`
// 	Ref           string                `json:"ref,omitempty"`
// 	Channel       *ChannelRef           `json:"channel,omitempty"`
// 	UserID        string                `json:"userId,omitempty"`
// 	DisplayName   string                `json:"displayName,omitempty"`
// 	Status        string                `json:"status,omitempty"`
// 	Presence      []PresenceEntry       `json:"presence,omitempty"`
// 	Message       *chat.MessageResponse `json:"message,omitempty"`
// 	Reason        string                `json:"reason,omitempty"`
// 	MaxAgeSeconds int                   `json:"maxAgeSeconds,omitempty"`
// 	Code          string                `json:"code,omitempty"`
// 	Error         string                `json:"error,omitempty"`
// }
// // // internal/realtime/protocol.go
// // package realtime

// // import (
// // 	"context"
// // 	"encoding/json"
// // 	"regexp"
// // 	"strings"
// // )

// // var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// // // Client -> server.
// // //
// // //	{"type":"subscribe",     "groupId":"...", "roomId":"..."|null}
// // //	{"type":"unsubscribe",   "groupId":"...", "roomId":"..."|null}
// // //	{"type":"presence.set",  "status":"online|focusing|break|away"}
// // //	{"type":"typing",        "groupId":"...", "roomId":"..."|null}
// // //	{"type":"call.signal",   "groupId":"...", "roomId":"...", "to":"<userId>",
// // //	                         "kind":"offer|answer|candidate", "data":{...}}
// // type ClientMsg struct {
// // 	Type    string  `json:"type"`
// // 	GroupID string  `json:"groupId,omitempty"`
// // 	RoomID  *string `json:"roomId,omitempty"`
// // 	Status  string  `json:"status,omitempty"`

// // 	// call.signal only: opaque WebRTC payload relayed to one peer.
// // 	To   string          `json:"to,omitempty"`
// // 	Kind string          `json:"kind,omitempty"`
// // 	Data json.RawMessage `json:"data,omitempty"`
// // }

// // // Server -> client. Topic is "g:<groupId>" (group-wide channel) or "r:<roomId>".
// // type ServerMsg struct {
// // 	Type  string `json:"type"`
// // 	Topic string `json:"topic,omitempty"`
// // 	Data  any    `json:"data,omitempty"`
// // }

// // type PresenceUser struct {
// // 	UserID string `json:"userId"`
// // 	Name   string `json:"name"`
// // 	Status string `json:"status"`
// // }

// // var validSignal = map[string]bool{"offer": true, "answer": true, "candidate": true}

// // var validStatus = map[string]bool{"online": true, "focusing": true, "break": true, "away": true}

// // func GroupTopic(groupID string) string { return "g:" + strings.ToLower(groupID) }
// // func RoomTopic(roomID string) string   { return "r:" + strings.ToLower(roomID) }

// // // Authorizer is satisfied by chat.Service (see CanAccess below). Keeping it an
// // // interface means realtime never imports the modules it protects.
// // type Authorizer interface {
// // 	CanAccess(ctx context.Context, userID, groupID string, roomID *string) error
// // }