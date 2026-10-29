// internal/rooms/model.go
package rooms

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("room not found")
	ErrForbidden     = errors.New("forbidden")
	ErrNameTaken     = errors.New("a room with this name already exists in this group")
	ErrNotHost       = errors.New("only room hosts can do that")
	ErrAlreadyHost   = errors.New("user is already a host of this room")
	ErrNotMember     = errors.New("user is not a group member")
	ErrArchived      = errors.New("room is archived")
	ErrInvalidAccess = errors.New("invalid access mode")
)

// GroupRole mirrors the enum in other packages so we can compare roles
// without importing groups.
type GroupRole int

const (
	GroupRoleNone GroupRole = iota
	GroupRoleMember
	GroupRoleModerator
	GroupRoleAdmin
	GroupRoleOwner
)

func ParseGroupRole(s string) GroupRole {
	switch s {
	case "owner":
		return GroupRoleOwner
	case "admin":
		return GroupRoleAdmin
	case "moderator":
		return GroupRoleModerator
	case "member":
		return GroupRoleMember
	default:
		return GroupRoleNone
	}
}

// RoomRole is privilege *within a room* (orthogonal to GroupRole).
type RoomRole int

const (
	RoomRoleNone RoomRole = iota
	RoomRoleMember
	RoomRoleHost
)

func ParseRoomRole(s string) RoomRole {
	switch s {
	case "host":
		return RoomRoleHost
	case "member":
		return RoomRoleMember
	default:
		return RoomRoleNone
	}
}

func (r RoomRole) String() string {
	switch r {
	case RoomRoleHost:
		return "host"
	case RoomRoleMember:
		return "member"
	default:
		return "none"
	}
}

// Access controls who may enter:
//   'group'      – every active group member
//   'restricted' – only users with an active room_memberships row
const (
	AccessGroup      = "group"
	AccessRestricted = "restricted"
)

func ValidAccess(s string) bool { return s == AccessGroup || s == AccessRestricted }

// Room mirrors a row in study_rooms.
type Room struct {
	ID          string
	GroupID     string
	CreatedBy   string
	Name        string
	Description string
	Access      string
	Capacity    *int
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (r *Room) Archived() bool { return r.ArchivedAt != nil }

// RoomMember mirrors a row in room_memberships.
type RoomMember struct {
	ID        string
	RoomID    string
	GroupID   string
	UserID    string
	Role      RoomRole
	Status    string // "active" | "revoked"
	JoinedAt  time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// RoomMemberSummary is enriched for the member list endpoint.
type RoomMemberSummary struct {
	UserID      string
	DisplayName string
	Email       string
	Role        RoomRole
	Status      string
	JoinedAt    time.Time
}

// RoomDetail is what "get one room" returns, plus the caller's context.
type RoomDetail struct {
	Room
	GroupName string
	MyRole    GroupRole    // caller's group role
	MyRoomRole RoomRole    // caller's room role (0 = none)
	CanEnter  bool         // resolved result of the two-gate check
}

// ListFilter drives List().
type ListFilter struct {
	GroupID         string
	IncludeArchived bool
	Access          string // "" | "group" | "restricted"
	Search          string
	Limit           int
	Offset          int
}