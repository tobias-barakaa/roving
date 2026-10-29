// internal/attendance/repository.go
package attendance

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// ---------- session context (auth prerequisite) ----------

func (r *Repository) GetSessionContext(ctx context.Context, ex pgxExecutor, sessionID string) (*SessionContext, error) {
	var s SessionContext
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, starts_at, ends_at, status
		   FROM study_sessions WHERE id = $1::uuid`, sessionID,
	).Scan(&s.ID, &s.GroupID, &s.StartsAt, &s.EndsAt, &s.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	return &s, err
}

// ---------- attendance rows ----------

// GetBySessionUser returns the existing row, or ErrNotFound.
func (r *Repository) GetBySessionUser(ctx context.Context, ex pgxExecutor, sessionID, userID string) (*Attendance, error) {
	var a Attendance
	err := ex.QueryRow(ctx,
		`SELECT id::text, session_id::text, user_id::text,
		        rsvp_status, attendance_status,
		        responded_at, checked_in_at, checked_out_at,
		        absence_reason, created_at, updated_at
		   FROM session_attendance
		  WHERE session_id = $1::uuid AND user_id = $2::uuid`,
		sessionID, userID,
	).Scan(&a.ID, &a.SessionID, &a.UserID, &a.RSVPStatus, &a.AttendanceStatus,
		&a.RespondedAt, &a.CheckedInAt, &a.CheckedOutAt, &a.AbsenceReason,
		&a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// SetRSVP: idempotent upsert that always updates responded_at.
// The DB CHECK ensures responded_at is set whenever rsvp_status != 'pending'.
func (r *Repository) SetRSVP(ctx context.Context, ex pgxExecutor, sessionID, userID, rsvp string) (*Attendance, error) {
	var a Attendance
	err := ex.QueryRow(ctx,
		`INSERT INTO session_attendance (session_id, user_id, rsvp_status, responded_at)
		 VALUES ($1::uuid, $2::uuid, $3, now())
		 ON CONFLICT (session_id, user_id) DO UPDATE SET
		    rsvp_status  = EXCLUDED.rsvp_status,
		    responded_at = now()
		 RETURNING id::text, session_id::text, user_id::text,
		           rsvp_status, attendance_status,
		           responded_at, checked_in_at, checked_out_at,
		           absence_reason, created_at, updated_at`,
		sessionID, userID, rsvp,
	).Scan(&a.ID, &a.SessionID, &a.UserID, &a.RSVPStatus, &a.AttendanceStatus,
		&a.RespondedAt, &a.CheckedInAt, &a.CheckedOutAt, &a.AbsenceReason,
		&a.CreatedAt, &a.UpdatedAt)
	return &a, err
}

// CheckIn sets checked_in_at (server-supplied timestamp), and marks attendance
// present or late depending on how far past starts_at we are. The WHERE clause
// makes this an idempotent no-op if checked_in_at is already set (which we then
// report as ErrAlreadyCheckedIn at the service layer).
//
// The `attendance_status` upgrade rule: 'unknown' → 'present' or 'late'.
// If the row was already 'excused' or 'absent', check-in upgrades it back
// to present/late (a moderator who marked absent was wrong; better to let
// the attendee prove they showed up). If you want stricter rules, gate
// this on a role check in the service.
func (r *Repository) CheckIn(ctx context.Context, ex pgxExecutor, sessionID, userID string, at time.Time, late bool) (*Attendance, error) {
	status := AttendancePresent
	if late {
		status = AttendanceLate
	}

	var a Attendance
	err := ex.QueryRow(ctx,
		`INSERT INTO session_attendance
		    (session_id, user_id, rsvp_status, attendance_status,
		     checked_in_at)
		 VALUES ($1::uuid, $2::uuid, 'going', $3, $4)
		 ON CONFLICT (session_id, user_id) DO UPDATE SET
		    attendance_status = $3,
		    checked_in_at     = $4,
		    -- If it was absent/excused, clear the reason on check-in.
		    absence_reason    = NULL
		 WHERE session_attendance.checked_in_at IS NULL
		 RETURNING id::text, session_id::text, user_id::text,
		           rsvp_status, attendance_status,
		           responded_at, checked_in_at, checked_out_at,
		           absence_reason, created_at, updated_at`,
		sessionID, userID, status, at,
	).Scan(&a.ID, &a.SessionID, &a.UserID, &a.RSVPStatus, &a.AttendanceStatus,
		&a.RespondedAt, &a.CheckedInAt, &a.CheckedOutAt, &a.AbsenceReason,
		&a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO UPDATE with a WHERE that failed → zero rows.
		return nil, ErrAlreadyCheckedIn
	}
	return &a, err
}

// CheckOut: server-supplied timestamp, only if checked_in_at is set and
// checked_out_at is null.
func (r *Repository) CheckOut(ctx context.Context, ex pgxExecutor, sessionID, userID string, at time.Time) (*Attendance, error) {
	var a Attendance
	err := ex.QueryRow(ctx,
		`UPDATE session_attendance
		    SET checked_out_at = $3
		  WHERE session_id = $1::uuid AND user_id = $2::uuid
		    AND checked_in_at IS NOT NULL
		    AND checked_out_at IS NULL
		 RETURNING id::text, session_id::text, user_id::text,
		           rsvp_status, attendance_status,
		           responded_at, checked_in_at, checked_out_at,
		           absence_reason, created_at, updated_at`,
		sessionID, userID, at,
	).Scan(&a.ID, &a.SessionID, &a.UserID, &a.RSVPStatus, &a.AttendanceStatus,
		&a.RespondedAt, &a.CheckedInAt, &a.CheckedOutAt, &a.AbsenceReason,
		&a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Distinguish "never checked in" from "already checked out" in the
		// service by reading the row after. Here we return ErrNotFound and
		// let service refine.
		return nil, ErrNotCheckedIn
	}
	return &a, err
}

// MarkAttendance is the moderator override: set status, reason, and check-in
// time explicitly. It doesn't preserve attendance_status the way CheckIn does;
// moderators mean what they say.
func (r *Repository) MarkAttendance(ctx context.Context, ex pgxExecutor, sessionID, userID, status string, reason *string, checkedInAt *time.Time) (*Attendance, error) {
	var a Attendance
	err := ex.QueryRow(ctx,
		`INSERT INTO session_attendance
		    (session_id, user_id, rsvp_status, attendance_status,
		     checked_in_at, absence_reason, responded_at)
		 VALUES ($1::uuid, $2::uuid, 'pending', $3, $4, $5,
		         CASE WHEN $3 <> 'pending' THEN now() ELSE NULL END)
		 ON CONFLICT (session_id, user_id) DO UPDATE SET
		    attendance_status = $3,
		    checked_in_at     = COALESCE($4, session_attendance.checked_in_at),
		    absence_reason    = $5
		 RETURNING id::text, session_id::text, user_id::text,
		           rsvp_status, attendance_status,
		           responded_at, checked_in_at, checked_out_at,
		           absence_reason, created_at, updated_at`,
		sessionID, userID, status, checkedInAt, reason,
	).Scan(&a.ID, &a.SessionID, &a.UserID, &a.RSVPStatus, &a.AttendanceStatus,
		&a.RespondedAt, &a.CheckedInAt, &a.CheckedOutAt, &a.AbsenceReason,
		&a.CreatedAt, &a.UpdatedAt)
	return &a, err
}

// ---------- roster ----------

// ListRoster joins group_memberships (the source of truth for "who's in the
// group") with session_attendance (may or may not exist). Every active group
// member appears in the roster — even those who haven't RSVPed — because the
// roster is "who might show up", not "who answered".
func (r *Repository) ListRoster(ctx context.Context, ex pgxExecutor, f ListFilter) ([]RosterEntry, error) {
	rows, err := ex.Query(ctx,
		`SELECT gm.user_id::text, u.display_name, u.email,
		        COALESCE(a.rsvp_status,       'pending') AS rsvp,
		        COALESCE(a.attendance_status,'unknown') AS att,
		        a.responded_at, a.checked_in_at, a.checked_out_at, a.absence_reason
		   FROM group_memberships gm
		   JOIN users u ON u.id = gm.user_id
		   JOIN study_sessions ss ON ss.id = $1::uuid
		    AND ss.group_id = gm.group_id
		   LEFT JOIN session_attendance a
		     ON a.session_id = ss.id AND a.user_id = gm.user_id
		  WHERE gm.group_id = ss.group_id
		    AND gm.status = 'active'
		    AND ($2 = '' OR COALESCE(a.rsvp_status, 'pending') = $2)
		    AND ($3 = '' OR COALESCE(a.attendance_status, 'unknown') = $3)
		  ORDER BY
		    CASE COALESCE(a.rsvp_status, 'pending')
		        WHEN 'going'    THEN 0
		        WHEN 'maybe'    THEN 1
		        WHEN 'pending'  THEN 2
		        ELSE 3
		    END,
		    u.display_name ASC
		  LIMIT $4 OFFSET $5`,
		f.SessionID, f.RSVP, f.Status, normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RosterEntry
	for rows.Next() {
		var e RosterEntry
		if err := rows.Scan(&e.UserID, &e.DisplayName, &e.Email,
			&e.RSVPStatus, &e.AttendanceStatus,
			&e.RespondedAt, &e.CheckedInAt, &e.CheckedOutAt, &e.AbsenceReason); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- helpers ----------

func normalizeLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}