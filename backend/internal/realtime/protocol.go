// internal/realtime/protocol.go
package realtime

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Client -> server.
//
//	{"type":"subscribe",     "groupId":"...", "roomId":"..."|null}
//	{"type":"unsubscribe",   "groupId":"...", "roomId":"..."|null}
//	{"type":"presence.set",  "status":"online|focusing|break|away"}
//	{"type":"typing",        "groupId":"...", "roomId":"..."|null}
//	{"type":"call.signal",   "groupId":"...", "roomId":"...", "to":"<userId>",
//	                         "kind":"offer|answer|candidate", "data":{...}}
type ClientMsg struct {
	Type    string  `json:"type"`
	GroupID string  `json:"groupId,omitempty"`
	RoomID  *string `json:"roomId,omitempty"`
	Status  string  `json:"status,omitempty"`

	// call.signal only: opaque WebRTC payload relayed to one peer.
	To   string          `json:"to,omitempty"`
	Kind string          `json:"kind,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Server -> client. Topic is "g:<groupId>" (group-wide channel) or "r:<roomId>".
type ServerMsg struct {
	Type  string `json:"type"`
	Topic string `json:"topic,omitempty"`
	Data  any    `json:"data,omitempty"`
}

type PresenceUser struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

var validSignal = map[string]bool{"offer": true, "answer": true, "candidate": true}

var validStatus = map[string]bool{"online": true, "focusing": true, "break": true, "away": true}

func GroupTopic(groupID string) string { return "g:" + strings.ToLower(groupID) }
func RoomTopic(roomID string) string   { return "r:" + strings.ToLower(roomID) }

// Authorizer is satisfied by chat.Service (see CanAccess below). Keeping it an
// interface means realtime never imports the modules it protects.
type Authorizer interface {
	CanAccess(ctx context.Context, userID, groupID string, roomID *string) error
}