package stream

import (
	"context"
	"encoding/json"
	"time"

	"roving/backend/internal/realtime"
)

const (
	TypeStreamJoin   = "stream.join"   // client -> server
	TypeStreamLeave  = "stream.leave"  // client -> server
	TypeStreamSignal = "stream.signal" // both ways: offer | answer | ice
	TypeStreamState  = "stream.state"  // server -> everyone in the room: the roster
	TypeStreamJoined = "stream.joined" // server -> the joiner only: the role you got
	TypeStreamMedia  = "stream.media"  // host -> server: {"roomId","camera":bool}
)

// RoleResolver decides, from the database, whether this user may enter the room
// and whether they are the publisher. main.go supplies a closure over the rooms
// service, which keeps this package free of database code.
// Return an error to refuse entry.
type RoleResolver func(ctx context.Context, userID, groupID, roomID string) (Role, error)

// Register wires the stream messages onto the hub. Call once from main.go.
func Register(h *realtime.Hub, reg *Registry, resolve RoleResolver) {

	h.Handle(TypeStreamJoin, func(ctx context.Context, c *realtime.Client, env realtime.Envelope) {
		var in struct {
			GroupID string `json:"groupId"`
			RoomID  string `json:"roomId"`
		}
		if err := json.Unmarshal(env.Payload, &in); err != nil || in.RoomID == "" || in.GroupID == "" {
			sendErr(c, env.ID, "bad_message", `stream.join needs {"groupId","roomId"}`)
			return
		}

		// THE authorization step. The role comes from the database, never from the client.
		role, err := resolve(ctx, c.UserID, in.GroupID, in.RoomID)
		if err != nil {
			sendErr(c, env.ID, "forbidden", "you cannot join this room")
			return
		}

		s, effective, left := reg.join(in.GroupID, in.RoomID, &Participant{
			ConnID: c.ID, UserID: c.UserID, Role: role, JoinedAt: time.Now(),
		})

		// The connection may have dropped while the database was being asked.
		if !h.HasConnection(c.ID) {
			if gone, still := reg.leave(c.ID); gone != nil && still {
				reg.broadcastState(gone, h)
			}
			return
		}

		c.Send(realtime.Envelope{Type: TypeStreamJoined, ID: env.ID, Payload: mustJSON(map[string]any{
			"roomId":       in.RoomID,
			"role":         effective,
			"connectionId": c.ID,
		})})
		if left != nil {
			reg.broadcastState(left, h)
		}
		reg.broadcastState(s, h)
	})

	h.Handle(TypeStreamLeave, func(_ context.Context, c *realtime.Client, _ realtime.Envelope) {
		if s, still := reg.leave(c.ID); s != nil && still {
			reg.broadcastState(s, h)
		}
	})

	// The host tells the room whether their camera is on. Video that stops
	// flowing is not a reliable signal in browsers, so this is explicit.
	h.Handle(TypeStreamMedia, func(_ context.Context, c *realtime.Client, env realtime.Envelope) {
		var in struct {
			RoomID string `json:"roomId"`
			Camera bool   `json:"camera"`
		}
		if err := json.Unmarshal(env.Payload, &in); err != nil || in.RoomID == "" {
			sendErr(c, env.ID, "bad_message", `stream.media needs {"roomId","camera"}`)
			return
		}
		s := reg.get(in.RoomID)
		if s == nil || !s.has(c.ID) {
			sendErr(c, env.ID, "forbidden", "not in this room")
			return
		}
		if s.setHostCamera(c.ID, in.Camera) { // false for anyone but the host: ignored
			reg.broadcastState(s, h)
		}
	})

	h.Handle(TypeStreamSignal, func(_ context.Context, c *realtime.Client, env realtime.Envelope) {
		var in struct {
			RoomID string          `json:"roomId"`
			To     string          `json:"to"`
			Kind   string          `json:"kind"` // offer | answer | ice
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(env.Payload, &in); err != nil || in.RoomID == "" || in.To == "" || len(in.Data) == 0 {
			sendErr(c, env.ID, "bad_message", "stream.signal needs roomId, to, kind and data")
			return
		}
		if in.Kind != "offer" && in.Kind != "answer" && in.Kind != "ice" {
			sendErr(c, env.ID, "bad_message", "unknown signal kind")
			return
		}

		s := reg.get(in.RoomID)
		// Both ends must be in this room's stream: nobody can signal a stranger.
		if s == nil || !s.has(c.ID) || !s.has(in.To) {
			sendErr(c, env.ID, "forbidden", "not in this room")
			return
		}
		// Only the publisher may send offers, and only the publisher's peers
		// answer them. A viewer cannot publish video by forging an offer.
		if in.Kind == "offer" && !s.isHostConn(c.ID) {
			sendErr(c, env.ID, "forbidden", "only the host may send offers")
			return
		}
		if !s.isHostConn(c.ID) && !s.isHostConn(in.To) {
			sendErr(c, env.ID, "forbidden", "viewers only connect to the host")
			return
		}

		out, _ := json.Marshal(map[string]any{
			"roomId": in.RoomID,
			"to":     in.To,
			"from":   c.ID,
			"kind":   in.Kind,
			"data":   in.Data,
		})
		if !h.SendToConnection(in.To, realtime.Envelope{Type: TypeStreamSignal, ID: env.ID, Payload: out}) {
			sendErr(c, env.ID, "peer_gone", "the other connection has left")
		}
	})

	// A closed tab or a dropped network never sends stream.leave.
	h.OnDisconnect(func(c *realtime.Client) {
		if s, still := reg.leave(c.ID); s != nil && still {
			reg.broadcastState(s, h)
		}
	})
}

func sendErr(c *realtime.Client, id, code, msg string) {
	c.Send(realtime.Envelope{Type: realtime.TypeError, ID: id, Payload: mustJSON(map[string]string{"code": code, "message": msg})})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
