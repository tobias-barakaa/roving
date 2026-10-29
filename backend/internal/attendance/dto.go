// internal/attendance/dto.go
package attendance

import "time"

// --- requests ---

type SetRSVPRequest struct {
	RSVP string `json:"rsvp" binding:"required,oneof=going maybe declined"`
}

// CheckInRequest is what the *attendee* sends. Moderators use the same shape
// via the "target user" endpoints. Timestamps come from the server.
type CheckInRequest struct {
	// Optional: server fills these if absent. When present, the service
	// uses them ONLY as a hint (it enforces the window itself).
	// Keeping these fields lets the moderator mark "was late, checked in
	// at 18:25" without needing a second endpoint.
	At *time.Time `json:"at" binding:"omitempty"`
}

type CheckOutRequest struct {
	At *time.Time `json:"at" binding:"omitempty"`
}

// MarkAttendanceRequest is used by moderators to explicitly set a status
// (present, late, absent, excused). This is the "attendance verification"
// endpoint, distinct from the "check-in yourself" endpoint.
type MarkAttendanceRequest struct {
	Status        string  `json:"status"         binding:"required,oneof=present late absent excused unknown"`
	AbsenceReason *string `json:"absenceReason"  binding:"omitempty,max=500"`
	// When non-nil, "present"/"late" require this — the moderator can backfill
	// the exact check-in time. If omitted, the server uses now().
	CheckedInAt   *time.Time `json:"checkedInAt" binding:"omitempty"`
}

type ListRosterQuery struct {
	RSVP   string `form:"rsvp"   binding:"omitempty,oneof=pending going maybe declined"`
	Status string `form:"status" binding:"omitempty,oneof=unknown present late absent excused"`
	Limit  int    `form:"limit"  binding:"omitempty,min=1,max=200"`
	Offset int    `form:"offset" binding:"min=0"`
}

// --- responses ---

type AttendanceResponse struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"sessionId"`
	UserID           string     `json:"userId"`
	RSVPStatus       string     `json:"rsvpStatus"`
	AttendanceStatus string     `json:"attendanceStatus"`
	RespondedAt      *time.Time `json:"respondedAt,omitempty"`
	CheckedInAt      *time.Time `json:"checkedInAt,omitempty"`
	CheckedOutAt     *time.Time `json:"checkedOutAt,omitempty"`
	AbsenceReason    *string    `json:"absenceReason,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type RosterEntryResponse struct {
	UserID           string     `json:"userId"`
	DisplayName      string     `json:"displayName"`
	Email            string     `json:"email"`
	RSVPStatus       string     `json:"rsvpStatus"`
	AttendanceStatus string     `json:"attendanceStatus"`
	RespondedAt      *time.Time `json:"respondedAt,omitempty"`
	CheckedInAt      *time.Time `json:"checkedInAt,omitempty"`
	CheckedOutAt     *time.Time `json:"checkedOutAt,omitempty"`
	AbsenceReason    *string    `json:"absenceReason,omitempty"`
	IsSelf           bool       `json:"isSelf"`
}

type RosterResponse struct {
	SessionID string                `json:"sessionId"`
	Entries   []RosterEntryResponse `json:"entries"`
}

// --- mapping ---

func toAttendanceResponse(a *Attendance) AttendanceResponse {
	return AttendanceResponse{
		ID:               a.ID,
		SessionID:        a.SessionID,
		UserID:           a.UserID,
		RSVPStatus:       a.RSVPStatus,
		AttendanceStatus: a.AttendanceStatus,
		RespondedAt:      a.RespondedAt,
		CheckedInAt:      a.CheckedInAt,
		CheckedOutAt:     a.CheckedOutAt,
		AbsenceReason:    a.AbsenceReason,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}
}

func toRosterEntryResponse(e RosterEntry) RosterEntryResponse {
	return RosterEntryResponse{
		UserID:           e.UserID,
		DisplayName:      e.DisplayName,
		Email:            e.Email,
		RSVPStatus:       e.RSVPStatus,
		AttendanceStatus: e.AttendanceStatus,
		RespondedAt:      e.RespondedAt,
		CheckedInAt:      e.CheckedInAt,
		CheckedOutAt:     e.CheckedOutAt,
		AbsenceReason:    e.AbsenceReason,
		IsSelf:           e.IsSelf,
	}
}