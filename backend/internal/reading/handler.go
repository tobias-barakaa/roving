// internal/reading/handler.go
package reading

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

// NOTE: gin requires the same wildcard name at the same path position, so the
// group id stays ":id" like every other /groups/:id/... module.
func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/groups/:id/reading", h.requireAuth)
	{
		g.POST("/materials", h.createMaterial)
		g.GET("/materials", h.listMaterials)
		g.GET("/materials/:mid", h.getMaterial)
		g.PATCH("/materials/:mid", h.updateMaterial)
		g.POST("/materials/:mid/archive", h.archiveMaterial)

		g.GET("/materials/:mid/progress", h.getProgress)
		g.PUT("/materials/:mid/progress", h.updateProgress)
		g.GET("/materials/:mid/roster", h.roster)
	}
}

func (h *Handler) createMaterial(c *gin.Context) {
	var req CreateMaterialRequest
	if !bind(c, &req) {
		return
	}
	m, err := h.svc.CreateMaterial(c.Request.Context(), auth.UserID(c), c.Param("id"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toMaterialResponse(m))
}

func (h *Handler) listMaterials(c *gin.Context) {
	var q struct {
		IncludeArchived bool `form:"includeArchived"`
		Limit           int  `form:"limit"`
		Offset          int  `form:"offset"`
	}
	_ = c.ShouldBindQuery(&q)
	rows, err := h.svc.ListMaterials(c.Request.Context(), auth.UserID(c), c.Param("id"),
		q.IncludeArchived, q.Limit, q.Offset)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]MaterialViewResponse, len(rows))
	for i := range rows {
		out[i] = toMaterialViewResponse(&rows[i])
	}
	c.JSON(http.StatusOK, gin.H{"materials": out})
}

func (h *Handler) getMaterial(c *gin.Context) {
	v, err := h.svc.GetMaterial(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toMaterialViewResponse(v))
}

func (h *Handler) updateMaterial(c *gin.Context) {
	var req UpdateMaterialRequest
	if !bind(c, &req) {
		return
	}
	if err := h.svc.UpdateMaterial(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid"), req); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) archiveMaterial(c *gin.Context) {
	if err := h.svc.ArchiveMaterial(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid")); err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) getProgress(c *gin.Context) {
	p, err := h.svc.GetMyProgress(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toProgressResponse(p))
}

func (h *Handler) updateProgress(c *gin.Context) {
	var req UpdateProgressRequest
	if !bind(c, &req) {
		return
	}
	p, err := h.svc.UpdateProgress(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toProgressResponse(p))
}

func (h *Handler) roster(c *gin.Context) {
	rows, err := h.svc.Roster(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("mid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]RosterEntryResponse, len(rows))
	for i, e := range rows {
		out[i] = RosterEntryResponse{
			UserID: e.UserID, DisplayName: e.DisplayName, CurrentUnit: e.CurrentUnit,
			Status: e.Status, FinishedAt: e.FinishedAt, UpdatedAt: e.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, gin.H{"readers": out})
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
		log.Printf("reading error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}