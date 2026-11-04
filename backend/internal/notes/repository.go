// internal/notes/repository.go
package notes

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

const noteCols = `n.id::text, n.user_id::text, n.group_id::text, n.material_id::text,
        n.study_session_id::text, n.title, n.body, n.visibility, n.pinned, n.tags,
        u.display_name, n.created_at, n.updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanNote(row scanner) (Note, error) {
	var n Note
	err := row.Scan(&n.ID, &n.UserID, &n.GroupID, &n.MaterialID, &n.SessionID,
		&n.Title, &n.Body, &n.Visibility, &n.Pinned, &n.Tags,
		&n.AuthorName, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func (r *Repository) Insert(ctx context.Context, ex pgxExecutor, n *Note) error {
	return ex.QueryRow(ctx,
		`INSERT INTO notes
		    (user_id, group_id, material_id, study_session_id, title, body,
		     visibility, pinned, tags)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9)
		 RETURNING id::text, created_at, updated_at`,
		n.UserID, n.GroupID, n.MaterialID, n.SessionID, n.Title, n.Body,
		n.Visibility, n.Pinned, n.Tags,
	).Scan(&n.ID, &n.CreatedAt, &n.UpdatedAt)
}

func (r *Repository) Get(ctx context.Context, ex pgxExecutor, id string) (*Note, error) {
	n, err := scanNote(ex.QueryRow(ctx,
		`SELECT `+noteCols+`
		   FROM notes n JOIN users u ON u.id = n.user_id
		  WHERE n.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

type UpdateFields struct {
	Title      *string
	Body       *string
	Visibility *string
	Pinned     *bool
	Tags       []string // nil = leave unchanged
}

func (r *Repository) Update(ctx context.Context, ex pgxExecutor, id, userID string, f UpdateFields) error {
	tag, err := ex.Exec(ctx,
		`UPDATE notes SET
		    title      = COALESCE($3, title),
		    body       = COALESCE($4, body),
		    visibility = COALESCE($5, visibility),
		    pinned     = COALESCE($6, pinned),
		    tags       = COALESCE($7::text[], tags)
		  WHERE id = $1::uuid AND user_id = $2::uuid`,
		id, userID, f.Title, f.Body, f.Visibility, f.Pinned, f.Tags)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, ex pgxExecutor, id, userID string) error {
	tag, err := ex.Exec(ctx,
		`DELETE FROM notes WHERE id = $1::uuid AND user_id = $2::uuid`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMine returns the caller's own notes (private and shared), pinned first.
func (r *Repository) ListMine(ctx context.Context, ex pgxExecutor, f ListFilter) ([]Note, error) {
	rows, err := ex.Query(ctx,
		`SELECT `+noteCols+`
		   FROM notes n JOIN users u ON u.id = n.user_id
		  WHERE n.user_id = $1::uuid
		    AND ($2 = '' OR n.group_id = $2::uuid)
		    AND ($3 = '' OR n.material_id = $3::uuid)
		    AND ($4 = '' OR n.study_session_id = $4::uuid)
		    AND (NOT $5::boolean OR n.pinned)
		    AND ($6 = '' OR position(lower($6) in lower(n.title || ' ' || n.body)) > 0)
		  ORDER BY n.pinned DESC, n.updated_at DESC, n.id DESC
		  LIMIT $7 OFFSET $8`,
		f.UserID, f.GroupID, f.MaterialID, f.SessionID, f.PinnedOnly, f.Query,
		normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// ListShared returns group-visible notes whose author is still an active member.
func (r *Repository) ListShared(ctx context.Context, ex pgxExecutor, f ListFilter) ([]Note, error) {
	rows, err := ex.Query(ctx,
		`SELECT `+noteCols+`
		   FROM notes n
		   JOIN users u ON u.id = n.user_id
		   JOIN group_memberships gm
		     ON gm.group_id = n.group_id AND gm.user_id = n.user_id AND gm.status = 'active'
		  WHERE n.group_id = $1::uuid AND n.visibility = 'group'
		    AND ($2 = '' OR n.material_id = $2::uuid)
		    AND ($3 = '' OR n.study_session_id = $3::uuid)
		    AND ($4 = '' OR position(lower($4) in lower(n.title || ' ' || n.body)) > 0)
		  ORDER BY n.updated_at DESC, n.id DESC
		  LIMIT $5 OFFSET $6`,
		f.GroupID, f.MaterialID, f.SessionID, f.Query,
		normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

func collect(rows pgx.Rows) ([]Note, error) {
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// --- context checks (read-only lookups into sibling modules' tables) ---

func (r *Repository) IsActiveMember(ctx context.Context, ex pgxExecutor, groupID, userID string) (bool, error) {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM group_memberships
		                 WHERE group_id = $1::uuid AND user_id = $2::uuid AND status = 'active')`,
		groupID, userID).Scan(&ok)
	return ok, err
}

func (r *Repository) MaterialInGroup(ctx context.Context, ex pgxExecutor, materialID, groupID string) (bool, error) {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM reading_materials
		                 WHERE id = $1::uuid AND group_id = $2::uuid)`,
		materialID, groupID).Scan(&ok)
	return ok, err
}

func (r *Repository) SessionInGroup(ctx context.Context, ex pgxExecutor, sessionID, groupID string) (bool, error) {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM study_sessions
		                 WHERE id = $1::uuid AND group_id = $2::uuid)`,
		sessionID, groupID).Scan(&ok)
	return ok, err
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

func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}