// internal/chat/repository.go
package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// Every message query selects through an alias `m` (a table or a CTE) joined
// to users `u`, so the author's display name always comes back with the row.
const msgCols = `m.id::text, m.group_id::text, m.room_id::text, m.user_id::text,
        u.display_name, m.reply_to_id::text, m.body, m.edited_at, m.deleted_at, m.created_at`

type scanner interface{ Scan(dest ...any) error }

func scanMessage(row scanner) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.GroupID, &m.RoomID, &m.UserID, &m.AuthorName,
		&m.ReplyToID, &m.Body, &m.EditedAt, &m.DeletedAt, &m.CreatedAt)
	return m, err
}

func oneMessage(row scanner) (*Message, error) {
	m, err := scanMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// --- messages ---

func (r *Repository) Insert(ctx context.Context, ex pgxExecutor, m *Message) (*Message, error) {
	return oneMessage(ex.QueryRow(ctx,
		`WITH ins AS (
		     INSERT INTO chat_messages (group_id, room_id, user_id, reply_to_id, body)
		     VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)
		     RETURNING *)
		 SELECT `+msgCols+`
		   FROM ins m LEFT JOIN users u ON u.id = m.user_id`,
		m.GroupID, m.RoomID, m.UserID, m.ReplyToID, m.Body))
}

func (r *Repository) GetMessage(ctx context.Context, ex pgxExecutor, id string) (*Message, error) {
	return oneMessage(ex.QueryRow(ctx,
		`SELECT `+msgCols+`
		   FROM chat_messages m LEFT JOIN users u ON u.id = m.user_id
		  WHERE m.id = $1::uuid`, id))
}

// ListMessages pages through one channel using the (created_at, id) of a
// cursor message, so ties on created_at can never skip or repeat a row.
//
//	neither before nor after → newest first
//	before=<id>              → older than that message, newest first
//	after=<id>               → newer than that message, OLDEST first (catch-up)
//
// The caller reverses the newest-first results into chronological order.
func (r *Repository) ListMessages(ctx context.Context, ex pgxExecutor, groupID string,
	roomID *string, before, after string, limit int) ([]Message, error) {

	args := []any{groupID}
	conds := []string{"m.group_id = $1::uuid"}
	add := func(format string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(format, len(args)))
	}

	if roomID == nil {
		conds = append(conds, "m.room_id IS NULL") // uses chat_messages_group_idx
	} else {
		add("m.room_id = $%d::uuid", *roomID)
	}

	const cursor = `(SELECT c.created_at, c.id FROM chat_messages c
	                  WHERE c.id = $%d::uuid AND c.group_id = $1::uuid)`
	dir := "DESC"
	if before != "" {
		add("(m.created_at, m.id) < "+cursor, before)
	}
	if after != "" {
		add("(m.created_at, m.id) > "+cursor, after)
		dir = "ASC"
	}

	args = append(args, limit)
	q := `SELECT ` + msgCols + `
	        FROM chat_messages m LEFT JOIN users u ON u.id = m.user_id
	       WHERE ` + strings.Join(conds, " AND ") + fmt.Sprintf(`
	       ORDER BY m.created_at %s, m.id %s
	       LIMIT $%d`, dir, dir, len(args))

	rows, err := ex.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// EditMessage only touches the author's own, live message.
func (r *Repository) EditMessage(ctx context.Context, ex pgxExecutor, id, userID, body string) (*Message, error) {
	return oneMessage(ex.QueryRow(ctx,
		`WITH upd AS (
		     UPDATE chat_messages SET body = $3, edited_at = now()
		      WHERE id = $1::uuid AND user_id = $2::uuid AND deleted_at IS NULL
		     RETURNING *)
		 SELECT `+msgCols+`
		   FROM upd m LEFT JOIN users u ON u.id = m.user_id`,
		id, userID, body))
}

// SoftDelete blanks the body and keeps a tombstone.
func (r *Repository) SoftDelete(ctx context.Context, ex pgxExecutor, id, deletedBy string) (*Message, error) {
	return oneMessage(ex.QueryRow(ctx,
		`WITH upd AS (
		     UPDATE chat_messages
		        SET body = '', deleted_at = now(), deleted_by = $2::uuid
		      WHERE id = $1::uuid AND deleted_at IS NULL
		     RETURNING *)
		 SELECT `+msgCols+`
		   FROM upd m LEFT JOIN users u ON u.id = m.user_id`,
		id, deletedBy))
}

// --- rooms (read-only lookups into the rooms module's tables) ---

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

// --- read state ---

// MarkRead moves the user's marker forward (never backward). With a nil
// messageID it uses now(); with one, that message's created_at.
func (r *Repository) MarkRead(ctx context.Context, ex pgxExecutor, userID, groupID string,
	roomID *string, messageID *string) error {

	const stamp = `COALESCE((SELECT created_at FROM chat_messages
	                           WHERE id = $4::uuid AND group_id = $2::uuid), now())`
	if roomID == nil {
		_, err := ex.Exec(ctx,
			`INSERT INTO chat_read_state (user_id, group_id, room_id, last_read_at)
			 VALUES ($1::uuid, $2::uuid, NULL, `+stamp+`)
			 ON CONFLICT (user_id, group_id) WHERE room_id IS NULL
			 DO UPDATE SET last_read_at = GREATEST(chat_read_state.last_read_at, EXCLUDED.last_read_at)`,
			userID, groupID, nil, messageID)
		return err
	}
	_, err := ex.Exec(ctx,
		`INSERT INTO chat_read_state (user_id, group_id, room_id, last_read_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, `+stamp+`)
		 ON CONFLICT (user_id, room_id) WHERE room_id IS NOT NULL
		 DO UPDATE SET last_read_at = GREATEST(chat_read_state.last_read_at, EXCLUDED.last_read_at)`,
		userID, groupID, *roomID, messageID)
	return err
}

// GeneralUnread counts others' live messages in the group-wide channel newer
// than the user's marker (or than when they joined, if they never marked).
func (r *Repository) GeneralUnread(ctx context.Context, ex pgxExecutor, groupID, userID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT count(m.id)
		   FROM group_memberships gm
		   LEFT JOIN chat_read_state rs
		          ON rs.user_id = gm.user_id AND rs.group_id = gm.group_id AND rs.room_id IS NULL
		   LEFT JOIN chat_messages m
		          ON m.group_id = gm.group_id AND m.room_id IS NULL
		         AND m.deleted_at IS NULL
		         AND m.user_id IS DISTINCT FROM gm.user_id
		         AND m.created_at > COALESCE(rs.last_read_at, gm.joined_at)
		  WHERE gm.group_id = $1::uuid AND gm.user_id = $2::uuid AND gm.status = 'active'`,
		groupID, userID).Scan(&n)
	return n, err
}

// RoomsUnread counts unread messages for every room the user may enter.
func (r *Repository) RoomsUnread(ctx context.Context, ex pgxExecutor, groupID, userID string) ([]RoomUnread, error) {
	rows, err := ex.Query(ctx,
		`SELECT r.id::text, count(m.id)
		   FROM study_rooms r
		   JOIN group_memberships gm
		     ON gm.group_id = r.group_id AND gm.user_id = $2::uuid AND gm.status = 'active'
		   LEFT JOIN room_memberships rm
		          ON rm.room_id = r.id AND rm.user_id = gm.user_id AND rm.status = 'active'
		   LEFT JOIN chat_read_state rs
		          ON rs.user_id = gm.user_id AND rs.room_id = r.id
		   LEFT JOIN chat_messages m
		          ON m.room_id = r.id AND m.deleted_at IS NULL
		         AND m.user_id IS DISTINCT FROM gm.user_id
		         AND m.created_at > COALESCE(rs.last_read_at, gm.joined_at)
		  WHERE r.group_id = $1::uuid AND r.archived_at IS NULL
		    AND (r.access = 'group' OR rm.user_id IS NOT NULL)
		  GROUP BY r.id
		  ORDER BY r.id`,
		groupID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomUnread
	for rows.Next() {
		var ru RoomUnread
		if err := rows.Scan(&ru.RoomID, &ru.Unread); err != nil {
			return nil, err
		}
		out = append(out, ru)
	}
	return out, rows.Err()
}

// --- helpers ---

func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}