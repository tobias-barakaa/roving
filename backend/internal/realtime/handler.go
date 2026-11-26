package realtime

import (
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

// Serve upgrades an already-authenticated request to a WebSocket and runs the
// connection until it ends. The caller (routes.go) has verified the session
// and passes the user id plus the raw session cookie value.
//
// allowedOrigin is the frontend origin (cfg.FrontendOrigin). Browsers always
// send an Origin header on WebSocket handshakes and cookies ride along
// automatically, so checking Origin is what stops another website from
// opening a socket as your logged-in user (cross-site WebSocket hijacking).
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID, sessionToken, allowedOrigin string) {
	if h.isClosed() {
		http.Error(w, "server is restarting", http.StatusServiceUnavailable)
		return
	}
	if h.UserConnections(userID) >= h.cfg.MaxConnsPerUser {
		http.Error(w, "too many open connections", http.StatusTooManyRequests)
		return
	}

	opts := &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	}
	if u, err := url.Parse(allowedOrigin); err == nil && u.Host != "" {
		opts.OriginPatterns = []string{u.Host}
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return // Accept already wrote the HTTP error (403 for a bad origin)
	}

	c := newClient(h, conn, userID, sessionToken)
	if err := h.register(c); err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	h.cfg.Logf("[realtime] connected conn=%s user=%s", c.ID, c.UserID)

	c.Send(Envelope{Type: TypeHello, Payload: payloadOf(HelloPayload{
		ConnectionID:    c.ID,
		UserID:          c.UserID,
		ServerTime:      time.Now().UTC().Format(time.RFC3339),
		MaxMessageBytes: h.cfg.MaxMessageBytes,
	})})
	c.run(r.Context())
}


// // backend/internal/realtime/handler.go

// package realtime

// import (
// 	"net/http"
// 	"net/url"
// 	"time"

// 	"github.com/coder/websocket"
// )

// // Serve upgrades an already-authenticated request to a WebSocket and runs the
// // connection until it ends. The caller (routes.go) has verified the session
// // and passes the user id plus the raw session cookie value.
// //
// // allowedOrigin is the frontend origin (cfg.FrontendOrigin). Browsers always
// // send an Origin header on WebSocket handshakes and cookies ride along
// // automatically, so checking Origin is what stops another website from
// // opening a socket as your logged-in user (cross-site WebSocket hijacking).
// func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID, sessionToken, allowedOrigin string) {
// 	if h.isClosed() {
// 		http.Error(w, "server is restarting", http.StatusServiceUnavailable)
// 		return
// 	}
// 	if h.UserConnections(userID) >= h.cfg.MaxConnsPerUser {
// 		http.Error(w, "too many open connections", http.StatusTooManyRequests)
// 		return
// 	}

// 	opts := &websocket.AcceptOptions{
// 		CompressionMode: websocket.CompressionDisabled,
// 	}
// 	if u, err := url.Parse(allowedOrigin); err == nil && u.Host != "" {
// 		opts.OriginPatterns = []string{u.Host}
// 	}
// 	conn, err := websocket.Accept(w, r, opts)
// 	if err != nil {
// 		return // Accept already wrote the HTTP error (403 for a bad origin)
// 	}

// 	c := newClient(h, conn, userID, sessionToken)
// 	if err := h.register(c); err != nil {
// 		_ = conn.Close(websocket.StatusTryAgainLater, err.Error())
// 		return
// 	}
// 	h.cfg.Logf("[realtime] connected conn=%s user=%s", c.ID, c.UserID)

// 	c.Send(Envelope{Type: TypeHello, Payload: payloadOf(HelloPayload{
// 		ConnectionID:    c.ID,
// 		UserID:          c.UserID,
// 		ServerTime:      time.Now().UTC().Format(time.RFC3339),
// 		MaxMessageBytes: h.cfg.MaxMessageBytes,
// 	})})
// 	c.run(r.Context())
// }
