package realtime

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"
)

const (
	TypeChatSend = "chat.send" // client -> server
	TypeChatMsg  = "chat.msg"  // server -> every connection in the room
	maxChatRunes = 2000
)

// ChatPayload is what the browser sends and what every receiver sees.
type ChatPayload struct {
	RoomID string `json:"roomId"`
	Text   string `json:"text,omitempty"`
	UserID string `json:"userId,omitempty"` // server-set on broadcast
	SentAt string `json:"sentAt,omitempty"` // server-set on broadcast
}

// Chatroom is the minimal interface chat needs from a room registry.
// stream.Registry satisfies it. Membership is whatever the registry says it is:
// only connections that were authorized to join the room are listed.
type Chatroom interface {
	RoomConnections(roomID string) []string
}

// RegisterChat installs chat.send. Call ONCE from main.go, next to stream.Register.
//
// Live delivery only: messages are not stored yet (persistent chat is its own stage).
func RegisterChat(h *Hub, rooms Chatroom) {
	h.Handle(TypeChatSend, func(ctx context.Context, c *Client, env Envelope) {
		var in ChatPayload
		if err := json.Unmarshal(env.Payload, &in); err != nil || in.RoomID == "" {
			c.sendError(env.ID, CodeBadMessage, `chat.send needs {"roomId","text"}`)
			return
		}
		if in.Text == "" || utf8.RuneCountInString(in.Text) > maxChatRunes {
			c.sendError(env.ID, CodeBadMessage, "chat text is empty or too long")
			return
		}

		conns := rooms.RoomConnections(in.RoomID)
		// The sender must itself be in the room, otherwise anyone could write
		// into any room just by knowing its id.
		member := false
		for _, id := range conns {
			if id == c.ID {
				member = true
				break
			}
		}
		if !member {
			c.sendError(env.ID, "forbidden", "join the room before chatting")
			return
		}

		payload, _ := json.Marshal(ChatPayload{
			RoomID: in.RoomID,
			Text:   in.Text,
			UserID: c.UserID,
			SentAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		envelope := Envelope{Type: TypeChatMsg, Payload: payload}
		for _, id := range conns {
			h.SendToConnection(id, envelope)
		}
	})
}


// package realtime

// import (
// 	"context"
// 	"encoding/json"
// 	"time"
// 	"unicode/utf8"
// )

// const (
// 	TypeChatSend = "chat.send" // client -> server
// 	TypeChatMsg  = "chat.msg"  // server -> every member of a room
// 	maxChatRunes = 2000
// )

// // ChatPayload is what the browser sends and what every receiver sees.
// type ChatPayload struct {
// 	RoomID string `json:"roomId"`
// 	Text   string `json:"text,omitempty"`
// 	UserID string `json:"userId,omitempty"` // server-set on broadcast
// 	SentAt string `json:"sentAt,omitempty"` // server-set on broadcast
// }

// // Chatroom is the minimal interface chat needs from a stream registry.
// // stream.Registry satisfies it via RoomConnections.
// type Chatroom interface {
// 	RoomConnections(roomID string) []string
// 	HasConn(roomID, connID string) bool
// }

// // RegisterChat installs chat.send. Call once from main.go after stream.Register.
// func RegisterChat(h *Hub, rooms Chatroom) {
// 	h.Handle(TypeChatSend, func(ctx context.Context, c *Client, env Envelope) {
// 		var in ChatPayload
// 		if err := json.Unmarshal(env.Payload, &in); err != nil || in.RoomID == "" {
// 			c.sendError(env.ID, CodeBadMessage, `chat.send needs {"roomId","text"}`)
// 			return
// 		}
// 		if in.Text == "" || utf8.RuneCountInString(in.Text) > maxChatRunes {
// 			c.sendError(env.ID, CodeBadMessage, "chat text is empty or too long")
// 			return
// 		}

// 		if !rooms.HasConn(in.RoomID, c.ID) {
//     c.sendError(env.ID, "forbidden", "join the room before chatting")
//     return
// 		}
		
// 		conns := rooms.RoomConnections(in.RoomID)
// 		if len(conns) == 0 {
// 			return // nobody in the room; message goes nowhere
// 		}

// 		out := ChatPayload{
// 			RoomID: in.RoomID,
// 			Text:   in.Text,
// 			UserID: c.UserID,
// 			SentAt: time.Now().UTC().Format(time.RFC3339Nano),
// 		}
// 		payload, _ := json.Marshal(out)
// 		envelope := Envelope{Type: TypeChatMsg, Payload: payload}

// 		for _, connID := range conns {
// 			h.SendToConnection(connID, envelope)
// 		}
// 	})
// }