// internal/reading/model.go
package reading

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

type Material struct {
	ID          string
	GroupID     string
	AddedBy     string
	Title       string
	Kind        string // book | article | paper | video | link | other
	Author      string
	URL         *string
	Description string
	Unit        string // pages | chapters | minutes | sections
	TotalUnits  *int
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaterialView is a material plus the caller's progress and group totals.
type MaterialView struct {
	Material
	MyUnit   int
	MyStatus string
	Readers  int // members who started or finished
	Finished int
}

// Progress is one user's progress on one material.
type Progress struct {
	MaterialID  string
	UserID      string
	CurrentUnit int
	Status      string // not_started | reading | finished
	StartedAt   *time.Time
	FinishedAt  *time.Time
	UpdatedAt   time.Time
}

type RosterEntry struct {
	UserID      string
	DisplayName string
	CurrentUnit int
	Status      string
	FinishedAt  *time.Time
	UpdatedAt   time.Time
}