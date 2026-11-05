// internal/realtime/handler.go
package realtime

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"roving/backend/internal/auth"
	"roving/backend/internal/chat"
)

type Handler struct {
	hub         *Hub
	db          *pgxpool.Pool
	requireAuth gin.HandlerFunc
	upgrader    websocket.Upgrader
	ice         ICEConfig
}

// allowedOrigin is cfg.FrontendOrigin. Browsers always send Origin on a
// WebSocket handshake and cookies ride along automatically, so it must match
// exactly or any website could open a socket as a logged-in user.
// Requests without an Origin header are not browsers and cannot carry a
// victim's cookies, so they are allowed.
func NewHandler(hub *Hub, db *pgxpool.Pool, requireAuth gin.HandlerFunc, allowedOrigin string) *Handler {
	allowed := strings.TrimRight(allowedOrigin, "/")
	return &Handler{
		hub:         hub,
		db:          db,
		requireAuth: requireAuth,
		ice:         LoadICEConfig(), // STUN_URLS / TURN_URLS / TURN_SECRET
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				return origin == "" || strings.EqualFold(strings.TrimRight(origin, "/"), allowed)
			},
		},
	}
}

func (h *Handler) Routes(rg *gin.RouterGroup) {
	rg.GET("/ws", h.requireAuth, h.serve)

	// Who is online, without opening a socket. The "presence" segment is new
	// under /groups/:id, so it cannot clash with the other modules' routes.
	g := rg.Group("/groups/:id/presence", h.requireAuth)
	{
		g.GET("", h.groupPresence)
		g.GET("/rooms/:rid", h.roomPresence)
	}

	// Audio/video: STUN/TURN settings, and "is a call running here?".
	rg.GET("/rtc/ice-servers", h.requireAuth, h.iceServers)
	c := rg.Group("/groups/:id/call", h.requireAuth)
	{
		c.GET("", h.groupCall)
		c.GET("/rooms/:rid", h.roomCall)
	}
}

// serve upgrades the request (already authenticated by requireAuth via the
// session cookie) and runs the connection until it ends.
func (h *Handler) serve(c *gin.Context) {
	userID := auth.UserID(c)

	var name string
	err := h.db.QueryRow(c.Request.Context(),
		`SELECT display_name FROM users WHERE id = $1::uuid`, userID).Scan(&name)
	if err != nil {
		log.Printf("realtime: load user: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
		return
	}

	// On failure Upgrade has already written the HTTP error response.
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	cl := newClient(h.hub, conn, userID, name)
	if !h.hub.register(cl) {
		cl.closeConn(websocket.CloseGoingAway, "server shutting down")
		return
	}
	go cl.writePump()

	// Sessions can expire while a socket stays open, so connections are
	// recycled; the client reconnects and re-subscribes.
	timer := time.AfterFunc(MaxConnAge, func() {
		cl.closeConn(websocket.CloseGoingAway, "session refresh")
	})
	defer timer.Stop()

	cl.sendJSON(outbound{
		Type: "hello", UserID: userID, DisplayName: name,
		MaxAgeSeconds: int(MaxConnAge.Seconds()),
	})
	cl.readPump() // blocks until the connection ends
}

func (h *Handler) groupPresence(c *gin.Context) {
	h.presence(c, c.Param("id"), nil)
}

func (h *Handler) roomPresence(c *gin.Context) {
	rid := c.Param("rid")
	h.presence(c, c.Param("id"), &rid)
}

func (h *Handler) presence(c *gin.Context, groupID string, roomID *string) {
	ref, ok := ChannelRef{GroupID: groupID, RoomID: roomID}.normalize()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	err := h.hub.auth.CanAccessChannel(c.Request.Context(), auth.UserID(c), ref.GroupID, ref.RoomID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"online": h.hub.PresenceFor(ref)})
	case errors.Is(err, chat.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, chat.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
	default:
		log.Printf("realtime presence error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}

// // internal/realtime/handler.go
// package realtime

// import (
// 	"errors"
// 	"log"
// 	"net/http"
// 	"strings"
// 	"time"

// 	"github.com/gin-gonic/gin"
// 	"github.com/gorilla/websocket"
// 	"github.com/jackc/pgx/v5/pgxpool"

// 	"roving/backend/internal/auth"
// 	"roving/backend/internal/chat"
// )

// type Handler struct {
// 	hub         *Hub
// 	db          *pgxpool.Pool
// 	requireAuth gin.HandlerFunc
// 	upgrader    websocket.Upgrader
// }

// // allowedOrigin is cfg.FrontendOrigin. Browsers always send Origin on a
// // WebSocket handshake and cookies ride along automatically, so it must match
// // exactly or any website could open a socket as a logged-in user.
// // Requests without an Origin header are not browsers and cannot carry a
// // victim's cookies, so they are allowed.
// func NewHandler(hub *Hub, db *pgxpool.Pool, requireAuth gin.HandlerFunc, allowedOrigin string) *Handler {
// 	allowed := strings.TrimRight(allowedOrigin, "/")
// 	return &Handler{
// 		hub:         hub,
// 		db:          db,
// 		requireAuth: requireAuth,
// 		upgrader: websocket.Upgrader{
// 			ReadBufferSize:  1024,
// 			WriteBufferSize: 1024,
// 			CheckOrigin: func(r *http.Request) bool {
// 				origin := r.Header.Get("Origin")
// 				return origin == "" || strings.EqualFold(strings.TrimRight(origin, "/"), allowed)
// 			},
// 		},
// 	}
// }

// func (h *Handler) Routes(rg *gin.RouterGroup) {
// 	rg.GET("/ws", h.requireAuth, h.serve)

// 	// Who is online, without opening a socket. The "presence" segment is new
// 	// under /groups/:id, so it cannot clash with the other modules' routes.
// 	g := rg.Group("/groups/:id/presence", h.requireAuth)
// 	{
// 		g.GET("", h.groupPresence)
// 		g.GET("/rooms/:rid", h.roomPresence)
// 	}
// }

// // serve upgrades the request (already authenticated by requireAuth via the
// // session cookie) and runs the connection until it ends.
// func (h *Handler) serve(c *gin.Context) {
// 	userID := auth.UserID(c)

// 	var name string
// 	err := h.db.QueryRow(c.Request.Context(),
// 		`SELECT display_name FROM users WHERE id = $1::uuid`, userID).Scan(&name)
// 	if err != nil {
// 		log.Printf("realtime: load user: %v", err)
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
// 		return
// 	}

// 	// On failure Upgrade has already written the HTTP error response.
// 	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
// 	if err != nil {
// 		return
// 	}

// 	cl := newClient(h.hub, conn, userID, name)
// 	if !h.hub.register(cl) {
// 		cl.closeConn(websocket.CloseGoingAway, "server shutting down")
// 		return
// 	}
// 	go cl.writePump()

// 	// Sessions can expire while a socket stays open, so connections are
// 	// recycled; the client reconnects and re-subscribes.
// 	timer := time.AfterFunc(MaxConnAge, func() {
// 		cl.closeConn(websocket.CloseGoingAway, "session refresh")
// 	})
// 	defer timer.Stop()

// 	cl.sendJSON(outbound{
// 		Type: "hello", UserID: userID, DisplayName: name,
// 		MaxAgeSeconds: int(MaxConnAge.Seconds()),
// 	})
// 	cl.readPump() // blocks until the connection ends
// }

// func (h *Handler) groupPresence(c *gin.Context) {
// 	h.presence(c, c.Param("id"), nil)
// }

// func (h *Handler) roomPresence(c *gin.Context) {
// 	rid := c.Param("rid")
// 	h.presence(c, c.Param("id"), &rid)
// }

// func (h *Handler) presence(c *gin.Context, groupID string, roomID *string) {
// 	ref, ok := ChannelRef{GroupID: groupID, RoomID: roomID}.normalize()
// 	if !ok {
// 		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
// 		return
// 	}
// 	err := h.hub.auth.CanAccessChannel(c.Request.Context(), auth.UserID(c), ref.GroupID, ref.RoomID)
// 	switch {
// 	case err == nil:
// 		c.JSON(http.StatusOK, gin.H{"online": h.hub.PresenceFor(ref)})
// 	case errors.Is(err, chat.ErrNotFound):
// 		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
// 	case errors.Is(err, chat.ErrForbidden):
// 		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
// 	default:
// 		log.Printf("realtime presence error: %v", err)
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
// 	}
// }

// // // internal/realtime/handler.go
// // package realtime

// // import (
// // 	"context"
// // 	"log"
// // 	"net/http"
// // 	"time"

// // 	"github.com/gin-gonic/gin"
// // 	"github.com/gorilla/websocket"
// // 	"github.com/jackc/pgx/v5/pgxpool"

// // 	"roving/backend/internal/auth"
// // )

// // type Handler struct {
// // 	hub         *Hub
// // 	db          *pgxpool.Pool
// // 	authz       Authorizer
// // 	requireAuth gin.HandlerFunc
// // 	upgrader    websocket.Upgrader
// // }

// // func NewHandler(hub *Hub, db *pgxpool.Pool, authz Authorizer, requireAuth gin.HandlerFunc, frontendOrigin string) *Handler {
// // 	return &Handler{
// // 		hub: hub, db: db, authz: authz, requireAuth: requireAuth,
// // 		upgrader: websocket.Upgrader{
// // 			ReadBufferSize:  1024,
// // 			WriteBufferSize: 1024,
// // 			// Browsers always send Origin on WS handshakes, and cookies ride along,
// // 			// so this check is what stops cross-site WebSocket hijacking.
// // 			CheckOrigin: func(r *http.Request) bool {
// // 				o := r.Header.Get("Origin")
// // 				return o == "" || o == frontendOrigin
// // 			},
// // 		},
// // 	}
// // }

// // func (h *Handler) Routes(rg *gin.RouterGroup) {
// // 	rg.GET("/ws", h.requireAuth, h.serve)
// // }

// // func (h *Handler) serve(c *gin.Context) {
// // 	userID := auth.UserID(c)

// // 	var name string
// // 	if err := h.db.QueryRow(c.Request.Context(),
// // 		`SELECT display_name FROM users WHERE id = $1::uuid`, userID).Scan(&name); err != nil {
// // 		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
// // 		return
// // 	}

// // 	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
// // 	if err != nil {
// // 		log.Printf("realtime: upgrade: %v", err) // Upgrade already replied
// // 		return
// // 	}
// // 	cl := &Client{
// // 		hub: h.hub, conn: conn, auth: h.authz,
// // 		userID: userID, name: name,
// // 		send: make(chan []byte, sendBuffer),
// // 		subs: make(map[string]struct{}),
// // 	}
// // 	// NOTE: the request context dies when this handler returns, so the pumps
// // 	// must not use it (they use their own timeouts).
// // 	go cl.writePump()
// // 	go cl.readPump()
// // }

// // var _ = context.Background
// // var _ = time.Second