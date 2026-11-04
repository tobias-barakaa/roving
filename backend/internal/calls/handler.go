// internal/calls/handler.go
package calls

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

// Namespaced under /calls/ (like /chat/) so the room-id wildcard can never
// collide with whatever name the rooms module uses under /groups/:id/rooms/.
func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/groups/:id/calls/rooms/:rid", h.requireAuth)
	{
		g.GET("", h.get)
		g.POST("/join", h.join)
		g.POST("/leave", h.leave)
		g.PATCH("/media", h.media)
		g.POST("/end", h.end)
	}
}

func (h *Handler) get(c *gin.Context) {
	v, err := h.svc.Get(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("rid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	if v == nil {
		c.JSON(http.StatusOK, gin.H{"call": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"call": toCallResponse(v)})
}

func (h *Handler) join(c *gin.Context) {
	var req JoinCallRequest
	if !bindOptional(c, &req) {
		return
	}
	res, err := h.svc.Join(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("rid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toJoinResponse(res))
}

func (h *Handler) leave(c *gin.Context) {
	if err := h.svc.Leave(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("rid")); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) media(c *gin.Context) {
	var req UpdateMediaRequest
	if !bindOptional(c, &req) {
		return
	}
	p, err := h.svc.UpdateMedia(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("rid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toParticipantResponse(*p))
}

func (h *Handler) end(c *gin.Context) {
	if err := h.svc.End(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("rid")); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- helpers ----------

// bindOptional: the body may be empty (all defaults).
func bindOptional(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	if err := c.ShouldBindJSON(dst); err != nil && !errors.Is(err, io.EOF) {
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
	case errors.Is(err, ErrCallFull), errors.Is(err, ErrNotConnected), errors.Is(err, ErrNotInCall):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		log.Printf("calls error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}