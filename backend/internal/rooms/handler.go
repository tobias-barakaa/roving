// internal/rooms/handler.go
package rooms

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
	g := rg.Group("/groups/:id/rooms", h.requireAuth)

	{
		g.POST("", h.create)
		g.GET("", h.list)
		g.GET("/:rid", h.get)
		g.PATCH("/:rid", h.update)
		g.POST("/:rid/archive", h.archive)
		g.POST("/:rid/unarchive", h.unarchive)

		g.GET("/:rid/members", h.listMembers)
		g.POST("/:rid/members", h.grantAccess)
		g.PATCH("/:rid/members/:userId", h.changeRole)
		g.DELETE("/:rid/members/:userId", h.revokeAccess)
	}
}

// ---------- handlers ----------

func (h *Handler) create(c *gin.Context) {
	var req CreateRoomRequest
	if !bind(c, &req) {
		return
	}
	room, err := h.svc.Create(c.Request.Context(), auth.UserID(c), c.Param("id"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toRoomResponse(room))
}

func (h *Handler) list(c *gin.Context) {
	var q ListRoomsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	rooms, err := h.svc.List(c.Request.Context(), auth.UserID(c), c.Param("id"), ListFilter{
		IncludeArchived: q.IncludeArchived,
		Access:          q.Access,
		Search:          q.Search,
		Limit:           q.Limit,
		Offset:          q.Offset,
	})
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]RoomResponse, len(rooms))
	for i := range rooms {
		out[i] = toRoomResponse(&rooms[i])
	}
	c.JSON(http.StatusOK, gin.H{"rooms": out})
}

func (h *Handler) get(c *gin.Context) {
	d, err := h.svc.Get(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toRoomDetailResponse(d))
}

func (h *Handler) update(c *gin.Context) {
	var req UpdateRoomRequest
	if !bind(c, &req) {
		return
	}
	err := h.svc.Update(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) archive(c *gin.Context) {
	err := h.svc.Archive(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) unarchive(c *gin.Context) {
	err := h.svc.Unarchive(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listMembers(c *gin.Context) {
	var q struct {
		Limit  int `form:"limit"`
		Offset int `form:"offset"`
	}
	_ = c.ShouldBindQuery(&q)
	members, err := h.svc.ListMembers(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"), q.Limit, q.Offset)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	out := make([]RoomMemberResponse, len(members))
	for i, m := range members {
		out[i] = toRoomMemberResponse(m)
	}
	c.JSON(http.StatusOK, gin.H{"members": out})
}

func (h *Handler) grantAccess(c *gin.Context) {
	var req GrantRoomAccessRequest
	if !bind(c, &req) {
		return
	}
	err := h.svc.GrantAccess(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) changeRole(c *gin.Context) {
	var req ChangeRoomRoleRequest
	if !bind(c, &req) {
		return
	}
	err := h.svc.ChangeRole(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"), c.Param("userId"), req)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) revokeAccess(c *gin.Context) {
	err := h.svc.RevokeAccess(c.Request.Context(), auth.UserID(c),
		c.Param("id"), c.Param("rid"), c.Param("userId"))
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
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotHost):
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
	case errors.Is(err, ErrNotMember):
		c.JSON(http.StatusBadRequest, gin.H{"error": "target user is not a member of this group"})
	case errors.Is(err, ErrNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "a room with that name already exists"})
	case errors.Is(err, ErrInvalidAccess):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid access mode for this operation"})
	case errors.Is(err, ErrArchived):
		c.JSON(http.StatusConflict, gin.H{"error": "room is archived"})
	case errors.Is(err, ErrAlreadyHost):
		c.JSON(http.StatusConflict, gin.H{"error": "user is already a host"})
	default:
		println("rooms error:", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}
