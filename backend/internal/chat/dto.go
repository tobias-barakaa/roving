// internal/chat/dto.go
package chat

import "time"

type SendMessageRequest struct {
	Body      string  `json:"body"      binding:"required,max=4000"`
	ReplyToID *string `json:"replyToId" binding:"omitempty,uuid"`
}

type EditMessageRequest struct {
	Body string `json:"body" binding:"required,max=4000"`
}

// MarkReadRequest: omit messageId to mark everything up to now as read; send
// the newest message the client actually displayed to avoid marking messages
// that arrived after the page was rendered.
type MarkReadRequest struct {
	MessageID *string `json:"messageId" binding:"omitempty,uuid"`
}

type MessageResponse struct {
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
	IsMine     bool       `json:"isMine"`
}

type RoomUnreadResponse struct {
	RoomID string `json:"roomId"`
	Unread int    `json:"unread"`
}

type UnreadResponse struct {
	General int                  `json:"general"`
	Rooms   []RoomUnreadResponse `json:"rooms"`
}

func toResponse(m *Message, viewerID string) MessageResponse {
	return MessageResponse{
		ID:         m.ID,
		GroupID:    m.GroupID,
		RoomID:     m.RoomID,
		UserID:     m.UserID,
		AuthorName: m.AuthorName,
		ReplyToID:  m.ReplyToID,
		Body:       m.Body,
		Deleted:    m.DeletedAt != nil,
		EditedAt:   m.EditedAt,
		CreatedAt:  m.CreatedAt,
		IsMine:     m.UserID != nil && *m.UserID == viewerID,
	}
}