// internal/groups/repository.go
package groups

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxExecutor is satisfied by *pgxpool.Pool AND pgx.Tx, so repo methods work
// both inside and outside a transaction without duplication.
type pgxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// --- groups ---

func (r *Repository) CreateGroup(ctx context.Context, ex pgxExecutor, g *Group) error {
	return ex.QueryRow(ctx,
		`INSERT INTO study_groups (created_by, name, slug, description, visibility)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING id::text, created_at, updated_at`,
		g.CreatedBy, g.Name, g.Slug, g.Description, g.Visibility,
	).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
}

func (r *Repository) GetGroupByID(ctx context.Context, ex pgxExecutor, id string) (*Group, error) {
	var g Group
	err := ex.QueryRow(ctx,
		`SELECT id::text, created_by::text, name, slug, description, visibility,
		        archived_at, created_at, updated_at
		   FROM study_groups WHERE id = $1::uuid`, id,
	).Scan(&g.ID, &g.CreatedBy, &g.Name, &g.Slug, &g.Description, &g.Visibility,
		&g.ArchivedAt, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}

func (r *Repository) GetGroupBySlug(ctx context.Context, ex pgxExecutor, slug string) (*Group, error) {
	var g Group
	err := ex.QueryRow(ctx,
		`SELECT id::text, created_by::text, name, slug, description, visibility,
		        archived_at, created_at, updated_at
		   FROM study_groups WHERE slug = $1`, slug,
	).Scan(&g.ID, &g.CreatedBy, &g.Name, &g.Slug, &g.Description, &g.Visibility,
		&g.ArchivedAt, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}

func (r *Repository) UpdateGroup(ctx context.Context, ex pgxExecutor, id string, f UpdateGroupFields) error {
	// COALESCE lets us PATCH only the fields the client sent.
	tag, err := ex.Exec(ctx,
		`UPDATE study_groups SET
		    name        = COALESCE($2, name),
		    description = COALESCE($3, description),
		    visibility  = COALESCE($4, visibility)
		 WHERE id = $1::uuid AND archived_at IS NULL`,
		id, f.Name, f.Description, f.Visibility)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type UpdateGroupFields struct {
	Name        *string
	Description *string
	Visibility  *string
}

func (r *Repository) ArchiveGroup(ctx context.Context, ex pgxExecutor, id string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_groups SET archived_at = now()
		 WHERE id = $1::uuid AND archived_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) UnarchiveGroup(ctx context.Context, ex pgxExecutor, id string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_groups SET archived_at = NULL
		 WHERE id = $1::uuid AND archived_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMyGroups: groups where the caller has an active membership.
func (r *Repository) ListMyGroups(ctx context.Context, ex pgxExecutor, userID string, f ListFilter) ([]Group, error) {
	rows, err := ex.Query(ctx,
		`SELECT g.id::text, g.created_by::text, g.name, g.slug, g.description,
		        g.visibility, g.archived_at, g.created_at, g.updated_at,
		        gm.role::text
		   FROM study_groups g
		   JOIN group_memberships gm ON gm.group_id = g.id
		  WHERE gm.user_id = $1::uuid
		    AND gm.status = 'active'
		    AND ($2::bool OR g.archived_at IS NULL)
		    AND ($3 = '' OR g.name ILIKE '%' || $3 || '%' OR g.slug ILIKE '%' || $3 || '%')
		  ORDER BY g.created_at DESC
		  LIMIT $4 OFFSET $5`,
		userID, f.IncludeArchived, f.Search, normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		var roleStr string
		if err := rows.Scan(&g.ID, &g.CreatedBy, &g.Name, &g.Slug, &g.Description,
			&g.Visibility, &g.ArchivedAt, &g.CreatedAt, &g.UpdatedAt, &roleStr); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListPublicGroups: for discovery (authenticated users only — the route still
// requires auth; "public" means "visible without joining", not "anonymous").
func (r *Repository) ListPublicGroups(ctx context.Context, ex pgxExecutor, f ListFilter) ([]Group, error) {
	rows, err := ex.Query(ctx,
		`SELECT id::text, created_by::text, name, slug, description, visibility,
		        archived_at, created_at, updated_at
		   FROM study_groups
		  WHERE visibility = 'public' AND archived_at IS NULL
		    AND ($1 = '' OR name ILIKE '%' || $1 || '%' OR slug ILIKE '%' || $1 || '%')
		  ORDER BY created_at DESC
		  LIMIT $2 OFFSET $3`,
		f.Search, normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.CreatedBy, &g.Name, &g.Slug, &g.Description,
			&g.Visibility, &g.ArchivedAt, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// --- memberships ---

func (r *Repository) GetMembership(ctx context.Context, ex pgxExecutor, groupID, userID string) (*Membership, error) {
	var m Membership
	var roleStr string
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, user_id::text, role::text, status,
		        invited_by::text, joined_at, created_at
		   FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid`,
		groupID, userID,
	).Scan(&m.ID, &m.GroupID, &m.UserID, &roleStr, &m.Status,
		&m.InvitedBy, &m.JoinedAt, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Role = ParseRole(roleStr)
	return &m, nil
}

// AddOwner inserts the founding owner row. Used only during CreateGroup.
func (r *Repository) AddOwner(ctx context.Context, ex pgxExecutor, groupID, userID string) error {
	_, err := ex.Exec(ctx,
		`INSERT INTO group_memberships (group_id, user_id, role, status, joined_at)
		 VALUES ($1::uuid, $2::uuid, 'owner', 'active', now())`,
		groupID, userID)
	return err
}

func (r *Repository) AddMember(ctx context.Context, ex pgxExecutor, groupID, userID, role, invitedBy string) error {
	_, err := ex.Exec(ctx,
		`INSERT INTO group_memberships (group_id, user_id, role, status, invited_by, joined_at)
		 VALUES ($1::uuid, $2::uuid, $3, 'active', $4::uuid, now())`,
		groupID, userID, role, invitedBy)
	return err
}

func (r *Repository) UpdateMemberRole(ctx context.Context, ex pgxExecutor, groupID, userID, role string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE group_memberships SET role = $3
		 WHERE group_id = $1::uuid AND user_id = $2::uuid AND status = 'active'`,
		groupID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) RemoveMember(ctx context.Context, ex pgxExecutor, groupID, userID string) error {
	tag, err := ex.Exec(ctx,
		`DELETE FROM group_memberships
		 WHERE group_id = $1::uuid AND user_id = $2::uuid`,
		groupID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) CountActiveOwners(ctx context.Context, ex pgxExecutor, groupID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT count(*) FROM group_memberships
		  WHERE group_id = $1::uuid AND role = 'owner' AND status = 'active'`,
		groupID).Scan(&n)
	return n, err
}

func (r *Repository) ListMembers(ctx context.Context, ex pgxExecutor, groupID string, f ListMembersQuery) ([]MemberSummary, error) {
	rows, err := ex.Query(ctx,
		`SELECT gm.user_id::text, u.display_name, u.email,
		        gm.role::text, gm.status, gm.joined_at
		   FROM group_memberships gm
		   JOIN users u ON u.id = gm.user_id
		  WHERE gm.group_id = $1::uuid
		    AND gm.status IN ('active', 'pending')
		    AND ($2 = '' OR gm.role = $2)
		  ORDER BY
		    CASE gm.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1
		                 WHEN 'moderator' THEN 2 ELSE 3 END,
		    gm.joined_at NULLS LAST, u.display_name
		  LIMIT $3 OFFSET $4`,
		groupID, f.Role, normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MemberSummary
	for rows.Next() {
		var m MemberSummary
		var roleStr string
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email,
			&roleStr, &m.Status, &m.JoinedAt); err != nil {
			return nil, err
		}
		m.Role = ParseRole(roleStr)
		out = append(out, m)
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

// IsUniqueViolation checks the pg error code 23505.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsForeignKeyViolation: 23503.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// IsCheckViolation: 23514 (includes the deferred-owner trigger).
func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// Slugify is the same transformation the DB CHECK constraint enforces.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == ' ' || r == '_':
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// fmtSlugSuffix used when the requested slug collides.
func fmtSlugSuffix(base string, n int) string {
	s := fmt.Sprintf("%s-%d", base, n)
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.Trim(s, "-")
}

// package groups

// import (
// 	"context"
// 	"encoding/base64"
// 	"errors"
// 	"strings"
// 	"time"

// 	"github.com/jackc/pgx/v5"
// 	"github.com/jackc/pgx/v5/pgconn"

// 	"study-platform/backend/internal/database"
// )

// const groupCols = `id::text, created_by::text, name, slug, description, visibility, archived_at, created_at, updated_at`

// // Repository owns all SQL for groups and memberships.
// type Repository struct{}

// func NewRepository() *Repository { return &Repository{} }

// // mapPgErr translates PostgreSQL errors into domain errors. It is idempotent:
// // non-PostgreSQL errors (including domain sentinels) pass through unchanged.
// func mapPgErr(err error) error {
// 	var pgErr *pgconn.PgError
// 	if !errors.As(err, &pgErr) {
// 		return err
// 	}
// 	switch pgErr.Code {
// 	case "23505": // unique violation
// 		if pgErr.ConstraintName == "study_groups_slug_key" {
// 			return ErrSlugTaken
// 		}
// 		return ErrConflict
// 	case "23503": // foreign key violation
// 		return ErrInvalidInput
// 	case "23514": // check violation
// 		// The deferred owner-invariant trigger raises check_violation with a
// 		// stable message ("... must have an active owner").
// 		if strings.Contains(pgErr.Message, "must have an active owner") {
// 			return ErrConflict
// 		}
// 		return ErrInvalidInput
// 	}
// 	return err
// }

// func scanGroup(row pgx.Row) (*Group, error) {
// 	var g Group
// 	err := row.Scan(&g.ID, &g.CreatedBy, &g.Name, &g.Slug, &g.Description, &g.Visibility, &g.ArchivedAt, &g.CreatedAt, &g.UpdatedAt)
// 	if errors.Is(err, pgx.ErrNoRows) {
// 		return nil, ErrNotFound
// 	}
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &g, nil
// }

// // ---------- cursor encoding ----------

// // encodeCursor builds an opaque keyset cursor from the last row's ordering key.
// func encodeCursor(createdAt time.Time, id string) string {
// 	raw := createdAt.UTC().Format(time.RFC3339Nano) + "\n" + id
// 	return base64.RawURLEncoding.EncodeToString([]byte(raw))
// }

// func decodeCursor(s string) (time.Time, string, error) {
// 	raw, err := base64.RawURLEncoding.DecodeString(s)
// 	if err != nil {
// 		return time.Time{}, "", ErrInvalidInput
// 	}
// 	parts := strings.SplitN(string(raw), "\n", 2)
// 	if len(parts) != 2 {
// 		return time.Time{}, "", ErrInvalidInput
// 	}
// 	t, err := time.Parse(time.RFC3339Nano, parts[0])
// 	if err != nil {
// 		return time.Time{}, "", ErrInvalidInput
// 	}
// 	return t, parts[1], nil
// }

// // cursorParams returns (createdAt, id) as query arguments, or (nil, nil) when
// // there is no cursor (so the WHERE clause's "$n IS NULL" branch matches all rows).
// func cursorParams(cursor string) (any, any, error) {
// 	if cursor == "" {
// 		return nil, nil, nil
// 	}
// 	t, id, err := decodeCursor(cursor)
// 	if err != nil {
// 		return nil, nil, err
// 	}
// 	return t, id, nil
// }

// // ---------- groups ----------

// func (r *Repository) CreateGroup(ctx context.Context, db database.DBTX, createdBy, name, slug, description, visibility string) (*Group, error) {
// 	return scanGroup(db.QueryRow(ctx,
// 		`INSERT INTO study_groups (created_by, name, slug, description, visibility)
// 		 VALUES ($1::uuid, $2, $3, $4, $5)
// 		 RETURNING `+groupCols,
// 		createdBy, name, slug, description, visibility))
// }

// func (r *Repository) GetByID(ctx context.Context, db database.DBTX, id string) (*Group, error) {
// 	return scanGroup(db.QueryRow(ctx,
// 		`SELECT `+groupCols+` FROM study_groups WHERE id = $1::uuid`, id))
// }

// func (r *Repository) Update(ctx context.Context, db database.DBTX, id, name, description, visibility string) (*Group, error) {
// 	return scanGroup(db.QueryRow(ctx,
// 		`UPDATE study_groups SET name = $2, description = $3, visibility = $4
// 		 WHERE id = $1::uuid
// 		 RETURNING `+groupCols,
// 		id, name, description, visibility))
// }

// func (r *Repository) Archive(ctx context.Context, db database.DBTX, id string) error {
// 	_, err := db.Exec(ctx,
// 		`UPDATE study_groups SET archived_at = now() WHERE id = $1::uuid AND archived_at IS NULL`, id)
// 	return mapPgErr(err)
// }

// func (r *Repository) Unarchive(ctx context.Context, db database.DBTX, id string) error {
// 	_, err := db.Exec(ctx,
// 		`UPDATE study_groups SET archived_at = NULL WHERE id = $1::uuid AND archived_at IS NOT NULL`, id)
// 	return mapPgErr(err)
// }

// // LockGroup takes a row lock on the group, serialising owner-invariant
// // mutations (e.g. ownership transfers) that touch the same group.
// func (r *Repository) LockGroup(ctx context.Context, db database.DBTX, id string) error {
// 	var one int
// 	err := db.QueryRow(ctx,
// 		`SELECT 1 FROM study_groups WHERE id = $1::uuid FOR UPDATE`, id).Scan(&one)
// 	if errors.Is(err, pgx.ErrNoRows) {
// 		return ErrNotFound
// 	}
// 	return err
// }

// func (r *Repository) ListForUser(ctx context.Context, db database.DBTX, userID string, limit int, cursor string) ([]Group, string, error) {
// 	cCreated, cID, err := cursorParams(cursor)
// 	if err != nil {
// 		return nil, "", err
// 	}
// 	rows, err := db.Query(ctx,
// 		`SELECT `+groupCols+`
// 		 FROM study_groups g
// 		 JOIN group_memberships m ON m.group_id = g.id AND m.user_id = $1::uuid AND m.status = 'active'
// 		 WHERE g.archived_at IS NULL
// 		   AND (($2::timestamptz IS NULL) OR (g.created_at, g.id) < ($2::timestamptz, $3::uuid))
// 		 ORDER BY g.created_at DESC, g.id DESC
// 		 LIMIT $4`,
// 		userID, cCreated, cID, limit+1)
// 	if err != nil {
// 		return nil, "", mapPgErr(err)
// 	}
// 	defer rows.Close()

// 	items := make([]Group, 0, limit+1)
// 	for rows.Next() {
// 		g, err := scanGroup(rows)
// 		if err != nil {
// 			return nil, "", err
// 		}
// 		items = append(items, *g)
// 	}
// 	if err := rows.Err(); err != nil {
// 		return nil, "", err
// 	}

// 	if len(items) > limit {
// 		last := items[limit-1]
// 		return items[:limit], encodeCursor(last.CreatedAt, last.ID), nil
// 	}
// 	return items, "", nil
// }

// func (r *Repository) ListDiscover(ctx context.Context, db database.DBTX, limit int, cursor string) ([]Group, string, error) {
// 	cCreated, cID, err := cursorParams(cursor)
// 	if err != nil {
// 		return nil, "", err
// 	}
// 	rows, err := db.Query(ctx,
// 		`SELECT `+groupCols+`
// 		 FROM study_groups g
// 		 WHERE g.archived_at IS NULL AND g.visibility = 'public'
// 		   AND (($1::timestamptz IS NULL) OR (g.created_at, g.id) < ($1::timestamptz, $2::uuid))
// 		 ORDER BY g.created_at DESC, g.id DESC
// 		 LIMIT $3`,
// 		cCreated, cID, limit+1)
// 	if err != nil {
// 		return nil, "", mapPgErr(err)
// 	}
// 	defer rows.Close()

// 	items := make([]Group, 0, limit+1)
// 	for rows.Next() {
// 		g, err := scanGroup(rows)
// 		if err != nil {
// 			return nil, "", err
// 		}
// 		items = append(items, *g)
// 	}
// 	if err := rows.Err(); err != nil {
// 		return nil, "", err
// 	}

// 	if len(items) > limit {
// 		last := items[limit-1]
// 		return items[:limit], encodeCursor(last.CreatedAt, last.ID), nil
// 	}
// 	return items, "", nil
// }

// // ---------- memberships ----------

// func scanMembership(row pgx.Row) (*Membership, error) {
// 	var m Membership
// 	err := row.Scan(&m.ID, &m.GroupID, &m.UserID, &m.Role, &m.Status, &m.JoinedAt, &m.CreatedAt)
// 	if errors.Is(err, pgx.ErrNoRows) {
// 		return nil, ErrNotFound
// 	}
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &m, nil
// }

// func (r *Repository) GetMembership(ctx context.Context, db database.DBTX, groupID, userID string) (*Membership, error) {
// 	return scanMembership(db.QueryRow(ctx,
// 		`SELECT id::text, group_id::text, user_id::text, role, status, joined_at, created_at
// 		 FROM group_memberships
// 		 WHERE group_id = $1::uuid AND user_id = $2::uuid`, groupID, userID))
// }

// // AddMembership inserts an active membership (the only state this phase creates).
// func (r *Repository) AddMembership(ctx context.Context, db database.DBTX, groupID, userID, role string) error {
// 	_, err := db.Exec(ctx,
// 		`INSERT INTO group_memberships (group_id, user_id, role, status, joined_at)
// 		 VALUES ($1::uuid, $2::uuid, $3, 'active', now())`,
// 		groupID, userID, role)
// 	return mapPgErr(err)
// }

// // ActiveRole returns the caller's role if they are an active member, else "".
// func (r *Repository) ActiveRole(ctx context.Context, db database.DBTX, groupID, userID string) (string, error) {
// 	var role string
// 	err := db.QueryRow(ctx,
// 		`SELECT role FROM group_memberships
// 		 WHERE group_id = $1::uuid AND user_id = $2::uuid AND status = 'active'`,
// 		groupID, userID).Scan(&role)
// 	if errors.Is(err, pgx.ErrNoRows) {
// 		return "", nil
// 	}
// 	return role, err
// }

// // UpdateRole sets the role of a membership and returns the updated member.
// func (r *Repository) UpdateRole(ctx context.Context, db database.DBTX, groupID, userID, role string) (*Member, error) {
// 	var m Member
// 	err := db.QueryRow(ctx,
// 		`UPDATE group_memberships AS m SET role = $3
// 		 FROM users AS u
// 		 WHERE m.group_id = $1::uuid AND m.user_id = $2::uuid AND u.id = m.user_id
// 		 RETURNING m.user_id::text, u.email, m.role, m.status, m.joined_at`,
// 		groupID, userID, role).Scan(&m.UserID, &m.Email, &m.Role, &m.Status, &m.JoinedAt)
// 	if errors.Is(err, pgx.ErrNoRows) {
// 		return nil, ErrNotFound
// 	}
// 	return &m, mapPgErr(err)
// }

// func (r *Repository) DeleteMembership(ctx context.Context, db database.DBTX, groupID, userID string) error {
// 	_, err := db.Exec(ctx,
// 		`DELETE FROM group_memberships WHERE group_id = $1::uuid AND user_id = $2::uuid`,
// 		groupID, userID)
// 	return mapPgErr(err)
// }

// func (r *Repository) ListMembers(ctx context.Context, db database.DBTX, groupID string, limit int, cursor string) ([]Member, string, error) {
// 	cCreated, cID, err := cursorParams(cursor)
// 	if err != nil {
// 		return nil, "", err
// 	}
// 	rows, err := db.Query(ctx,
// 		`SELECT m.user_id::text, u.email, m.role, m.status, m.joined_at, m.created_at, m.id::text
// 		 FROM group_memberships m
// 		 JOIN users u ON u.id = m.user_id
// 		 WHERE m.group_id = $1::uuid
// 		   AND (($2::timestamptz IS NULL) OR (m.created_at, m.id) < ($2::timestamptz, $3::uuid))
// 		 ORDER BY m.created_at DESC, m.id DESC
// 		 LIMIT $4`,
// 		groupID, cCreated, cID, limit+1)
// 	if err != nil {
// 		return nil, "", mapPgErr(err)
// 	}
// 	defer rows.Close()

// 	type memberRow struct {
// 		m         Member
// 		createdAt time.Time
// 		id        string
// 	}
// 	scanRows := make([]memberRow, 0, limit+1)
// 	for rows.Next() {
// 		var rw memberRow
// 		if err := rows.Scan(&rw.m.UserID, &rw.m.Email, &rw.m.Role, &rw.m.Status, &rw.m.JoinedAt, &rw.createdAt, &rw.id); err != nil {
// 			return nil, "", err
// 		}
// 		scanRows = append(scanRows, rw)
// 	}
// 	if err := rows.Err(); err != nil {
// 		return nil, "", err
// 	}

// 	items := make([]Member, 0, limit)
// 	if len(scanRows) > limit {
// 		last := scanRows[limit-1]
// 		for _, rw := range scanRows[:limit] {
// 			items = append(items, rw.m)
// 		}
// 		return items, encodeCursor(last.createdAt, last.id), nil
// 	}
// 	for _, rw := range scanRows {
// 		items = append(items, rw.m)
// 	}
// 	return items, "", nil
// }
