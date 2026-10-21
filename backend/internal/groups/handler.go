package groups

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"study-platform/backend/internal/auth"
	"study-platform/backend/internal/httpx"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

type Handler struct {
	service     *Service
	requireAuth gin.HandlerFunc
}

func NewHandler(service *Service, requireAuth gin.HandlerFunc) *Handler {
	return &Handler{service: service, requireAuth: requireAuth}
}

func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/groups", httpx.NoStore(), h.requireAuth)
	g.GET("", h.listMyGroups)
	g.POST("", h.create)
	g.GET("/discover", h.discover)
	g.GET("/:id", h.get)
	g.PATCH("/:id", h.update)
	g.POST("/:id/archive", h.archive)
	g.POST("/:id/unarchive", h.unarchive)
	g.GET("/:id/members", h.listMembers)
	g.PATCH("/:id/members/:uid", h.changeRole)
	g.DELETE("/:id/members/:uid", h.removeMember)
	g.POST("/:id/leave", h.leave)
}

func (h *Handler) create(c *gin.Context) {
	var req createGroupRequest
	if !bind(c, &req) {
		return
	}
	g, err := h.service.Create(c.Request.Context(), auth.UserID(c), req.Name, req.Slug, req.Description, req.Visibility)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, newGroupResponse(g))
}

func (h *Handler) listMyGroups(c *gin.Context) {
	limit, cursor, ok := h.page(c)
	if !ok {
		return
	}
	groups, next, err := h.service.ListMyGroups(c.Request.Context(), auth.UserID(c), limit, cursor)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.writeGroups(c, groups, next)
}

func (h *Handler) discover(c *gin.Context) {
	limit, cursor, ok := h.page(c)
	if !ok {
		return
	}
	groups, next, err := h.service.Discover(c.Request.Context(), limit, cursor)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.writeGroups(c, groups, next)
}

func (h *Handler) get(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	g, err := h.service.Get(c.Request.Context(), auth.UserID(c), id)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, newGroupResponse(g))
}

func (h *Handler) update(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	var req updateGroupRequest
	if !bind(c, &req) {
		return
	}
	if req.Slug != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug cannot be changed"})
		return
	}
	g, err := h.service.Update(c.Request.Context(), auth.UserID(c), id, req.Name, req.Description, req.Visibility)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, newGroupResponse(g))
}

func (h *Handler) archive(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.Archive(c.Request.Context(), auth.UserID(c), id); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) unarchive(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.Unarchive(c.Request.Context(), auth.UserID(c), id); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listMembers(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	limit, cursor, ok := h.page(c)
	if !ok {
		return
	}
	members, next, err := h.service.ListMembers(c.Request.Context(), auth.UserID(c), id, limit, cursor)
	if err != nil {
		h.fail(c, err)
		return
	}
	items := make([]memberResponse, 0, len(members))
	for i := range members {
		items = append(items, newMemberResponse(&members[i]))
	}
	c.JSON(http.StatusOK, memberListResponse{Items: items, NextCursor: next})
}

func (h *Handler) changeRole(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	uid, ok := h.uuidParam(c, "uid")
	if !ok {
		return
	}
	var req changeRoleRequest
	if !bind(c, &req) {
		return
	}
	m, err := h.service.ChangeRole(c.Request.Context(), auth.UserID(c), id, uid, req.Role)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, newMemberResponse(m))
}

func (h *Handler) removeMember(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	uid, ok := h.uuidParam(c, "uid")
	if !ok {
		return
	}
	if err := h.service.RemoveMember(c.Request.Context(), auth.UserID(c), id, uid); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) leave(c *gin.Context) {
	id, ok := h.uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.Leave(c.Request.Context(), auth.UserID(c), id); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- helpers ----------

func (h *Handler) writeGroups(c *gin.Context, groups []Group, next string) {
	items := make([]groupResponse, 0, len(groups))
	for i := range groups {
		items = append(items, newGroupResponse(&groups[i]))
	}
	c.JSON(http.StatusOK, groupListResponse{Items: items, NextCursor: next})
}

func (h *Handler) uuidParam(c *gin.Context, name string) (string, bool) {
	v := c.Param(name)
	if !validUUID(v) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return "", false
	}
	return v, true
}

func (h *Handler) page(c *gin.Context) (int, string, bool) {
	limit := defaultPageSize
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 100"})
			return 0, "", false
		}
		limit = n
	}
	return limit, c.Query("cursor"), true
}

func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrSlugTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrGroupArchived):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrLastOwner):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "conflict"})
	default:
		log.Printf("groups error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}

func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<16)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			c := s[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}
