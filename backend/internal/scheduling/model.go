// internal/scheduling/model.go
package scheduling

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidRRule  = errors.New("invalid recurrence rule")
	ErrInvalidTime   = errors.New("invalid time range")
	ErrSeriesEnded   = errors.New("series is not active")
	ErrSessionLocked = errors.New("session cannot be modified")
	ErrNoRoom        = errors.New("room does not belong to this group")
)

// Role mirrors groups.Role numerically so comparisons across packages are
// safe. Kept as a separate type to avoid importing groups here.
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

// Series is the recurring plan.
type Series struct {
	ID              string
	GroupID         string
	RoomID          *string
	CreatedBy       string
	Title           string
	Description     string
	Timezone        string    // IANA, e.g. "Africa/Nairobi"
	RecurrenceRule  string    // "FREQ=WEEKLY;BYDAY=MO,WE"
	LocalStartTime  string    // "18:30:00"
	DurationSeconds int
	StartsOn        time.Time // date only
	EndsOn          *time.Time
	Status          string // "active" | "paused" | "ended"
	GeneratedUntil  *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Session is one occurrence.
type Session struct {
	ID               string
	GroupID          string
	RoomID           *string
	SeriesID         *string
	OriginalStartsAt *time.Time // generator key; NULL for one-off sessions
	CreatedBy        string
	Title            string
	Description      string
	StartsAt         time.Time
	EndsAt           time.Time
	Status           string // "scheduled" | "completed" | "cancelled"
	CancelledAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (s *Session) Cancelled() bool { return s.Status == "cancelled" }
func (s *Session) Ended() bool     { return s.Status == "completed" || s.Cancelled() }

// SessionDetail is what a "get one session" endpoint returns, with room and
// series info joined in.
type SessionDetail struct {
	Session
	RoomName   *string
	SeriesTitle *string
	MyRole     Role
}

// ListFilter drives session listing.
type ListFilter struct {
	GroupID    string
	RoomID     string
	SeriesID   string
	From       *time.Time
	To         *time.Time
	Status     string // "", "scheduled", "completed", "cancelled"
	Limit      int
	Offset     int
}