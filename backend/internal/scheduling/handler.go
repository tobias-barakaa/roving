// internal/scheduling/handler.go
package scheduling

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"study-platform/backend/internal/auth"
)

type Handler struct {
	svc         *Service
	requireAuth gin.HandlerFunc
}

func NewHandler(svc *Service, requireAuth gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireAuth: requireAuth}
}

func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/groups/:groupId/schedule", h.requireAuth)
	{
		g.POST("/series",       h.createSeries)
		g.GET("/series/:sid",   h.getSeries)
		g.PATCH("/series/:sid", h.updateSeries)
		g.POST("/series/:sid/end", h.endSeries)

		g.POST("/sessions",     h.createSession)
		g.GET("/sessions",      h.listSessions)
		g.GET("/sessions/:sid", h.getSession)
		g.PATCH("/sessions/:sid", h.updateSession)
		g.POST("/sessions/:sid/cancel", h.cancelSession)
	}
}

// ---------- series handlers ----------

func (h *Handler) createSeries(c *gin.Context) {
	var req CreateSeriesRequest
	if !bind(c, &req) {
		return
	}
	ser, err := h.svc.CreateSeries(c.Request.Context(), auth.UserID(c), c.Param("groupId"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toSeriesResponse(ser))
}

func (h *Handler) getSeries(c *gin.Context) {
	ser, err := h.svc.GetSeries(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toSeriesResponse(ser))
}

func (h *Handler) updateSeries(c *gin.Context) {
	var req UpdateSeriesRequest
	if !bind(c, &req) {
		return
	}
	err := h.svc.UpdateSeries(c.Request.Context(), auth.UserID(c), c.Param("sid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) endSeries(c *gin.Context) {
	err := h.svc.EndSeries(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- session handlers ----------

func (h *Handler) createSession(c *gin.Context) {
	var req CreateSessionRequest
	if !bind(c, &req) {
		return
	}
	sess, err := h.svc.CreateSession(c.Request.Context(), auth.UserID(c), c.Param("groupId"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	d := &SessionDetail{Session: *sess}
	c.JSON(http.StatusCreated, toSessionResponse(d))
}

func (h *Handler) listSessions(c *gin.Context) {
	var q struct {
		RoomID   string `form:"roomId"`
		SeriesID string `form:"seriesId"`
		From     string `form:"from"` // RFC3339
		To       string `form:"to"`
		Status   string `form:"status"`
		Limit    int    `form:"limit"`
		Offset   int    `form:"offset"`
	}
	_ = c.ShouldBindQuery(&q)

	f := ListFilter{
		GroupID:  c.Param("groupId"),
		RoomID:   q.RoomID,
		SeriesID: q.SeriesID,
		Status:   q.Status,
		Limit:    q.Limit,
		Offset:   q.Offset,
	}
	if q.From != "" {
		t, err := time.Parse(time.RFC3339, q.From)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from"})
			return
		}
		f.From = &t
	}
	if q.To != "" {
		t, err := time.Parse(time.RFC3339, q.To)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to"})
			return
		}
		f.To = &t
	}

	sessions, err := h.svc.ListSessions(c.Request.Context(), auth.UserID(c), f)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]SessionResponse, len(sessions))
	for i := range sessions {
		out[i] = toSessionResponse(&sessions[i])
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

func (h *Handler) getSession(c *gin.Context) {
	d, err := h.svc.GetSession(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toSessionResponse(d))
}

func (h *Handler) updateSession(c *gin.Context) {
	var req UpdateSessionRequest
	if !bind(c, &req) {
		return
	}
	err := h.svc.UpdateSession(c.Request.Context(), auth.UserID(c), c.Param("sid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) cancelSession(c *gin.Context) {
	err := h.svc.CancelSession(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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
	case errors.Is(err, ErrInvalidRRule),
		errors.Is(err, ErrInvalidTime),
		errors.Is(err, ErrNoRoom),
		errors.Is(err, ErrSeriesEnded):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrSessionLocked):
		c.JSON(http.StatusConflict, gin.H{"error": "this session cannot be modified"})
	default:
		// Log unknown errors; never leak them.
		println("scheduling error:", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}