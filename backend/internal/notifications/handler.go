// internal/notifications/handler.go
package notifications

import (
	"errors"
	"log"
	"net/http"
	"time"

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

func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/notifications", h.requireAuth)
	{
		g.GET("", h.list)
		g.GET("/unread-count", h.unreadCount)
		g.POST("/read-all", h.markAllRead)
		g.POST("/:nid/read", h.markRead)

		g.GET("/preferences", h.getPreferences)
		g.PUT("/preferences/:kind", h.setPreference)
	}
}

func (h *Handler) list(c *gin.Context) {
	var q struct {
		Unread bool   `form:"unread"`
		Before string `form:"before"` // RFC3339, from a previous nextBefore
		Limit  int    `form:"limit"`
	}
	_ = c.ShouldBindQuery(&q)

	f := ListFilter{UserID: auth.UserID(c), UnreadOnly: q.Unread, Limit: q.Limit}
	if q.Before != "" {
		t, err := time.Parse(time.RFC3339Nano, q.Before)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid before"})
			return
		}
		f.Before = &t
	}

	rows, next, err := h.svc.List(c.Request.Context(), f)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]NotificationResponse, len(rows))
	for i := range rows {
		out[i] = toResponse(&rows[i])
	}
	c.JSON(http.StatusOK, gin.H{"notifications": out, "nextBefore": next})
}

func (h *Handler) unreadCount(c *gin.Context) {
	n, err := h.svc.UnreadCount(c.Request.Context(), auth.UserID(c))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": n})
}

func (h *Handler) markRead(c *gin.Context) {
	if err := h.svc.MarkRead(c.Request.Context(), auth.UserID(c), c.Param("nid")); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) markAllRead(c *gin.Context) {
	n, err := h.svc.MarkAllRead(c.Request.Context(), auth.UserID(c))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"marked": n})
}

func (h *Handler) getPreferences(c *gin.Context) {
	prefs, err := h.svc.Preferences(c.Request.Context(), auth.UserID(c))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]PreferenceResponse, len(prefs))
	for i, p := range prefs {
		out[i] = PreferenceResponse{Kind: p.Kind, InApp: p.InApp}
	}
	c.JSON(http.StatusOK, gin.H{"preferences": out})
}

func (h *Handler) setPreference(c *gin.Context) {
	var req SetPreferenceRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := h.svc.SetPreference(c.Request.Context(), auth.UserID(c), c.Param("kind"), *req.InApp); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		log.Printf("notifications error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}