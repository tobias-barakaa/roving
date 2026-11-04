// internal/reading/repository.go
package reading

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

// --- materials ---

func (r *Repository) InsertMaterial(ctx context.Context, ex pgxExecutor, m *Material) error {
	return ex.QueryRow(ctx,
		`INSERT INTO reading_materials
		    (group_id, added_by, title, kind, author, url, description, unit, total_units)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id::text, created_at, updated_at`,
		m.GroupID, m.AddedBy, m.Title, m.Kind, m.Author, m.URL, m.Description,
		m.Unit, m.TotalUnits,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
}

func (r *Repository) GetMaterial(ctx context.Context, ex pgxExecutor, id string) (*Material, error) {
	var m Material
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, added_by::text, title, kind, author, url,
		        description, unit, total_units, archived_at, created_at, updated_at
		   FROM reading_materials WHERE id = $1::uuid`, id,
	).Scan(&m.ID, &m.GroupID, &m.AddedBy, &m.Title, &m.Kind, &m.Author, &m.URL,
		&m.Description, &m.Unit, &m.TotalUnits, &m.ArchivedAt, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, err
}

const viewCols = `m.id::text, m.group_id::text, m.added_by::text, m.title, m.kind, m.author,
        m.url, m.description, m.unit, m.total_units, m.archived_at, m.created_at, m.updated_at,
        COALESCE(mp.current_unit, 0), COALESCE(mp.status, 'not_started'),
        (SELECT count(*) FROM reading_progress rp
          WHERE rp.material_id = m.id AND rp.status IN ('reading', 'finished')),
        (SELECT count(*) FROM reading_progress rp
          WHERE rp.material_id = m.id AND rp.status = 'finished')`

type scanner interface{ Scan(dest ...any) error }

func scanView(row scanner) (MaterialView, error) {
	var v MaterialView
	err := row.Scan(&v.ID, &v.GroupID, &v.AddedBy, &v.Title, &v.Kind, &v.Author,
		&v.URL, &v.Description, &v.Unit, &v.TotalUnits, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt,
		&v.MyUnit, &v.MyStatus, &v.Readers, &v.Finished)
	return v, err
}

func (r *Repository) GetMaterialView(ctx context.Context, ex pgxExecutor, id, userID string) (*MaterialView, error) {
	v, err := scanView(ex.QueryRow(ctx,
		`SELECT `+viewCols+`
		   FROM reading_materials m
		   LEFT JOIN reading_progress mp ON mp.material_id = m.id AND mp.user_id = $2::uuid
		  WHERE m.id = $1::uuid`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *Repository) ListMaterials(ctx context.Context, ex pgxExecutor, groupID, userID string,
	includeArchived bool, limit, offset int) ([]MaterialView, error) {
	rows, err := ex.Query(ctx,
		`SELECT `+viewCols+`
		   FROM reading_materials m
		   LEFT JOIN reading_progress mp ON mp.material_id = m.id AND mp.user_id = $2::uuid
		  WHERE m.group_id = $1::uuid
		    AND ($3::boolean OR m.archived_at IS NULL)
		  ORDER BY m.created_at DESC, m.id DESC
		  LIMIT $4 OFFSET $5`,
		groupID, userID, includeArchived, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaterialView
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type UpdateMaterialFields struct {
	Title       *string
	Author      *string
	URL         *string
	Description *string
	TotalUnits  *int
}

func (r *Repository) UpdateMaterial(ctx context.Context, ex pgxExecutor, id string, f UpdateMaterialFields) error {
	tag, err := ex.Exec(ctx,
		`UPDATE reading_materials SET
		    title       = COALESCE($2, title),
		    author      = COALESCE($3, author),
		    url         = COALESCE($4, url),
		    description = COALESCE($5, description),
		    total_units = COALESCE($6, total_units)
		  WHERE id = $1::uuid AND archived_at IS NULL`,
		id, f.Title, f.Author, f.URL, f.Description, f.TotalUnits)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) ArchiveMaterial(ctx context.Context, ex pgxExecutor, id string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE reading_materials SET archived_at = now()
		  WHERE id = $1::uuid AND archived_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MaxCurrentUnit is the furthest any member has read; totalUnits may not drop below it.
func (r *Repository) MaxCurrentUnit(ctx context.Context, ex pgxExecutor, materialID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT COALESCE(max(current_unit), 0) FROM reading_progress WHERE material_id = $1::uuid`,
		materialID).Scan(&n)
	return n, err
}

// --- progress ---

// GetProgress returns nil (not an error) when the user has no row yet.
func (r *Repository) GetProgress(ctx context.Context, ex pgxExecutor, materialID, userID string) (*Progress, error) {
	var p Progress
	err := ex.QueryRow(ctx,
		`SELECT material_id::text, user_id::text, current_unit, status,
		        started_at, finished_at, updated_at
		   FROM reading_progress WHERE material_id = $1::uuid AND user_id = $2::uuid`,
		materialID, userID,
	).Scan(&p.MaterialID, &p.UserID, &p.CurrentUnit, &p.Status,
		&p.StartedAt, &p.FinishedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) UpsertProgress(ctx context.Context, ex pgxExecutor, groupID string, p *Progress) error {
	return ex.QueryRow(ctx,
		`INSERT INTO reading_progress
		    (material_id, group_id, user_id, current_unit, status, started_at, finished_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)
		 ON CONFLICT (material_id, user_id) DO UPDATE SET
		    current_unit = EXCLUDED.current_unit,
		    status       = EXCLUDED.status,
		    started_at   = EXCLUDED.started_at,
		    finished_at  = EXCLUDED.finished_at
		 RETURNING updated_at`,
		p.MaterialID, groupID, p.UserID, p.CurrentUnit, p.Status, p.StartedAt, p.FinishedAt,
	).Scan(&p.UpdatedAt)
}

// ListRoster shows every active member's progress on one material.
func (r *Repository) ListRoster(ctx context.Context, ex pgxExecutor, materialID string) ([]RosterEntry, error) {
	rows, err := ex.Query(ctx,
		`SELECT rp.user_id::text, u.display_name, rp.current_unit, rp.status,
		        rp.finished_at, rp.updated_at
		   FROM reading_progress rp
		   JOIN users u ON u.id = rp.user_id
		   JOIN group_memberships gm
		     ON gm.group_id = rp.group_id AND gm.user_id = rp.user_id AND gm.status = 'active'
		  WHERE rp.material_id = $1::uuid
		  ORDER BY rp.current_unit DESC, u.display_name`, materialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RosterEntry
	for rows.Next() {
		var e RosterEntry
		if err := rows.Scan(&e.UserID, &e.DisplayName, &e.CurrentUnit, &e.Status,
			&e.FinishedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
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

func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

var _ = time.Now // keep import if fields change