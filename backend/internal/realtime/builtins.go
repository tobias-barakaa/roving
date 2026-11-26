package realtime

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"
)

const maxEchoRunes = 1000

// registerBuiltins installs the two stage-1 messages. They exist so the
// connection can be tested end to end before any real feature is built.
func (h *Hub) registerBuiltins() {
	// ping -> pong: lets the browser measure round-trip time and notice a dead
	// connection. Browsers cannot send protocol-level pings, so this is the
	// client-side heartbeat.
	h.handlers[TypePing] = func(_ context.Context, c *Client, env Envelope) {
		c.Send(Envelope{Type: TypePong, ID: env.ID, Payload: payloadOf(map[string]any{
			"serverTime": time.Now().UTC().Format(time.RFC3339Nano),
		})})
	}

	// echo: the server answers with what it received plus who it came from.
	// With "toAllMyTabs": true it also delivers the message to the user's other
	// open connections, which exercises the per-user registry.
	h.handlers[TypeEcho] = func(_ context.Context, c *Client, env Envelope) {
		var in struct {
			Text        string `json:"text"`
			ToAllMyTabs bool   `json:"toAllMyTabs"`
		}
		if err := json.Unmarshal(env.Payload, &in); err != nil || in.Text == "" {
			c.sendError(env.ID, CodeBadMessage, `echo needs a payload like {"text":"hello"}`)
			return
		}
		if utf8.RuneCountInString(in.Text) > maxEchoRunes {
			c.sendError(env.ID, CodeBadMessage, "echo text is too long")
			return
		}
		out := map[string]any{
			"text":         in.Text,
			"from":         c.UserID,
			"connectionId": c.ID,
			"receivedAt":   time.Now().UTC().Format(time.RFC3339Nano),
		}
		c.Send(Envelope{Type: TypeEcho, ID: env.ID, Payload: payloadOf(out)})

		if in.ToAllMyTabs {
			out["fanout"] = true
			h.SendToUser(c.UserID, Envelope{Type: TypeEcho, Payload: payloadOf(out)}, c.ID)
		}
	}
}


// // backend/internal/realtime/builtins.go
// package realtime

// import (
// 	"context"
// 	"encoding/json"
// 	"time"
// 	"unicode/utf8"
// )

// const maxEchoRunes = 1000

// // registerBuiltins installs the two stage-1 messages. They exist so the
// // connection can be tested end to end before any real feature is built.
// func (h *Hub) registerBuiltins() {
// 	// ping -> pong: lets the browser measure round-trip time and notice a dead
// 	// connection. Browsers cannot send protocol-level pings, so this is the
// 	// client-side heartbeat.
// 	h.handlers[TypePing] = func(_ context.Context, c *Client, env Envelope) {
// 		c.Send(Envelope{Type: TypePong, ID: env.ID, Payload: payloadOf(map[string]any{
// 			"serverTime": time.Now().UTC().Format(time.RFC3339Nano),
// 		})})
// 	}

// 	// echo: the server answers with what it received plus who it came from.
// 	// With "toAllMyTabs": true it also delivers the message to the user's other
// 	// open connections, which exercises the per-user registry.
// 	h.handlers[TypeEcho] = func(_ context.Context, c *Client, env Envelope) {
// 		var in struct {
// 			Text        string `json:"text"`
// 			ToAllMyTabs bool   `json:"toAllMyTabs"`
// 		}
// 		if err := json.Unmarshal(env.Payload, &in); err != nil || in.Text == "" {
// 			c.sendError(env.ID, CodeBadMessage, `echo needs a payload like {"text":"hello"}`)
// 			return
// 		}
// 		if utf8.RuneCountInString(in.Text) > maxEchoRunes {
// 			c.sendError(env.ID, CodeBadMessage, "echo text is too long")
// 			return
// 		}
// 		out := map[string]any{
// 			"text":         in.Text,
// 			"from":         c.UserID,
// 			"connectionId": c.ID,
// 			"receivedAt":   time.Now().UTC().Format(time.RFC3339Nano),
// 		}
// 		c.Send(Envelope{Type: TypeEcho, ID: env.ID, Payload: payloadOf(out)})

// 		if in.ToAllMyTabs {
// 			out["fanout"] = true
// 			h.SendToUser(c.UserID, Envelope{Type: TypeEcho, Payload: payloadOf(out)}, c.ID)
// 		}
// 	}
// }
