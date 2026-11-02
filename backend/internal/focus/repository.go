// internal/focus/repository.go
package focus

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

// ---------- cycles ----------

func (r *Repository) InsertCycle(ctx context.Context, ex pgxExecutor, c *Cycle) error {
	return ex.QueryRow(ctx,
		`INSERT INTO focus_cycles
		    (user_id, study_session_id, room_id, status,
		     planned_focus_seconds, planned_break_seconds, planned_rounds,
		     started_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, 'running',
		         $4, $5, $6, now())
		 RETURNING id::text, started_at, created_at, updated_at`,
		c.UserID, c.StudySessionID, c.RoomID,
		c.PlannedFocusSeconds, c.PlannedBreakSeconds, c.PlannedRounds,
	).Scan(&c.ID, &c.StartedAt, &c.CreatedAt, &c.UpdatedAt)
}

func (r *Repository) GetCycle(ctx context.Context, ex pgxExecutor, cycleID, userID string) (*Cycle, error) {
	var c Cycle
	err := ex.QueryRow(ctx,
		`SELECT id::text, user_id::text, study_session_id::text, room_id::text,
		        status, planned_focus_seconds, planned_break_seconds, planned_rounds,
		        started_at, ended_at, created_at, updated_at
		   FROM focus_cycles
		  WHERE id = $1::uuid AND user_id = $2::uuid`,
		cycleID, userID,
	).Scan(&c.ID, &c.UserID, &c.StudySessionID, &c.RoomID,
		&c.Status, &c.PlannedFocusSeconds, &c.PlannedBreakSeconds, &c.PlannedRounds,
		&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// GetRunningCycle uses the partial unique index. Returns (nil, nil) if none.
// We don't return ErrNotFound because "no running cycle" is a normal state.
func (r *Repository) GetRunningCycle(ctx context.Context, ex pgxExecutor, userID string) (*Cycle, error) {
	var c Cycle
	err := ex.QueryRow(ctx,
		`SELECT id::text, user_id::text, study_session_id::text, room_id::text,
		        status, planned_focus_seconds, planned_break_seconds, planned_rounds,
		        started_at, ended_at, created_at, updated_at
		   FROM focus_cycles
		  WHERE user_id = $1::uuid AND status = 'running'`,
		userID,
	).Scan(&c.ID, &c.UserID, &c.StudySessionID, &c.RoomID,
		&c.Status, &c.PlannedFocusSeconds, &c.PlannedBreakSeconds, &c.PlannedRounds,
		&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repository) FinishCycle(ctx context.Context, ex pgxExecutor, cycleID, userID, status string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE focus_cycles
		    SET status = $3, ended_at = now()
		  WHERE id = $1::uuid AND user_id = $2::uuid AND status = 'running'`,
		cycleID, userID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) ListCycles(ctx context.Context, ex pgxExecutor, f ListFilter) ([]Cycle, error) {
	rows, err := ex.Query(ctx,
		`SELECT id::text, user_id::text, study_session_id::text, room_id::text,
		        status, planned_focus_seconds, planned_break_seconds, planned_rounds,
		        started_at, ended_at, created_at, updated_at
		   FROM focus_cycles
		  WHERE user_id = $1::uuid
		    AND ($2 = '' OR status = $2)
		    AND ($3::timestamptz IS NULL OR created_at >= $3)
		    AND ($4::timestamptz IS NULL OR created_at <  $4)
		  ORDER BY created_at DESC
		  LIMIT $5 OFFSET $6`,
		f.UserID, f.Status, f.From, f.To, normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Cycle
	for rows.Next() {
		var c Cycle
		if err := rows.Scan(&c.ID, &c.UserID, &c.StudySessionID, &c.RoomID,
			&c.Status, &c.PlannedFocusSeconds, &c.PlannedBreakSeconds, &c.PlannedRounds,
			&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------- intervals ----------

// NextSequenceNo returns MAX(sequence_no)+1 for a cycle, or 1 if empty.
// `FOR UPDATE` on the cycle row is the lock; the query itself is a simple
// aggregate. We don't need a separate lock because the running-interval
// unique index already prevents two intervals from being created concurrently.
func (r *Repository) NextSequenceNo(ctx context.Context, ex pgxExecutor, cycleID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT COALESCE(MAX(sequence_no), 0) + 1
		   FROM focus_sessions WHERE cycle_id = $1::uuid`,
		cycleID).Scan(&n)
	return n, err
}

func (r *Repository) InsertRunningInterval(ctx context.Context, ex pgxExecutor, i *Interval) error {
	return ex.QueryRow(ctx,
		`INSERT INTO focus_sessions
		    (user_id, cycle_id, study_session_id, room_id, sequence_no,
		     kind, status, planned_seconds, started_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
		         $6, 'running', $7, now())
		 RETURNING id::text, started_at, created_at, updated_at`,
		i.UserID, i.CycleID, i.StudySessionID, i.RoomID, i.SequenceNo,
		i.Kind, i.PlannedSeconds,
	).Scan(&i.ID, &i.StartedAt, &i.CreatedAt, &i.UpdatedAt)
}

func (r *Repository) GetInterval(ctx context.Context, ex pgxExecutor, id, userID string) (*Interval, error) {
	var i Interval
	err := ex.QueryRow(ctx,
		`SELECT id::text, user_id::text, cycle_id::text, study_session_id::text,
		        room_id::text, sequence_no, kind, status, planned_seconds,
		        started_at, ended_at, actual_seconds, created_at, updated_at
		   FROM focus_sessions
		  WHERE id = $1::uuid AND user_id = $2::uuid`,
		id, userID,
	).Scan(&i.ID, &i.UserID, &i.CycleID, &i.StudySessionID, &i.RoomID,
		&i.SequenceNo, &i.Kind, &i.Status, &i.PlannedSeconds,
		&i.StartedAt, &i.EndedAt, &i.ActualSeconds, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &i, err
}

// GetRunningInterval returns (nil, nil) if none.
func (r *Repository) GetRunningInterval(ctx context.Context, ex pgxExecutor, userID string) (*Interval, error) {
	var i Interval
	err := ex.QueryRow(ctx,
		`SELECT id::text, user_id::text, cycle_id::text, study_session_id::text,
		        room_id::text, sequence_no, kind, status, planned_seconds,
		        started_at, ended_at, actual_seconds, created_at, updated_at
		   FROM focus_sessions
		  WHERE user_id = $1::uuid AND status = 'running'`,
		userID,
	).Scan(&i.ID, &i.UserID, &i.CycleID, &i.StudySessionID, &i.RoomID,
		&i.SequenceNo, &i.Kind, &i.Status, &i.PlannedSeconds,
		&i.StartedAt, &i.EndedAt, &i.ActualSeconds, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// EndInterval transitions a running interval to completed or interrupted.
// The WHERE status='running' clause makes it a no-op on a finished interval.
func (r *Repository) EndInterval(ctx context.Context, ex pgxExecutor, id, userID, finalStatus string) (*Interval, error) {
	var i Interval
	err := ex.QueryRow(ctx,
		`UPDATE focus_sessions
		    SET status = $3, ended_at = now()
		  WHERE id = $1::uuid AND user_id = $2::uuid AND status = 'running'
		 RETURNING id::text, user_id::text, cycle_id::text, study_session_id::text,
		           room_id::text, sequence_no, kind, status, planned_seconds,
		           started_at, ended_at, actual_seconds, created_at, updated_at`,
		id, userID, finalStatus,
	).Scan(&i.ID, &i.UserID, &i.CycleID, &i.StudySessionID, &i.RoomID,
		&i.SequenceNo, &i.Kind, &i.Status, &i.PlannedSeconds,
		&i.StartedAt, &i.EndedAt, &i.ActualSeconds, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoRunningInterval
	}
	return &i, err
}

// InterruptRunningIntervalsForCycle is used when a cycle is cancelled.
// It returns the number of rows touched. Safe on cycles with no running
// interval (returns 0, nil).
func (r *Repository) InterruptRunningIntervalsForCycle(ctx context.Context, ex pgxExecutor, cycleID, userID string) (int64, error) {
	tag, err := ex.Exec(ctx,
		`UPDATE focus_sessions
		    SET status = 'interrupted', ended_at = now()
		  WHERE cycle_id = $1::uuid AND user_id = $2::uuid AND status = 'running'`,
		cycleID, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) ListIntervalsByCycle(ctx context.Context, ex pgxExecutor, cycleID, userID string) ([]Interval, error) {
	rows, err := ex.Query(ctx,
		`SELECT id::text, user_id::text, cycle_id::text, study_session_id::text,
		        room_id::text, sequence_no, kind, status, planned_seconds,
		        started_at, ended_at, actual_seconds, created_at, updated_at
		   FROM focus_sessions
		  WHERE cycle_id = $1::uuid AND user_id = $2::uuid
		  ORDER BY sequence_no ASC NULLS LAST, created_at ASC`,
		cycleID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Interval
	for rows.Next() {
		var i Interval
		if err := rows.Scan(&i.ID, &i.UserID, &i.CycleID, &i.StudySessionID,
			&i.RoomID, &i.SequenceNo, &i.Kind, &i.Status, &i.PlannedSeconds,
			&i.StartedAt, &i.EndedAt, &i.ActualSeconds, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// AggregateStats sums focus and break time for a user in [from, to).
// Uses FILTER for a single round-trip. actual_seconds is NULL for
// never-started or running intervals; coalesce handles that.
func (r *Repository) AggregateStats(ctx context.Context, ex pgxExecutor, userID string, from, to time.Time) (*Stats, error) {
	var s Stats
	s.From = from
	s.To = to
	err := ex.QueryRow(ctx,
		`SELECT
		    COALESCE(SUM(fs.actual_seconds) FILTER (WHERE fs.kind = 'focus'), 0),
		    COALESCE(SUM(fs.actual_seconds) FILTER (WHERE fs.kind IN ('short_break','long_break')), 0),
		    COUNT(DISTINCT fs.cycle_id) FILTER (WHERE fs.cycle_id IS NOT NULL),
		    COUNT(*) FILTER (WHERE fs.kind = 'focus' AND fs.status = 'completed')
		   FROM focus_sessions fs
		  WHERE fs.user_id = $1::uuid
		    AND fs.started_at >= $2 AND fs.started_at < $3`,
		userID, from, to,
	).Scan(&s.TotalFocusSeconds, &s.TotalBreakSeconds, &s.CycleCount, &s.SessionCount)
	return &s, err
}

// ---------- context eligibility ----------

// SessionGroupID returns the group_id for a study_session_id, or ErrSessionNotFound.
func (r *Repository) SessionGroupID(ctx context.Context, ex pgxExecutor, sessionID string) (string, error) {
	var groupID string
	err := ex.QueryRow(ctx,
		`SELECT group_id::text FROM study_sessions WHERE id = $1::uuid`, sessionID,
	).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	return groupID, err
}

// RoomGroupID returns the group_id for a room, or ErrRoomNotFound.
func (r *Repository) RoomGroupID(ctx context.Context, ex pgxExecutor, roomID string) (string, error) {
	var groupID string
	err := ex.QueryRow(ctx,
		`SELECT group_id::text FROM study_rooms WHERE id = $1::uuid`, roomID,
	).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrRoomNotFound
	}
	return groupID, err
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