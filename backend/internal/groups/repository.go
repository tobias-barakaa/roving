package groups

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"study-platform/backend/internal/database"
)

const groupCols = `id::text, created_by::text, name, slug, description, visibility, archived_at, created_at, updated_at`

// Repository owns all SQL for groups and memberships.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

// mapPgErr translates PostgreSQL errors into domain errors. It is idempotent:
// non-PostgreSQL errors (including domain sentinels) pass through unchanged.
func mapPgErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505": // unique violation
		if pgErr.ConstraintName == "study_groups_slug_key" {
			return ErrSlugTaken
		}
		return ErrConflict
	case "23503": // foreign key violation
		return ErrInvalidInput
	case "23514": // check violation
		// The deferred owner-invariant trigger raises check_violation with a
		// stable message ("... must have an active owner").
		if strings.Contains(pgErr.Message, "must have an active owner") {
			return ErrConflict
		}
		return ErrInvalidInput
	}
	return err
}

func scanGroup(row pgx.Row) (*Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.CreatedBy, &g.Name, &g.Slug, &g.Description, &g.Visibility, &g.ArchivedAt, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ---------- cursor encoding ----------

// encodeCursor builds an opaque keyset cursor from the last row's ordering key.
func encodeCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "\n" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", ErrInvalidInput
	}
	parts := strings.SplitN(string(raw), "\n", 2)
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidInput
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", ErrInvalidInput
	}
	return t, parts[1], nil
}

// cursorParams returns (createdAt, id) as query arguments, or (nil, nil) when
// there is no cursor (so the WHERE clause's "$n IS NULL" branch matches all rows).
func cursorParams(cursor string) (any, any, error) {
	if cursor == "" {
		return nil, nil, nil
	}
	t, id, err := decodeCursor(cursor)
	if err != nil {
		return nil, nil, err
	}
	return t, id, nil
}

// ---------- groups ----------

func (r *Repository) CreateGroup(ctx context.Context, db database.DBTX, createdBy, name, slug, description, visibility string) (*Group, error) {
	return scanGroup(db.QueryRow(ctx,
		`INSERT INTO study_groups (created_by, name, slug, description, visibility)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING `+groupCols,
		createdBy, name, slug, description, visibility))
}

func (r *Repository) GetByID(ctx context.Context, db database.DBTX, id string) (*Group, error) {
	return scanGroup(db.QueryRow(ctx,
		`SELECT `+groupCols+` FROM study_groups WHERE id = $1::uuid`, id))
}

func (r *Repository) Update(ctx context.Context, db database.DBTX, id, name, description, visibility string) (*Group, error) {
	return scanGroup(db.QueryRow(ctx,
		`UPDATE study_groups SET name = $2, description = $3, visibility = $4
		 WHERE id = $1::uuid
		 RETURNING `+groupCols,
		id, name, description, visibility))
}

func (r *Repository) Archive(ctx context.Context, db database.DBTX, id string) error {
	_, err := db.Exec(ctx,
		`UPDATE study_groups SET archived_at = now() WHERE id = $1::uuid AND archived_at IS NULL`, id)
	return mapPgErr(err)
}

func (r *Repository) Unarchive(ctx context.Context, db database.DBTX, id string) error {
	_, err := db.Exec(ctx,
		`UPDATE study_groups SET archived_at = NULL WHERE id = $1::uuid AND archived_at IS NOT NULL`, id)
	return mapPgErr(err)
}

// LockGroup takes a row lock on the group, serialising owner-invariant
// mutations (e.g. ownership transfers) that touch the same group.
func (r *Repository) LockGroup(ctx context.Context, db database.DBTX, id string) error {
	var one int
	err := db.QueryRow(ctx,
		`SELECT 1 FROM study_groups WHERE id = $1::uuid FOR UPDATE`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r *Repository) ListForUser(ctx context.Context, db database.DBTX, userID string, limit int, cursor string) ([]Group, string, error) {
	cCreated, cID, err := cursorParams(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := db.Query(ctx,
		`SELECT `+groupCols+`
		 FROM study_groups g
		 JOIN group_memberships m ON m.group_id = g.id AND m.user_id = $1::uuid AND m.status = 'active'
		 WHERE g.archived_at IS NULL
		   AND (($2::timestamptz IS NULL) OR (g.created_at, g.id) < ($2::timestamptz, $3::uuid))
		 ORDER BY g.created_at DESC, g.id DESC
		 LIMIT $4`,
		userID, cCreated, cID, limit+1)
	if err != nil {
		return nil, "", mapPgErr(err)
	}
	defer rows.Close()

	items := make([]Group, 0, limit+1)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	if len(items) > limit {
		last := items[limit-1]
		return items[:limit], encodeCursor(last.CreatedAt, last.ID), nil
	}
	return items, "", nil
}

func (r *Repository) ListDiscover(ctx context.Context, db database.DBTX, limit int, cursor string) ([]Group, string, error) {
	cCreated, cID, err := cursorParams(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := db.Query(ctx,
		`SELECT `+groupCols+`
		 FROM study_groups g
		 WHERE g.archived_at IS NULL AND g.visibility = 'public'
		   AND (($1::timestamptz IS NULL) OR (g.created_at, g.id) < ($1::timestamptz, $2::uuid))
		 ORDER BY g.created_at DESC, g.id DESC
		 LIMIT $3`,
		cCreated, cID, limit+1)
	if err != nil {
		return nil, "", mapPgErr(err)
	}
	defer rows.Close()

	items := make([]Group, 0, limit+1)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	if len(items) > limit {
		last := items[limit-1]
		return items[:limit], encodeCursor(last.CreatedAt, last.ID), nil
	}
	return items, "", nil
}

// ---------- memberships ----------

func scanMembership(row pgx.Row) (*Membership, error) {
	var m Membership
	err := row.Scan(&m.ID, &m.GroupID, &m.UserID, &m.Role, &m.Status, &m.JoinedAt, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Repository) GetMembership(ctx context.Context, db database.DBTX, groupID, userID string) (*Membership, error) {
	return scanMembership(db.QueryRow(ctx,
		`SELECT id::text, group_id::text, user_id::text, role, status, joined_at, created_at
		 FROM group_memberships
		 WHERE group_id = $1::uuid AND user_id = $2::uuid`, groupID, userID))
}

// AddMembership inserts an active membership (the only state this phase creates).
func (r *Repository) AddMembership(ctx context.Context, db database.DBTX, groupID, userID, role string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO group_memberships (group_id, user_id, role, status, joined_at)
		 VALUES ($1::uuid, $2::uuid, $3, 'active', now())`,
		groupID, userID, role)
	return mapPgErr(err)
}

// ActiveRole returns the caller's role if they are an active member, else "".
func (r *Repository) ActiveRole(ctx context.Context, db database.DBTX, groupID, userID string) (string, error) {
	var role string
	err := db.QueryRow(ctx,
		`SELECT role FROM group_memberships
		 WHERE group_id = $1::uuid AND user_id = $2::uuid AND status = 'active'`,
		groupID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// UpdateRole sets the role of a membership and returns the updated member.
func (r *Repository) UpdateRole(ctx context.Context, db database.DBTX, groupID, userID, role string) (*Member, error) {
	var m Member
	err := db.QueryRow(ctx,
		`UPDATE group_memberships AS m SET role = $3
		 FROM users AS u
		 WHERE m.group_id = $1::uuid AND m.user_id = $2::uuid AND u.id = m.user_id
		 RETURNING m.user_id::text, u.email, m.role, m.status, m.joined_at`,
		groupID, userID, role).Scan(&m.UserID, &m.Email, &m.Role, &m.Status, &m.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, mapPgErr(err)
}

func (r *Repository) DeleteMembership(ctx context.Context, db database.DBTX, groupID, userID string) error {
	_, err := db.Exec(ctx,
		`DELETE FROM group_memberships WHERE group_id = $1::uuid AND user_id = $2::uuid`,
		groupID, userID)
	return mapPgErr(err)
}

func (r *Repository) ListMembers(ctx context.Context, db database.DBTX, groupID string, limit int, cursor string) ([]Member, string, error) {
	cCreated, cID, err := cursorParams(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := db.Query(ctx,
		`SELECT m.user_id::text, u.email, m.role, m.status, m.joined_at, m.created_at, m.id::text
		 FROM group_memberships m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.group_id = $1::uuid
		   AND (($2::timestamptz IS NULL) OR (m.created_at, m.id) < ($2::timestamptz, $3::uuid))
		 ORDER BY m.created_at DESC, m.id DESC
		 LIMIT $4`,
		groupID, cCreated, cID, limit+1)
	if err != nil {
		return nil, "", mapPgErr(err)
	}
	defer rows.Close()

	type memberRow struct {
		m         Member
		createdAt time.Time
		id        string
	}
	scanRows := make([]memberRow, 0, limit+1)
	for rows.Next() {
		var rw memberRow
		if err := rows.Scan(&rw.m.UserID, &rw.m.Email, &rw.m.Role, &rw.m.Status, &rw.m.JoinedAt, &rw.createdAt, &rw.id); err != nil {
			return nil, "", err
		}
		scanRows = append(scanRows, rw)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	items := make([]Member, 0, limit)
	if len(scanRows) > limit {
		last := scanRows[limit-1]
		for _, rw := range scanRows[:limit] {
			items = append(items, rw.m)
		}
		return items, encodeCursor(last.createdAt, last.id), nil
	}
	for _, rw := range scanRows {
		items = append(items, rw.m)
	}
	return items, "", nil
}
