// internal/focus/model.go
package focus

import (
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("focus not found")
	ErrForbidden         = errors.New("forbidden")
	ErrCycleRunning      = errors.New("you already have a running cycle")
	ErrNoRunningCycle    = errors.New("no running cycle")
	ErrIntervalRunning   = errors.New("an interval is already running")
	ErrNoRunningInterval = errors.New("no running interval")
	ErrCycleFinished     = errors.New("cycle has already finished")
	ErrInvalidKind       = errors.New("invalid interval kind")
	ErrInvalidContext    = errors.New("session and room belong to different groups")
	ErrInvalidPlan       = errors.New("invalid plan parameters")
	ErrSessionNotFound   = errors.New("study session not found")
	ErrRoomNotFound      = errors.New("room not found")
)

// GroupRole mirrors the enum used by other modules.
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

// Statuses for focus_cycles.
const (
	CyclePlanned   = "planned"
	CycleRunning   = "running"
	CycleCompleted = "completed"
	CycleCancelled = "cancelled"
)

// Statuses for focus_sessions.
const (
	IntervalPlanned     = "planned"
	IntervalRunning     = "running"
	IntervalCompleted   = "completed"
	IntervalCancelled   = "cancelled"
	IntervalInterrupted = "interrupted"
)

// Kinds for focus_sessions.
const (
	KindFocus      = "focus"
	KindShortBreak = "short_break"
	KindLongBreak  = "long_break"
)

func ValidKind(s string) bool {
	return s == KindFocus || s == KindShortBreak || s == KindLongBreak
}

// Cycle mirrors a row in focus_cycles.
type Cycle struct {
	ID                   string
	UserID               string
	StudySessionID       *string
	RoomID               *string
	Status               string
	PlannedFocusSeconds  int
	PlannedBreakSeconds  int
	PlannedRounds        int
	StartedAt            *time.Time
	EndedAt              *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (c *Cycle) Running() bool   { return c.Status == CycleRunning }
func (c *Cycle) Finished() bool  { return c.Status == CycleCompleted || c.Status == CycleCancelled }

// Interval mirrors a row in focus_sessions. (The DB calls them focus_sessions
// to distinguish from the parent cycle, but "interval" is more readable
// in code. Both names refer to the same table.)
type Interval struct {
	ID             string
	UserID         string
	CycleID        *string
	StudySessionID *string
	RoomID         *string
	SequenceNo     *int
	Kind           string
	Status         string
	PlannedSeconds int
	StartedAt      *time.Time
	EndedAt        *time.Time
	ActualSeconds  *int // GENERATED ALWAYS in DB; read-only here.
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (i *Interval) Running() bool { return i.Status == IntervalRunning }

// CycleDetail is the response shape for "get one cycle": the cycle, its
// intervals, and aggregated stats.
type CycleDetail struct {
	Cycle
	Intervals []Interval
	// Derived:
	TotalFocusSeconds   int
	TotalBreakSeconds   int
	CompletedFocusRounds int
}

// Stats aggregates a user's focus over a time window.
type Stats struct {
	From              time.Time
	To                time.Time
	TotalFocusSeconds int
	TotalBreakSeconds int
	CycleCount        int
	SessionCount      int // number of completed focus intervals
}

// ContextInput is the optional context a cycle or interval can be tied to.
// Service verifies both IDs belong to the same group the user is a member of.
type ContextInput struct {
	StudySessionID *string
	RoomID         *string
}

// ListFilter for cycles / intervals listings.
type ListFilter struct {
	UserID    string
	Status    string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}