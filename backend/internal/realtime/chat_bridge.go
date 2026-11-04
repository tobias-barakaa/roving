// internal/realtime/chat_bridge.go
package realtime

import (
	"time"

	"roving/backend/internal/chat"
)

// Hub satisfies chat.Publisher: chat.Service.SetPublisher(hub).
// Messages are still written via the REST API; the hub only fans out the result.

type chatPayload struct {
	ID         string     `json:"id"`
	GroupID    string     `json:"groupId"`
	RoomID     *string    `json:"roomId,omitempty"`
	UserID     *string    `json:"userId,omitempty"`
	AuthorName *string    `json:"authorName,omitempty"`
	ReplyToID  *string    `json:"replyToId,omitempty"`
	Body       string     `json:"body"`
	Deleted    bool       `json:"deleted"`
	EditedAt   *time.Time `json:"editedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	// No isMine: one payload goes to everyone, so the client compares userId.
}

func (h *Hub) Publish(ev chat.Event) {
	m := ev.Message
	if m == nil {
		return
	}
	key := GroupTopic(ev.GroupID)
	if ev.RoomID != nil {
		key = RoomTopic(*ev.RoomID)
	}
	h.Broadcast(key, ev.Type, chatPayload{
		ID: m.ID, GroupID: m.GroupID, RoomID: m.RoomID, UserID: m.UserID,
		AuthorName: m.AuthorName, ReplyToID: m.ReplyToID, Body: m.Body,
		Deleted: m.DeletedAt != nil, EditedAt: m.EditedAt, CreatedAt: m.CreatedAt,
	})
}