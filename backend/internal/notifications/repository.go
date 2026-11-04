// internal/notifications/repository.go
package notifications

import (
	"context"
	"encoding/json"

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

const notifCols = `id::text, user_id::text, group_id::text, kind, title, body, data, read_at, created_at`

type scanner interface{ Scan(dest ...any) error }

func scanNotification(row scanner) (Notification, error) {
	var n Notification
	err := row.Scan(&n.ID, &n.UserID, &n.GroupID, &n.Kind, &n.Title, &n.Body,
		&n.Data, &n.ReadAt, &n.CreatedAt)
	return n, err
}

// Insert fans one notification out to many users in a single statement.
// Users who muted this kind are skipped; a repeated (user, dedupe_key) is
// silently ignored. Returns how many rows were actually created.
func (r *Repository) Insert(ctx context.Context, ex pgxExecutor, userIDs []string, n NewNotification) (int64, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}
	data := []byte(`{}`)
	if n.Data != nil {
		b, err := json.Marshal(n.Data)
		if err != nil {
			return 0, err
		}
		data = b
	}
	var dedupe *string
	if n.DedupeKey != "" {
		dedupe = &n.DedupeKey
	}
	tag, err := ex.Exec(ctx,
		`INSERT INTO notifications (user_id, group_id, kind, title, body, data, dedupe_key)
		 SELECT t.uid, $2::uuid, $3, $4, $5, $6::jsonb, $7::text
		   FROM (SELECT unnest($1::text[])::uuid AS uid) t
		  WHERE NOT EXISTS (
		        SELECT 1 FROM notification_preferences p
		         WHERE p.user_id = t.uid AND p.kind = $3 AND NOT p.in_app)
		 ON CONFLICT (user_id, dedupe_key) DO NOTHING`,
		userIDs, n.GroupID, n.Kind, n.Title, n.Body, string(data), dedupe)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GroupMemberIDs returns active members of a group, optionally excluding one user.
func (r *Repository) GroupMemberIDs(ctx context.Context, ex pgxExecutor, groupID, exceptUserID string) ([]string, error) {
	rows, err := ex.Query(ctx,
		`SELECT user_id::text FROM group_memberships
		  WHERE group_id = $1::uuid AND status = 'active'
		    AND ($2 = '' OR user_id <> $2::uuid)`,
		groupID, exceptUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *Repository) List(ctx context.Context, ex pgxExecutor, f ListFilter) ([]Notification, error) {
	rows, err := ex.Query(ctx,
		`SELECT `+notifCols+`
		   FROM notifications
		  WHERE user_id = $1::uuid
		    AND (NOT $2::boolean OR read_at IS NULL)
		    AND ($3::timestamptz IS NULL OR created_at < $3)
		  ORDER BY created_at DESC, id DESC
		  LIMIT $4`,
		f.UserID, f.UnreadOnly, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r *Repository) UnreadCount(ctx context.Context, ex pgxExecutor, userID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND read_at IS NULL`,
		userID).Scan(&n)
	return n, err
}

// MarkRead is idempotent: re-reading an already-read row keeps its first read_at.
func (r *Repository) MarkRead(ctx context.Context, ex pgxExecutor, id, userID string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE notifications SET read_at = COALESCE(read_at, now())
		  WHERE id = $1::uuid AND user_id = $2::uuid`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) MarkAllRead(ctx context.Context, ex pgxExecutor, userID string) (int64, error) {
	tag, err := ex.Exec(ctx,
		`UPDATE notifications SET read_at = now()
		  WHERE user_id = $1::uuid AND read_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// --- preferences ---

func (r *Repository) ListPreferences(ctx context.Context, ex pgxExecutor, userID string) ([]Preference, error) {
	rows, err := ex.Query(ctx,
		`SELECT kind, in_app FROM notification_preferences WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preference
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.Kind, &p.InApp); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) SetPreference(ctx context.Context, ex pgxExecutor, userID, kind string, inApp bool) error {
	_, err := ex.Exec(ctx,
		`INSERT INTO notification_preferences (user_id, kind, in_app)
		 VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (user_id, kind) DO UPDATE SET in_app = EXCLUDED.in_app`,
		userID, kind, inApp)
	return err
}

// --- background jobs ---

// EnqueueSessionReminders notifies active group members of every scheduled
// session starting within `leadSeconds`, except those who declined. The dedupe
// key includes the start time, so a session that is moved gets a fresh reminder
// while a re-run for the same time creates nothing.
func (r *Repository) EnqueueSessionReminders(ctx context.Context, ex pgxExecutor, leadSeconds float64) (int64, error) {
	tag, err := ex.Exec(ctx,
		`INSERT INTO notifications (user_id, group_id, kind, title, body, data, dedupe_key)
		 SELECT gm.user_id, s.group_id, 'session_reminder', s.title, 'Starting soon',
		        jsonb_build_object('sessionId', s.id, 'startsAt', s.starts_at),
		        'session_reminder:' || s.id::text || ':' || (extract(epoch FROM s.starts_at))::bigint::text
		   FROM study_sessions s
		   JOIN group_memberships gm ON gm.group_id = s.group_id AND gm.status = 'active'
		   LEFT JOIN session_attendance a ON a.session_id = s.id AND a.user_id = gm.user_id
		  WHERE s.status = 'scheduled'
		    AND s.starts_at > now()
		    AND s.starts_at <= now() + make_interval(secs => $1)
		    AND COALESCE(a.rsvp_status, 'pending') <> 'declined'
		    AND NOT EXISTS (
		        SELECT 1 FROM notification_preferences p
		         WHERE p.user_id = gm.user_id AND p.kind = 'session_reminder' AND NOT p.in_app)
		 ON CONFLICT (user_id, dedupe_key) DO NOTHING`, leadSeconds)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Purge removes read notifications after 30 days and anything after 180.
func (r *Repository) Purge(ctx context.Context, ex pgxExecutor) (int64, error) {
	tag, err := ex.Exec(ctx,
		`DELETE FROM notifications
		  WHERE (read_at IS NOT NULL AND created_at < now() - interval '30 days')
		     OR created_at < now() - interval '180 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}