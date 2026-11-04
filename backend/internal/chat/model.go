// internal/chat/model.go
package chat

import (
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid request")
)

// Role mirrors groups.Role numerically (kept separate to avoid an import cycle).
type Role int

const (
	RoleNone Role = iota
	RoleMember
	RoleModerator
	RoleAdmin
	RoleOwner
)

func ParseRole(s string) Role {
	switch s {
	case "owner":
		return RoleOwner
	case "admin":
		return RoleAdmin
	case "moderator":
		return RoleModerator
	case "member":
		return RoleMember
	default:
		return RoleNone
	}
}

// Message is one chat message. A deleted message is a tombstone: Body is
// empty and DeletedAt is set.
type Message struct {
	ID         string
	GroupID    string
	RoomID     *string // nil = group-wide channel
	UserID     *string // nil once the author's account is gone
	AuthorName *string
	ReplyToID  *string
	Body       string
	EditedAt   *time.Time
	DeletedAt  *time.Time
	CreatedAt  time.Time
}

// Room is the slice of study_rooms chat needs for access decisions.
type Room struct {
	ID       string
	GroupID  string
	Access   string // "group" | "restricted"
	Archived bool
}

type RoomUnread struct {
	RoomID string
	Unread int
}

// Event types published after a successful change.
const (
	EventMessageCreated = "message.created"
	EventMessageUpdated = "message.updated"
	EventMessageDeleted = "message.deleted"
)

// Event is handed to the Publisher once the database commit has succeeded.
// RoomID nil means the group-wide channel.
type Event struct {
	Type    string
	GroupID string
	RoomID  *string
	Message *Message
}

// Publisher is the seam for the real-time layer: the WebSocket hub will
// implement it and fan the event out to connected members of that channel.
// It must not block, and the chat service treats it as best-effort.
type Publisher interface {
	Publish(ev Event)
}