// internal/calls/repository.go
package calls

import (
	"context"
	"errors"

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

const callCols = `id::text, group_id::text, room_id::text, started_by::text, started_at, ended_at`

func scanCall(row pgx.Row) (*Call, error) {
	var c Call
	err := row.Scan(&c.ID, &c.GroupID, &c.RoomID, &c.StartedBy, &c.StartedAt, &c.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// --- calls ---

// ActiveCallByRoom returns the room's live call. forUpdate serialises
// concurrent joins so the capacity check can't be raced.
func (r *Repository) ActiveCallByRoom(ctx context.Context, ex pgxExecutor, roomID string, forUpdate bool) (*Call, error) {
	q := `SELECT ` + callCols + ` FROM room_calls WHERE room_id = $1::uuid AND ended_at IS NULL`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanCall(ex.QueryRow(ctx, q, roomID))
}

// OpenCall returns the room's active call, creating it if there is none.
// created reports whether this call started it. The partial unique index
// room_calls_one_active_idx makes concurrent starters converge on one row.
func (r *Repository) OpenCall(ctx context.Context, ex pgxExecutor, groupID, roomID, startedBy string) (*Call, bool, error) {
	for i := 0; i < 3; i++ {
		c, err := scanCall(ex.QueryRow(ctx,
			`INSERT INTO room_calls (group_id, room_id, started_by)
			 VALUES ($1::uuid, $2::uuid, $3::uuid)
			 ON CONFLICT (room_id) WHERE ended_at IS NULL DO NOTHING
			 RETURNING `+callCols, groupID, roomID, startedBy))
		if err == nil {
			return c, true, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
		// Someone else's call is active: lock and use it.
		c, err = r.ActiveCallByRoom(ctx, ex, roomID, true)
		if err == nil {
			return c, false, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
		// It ended between the two statements; try again.
	}
	return nil, false, ErrNotFound
}

// EndCallIfEmpty closes a call that has nobody left in it.
func (r *Repository) EndCallIfEmpty(ctx context.Context, ex pgxExecutor, callID string) (bool, error) {
	tag, err := ex.Exec(ctx,
		`UPDATE room_calls SET ended_at = now()
		  WHERE id = $1::uuid AND ended_at IS NULL
		    AND NOT EXISTS (SELECT 1 FROM call_participants
		                     WHERE call_id = $1::uuid AND left_at IS NULL)`, callID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// EndCall force-ends a call and returns the users who were still in it.
func (r *Repository) EndCall(ctx context.Context, ex pgxExecutor, callID string) ([]string, error) {
	rows, err := ex.Query(ctx,
		`UPDATE call_participants SET left_at = now()
		  WHERE call_id = $1::uuid AND left_at IS NULL
		  RETURNING user_id::text`, callID)
	if err != nil {
		return nil, err
	}
	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = ex.Exec(ctx,
		`UPDATE room_calls SET ended_at = now() WHERE id = $1::uuid AND ended_at IS NULL`, callID)
	return users, err
}

// CloseAllOpen cleans up after a crash/restart: no socket survives a restart,
// so nobody can still be "in" a call.
func (r *Repository) CloseAllOpen(ctx context.Context, ex pgxExecutor) error {
	if _, err := ex.Exec(ctx,
		`UPDATE call_participants SET left_at = now() WHERE left_at IS NULL`); err != nil {
		return err
	}
	_, err := ex.Exec(ctx, `UPDATE room_calls SET ended_at = now() WHERE ended_at IS NULL`)
	return err
}

// --- participants ---

// UpsertParticipant adds the user to the call. Idempotent: a second tab or a
// retried request just refreshes the media flags. inserted is false in that case.
// (xmax = 0 is true only for a freshly inserted row.)
func (r *Repository) UpsertParticipant(ctx context.Context, ex pgxExecutor, callID, userID string, audio, video bool) (Participant, bool, error) {
	var p Participant
	var inserted bool
	err := ex.QueryRow(ctx,
		`WITH up AS (
		     INSERT INTO call_participants (call_id, user_id, audio_on, video_on)
		     VALUES ($1::uuid, $2::uuid, $3, $4)
		     ON CONFLICT (user_id) WHERE left_at IS NULL
		     DO UPDATE SET audio_on = EXCLUDED.audio_on, video_on = EXCLUDED.video_on
		         WHERE call_participants.call_id = EXCLUDED.call_id
		     RETURNING user_id, audio_on, video_on, joined_at, (xmax = 0) AS inserted)
		 SELECT up.user_id::text, u.display_name, up.audio_on, up.video_on, up.joined_at, up.inserted
		   FROM up JOIN users u ON u.id = up.user_id`,
		callID, userID, audio, video,
	).Scan(&p.UserID, &p.Name, &p.AudioOn, &p.VideoOn, &p.JoinedAt, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, ErrInvalid // user is active in a different call
	}
	return p, inserted, err
}

// LeaveOtherCalls moves the user out of any call except exceptCallID
// (one live call per person) and reports which calls they left.
func (r *Repository) LeaveOtherCalls(ctx context.Context, ex pgxExecutor, userID, exceptCallID string) ([]leftRef, error) {
	rows, err := ex.Query(ctx,
		`UPDATE call_participants p SET left_at = now()
		   FROM room_calls c
		  WHERE c.id = p.call_id AND p.user_id = $1::uuid
		    AND p.left_at IS NULL AND p.call_id <> $2::uuid
		  RETURNING c.id::text, c.room_id::text, c.group_id::text`, userID, exceptCallID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []leftRef
	for rows.Next() {
		var l leftRef
		if err := rows.Scan(&l.CallID, &l.RoomID, &l.GroupID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *Repository) LeaveParticipant(ctx context.Context, ex pgxExecutor, callID, userID string) (bool, error) {
	tag, err := ex.Exec(ctx,
		`UPDATE call_participants SET left_at = now()
		  WHERE call_id = $1::uuid AND user_id = $2::uuid AND left_at IS NULL`, callID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) SetMedia(ctx context.Context, ex pgxExecutor, callID, userID string, audio, video *bool) (Participant, error) {
	var p Participant
	err := ex.QueryRow(ctx,
		`WITH upd AS (
		     UPDATE call_participants
		        SET audio_on = COALESCE($3, audio_on), video_on = COALESCE($4, video_on)
		      WHERE call_id = $1::uuid AND user_id = $2::uuid AND left_at IS NULL
		     RETURNING user_id, audio_on, video_on, joined_at)
		 SELECT upd.user_id::text, u.display_name, upd.audio_on, upd.video_on, upd.joined_at
		   FROM upd JOIN users u ON u.id = upd.user_id`,
		callID, userID, audio, video,
	).Scan(&p.UserID, &p.Name, &p.AudioOn, &p.VideoOn, &p.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotInCall
	}
	return p, err
}

func (r *Repository) CountActive(ctx context.Context, ex pgxExecutor, callID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT count(*) FROM call_participants WHERE call_id = $1::uuid AND left_at IS NULL`,
		callID).Scan(&n)
	return n, err
}

func (r *Repository) ListParticipants(ctx context.Context, ex pgxExecutor, callID string) ([]Participant, error) {
	rows, err := ex.Query(ctx,
		`SELECT p.user_id::text, u.display_name, p.audio_on, p.video_on, p.joined_at
		   FROM call_participants p JOIN users u ON u.id = p.user_id
		  WHERE p.call_id = $1::uuid AND p.left_at IS NULL
		  ORDER BY p.joined_at, p.id`, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Participant{}
	for rows.Next() {
		var p Participant
		if err := rows.Scan(&p.UserID, &p.Name, &p.AudioOn, &p.VideoOn, &p.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- read-only lookups into other modules' tables (same approach as chat) ---

func (r *Repository) MemberRole(ctx context.Context, ex pgxExecutor, groupID, userID string) (role, status string, err error) {
	err = ex.QueryRow(ctx,
		`SELECT role::text, status FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid FOR SHARE`,
		groupID, userID).Scan(&role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return
}

func (r *Repository) GetRoom(ctx context.Context, ex pgxExecutor, id string) (*Room, error) {
	var rm Room
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, access, archived_at IS NOT NULL
		   FROM study_rooms WHERE id = $1::uuid`, id,
	).Scan(&rm.ID, &rm.GroupID, &rm.Access, &rm.Archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rm, nil
}

func (r *Repository) HasRoomMembership(ctx context.Context, ex pgxExecutor, roomID, userID string) (bool, error) {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM room_memberships
		                 WHERE room_id = $1::uuid AND user_id = $2::uuid AND status = 'active')`,
		roomID, userID).Scan(&ok)
	return ok, err
}

// --- helpers ---

func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}