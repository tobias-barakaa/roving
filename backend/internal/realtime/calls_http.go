// internal/realtime/calls_http.go
package realtime

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"roving/backend/internal/auth"
	"roving/backend/internal/chat"
)

// iceServers returns STUN/TURN settings with short-lived TURN credentials.
// Fetch it when joining a call, not once at page load.
func (h *Handler) iceServers(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"iceServers": h.ice.Servers(auth.UserID(c), time.Now()),
		"ttlSeconds": int(ICECredentialTTL.Seconds()),
	})
}

func (h *Handler) groupCall(c *gin.Context) {
	h.callInfo(c, c.Param("id"), nil)
}

func (h *Handler) roomCall(c *gin.Context) {
	rid := c.Param("rid")
	h.callInfo(c, c.Param("id"), &rid)
}

// callInfo says whether a call is in progress on a channel and who is in it,
// so the UI can show a "join call" badge without opening a socket.
func (h *Handler) callInfo(c *gin.Context, groupID string, roomID *string) {
	ref, ok := ChannelRef{GroupID: groupID, RoomID: roomID}.normalize()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	err := h.hub.auth.CanAccessChannel(c.Request.Context(), auth.UserID(c), ref.GroupID, ref.RoomID)
	switch {
	case err == nil:
		parts := h.hub.CallFor(ref)
		c.JSON(http.StatusOK, gin.H{
			"active":          len(parts) > 0,
			"participants":    parts,
			"maxParticipants": MaxCallParticipants,
		})
	case errors.Is(err, chat.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, chat.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not have permission to do that"})
	default:
		log.Printf("realtime call info error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
	}
}