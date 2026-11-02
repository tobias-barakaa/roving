// internal/focus/handler.go
package focus

import (
	"errors"
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

// Routes mounts the focus API. Everything is scoped to the authenticated
// user — there is no /users/:id/focus endpoint and there never should be.
// Focus is private; the ownership gate is `AND user_id = $actor` in SQL.
func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/focus", h.requireAuth)
	{
		// Cycle lifecycle
		g.POST("/cycles",        h.startCycle)
		g.GET("/cycles",         h.listCycles)
		g.GET("/cycles/:cid",    h.getCycleDetail)
		g.POST("/cycles/end",    h.endCycle)

		// Interval lifecycle (within the running cycle)
		g.POST("/intervals",     h.startInterval)
		g.POST("/intervals/end", h.endInterval)

		// Current state — polled by the timer UI after refresh
		g.GET("/current",        h.getCurrent)

		// Stats
		g.GET("/stats",          h.stats)
	}
}

// ---------- cycles ----------

func (h *Handler) startCycle(c *gin.Context) {
	var req StartCycleRequest
	if !bind(c, &req) {
		return
	}
	cyc, err := h.svc.StartCycle(c.Request.Context(), auth.UserID(c), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toCycleResponse(cyc))
}

func (h *Handler) listCycles(c *gin.Context) {
	var q ListCyclesQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	f := ListFilter{Status: q.Status, Limit: q.Limit, Offset: q.Offset}
	if q.From != "" {
		t, err := time.Parse(time.RFC3339, q.From)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid 'from'"})
			return
		}
		f.From = &t
	}
	if q.To != "" {
		t, err := time.Parse(time.RFC3339, q.To)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid 'to'"})
			return
		}
		f.To = &t
	}
	cycles, err := h.svc.ListCycles(c.Request.Context(), auth.UserID(c), f)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]CycleResponse, len(cycles))
	for i := range cycles {
		out[i] = toCycleResponse(&cycles[i])
	}
	c.JSON(http.StatusOK, gin.H{"cycles": out})
}

func (h *Handler) getCycleDetail(c *gin.Context) {
	d, err := h.svc.GetCycleDetail(c.Request.Context(), auth.UserID(c), c.Param("cid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toCycleDetailResponse(d))
}

func (h *Handler) endCycle(c *gin.Context) {
	var req EndCycleRequest
	if !bind(c, &req) {
		return
	}
	cyc, err := h.svc.EndCycle(c.Request.Context(), auth.UserID(c), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toCycleResponse(cyc))
}

// ---------- intervals ----------

func (h *Handler) startInterval(c *gin.Context) {
	var req StartIntervalRequest
	if !bind(c, &req) {
		return
	}
	iv, err := h.svc.StartInterval(c.Request.Context(), auth.UserID(c), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toIntervalResponse(iv))
}

func (h *Handler) endInterval(c *gin.Context) {
	var req EndIntervalRequest
	if !bind(c, &req) {
		return
	}
	iv, err := h.svc.EndInterval(c.Request.Context(), auth.UserID(c), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toIntervalResponse(iv))
}

// ---------- current ----------

func (h *Handler) getCurrent(c *gin.Context) {
	cyc, iv, err := h.svc.GetCurrent(c.Request.Context(), auth.UserID(c))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	resp := gin.H{}
	if cyc != nil {
		resp["cycle"] = toCycleResponse(cyc)
	}
	if iv != nil {
		resp["interval"] = toIntervalResponse(iv)
	}
	if cyc == nil && iv == nil {
		resp["idle"] = true
	}
	c.JSON(http.StatusOK, resp)
}

// ---------- stats ----------

func (h *Handler) stats(c *gin.Context) {
	var q StatsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from and to are required (RFC3339)"})
		return
	}
	from, err := time.Parse(time.RFC3339, q.From)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid 'from'"})
		return
	}
	to, err := time.Parse(time.RFC3339, q.To)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid 'to'"})
		return
	}
	s, err := h.svc.Stats(c.Request.Context(), auth.UserID(c), from, to)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, StatsResponse{
		From:              s.From,
		To:                s.To,
		TotalFocusSeconds: s.TotalFocusSeconds,
		TotalBreakSeconds: s.TotalBreakSeconds,
		CycleCount:        s.CycleCount,
		SessionCount:      s.SessionCount,
	})
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
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrSessionNotFound),
		errors.Is(err, ErrRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
	case errors.Is(err, ErrCycleRunning),
		errors.Is(err, ErrNoRunningCycle),
		errors.Is(err, ErrIntervalRunning),
		errors.Is(err, ErrNoRunningInterval),
		errors.Is(err, ErrCycleFinished):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrInvalidKind),
		errors.Is(err, ErrInvalidContext),
		errors.Is(err, ErrInvalidPlan):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		println("focus error:", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}