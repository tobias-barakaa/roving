// internal/chess/handler.go
package chess

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"roving/backend/internal/auth"
)

type Handler struct {
	svc         *Service
	hub         *Hub
	requireAuth gin.HandlerFunc
	upgrader    websocket.Upgrader
}

// allowedOrigin is cfg.FrontendOrigin. Browsers always send Origin on a
// WebSocket handshake and cookies ride along automatically, so it must match
// exactly or any website could open a socket as a logged-in user.
func NewHandler(svc *Service, hub *Hub, requireAuth gin.HandlerFunc, allowedOrigin string) *Handler {
	allowed := strings.TrimRight(allowedOrigin, "/")
	return &Handler{
		svc:         svc,
		hub:         hub,
		requireAuth: requireAuth,
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
	g := rg.Group("/groups/:groupId/chess/games", h.requireAuth)
	{
		g.POST("", h.create)
		g.GET("/:id", h.get)
		g.POST("/:id/join", h.join)
		g.POST("/:id/move", h.move)
		g.GET("/:id/moves", h.moves)
		g.GET("/:id/legal-moves", h.legalMoves)
		g.POST("/:id/resign", h.resign)
		g.POST("/:id/draw/offer", h.offerDraw)
		g.POST("/:id/draw/accept", h.acceptDraw)
		g.POST("/:id/draw/decline", h.declineDraw)
		g.GET("/:id/ws", h.ws)
	}
}

func (h *Handler) create(c *gin.Context) {
	var req CreateGameRequest
	if !bind(c, &req) {
		return
	}
	state, err := h.svc.Create(c.Request.Context(), auth.UserID(c), c.Param("groupId"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, state)
}

func (h *Handler) get(c *gin.Context) {
	state, err := h.svc.Get(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) join(c *gin.Context) {
	state, err := h.svc.Join(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) move(c *gin.Context) {
	var req MoveRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.svc.Move(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *Handler) moves(c *gin.Context) {
	state, err := h.svc.Get(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"moves": state.Moves})
}

func (h *Handler) legalMoves(c *gin.Context) {
	res, err := h.svc.LegalMoves(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *Handler) resign(c *gin.Context) {
	state, err := h.svc.Resign(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) offerDraw(c *gin.Context) {
	state, err := h.svc.OfferDraw(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) acceptDraw(c *gin.Context) {
	state, err := h.svc.AcceptDraw(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) declineDraw(c *gin.Context) {
	state, err := h.svc.DeclineDraw(c.Request.Context(), auth.UserID(c), c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) ws(c *gin.Context) {
	userID := auth.UserID(c)
	state, err := h.svc.Connect(c.Request.Context(), userID, c.Param("groupId"), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}

	// On failure Upgrade has already written the HTTP error response.
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	cl := h.hub.register(userID, c.Param("id"), conn, state)
	if cl == nil {
		_ = conn.Close()
		return
	}
	cl.readPump() // blocks until the connection ends
}

// ---------- helpers ----------

func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<16)
	if err := c.ShouldBindJSON(dst); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "code": "invalid_request"})
		return false
	}
	return true
}

func (h *Handler) writeErr(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	msg := "something went wrong"
	code := "internal"

	switch {
	case errors.Is(err, ErrNotFound):
		status, msg, code = http.StatusNotFound, "not found", "not_found"
	case errors.Is(err, ErrForbidden):
		status, msg, code = http.StatusForbidden, "you do not have permission to do that", "forbidden"
	case errors.Is(err, ErrNotAPlayer):
		status, msg, code = http.StatusForbidden, "you are not a player in this game", "not_a_player"
	case errors.Is(err, ErrInvalid):
		status, msg, code = http.StatusBadRequest, err.Error(), "invalid_request"
	case errors.Is(err, ErrIllegalMove):
		status, msg, code = http.StatusBadRequest, "illegal move", "illegal_move"
	case errors.Is(err, ErrPromotionRequired):
		status, msg, code = http.StatusBadRequest, "a promotion piece is required", "promotion_required"
	case errors.Is(err, ErrNotYourTurn):
		status, msg, code = http.StatusConflict, "not your turn", "not_your_turn"
	case errors.Is(err, ErrStaleMove):
		status, msg, code = http.StatusConflict, "stale move, please refresh", "stale_move"
	case errors.Is(err, ErrGameNotActive):
		status, msg, code = http.StatusConflict, "game is not active", "game_over"
	case errors.Is(err, ErrFlagFall):
		status, msg, code = http.StatusConflict, "time expired", "game_over"
	case errors.Is(err, ErrGameFull):
		status, msg, code = http.StatusConflict, "game already has two players", "game_full"
	case errors.Is(err, ErrAlreadyJoined):
		status, msg, code = http.StatusConflict, "you are already in this game", "already_joined"
	default:
		log.Printf("chess error: %v", err)
	}

	c.JSON(status, gin.H{"error": msg, "code": code})
}
