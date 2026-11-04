// internal/notes/handler.go
package notes

import (
	"errors"
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

func (h *Handler) Routes(rg *gin.RouterGroup) {
	// Personal notes.
	n := rg.Group("/notes", h.requireAuth)
	{
		n.POST("", h.create)
		n.GET("", h.listMine)
		n.GET("/:nid", h.get)
		n.PATCH("/:nid", h.update)
		n.DELETE("/:nid", h.delete)
	}

	// Notes members have shared with a group. (":id" must match the group
	// wildcard name used by every other /groups/:id/... module.)
	rg.GET("/groups/:id/notes", h.requireAuth, h.listShared)
}

func (h *Handler) create(c *gin.Context) {
	var req CreateNoteRequest
	if !bind(c, &req) {
		return
	}
	n, err := h.svc.Create(c.Request.Context(), auth.UserID(c), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toResponse(n, auth.UserID(c)))
}

type listQuery struct {
	GroupID    string `form:"groupId"`
	MaterialID string `form:"materialId"`
	SessionID  string `form:"sessionId"`
	Q          string `form:"q"`
	Pinned     bool   `form:"pinned"`
	Limit      int    `form:"limit"`
	Offset     int    `form:"offset"`
}

func (h *Handler) listMine(c *gin.Context) {
	var q listQuery
	_ = c.ShouldBindQuery(&q)
	rows, err := h.svc.ListMine(c.Request.Context(), auth.UserID(c), ListFilter{
		GroupID: q.GroupID, MaterialID: q.MaterialID, SessionID: q.SessionID,
		Query: q.Q, PinnedOnly: q.Pinned, Limit: q.Limit, Offset: q.Offset,
	})
	if err != nil {
		h.writeErr(c, err)
		return
	}
	h.writeList(c, rows)
}

func (h *Handler) listShared(c *gin.Context) {
	var q listQuery
	_ = c.ShouldBindQuery(&q)
	rows, err := h.svc.ListShared(c.Request.Context(), auth.UserID(c), ListFilter{
		GroupID: c.Param("id"), MaterialID: q.MaterialID, SessionID: q.SessionID,
		Query: q.Q, Limit: q.Limit, Offset: q.Offset,
	})
	if err != nil {
		h.writeErr(c, err)
		return
	}
	h.writeList(c, rows)
}

func (h *Handler) writeList(c *gin.Context, rows []Note) {
	uid := auth.UserID(c)
	out := make([]NoteResponse, len(rows))
	for i := range rows {
		out[i] = toResponse(&rows[i], uid)
	}
	c.JSON(http.StatusOK, gin.H{"notes": out})
}

func (h *Handler) get(c *gin.Context) {
	n, err := h.svc.Get(c.Request.Context(), auth.UserID(c), c.Param("nid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(n, auth.UserID(c)))
}

func (h *Handler) update(c *gin.Context) {
	var req UpdateNoteRequest
	if !bind(c, &req) {
		return
	}
	if err := h.svc.Update(c.Request.Context(), auth.UserID(c), c.Param("nid"), req); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), auth.UserID(c), c.Param("nid")); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- helpers ----------

// Notes allow up to 50,000 characters (can exceed 64 KiB in UTF-8), so the
// body limit is larger than the other modules'.
func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<19)
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
	case errors.Is(err, ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		log.Printf("notes error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}