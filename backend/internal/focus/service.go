// internal/focus/service.go
package focus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/pgconn"
)

// Defaults for a cycle when the client doesn't specify.
const (
	DefaultFocusSeconds = 1500
	DefaultBreakSeconds = 300
	DefaultRounds       = 4
)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
	now  func() time.Time // injectable for tests
}

func NewService(repo *Repository, db *pgxpool.Pool) *Service {
	return &Service{repo: repo, db: db, now: time.Now}
}

// ==================== Start cycle ====================

func (s *Service) StartCycle(ctx context.Context, actorID string, req StartCycleRequest) (*Cycle, error) {
	// Defaults.
	focusSec := DefaultFocusSeconds
	breakSec := DefaultBreakSeconds
	rounds := DefaultRounds
	if req.PlannedFocusSeconds != nil {
		focusSec = *req.PlannedFocusSeconds
	}
	if req.PlannedBreakSeconds != nil {
		breakSec = *req.PlannedBreakSeconds
	}
	if req.PlannedRounds != nil {
		rounds = *req.PlannedRounds
	}
	if focusSec < 60 || focusSec > 14400 || breakSec < 0 || breakSec > 3600 || rounds < 1 || rounds > 20 {
		return nil, ErrInvalidPlan
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Context gate (optional).
	if err := s.validateContext(ctx, tx, actorID, ContextInput{
		StudySessionID: req.StudySessionID,
		RoomID:         req.RoomID,
	}); err != nil {
		return nil, err
	}

	c := &Cycle{
		UserID:              actorID,
		StudySessionID:      req.StudySessionID,
		RoomID:              req.RoomID,
		PlannedFocusSeconds: focusSec,
		PlannedBreakSeconds: breakSec,
		PlannedRounds:       rounds,
	}
	if err := s.repo.InsertCycle(ctx, tx, c); err != nil {
		// Partial unique index violation = one cycle running.
		if isUniqueViolation(err) {
			return nil, ErrCycleRunning
		}
		return nil, err
	}
	return c, tx.Commit(ctx)
}

// ==================== Start interval ====================

// StartInterval: if a cycle is running, start an interval in it.
// If no cycle is running, auto-create a default cycle (with no context)
// and start the first interval. This makes "just start a Pomodoro" a
// single API call.
func (s *Service) StartInterval(ctx context.Context, actorID string, req StartIntervalRequest) (*Interval, error) {
	if !ValidKind(req.Kind) {
		return nil, ErrInvalidKind
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Already running? Refuse (index would refuse anyway, but clean error).
	running, err := s.repo.GetRunningInterval(ctx, tx, actorID)
	if err != nil {
		return nil, err
	}
	if running != nil {
		return nil, ErrIntervalRunning
	}

	cycle, err := s.repo.GetRunningCycle(ctx, tx, actorID)
	if err != nil {
		return nil, err
	}
	if cycle == nil {
		// Auto-create a default cycle.
		cycle = &Cycle{
			UserID:              actorID,
			PlannedFocusSeconds: DefaultFocusSeconds,
			PlannedBreakSeconds: DefaultBreakSeconds,
			PlannedRounds:       DefaultRounds,
			}
		if err := s.repo.InsertCycle(ctx, tx, cycle); err != nil {
					if isUniqueViolation(err) {
						return nil, ErrCycleRunning
					}
					return nil, err
				}
			} else if cycle.Finished() {
				// Defensive: a "running" cycle that already ended shouldn't exist,
				// but if it does, refuse rather than corrupt state.
				return nil, ErrCycleFinished
			}
		
			// Planned seconds: explicit override, else derived from cycle plan + kind.
			planned := 0
			switch {
			case req.PlannedSeconds != nil:
				planned = *req.PlannedSeconds
			case req.Kind == KindFocus:
				planned = cycle.PlannedFocusSeconds
			default: // short_break / long_break
				planned = cycle.PlannedBreakSeconds
			}
			if planned < 1 || planned > 14400 {
				return nil, ErrInvalidPlan
			}
		
			// Sequence number: next in this cycle.
			seq, err := s.repo.NextSequenceNo(ctx, tx, cycle.ID)
			if err != nil {
				return nil, err
			}
		
			// Inherit cycle's context so intervals are queryable without a join.
			iv := &Interval{
				UserID:         actorID,
				CycleID:        &cycle.ID,
				StudySessionID: cycle.StudySessionID,
				RoomID:         cycle.RoomID,
				SequenceNo:     &seq,
				Kind:           req.Kind,
				PlannedSeconds: planned,
			}
			if err := s.repo.InsertRunningInterval(ctx, tx, iv); err != nil {
				if isUniqueViolation(err) {
					return nil, ErrIntervalRunning
				}
				return nil, err
			}
			return iv, tx.Commit(ctx)
		}
		
		// ==================== End interval ====================
		
		// EndInterval finishes the currently running interval for the actor.
		// Interrupted=true → status 'interrupted' (pause / abandon);
		// Interrupted=false → status 'completed' (ran its course).
		//
		// After completing a focus interval, the client is expected to start a
		// break (or another focus interval). We don't auto-chain — explicit is
		// better, and it lets the UI prompt "take a 5-minute break?".
		func (s *Service) EndInterval(ctx context.Context, actorID string, req EndIntervalRequest) (*Interval, error) {
			tx, err := s.db.Begin(ctx)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback(ctx)
		
			running, err := s.repo.GetRunningInterval(ctx, tx, actorID)
			if err != nil {
				return nil, err
			}
			if running == nil {
				return nil, ErrNoRunningInterval
			}
		
			finalStatus := IntervalCompleted
			if req.Interrupted {
				finalStatus = IntervalInterrupted
			}
		
			iv, err := s.repo.EndInterval(ctx, tx, running.ID, actorID, finalStatus)
			if err != nil {
				return nil, err // ErrNoRunningInterval if race lost
			}
			return iv, tx.Commit(ctx)
		}
		
		// ==================== End cycle ====================
		
		// EndCycle finishes the running cycle. If an interval is still running it
		// is interrupted in the same transaction — you cannot have a completed
		// cycle with a live interval, and the DB doesn't enforce that, so we do.
		func (s *Service) EndCycle(ctx context.Context, actorID string, req EndCycleRequest) (*Cycle, error) {
			tx, err := s.db.Begin(ctx)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback(ctx)
		
			cycle, err := s.repo.GetRunningCycle(ctx, tx, actorID)
			if err != nil {
				return nil, err
			}
			if cycle == nil {
				return nil, ErrNoRunningCycle
			}
		
			// Interrupt any live interval first.
			if _, err := s.repo.InterruptRunningIntervalsForCycle(ctx, tx, cycle.ID, actorID); err != nil {
				return nil, err
			}
		
			finalStatus := CycleCancelled
			if req.Complete {
				finalStatus = CycleCompleted
			}
			if err := s.repo.FinishCycle(ctx, tx, cycle.ID, actorID, finalStatus); err != nil {
				return nil, err
			}
		
			// Re-read so the response carries the new status/timestamps.
			cycle, err = s.repo.GetCycle(ctx, tx, cycle.ID, actorID)
			if err != nil {
				return nil, err
			}
			return cycle, tx.Commit(ctx)
		}
		
		// ==================== Read: cycle detail ====================
		
		func (s *Service) GetCycleDetail(ctx context.Context, actorID, cycleID string) (*CycleDetail, error) {
			cycle, err := s.repo.GetCycle(ctx, s.db, cycleID, actorID)
			if err != nil {
				return nil, err // ErrNotFound if it's not yours (or doesn't exist)
			}
		
			intervals, err := s.repo.ListIntervalsByCycle(ctx, s.db, cycleID, actorID)
			if err != nil {
				return nil, err
			}
		
			detail := &CycleDetail{Cycle: *cycle, Intervals: intervals}
			// Aggregate in Go — we already loaded the rows, no second round-trip.
			for _, iv := range intervals {
				if iv.ActualSeconds == nil {
					continue
				}
				switch iv.Kind {
				case KindFocus:
					detail.TotalFocusSeconds += *iv.ActualSeconds
					if iv.Status == IntervalCompleted {
						detail.CompletedFocusRounds++
					}
				default:
					detail.TotalBreakSeconds += *iv.ActualSeconds
				}
			}
			return detail, nil
		}
		
		// ==================== Read: running state ====================
		
		// GetCurrent returns whatever the actor has running right now: the cycle
		// (if any) and the interval (if any). Both nil is the normal idle state.
		// This is the endpoint the frontend polls / websocket-pushes to keep the
		// timer UI in sync after a refresh.
		func (s *Service) GetCurrent(ctx context.Context, actorID string) (*Cycle, *Interval, error) {
			cycle, err := s.repo.GetRunningCycle(ctx, s.db, actorID)
			if err != nil {
				return nil, nil, err
			}
			iv, err := s.repo.GetRunningInterval(ctx, s.db, actorID)
			if err != nil {
				return nil, nil, err
			}
			return cycle, iv, nil
		}
		
		// ==================== List cycles ====================
		
		func (s *Service) ListCycles(ctx context.Context, actorID string, f ListFilter) ([]Cycle, error) {
			f.UserID = actorID // ownership is non-negotiable
			return s.repo.ListCycles(ctx, s.db, f)
		}
		
		// ==================== Stats ====================
		
		func (s *Service) Stats(ctx context.Context, actorID string, from, to time.Time) (*Stats, error) {
			if !to.After(from) {
				return nil, ErrInvalidPlan
			}
			return s.repo.AggregateStats(ctx, s.db, actorID, from.UTC(), to.UTC())
		}
		
		// ==================== context eligibility ====================
		
		// validateContext enforces Gate B: if a study_session_id and/or room_id is
		// provided, the actor must be an active member of each context's group, and
		// if both are provided they must belong to the same group.
		//
		// Both nil → no-op (personal cycle, no context).
		func (s *Service) validateContext(ctx context.Context, ex pgxExecutor, actorID string, ci ContextInput) error {
			var sessionGroup, roomGroup string
		
			if ci.StudySessionID != nil {
				g, err := s.repo.SessionGroupID(ctx, ex, *ci.StudySessionID)
				if err != nil {
					return err // ErrSessionNotFound
				}
				// if err := s.requireGroupMember(ctx, ex, g, actorID); err != nil {
					if _, err := s.requireGroupMember(ctx, ex, g, actorID); err != nil {
					// Anti-enumeration: non-member sees "not found", not "forbidden".
					if errors.Is(err, ErrNotFound) {
						return ErrSessionNotFound
					}
					return err
				}
				sessionGroup = g
			}
		
			if ci.RoomID != nil {
				g, err := s.repo.RoomGroupID(ctx, ex, *ci.RoomID)
				if err != nil {
					return err // ErrRoomNotFound
				}
				// if err := s.requireGroupMember(ctx, ex, g, actorID); err != nil {
					if _, err := s.requireGroupMember(ctx, ex, g, actorID); err != nil {
					if errors.Is(err, ErrNotFound) {
						return ErrRoomNotFound
					}
					return err
				}
				roomGroup = g
			}
		
			// If both were supplied, they must agree. The schema can't express this
			// (two independent FKs), so it lives here.
			if sessionGroup != "" && roomGroup != "" && sessionGroup != roomGroup {
				return ErrInvalidContext
			}
			return nil
		}
		
		// requireGroupMember is the same primitive the other modules use. Returns
		// the actor's role, or ErrNotFound (non-member) / ErrForbidden (suspended).
		// Focus only needs "is a member", not the role, but we keep the shape
		// consistent so a future "moderator can see group focus stats" feature
		// drops in without changing this.
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
		
		// ==================== helpers ====================
		
		// isUniqueViolation detects Postgres 23505 so the service can translate the
		// partial-unique-index violation ("one running cycle/interval per user")
		// into a domain error instead of leaking a DB error to the handler.
		func isUniqueViolation(err error) bool {
			var pgErr *pgconn.PgError
			return errors.As(err, &pgErr) && pgErr.Code == "23505"
		}
		
		// referenced so the import is used even if a branch is optimized away in a
		// future edit; keeps `go vet` quiet.
		var _ = fmt.Sprintf
		