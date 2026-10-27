// internal/groups/model.go
package groups

import (
	"errors"
	"time"
)

// Sentinel errors. Handlers translate these to HTTP status codes.
var (
	ErrNotFound      = errors.New("group not found")
	ErrForbidden     = errors.New("forbidden")
	ErrSlugTaken     = errors.New("slug already taken")
	ErrInvalidRole   = errors.New("invalid role")
	ErrLastOwner     = errors.New("cannot remove or demote the last owner")
	ErrAlreadyMember = errors.New("user is already a member of this group")
	ErrNotMember     = errors.New("user is not a member of this group")
)

// Role is the caller's privilege level inside a group, ordered by privilege.
// Comparisons like `role >= RoleAdmin` are the entire authorization vocabulary
// for the groups package.
type Role int

const (
	RoleNone Role = iota
	RoleMember
	RoleModerator
	RoleAdmin
	RoleOwner
)

func (r Role) String() string {
	switch r {
	case RoleMember:
		return "member"
	case RoleModerator:
		return "moderator"
	case RoleAdmin:
		return "admin"
	case RoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// ParseRole maps a DB string to a Role. Anything unrecognized (which the DB
// CHECK constraint already forbids, but defense in depth) becomes RoleNone.
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

// ValidAssignableRole is what a client may pass when inviting / promoting.
// Note: 'owner' is intentionally absent — ownership is transferred via a
// dedicated endpoint, not by granting the role.
func ValidAssignableRole(r Role) bool {
	return r == RoleMember || r == RoleModerator || r == RoleAdmin
}

// Group mirrors a row in study_groups.
type Group struct {
	ID          string
	CreatedBy   string
	Name        string
	Slug        string
	Description string
	Visibility  string // "private" | "public"
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (g *Group) Archived() bool { return g.ArchivedAt != nil }

// Membership mirrors a row in group_memberships.
type Membership struct {
	ID        string
	GroupID   string
	UserID    string
	Role      Role
	Status    string // "pending" | "active" | "suspended" | "left"
	InvitedBy *string
	JoinedAt  *time.Time
	CreatedAt time.Time
}

// MemberSummary is what we return when listing members (joined user info).
type MemberSummary struct {
	UserID      string
	DisplayName string
	Email       string
	Role        Role
	Status      string
	JoinedAt    *time.Time
}

// ListFilter drives List().
type ListFilter struct {
	// Search matches against name or slug (case-insensitive substring).
	Search string
	// Visibility: "", "private", "public"
	Visibility string
	// IncludeArchived: default false.
	IncludeArchived bool
	Limit           int
	Offset          int
}


// package groups

// import "time"

// // Group is the domain model for a study group.
// type Group struct {
// 	ID          string
// 	CreatedBy   string
// 	Name        string
// 	Slug        string
// 	Description string
// 	Visibility  string
// 	ArchivedAt  *time.Time
// 	CreatedAt   time.Time
// 	UpdatedAt   time.Time
// }

// // Membership is a single group_memberships row.
// type Membership struct {
// 	ID        string
// 	GroupID   string
// 	UserID    string
// 	Role      string
// 	Status    string
// 	JoinedAt  *time.Time
// 	CreatedAt time.Time
// }

// // Member is a membership joined with the user's email, used for listings.
// type Member struct {
// 	UserID   string
// 	Email    string
// 	Role     string
// 	Status   string
// 	JoinedAt *time.Time
// }
