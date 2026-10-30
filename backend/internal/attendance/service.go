// internal/attendance/service.go
package attendance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
)

// CheckInWindow is how early (relative to starts_at) an attendee may check in.
// Late-ness is computed relative to starts_at.
const (
	CheckInEarlyWindow = 15 * time.Minute
	CheckInGraceWindow = 15 * time.Minute // "present" if within this after starts_at
	// After ends_at: no more check-ins (the session is history).
)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
	now  func() time.Time // injectable for tests
}

func NewService(repo *Repository, db *pgxpool.Pool) *Service {
	return &Service{repo: repo, db: db, now: time.Now}
}

// ==================== RSVP (self-only) ====================



// SetRSVP lets any active group member RSVP to a scheduled session.
// Called with `actorID == userID` by the handler. If you ever expose a
// "set someone else's RSVP" endpoint, add a separate method — don't overload
// this one.
func (s *Service) SetRSVP(ctx context.Context, actorID, sessionID, rsvp string) (*Attendance, error) {
	if !ValidRSVP(rsvp) || rsvp == RSVPPending {
		return nil, ErrInvalidStatus
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	sc, err := s.getSessionForActor(ctx, tx, sessionID, actorID)
	if err != nil {
		return nil, err
	}
	if sc.Cancelled() {
		return nil, ErrSessionCancelled
	}
	if s.now().After(sc.EndsAt) {
		return nil, ErrTooLate // can't RSVP for a session that's over
	}

	a, err := s.repo.SetRSVP(ctx, tx, sessionID, actorID, rsvp)
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

// ==================== Check-in (self-only) ====================

func (s *Service) CheckIn(ctx context.Context, actorID, sessionID string) (*Attendance, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	sc, err := s.getSessionForActor(ctx, tx, sessionID, actorID)
	if err != nil {
		return nil, err
	}
	if sc.Cancelled() {
		return nil, ErrSessionCancelled
	}

	now := s.now().UTC()
	if now.Before(sc.StartsAt.Add(-CheckInEarlyWindow)) {
		return nil, ErrTooEarly
	}
	if now.After(sc.EndsAt) {
		return nil, ErrTooLate
	}

	// Late = starts_at + grace exceeded.
	late := now.After(sc.StartsAt.Add(CheckInGraceWindow))

	a, err := s.repo.CheckIn(ctx, tx, sessionID, actorID, now, late)
	if err != nil {
		return nil, err // ErrAlreadyCheckedIn passes through
	}
	return a, tx.Commit(ctx)
}

func (s *Service) CheckOut(ctx context.Context, actorID, sessionID string) (*Attendance, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := s.getSessionForActor(ctx, tx, sessionID, actorID); err != nil {
		return nil, err
	}

	now := s.now().UTC()
	a, err := s.repo.CheckOut(ctx, tx, sessionID, actorID, now)
	if errors.Is(err, ErrNotCheckedIn) {
		// Refine: was the user never checked in, or already checked out?
		existing, gerr := s.repo.GetBySessionUser(ctx, tx, sessionID, actorID)
		if gerr == nil && existing.CheckedInAt != nil && existing.CheckedOutAt != nil {
			return existing, tx.Commit(ctx) // idempotent
		}
		return nil, ErrNotCheckedIn
	}
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

// ==================== Self: get own row ====================

// func (s *Service) GetMine(ctx context.Context, actorID, sessionID string) (*Attendance, error) {
// 	if _, err := s.getSessionForActor(ctx, s.db, sessionID, actorID); err != nil {
// 		return nil, err
// 	}
// 	a, err := s.repo.GetBySessionUser(ctx, s.db, sessionID, actorID)
// 	if errors.Is(err, ErrNotFound) {
// 		return nil, ErrNotFound
// 	}
// 	return a, err
// }
func (s *Service) GetMine(
    ctx context.Context,
    actorID, sessionID string,
) (*Attendance, error) {

    // Verify that the session exists and the user
    // is an active member of its group.
    if _, err := s.getSessionForActor(
        ctx,
        s.db,
        sessionID,
        actorID,
    ); err != nil {
        return nil, err
    }

    // Retrieve existing attendance or create it.
    return s.repo.GetOrCreateBySessionUser(
        ctx,
        s.db,
        sessionID,
        actorID,
    )
}
// func (s *Service) GetMine(
//     ctx context.Context,
//     actorID, sessionID string,
// ) (*Attendance, error) {

//     if _, err := s.getSessionForActor(ctx, s.db, sessionID, actorID); err != nil {
//         return nil, err
//     }

//     log.Println("[attendance] About to get or create attendance")

//     a, err := s.repo.GetOrCreateBySessionUser(
//         ctx,
//         s.db,
//         sessionID,
//         actorID,
//     )

//     if err != nil {
//         log.Printf("[attendance] GetOrCreateBySessionUser failed: %v", err)
//         return nil, err
//     }

//     log.Printf("[attendance] Attendance retrieved: %s", a.ID)

//     return a, nil
// }
// }
// func (s *Service) GetMine(ctx context.Context, actorID, sessionID string) (*Attendance, error) {
//     if _, err := s.getSessionForActor(ctx, s.db, sessionID, actorID); err != nil {
//         return nil, err
//     }

//     a, err := s.repo.GetBySessionUser(ctx, s.db, sessionID, actorID)
//     if errors.Is(err, ErrNotFound) {
//         return nil, ErrNotFound
//     }

//     return a, err
// }

// ==================== Moderator: mark attendance ====================

// Mark lets a moderator+ set another user's attendance status (present,
// late, absent, excused). This is the "attendance verification" flow.
func (s *Service) Mark(ctx context.Context, actorID, sessionID, targetUserID string, req MarkAttendanceRequest) (*Attendance, error) {
	if !ValidAttendanceStatus(req.Status) {
		return nil, ErrInvalidStatus
	}
	// Reason is only valid for absent/excused.
	if req.AbsenceReason != nil && req.Status != AttendanceAbsent && req.Status != AttendanceExcused {
		return nil, ErrInvalidReason
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	sc, actorRole, err := s.requireSessionModerator(ctx, tx, sessionID, actorID)
	if err != nil {
		return nil, err
	}
	_ = actorRole

	if sc.Cancelled() {
		return nil, ErrSessionCancelled
	}

	// Target must be an active group member of the session's group.
	if err := s.requireTargetGroupMember(ctx, tx, sc.GroupID, targetUserID); err != nil {
		return nil, err
	}

	// If status is present/late, checked_in_at must be provided OR default
	// to now. If it's absent/excused/unknown, checked_in_at stays as-is
	// (unless explicitly cleared — we don't support clearing here).
	var at *time.Time
	if req.Status == AttendancePresent || req.Status == AttendanceLate {
		if req.CheckedInAt != nil {
			t := req.CheckedInAt.UTC()
			at = &t
		} else {
			t := s.now().UTC()
			at = &t
		}
	}

	a, err := s.repo.MarkAttendance(ctx, tx, sessionID, targetUserID, req.Status, req.AbsenceReason, at)
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

// ==================== Roster ====================

func (s *Service) ListRoster(ctx context.Context, actorID, sessionID string, f ListFilter) ([]RosterEntry, error) {
	sc, _, err := s.requireSessionMember(ctx, s.db, sessionID, actorID)
	if err != nil {
		return nil, err
	}
	_ = sc
	f.SessionID = sessionID
	entries, err := s.repo.ListRoster(ctx, s.db, f)
	if err != nil {
		return nil, err
	}
	// Mark IsSelf so frontend doesn't have to compare user IDs.
	for i := range entries {
		entries[i].IsSelf = entries[i].UserID == actorID
	}
	return entries, nil
}

// ==================== authorization ====================

// getSessionForActor: minimum gate for self-service. Session must exist;
// actor must be an active group member of the session's group. Returns
// the session context for further time checks.
//
// Anti-enumeration: if the actor is not in the group, the session is
// "not found" from their perspective — even though it exists.
// func (s *Service) getSessionForActor(ctx context.Context, ex pgxExecutor, sessionID, actorID string) (*SessionContext, error) {
// 	sc, err := s.repo.GetSessionContext(ctx, ex, sessionID)
// 	if errors.Is(err, ErrSessionNotFound) {
// 		return nil, ErrSessionNotFound
// 	}
// 	if err != nil {
// 		return nil, err
// 	}
// 	if _, err := s.requireGroupMember(ctx, ex, sc.GroupID, actorID); err != nil {
// 		// Map ErrNotFound → ErrSessionNotFound to keep the abstraction
// 		// coherent: "you can't see this session" and "this session doesn't
// 		// exist for you" are the same answer.
// 		if errors.Is(err, ErrNotFound) {
// 			return nil, ErrSessionNotFound
// 		}
// 		return nil, err
// 	}
// 	return sc, nil
// }
// 
// 
// 



func (s *Service) getSessionForActor(
    ctx context.Context,
    ex pgxExecutor,
    sessionID, actorID string,
) (*SessionContext, error) {

    sc, err := s.repo.GetSessionContext(ctx, ex, sessionID)

    if err != nil {
        log.Printf("[attendance] GetSessionContext failed: %v", err)

        if errors.Is(err, ErrSessionNotFound) {
            return nil, ErrSessionNotFound
        }

        return nil, err
    }

    log.Printf("[attendance] Session found: %s", sc.ID)

    _, err = s.requireGroupMember(ctx, ex, sc.GroupID, actorID)

    if err != nil {
        log.Printf("[attendance] requireGroupMember failed: %v", err)

        if errors.Is(err, ErrNotFound) {
            return nil, ErrSessionNotFound
        }

        return nil, err
    }

    log.Printf("[attendance] Group membership verified")

    return sc, nil
}
// requireSessionMember: gate for reading (roster). Returns the session and
// the caller's role.
func (s *Service) requireSessionMember(ctx context.Context, ex pgxExecutor, sessionID, actorID string) (*SessionContext, GroupRole, error) {
	sc, err := s.repo.GetSessionContext(ctx, ex, sessionID)
	if errors.Is(err, ErrSessionNotFound) {
		return nil, GroupRoleNone, ErrSessionNotFound
	}
	if err != nil {
		return nil, GroupRoleNone, err
	}
	role, err := s.requireGroupMember(ctx, ex, sc.GroupID, actorID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, GroupRoleNone, ErrSessionNotFound
		}
		return nil, GroupRoleNone, err
	}
	return sc, role, nil
}

// requireSessionModerator: gate for writes on other users' attendance.
// Same as requireSessionMember but requires RoleModerator+.
func (s *Service) requireSessionModerator(ctx context.Context, ex pgxExecutor, sessionID, actorID string) (*SessionContext, GroupRole, error) {
	sc, role, err := s.requireSessionMember(ctx, ex, sessionID, actorID)
	if err != nil {
		return nil, GroupRoleNone, err
	}
	if role < GroupRoleModerator {
		return nil, GroupRoleNone, ErrForbidden
	}
	return sc, role, nil
}

// requireGroupMember: the low-level gate. Returns the actor's role.
// Non-member → ErrNotFound (anti-enumeration). Suspended → ErrForbidden.
func (s *Service) requireGroupMember(ctx context.Context, ex pgxExecutor, groupID, userID string) (GroupRole, error) {
	var roleStr, status string
	err := ex.QueryRow(ctx,
		`SELECT role::text, status
		   FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid
		  FOR SHARE`,
		groupID, userID).Scan(&roleStr, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupRoleNone, ErrNotFound
	}
	if err != nil {
		return GroupRoleNone, err
	}
	if status != "active" {
		return GroupRoleNone, ErrForbidden
	}
	return ParseGroupRole(roleStr), nil
}

// requireTargetGroupMember: the target of a moderator write must also be an
// active group member. Uses FOR SHARE so a concurrent leave can't slip in.
func (s *Service) requireTargetGroupMember(ctx context.Context, ex pgxExecutor, groupID, userID string) error {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM group_memberships
		     WHERE group_id = $1::uuid AND user_id = $2::uuid
		       AND status = 'active'
		     FOR SHARE
		 )`, groupID, userID,
	).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotGroupMember
	}
	return nil
}

// expose the self-service gate for other packages (e.g. a WebSocket join
// that wants "may this user touch this session?").
func (s *Service) RequireSessionMember(ctx context.Context, sessionID, actorID string) error {
	_, _, err := s.requireSessionMember(ctx, s.db, sessionID, actorID)
	return err
}

// sanity: silence unused import when fmt is only used in some branches.
var _ = strings.TrimSpace
var _ = fmt.Sprintf