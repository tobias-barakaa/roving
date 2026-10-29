// internal/attendance/model.go
package attendance

import (
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("attendance not found")
	ErrForbidden         = errors.New("forbidden")
	ErrSessionNotFound   = errors.New("session not found")
	ErrSessionCancelled  = errors.New("session is cancelled")
	ErrNotGroupMember    = errors.New("user is not an active group member")
	ErrTooEarly          = errors.New("too early to check in for this session")
	ErrTooLate           = errors.New("this session has already ended")
	ErrAlreadyCheckedIn  = errors.New("already checked in")
	ErrNotCheckedIn      = errors.New("not checked in")
	ErrInvalidStatus     = errors.New("invalid status transition")
	ErrInvalidReason     = errors.New("absence reason is only valid for absent/excused")
)

// GroupRole mirrors the enum elsewhere.
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

// RSVP is the *plan*.
const (
	RSVPPending  = "pending"
	RSVPGoing    = "going"
	RSVPMaybe    = "maybe"
	RSVPDeclined = "declined"
)

func ValidRSVP(s string) bool {
	switch s {
	case RSVPPending, RSVPGoing, RSVPMaybe, RSVPDeclined:
		return true
	}
	return false
}

// AttendanceStatus is the *evidence*.
const (
	AttendanceUnknown  = "unknown"
	AttendancePresent  = "present"
	AttendanceLate     = "late"
	AttendanceAbsent   = "absent"
	AttendanceExcused  = "excused"
)

func ValidAttendanceStatus(s string) bool {
	switch s {
	case AttendanceUnknown, AttendancePresent, AttendanceLate, AttendanceAbsent, AttendanceExcused:
		return true
	}
	return false
}

// Attendance mirrors a row in session_attendance.
type Attendance struct {
	ID              string
	SessionID       string
	UserID          string
	RSVPStatus      string
	AttendanceStatus string
	RespondedAt     *time.Time
	CheckedInAt     *time.Time
	CheckedOutAt    *time.Time
	AbsenceReason   *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SessionContext is the subset of study_sessions we need for authorization.
// Reused across service methods.
type SessionContext struct {
	ID         string
	GroupID    string
	StartsAt   time.Time
	EndsAt     time.Time
	Status     string // "scheduled" | "completed" | "cancelled"
}

func (s *SessionContext) Cancelled() bool { return s.Status == "cancelled" }
func (s *SessionContext) Ended() bool     { return s.Status == "completed" }

// RosterEntry is what the roster endpoint returns: user info joined with
// attendance, plus the caller's role (for frontend conditional rendering).
type RosterEntry struct {
	UserID           string
	DisplayName      string
	Email            string
	RSVPStatus       string
	AttendanceStatus string
	RespondedAt      *time.Time
	CheckedInAt      *time.Time
	CheckedOutAt     *time.Time
	AbsenceReason    *string
	IsSelf           bool // convenience: entry belongs to the caller
}

// ListFilter for roster.
type ListFilter struct {
	SessionID string
	RSVP      string // "" | one of the RSVP constants
	Status    string // "" | one of the attendance constants
	Limit     int
	Offset    int
}