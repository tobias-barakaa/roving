// internal/chat/handler.go
package chat

import (
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"roving/backend/internal/auth"
)

type Handler struct {
	svc         *Service
	requireAuth gin.HandlerFunc
}

func NewHandler(svc *Service, requireAuth gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireAuth: requireAuth}
}

// Two channel kinds share the same handlers:
//
//	/groups/:id/chat/messages            group-wide channel
//	/groups/:id/chat/rooms/:rid/messages a room's channel
//
// The group id stays ":id" like every other /groups/:id/... module (gin
// requires one wildcard name per path position).
func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/groups/:id/chat", h.requireAuth)
	{
		g.POST("/messages", h.send)
		g.GET("/messages", h.list)
		g.PUT("/read", h.markRead)

		g.POST("/rooms/:rid/messages", h.send)
		g.GET("/rooms/:rid/messages", h.list)
		g.PUT("/rooms/:rid/read", h.markRead)

		// A message id is unique across channels, so edit/delete need no room.
		g.PATCH("/messages/:mid", h.edit)
		g.DELETE("/messages/:mid", h.delete)

		g.GET("/unread", h.unread)
	}
}

// roomParam is nil on the group-wide routes.
func roomParam(c *gin.Context) *string {
	if rid := c.Param("rid"); rid != "" {
		return &rid
	}
	return nil
}

func (h *Handler) send(c *gin.Context) {
	var req SendMessageRequest
	if !bind(c, &req) {
		return
	}
	m, err := h.svc.Send(c.Request.Context(), auth.UserID(c), c.Param("id"), roomParam(c), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toResponse(m, auth.UserID(c)))
}

func (h *Handler) list(c *gin.Context) {
	var q struct {
		Before string `form:"before"` // message id: older than this
		After  string `form:"after"`  // message id: newer than this (catch-up)
		Limit  int    `form:"limit"`
	}
	_ = c.ShouldBindQuery(&q)

	rows, hasMore, err := h.svc.List(c.Request.Context(), auth.UserID(c), c.Param("id"),
		roomParam(c), q.Before, q.After, q.Limit)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	uid := auth.UserID(c)
	out := make([]MessageResponse, len(rows))
	for i := range rows {
		out[i] = toResponse(&rows[i], uid)
	}
	c.JSON(http.StatusOK, gin.H{"messages": out, "hasMore": hasMore})
}

func (h *Handler) edit(c *gin.Context) {
	var req EditMessageRequest
	if !bind(c, &req) {
		return
	}
	m, err := h.svc.Edit(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(m, auth.UserID(c)))
}

func (h *Handler) delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid")); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) markRead(c *gin.Context) {
	var req MarkReadRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	// The body is optional: an empty one means "mark everything read".
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := h.svc.MarkRead(c.Request.Context(), auth.UserID(c), c.Param("id"), roomParam(c), req); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) unread(c *gin.Context) {
	general, rooms, err := h.svc.Unread(c.Request.Context(), auth.UserID(c), c.Param("id"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := UnreadResponse{General: general, Rooms: make([]RoomUnreadResponse, len(rooms))}
	for i, r := range rooms {
		out.Rooms[i] = RoomUnreadResponse{RoomID: r.RoomID, Unread: r.Unread}
	}
	c.JSON(http.StatusOK, out)
}

// ---------- helpers ----------

func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<16)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

func (h *Handler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
	case errors.Is(err, ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		log.Printf("chat error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}