// Package realtime is Rovingr's WebSocket layer.
//
// Stage 1 (this stage) provides only the foundation:
//   - an authenticated upgrade (cookie session, origin-checked)
//   - a connection registry (who is connected, how many tabs)
//   - heartbeat, rate limiting, session re-validation and graceful shutdown
//   - a tiny message router with two built-in messages: "ping" and "echo"
//
// Later stages (presence, WebRTC signalling, chat delivery) plug in by calling
// Hub.Handle("some.type", fn) and Hub.SendToUser(...). Nothing here knows about
// groups, rooms, calls or chat on purpose: those are separate concepts.
package realtime

import (
	"encoding/json"
	"errors"
)

// Envelope is the JSON shape of every message in both directions.
//
//	{"type":"echo","id":"c-7","payload":{"text":"hi"}}
//
// type    names the message and decides which handler runs.
// id      is optional and chosen by the sender. Replies echo it back so the
//
//	sender can match a reply to its request.
//
// payload is a type-specific JSON object.
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Message types defined in stage 1.
const (
	TypeHello = "hello" // server -> client, first message after connecting
	TypePing  = "ping"  // client -> server
	TypePong  = "pong"  // server -> client
	TypeEcho  = "echo"  // both ways (test message)
	TypeError = "error" // server -> client
)

// Error codes sent in an "error" message payload.
const (
	CodeBadMessage  = "bad_message"
	CodeUnknownType = "unknown_type"
	CodeInternal    = "internal"
)

// HelloPayload is sent once, right after the connection is accepted.
type HelloPayload struct {
	ConnectionID    string `json:"connectionId"`
	UserID          string `json:"userId"`
	ServerTime      string `json:"serverTime"` // RFC 3339
	MaxMessageBytes int64  `json:"maxMessageBytes"`
}

// ErrorPayload is the payload of an "error" message.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrSessionInvalid is what Config.Reauth must return when the session behind a
// connection has ended (logout, expiry, revoked). Any other error is treated as
// a temporary failure (for example the database is busy) and the connection is
// kept.
var ErrSessionInvalid = errors.New("realtime: session is no longer valid")

func payloadOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
