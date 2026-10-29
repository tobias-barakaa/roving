// internal/attendance/handler.go
package attendance

import (
	"errors"
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

func (h *Handler) Routes(rg *gin.RouterGroup) {
	// All attendance endpoints are rooted at a specific session.
	g := rg.Group("/sessions/:sid/attendance", h.requireAuth)
	{
		// Self-service (any active member)
		g.GET("/me",           h.getMine)
		g.POST("/rsvp",        h.setRSVP)
		g.POST("/check-in",    h.checkIn)
		g.POST("/check-out",   h.checkOut)

		// Roster (any active member — you can see who's coming)
		g.GET("/roster",       h.listRoster)

		// Moderator (RoleModerator+)
		g.POST("/:userId/mark", h.mark)
	}
}

// ---------- self ----------

func (h *Handler) getMine(c *gin.Context) {
	a, err := h.svc.GetMine(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toAttendanceResponse(a))
}

func (h *Handler) setRSVP(c *gin.Context) {
	var req SetRSVPRequest
	if !bind(c, &req) {
		return
	}
	a, err := h.svc.SetRSVP(c.Request.Context(), auth.UserID(c), c.Param("sid"), req.RSVP)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toAttendanceResponse(a))
}

func (h *Handler) checkIn(c *gin.Context) {
	a, err := h.svc.CheckIn(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toAttendanceResponse(a))
}

func (h *Handler) checkOut(c *gin.Context) {
	a, err := h.svc.CheckOut(c.Request.Context(), auth.UserID(c), c.Param("sid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toAttendanceResponse(a))
}

// ---------- roster ----------

func (h *Handler) listRoster(c *gin.Context) {
	var q ListRosterQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	entries, err := h.svc.ListRoster(c.Request.Context(), auth.UserID(c), c.Param("sid"), ListFilter{
		RSVP:   q.RSVP,
		Status: q.Status,
		Limit:  q.Limit,
		Offset: q.Offset,
	})
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]RosterEntryResponse, len(entries))
	for i, e := range entries {
		out[i] = toRosterEntryResponse(e)
	}
	c.JSON(http.StatusOK, RosterResponse{
		SessionID: c.Param("sid"),
		Entries:   out,
	})
}

// ---------- moderator ----------

func (h *Handler) mark(c *gin.Context) {
	var req MarkAttendanceRequest
	if !bind(c, &req) {
		return
	}
	a, err := h.svc.Mark(c.Request.Context(), auth.UserID(c),
		c.Param("sid"), c.Param("userId"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toAttendanceResponse(a))
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
	case errors.Is(err, ErrSessionNotFound), errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
	case errors.Is(err, ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
	case errors.Is(err, ErrSessionCancelled):
		c.JSON(http.StatusConflict, gin.H{"error": "this session has been cancelled"})
	case errors.Is(err, ErrTooEarly), errors.Is(err, ErrTooLate):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
	case errors.Is(err, ErrAlreadyCheckedIn), errors.Is(err, ErrNotCheckedIn):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrInvalidStatus), errors.Is(err, ErrInvalidReason),
		errors.Is(err, ErrNotGroupMember):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		println("attendance error:", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}