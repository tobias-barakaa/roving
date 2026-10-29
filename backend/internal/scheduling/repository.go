// internal/scheduling/repository.go
package scheduling

import (
	"context"
	"errors"
    "time"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxExecutor: satisfied by *pgxpool.Pool and pgx.Tx.
type pgxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// --- series ---

func (r *Repository) InsertSeries(ctx context.Context, ex pgxExecutor, s *Series) error {
	return ex.QueryRow(ctx,
		`INSERT INTO study_session_series
		    (group_id, room_id, created_by, title, description, timezone,
		     recurrence_rule, local_start_time, duration_seconds, starts_on, ends_on)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::time, $9, $10::date, $11::date)
		 RETURNING id::text, status, generated_until, created_at, updated_at`,
		s.GroupID, s.RoomID, s.CreatedBy, s.Title, s.Description,
		s.Timezone, s.RecurrenceRule, s.LocalStartTime, s.DurationSeconds,
		s.StartsOn, s.EndsOn,
	).Scan(&s.ID, &s.Status, &s.GeneratedUntil, &s.CreatedAt, &s.UpdatedAt)
}

func (r *Repository) GetSeriesByID(ctx context.Context, ex pgxExecutor, id string) (*Series, error) {
	var s Series
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, room_id::text, created_by::text,
		        title, description, timezone, recurrence_rule,
		        local_start_time::text, duration_seconds, starts_on, ends_on,
		        status, generated_until, created_at, updated_at
		   FROM study_session_series WHERE id = $1::uuid`, id,
	).Scan(&s.ID, &s.GroupID, &s.RoomID, &s.CreatedBy, &s.Title, &s.Description,
		&s.Timezone, &s.RecurrenceRule, &s.LocalStartTime, &s.DurationSeconds,
		&s.StartsOn, &s.EndsOn, &s.Status, &s.GeneratedUntil, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

func (r *Repository) UpdateSeries(ctx context.Context, ex pgxExecutor, id string, f UpdateSeriesFields) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_session_series SET
		    room_id          = COALESCE($2::uuid, room_id),
		    title            = COALESCE($3, title),
		    description      = COALESCE($4, description),
		    local_start_time = COALESCE($5::time, local_start_time),
		    duration_seconds = COALESCE($6, duration_seconds)
		 WHERE id = $1::uuid`,
		id, f.RoomID, f.Title, f.Description, f.LocalStartTime, f.DurationSeconds)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type UpdateSeriesFields struct {
	RoomID          *string
	Title           *string
	Description     *string
	LocalStartTime  *string
	DurationSeconds *int
}

func (r *Repository) SetSeriesStatus(ctx context.Context, ex pgxExecutor, id, status string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_session_series SET status = $2 WHERE id = $1::uuid`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ExtendGenerationWindow returns active series whose generated_until is before
// cutoff, plus the group. Ordered by generated_until so older backlog first.
func (r *Repository) ExtendGenerationWindow(ctx context.Context, ex pgxExecutor, cutoff time.Time) ([]Series, error) {
	rows, err := ex.Query(ctx,
		`SELECT id::text, group_id::text, room_id::text, created_by::text,
		        title, description, timezone, recurrence_rule,
		        local_start_time::text, duration_seconds, starts_on, ends_on,
		        status, generated_until, created_at, updated_at
		   FROM study_session_series
		  WHERE status = 'active'
		    AND (generated_until IS NULL OR generated_until < $1)
		  ORDER BY generated_until NULLS FIRST
		  LIMIT 200`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Series
	for rows.Next() {
		var s Series
		if err := rows.Scan(&s.ID, &s.GroupID, &s.RoomID, &s.CreatedBy, &s.Title,
			&s.Description, &s.Timezone, &s.RecurrenceRule,
			&s.LocalStartTime, &s.DurationSeconds, &s.StartsOn, &s.EndsOn,
			&s.Status, &s.GeneratedUntil, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PauseSeriesByGroup archives a group's scheduling as part of `groups.Archive`.
// Called via the Pauser adapter.
func (r *Repository) PauseSeriesByGroup(ctx context.Context, ex pgxExecutor, groupID string) error {
	_, err := ex.Exec(ctx,
		`UPDATE study_session_series SET status = 'paused'
		  WHERE group_id = $1::uuid AND status = 'active'`, groupID)
	return err
}

// --- sessions (occurrences) ---

// InsertSession is idempotent for series-generated sessions thanks to the
// UNIQUE (series_id, original_starts_at) constraint. Use ON CONFLICT DO NOTHING
// so concurrent generators can't fail each other.
func (r *Repository) InsertSession(ctx context.Context, ex pgxExecutor, s *Session) (bool, error) {
	err := ex.QueryRow(ctx,
		`INSERT INTO study_sessions
		    (group_id, room_id, series_id, original_starts_at, created_by,
		     title, description, starts_at, ends_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6, $7, $8, $9)
		 ON CONFLICT (series_id, original_starts_at) DO NOTHING
		 RETURNING id::text, status, created_at, updated_at`,
		s.GroupID, s.RoomID, s.SeriesID, s.OriginalStartsAt, s.CreatedBy,
		s.Title, s.Description, s.StartsAt, s.EndsAt,
	).Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // already existed
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) GetSessionByID(ctx context.Context, ex pgxExecutor, id string) (*Session, error) {
	var s Session
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, room_id::text, series_id::text,
		        original_starts_at, created_by::text, title, description,
		        starts_at, ends_at, status, cancelled_at, created_at, updated_at
		   FROM study_sessions WHERE id = $1::uuid`, id,
	).Scan(&s.ID, &s.GroupID, &s.RoomID, &s.SeriesID, &s.OriginalStartsAt,
		&s.CreatedBy, &s.Title, &s.Description, &s.StartsAt, &s.EndsAt,
		&s.Status, &s.CancelledAt, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// GetSessionDetail joins room name and series title in one round-trip.
func (r *Repository) GetSessionDetail(ctx context.Context, ex pgxExecutor, id string) (*SessionDetail, error) {
	var d SessionDetail
	err := ex.QueryRow(ctx,
		`SELECT s.id::text, s.group_id::text, s.room_id::text, s.series_id::text,
		        s.original_starts_at, s.created_by::text, s.title, s.description,
		        s.starts_at, s.ends_at, s.status, s.cancelled_at, s.created_at, s.updated_at,
		        r.name AS room_name, ser.title AS series_title
		   FROM study_sessions s
		   LEFT JOIN study_rooms r ON r.id = s.room_id
		   LEFT JOIN study_session_series ser ON ser.id = s.series_id
		  WHERE s.id = $1::uuid`, id,
	).Scan(&d.ID, &d.GroupID, &d.RoomID, &d.SeriesID, &d.OriginalStartsAt,
		&d.CreatedBy, &d.Title, &d.Description, &d.StartsAt, &d.EndsAt,
		&d.Status, &d.CancelledAt, &d.CreatedAt, &d.UpdatedAt,
		&d.RoomName, &d.SeriesTitle)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

func (r *Repository) UpdateSession(ctx context.Context, ex pgxExecutor, id string, f UpdateSessionFields) error {
	// Only 'scheduled' sessions can be edited. Cancelled/completed are frozen.
	tag, err := ex.Exec(ctx,
		`UPDATE study_sessions SET
		    room_id     = COALESCE($2::uuid, room_id),
		    title       = COALESCE($3, title),
		    description = COALESCE($4, description),
		    starts_at   = COALESCE($5, starts_at),
		    ends_at     = COALESCE($6, ends_at)
		 WHERE id = $1::uuid AND status = 'scheduled'`,
		id, f.RoomID, f.Title, f.Description, f.StartsAt, f.EndsAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionLocked
	}
	return nil
}

type UpdateSessionFields struct {
	RoomID      *string
	Title       *string
	Description *string
	StartsAt    *time.Time
	EndsAt      *time.Time
}

func (r *Repository) CancelSession(ctx context.Context, ex pgxExecutor, id string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_sessions
		    SET status = 'cancelled', cancelled_at = now()
		  WHERE id = $1::uuid AND status = 'scheduled'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionLocked
	}
	return nil
}

// ListSessions returns sessions for a group, with optional filters.
// NOTE: room_id/series_id are filtered only if non-empty; from/to are
// inclusive on starts_at.
func (r *Repository) ListSessions(ctx context.Context, ex pgxExecutor, f ListFilter) ([]SessionDetail, error) {
	rows, err := ex.Query(ctx,
		`SELECT s.id::text, s.group_id::text, s.room_id::text, s.series_id::text,
		        s.original_starts_at, s.created_by::text, s.title, s.description,
		        s.starts_at, s.ends_at, s.status, s.cancelled_at, s.created_at, s.updated_at,
		        r.name, ser.title
		   FROM study_sessions s
		   LEFT JOIN study_rooms r ON r.id = s.room_id
		   LEFT JOIN study_session_series ser ON ser.id = s.series_id
		  WHERE s.group_id = $1::uuid
		    AND ($2 = '' OR s.room_id = $2::uuid)
		    AND ($3 = '' OR s.series_id = $3::uuid)
		    AND ($4::timestamptz IS NULL OR s.starts_at >= $4)
		    AND ($5::timestamptz IS NULL OR s.starts_at <  $5)
		    AND ($6 = '' OR s.status = $6)
		  ORDER BY s.starts_at ASC
		  LIMIT $7 OFFSET $8`,
		f.GroupID, f.RoomID, f.SeriesID, f.From, f.To, f.Status,
		normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionDetail
	for rows.Next() {
		var d SessionDetail
		if err := rows.Scan(&d.ID, &d.GroupID, &d.RoomID, &d.SeriesID,
			&d.OriginalStartsAt, &d.CreatedBy, &d.Title, &d.Description,
			&d.StartsAt, &d.EndsAt, &d.Status, &d.CancelledAt, &d.CreatedAt, &d.UpdatedAt,
			&d.RoomName, &d.SeriesTitle); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- helpers ---

func normalizeLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}

func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}