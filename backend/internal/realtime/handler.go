// internal/realtime/handler.go
package realtime

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"roving/backend/internal/auth"
)

type Handler struct {
	hub         *Hub
	db          *pgxpool.Pool
	authz       Authorizer
	requireAuth gin.HandlerFunc
	upgrader    websocket.Upgrader
}

func NewHandler(hub *Hub, db *pgxpool.Pool, authz Authorizer, requireAuth gin.HandlerFunc, frontendOrigin string) *Handler {
	return &Handler{
		hub: hub, db: db, authz: authz, requireAuth: requireAuth,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			// Browsers always send Origin on WS handshakes, and cookies ride along,
			// so this check is what stops cross-site WebSocket hijacking.
			CheckOrigin: func(r *http.Request) bool {
				o := r.Header.Get("Origin")
				return o == "" || o == frontendOrigin
			},
		},
	}
}

func (h *Handler) Routes(rg *gin.RouterGroup) {
	rg.GET("/ws", h.requireAuth, h.serve)
}

func (h *Handler) serve(c *gin.Context) {
	userID := auth.UserID(c)

	var name string
	if err := h.db.QueryRow(c.Request.Context(),
		`SELECT display_name FROM users WHERE id = $1::uuid`, userID).Scan(&name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("realtime: upgrade: %v", err) // Upgrade already replied
		return
	}
	cl := &Client{
		hub: h.hub, conn: conn, auth: h.authz,
		userID: userID, name: name,
		send: make(chan []byte, sendBuffer),
		subs: make(map[string]struct{}),
	}
	// NOTE: the request context dies when this handler returns, so the pumps
	// must not use it (they use their own timeouts).
	go cl.writePump()
	go cl.readPump()
}

var _ = context.Background
var _ = time.Second