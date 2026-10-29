// internal/rooms/repository.go
package rooms

import (
	"context"
	"errors"

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

// ---------- rooms ----------

func (r *Repository) Insert(ctx context.Context, ex pgxExecutor, room *Room) error {
	return ex.QueryRow(ctx,
		`INSERT INTO study_rooms
		    (group_id, created_by, name, description, access, capacity)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)
		 RETURNING id::text, created_at, updated_at`,
		room.GroupID, room.CreatedBy, room.Name, room.Description, room.Access, room.Capacity,
	).Scan(&room.ID, &room.CreatedAt, &room.UpdatedAt)
}

// GetByIDGroup is the canonical fetch. It always requires both IDs so a caller
// who mixed up two groups can't accidentally read a room in the wrong group.
func (r *Repository) GetByIDGroup(ctx context.Context, ex pgxExecutor, roomID, groupID string) (*Room, error) {
	var room Room
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, created_by::text, name, description,
		        access, capacity, archived_at, created_at, updated_at
		   FROM study_rooms
		  WHERE id = $1::uuid AND group_id = $2::uuid`,
		roomID, groupID,
	).Scan(&room.ID, &room.GroupID, &room.CreatedBy, &room.Name, &room.Description,
		&room.Access, &room.Capacity, &room.ArchivedAt, &room.CreatedAt, &room.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &room, err
}

// GetRoomGroupID returns just the group ID for a room; used to resolve a
// session-attached room before validating group membership.
func (r *Repository) GetRoomGroupID(ctx context.Context, ex pgxExecutor, roomID string) (string, error) {
	var groupID string
	err := ex.QueryRow(ctx,
		`SELECT group_id::text FROM study_rooms WHERE id = $1::uuid`, roomID,
	).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return groupID, err
}

func (r *Repository) Update(ctx context.Context, ex pgxExecutor, roomID, groupID string, f UpdateFields) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_rooms SET
		    name        = COALESCE($3, name),
		    description = COALESCE($4, description),
		    access      = COALESCE($5, access),
		    capacity    = COALESCE($6, capacity)
		 WHERE id = $1::uuid AND group_id = $2::uuid AND archived_at IS NULL`,
		roomID, groupID, f.Name, f.Description, f.Access, f.Capacity)
	if err != nil {
		if IsUniqueViolation(err) {
			return ErrNameTaken
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type UpdateFields struct {
	Name        *string
	Description *string
	Access      *string
	Capacity    *int
}

func (r *Repository) Archive(ctx context.Context, ex pgxExecutor, roomID, groupID string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_rooms SET archived_at = now()
		 WHERE id = $1::uuid AND group_id = $2::uuid AND archived_at IS NULL`,
		roomID, groupID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) Unarchive(ctx context.Context, ex pgxExecutor, roomID, groupID string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE study_rooms SET archived_at = NULL
		 WHERE id = $1::uuid AND group_id = $2::uuid AND archived_at IS NOT NULL`,
		roomID, groupID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByGroup returns all rooms in a group. Caller has already passed the
// group-member gate; we return everything (including restricted) so the
// frontend can render lock icons. Restricted-room *access* is checked per
// room on entry, not in this list.
func (r *Repository) ListByGroup(ctx context.Context, ex pgxExecutor, f ListFilter) ([]Room, error) {
	rows, err := ex.Query(ctx,
		`SELECT id::text, group_id::text, created_by::text, name, description,
		        access, capacity, archived_at, created_at, updated_at
		   FROM study_rooms
		  WHERE group_id = $1::uuid
		    AND ($2::bool OR archived_at IS NULL)
		    AND ($3 = '' OR access = $3)
		    AND ($4 = '' OR name ILIKE '%' || $4 || '%')
		  ORDER BY archived_at NULLS LAST, created_at DESC
		  LIMIT $5 OFFSET $6`,
		f.GroupID, f.IncludeArchived, f.Access, f.Search,
		normalizeLimit(f.Limit), f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Room
	for rows.Next() {
		var room Room
		if err := rows.Scan(&room.ID, &room.GroupID, &room.CreatedBy, &room.Name,
			&room.Description, &room.Access, &room.Capacity, &room.ArchivedAt,
			&room.CreatedAt, &room.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, room)
	}
	return out, rows.Err()
}

// ---------- room memberships ----------

func (r *Repository) GetRoomMembership(ctx context.Context, ex pgxExecutor, roomID, userID string) (*RoomMember, error) {
	var m RoomMember
	var roleStr string
	err := ex.QueryRow(ctx,
		`SELECT id::text, room_id::text, group_id::text, user_id::text,
		        role::text, status, joined_at, revoked_at, created_at
		   FROM room_memberships
		  WHERE room_id = $1::uuid AND user_id = $2::uuid`,
		roomID, userID,
	).Scan(&m.ID, &m.RoomID, &m.GroupID, &m.UserID, &roleStr, &m.Status,
		&m.JoinedAt, &m.RevokedAt, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Role = ParseRoomRole(roleStr)
	return &m, nil
}

// HasActiveRoomMembership returns true if there's an active room_memberships row.
// Cheaper than GetRoomMembership when we only need the boolean.
func (r *Repository) HasActiveRoomMembership(ctx context.Context, ex pgxExecutor, roomID, userID string) (bool, error) {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM room_memberships
		     WHERE room_id = $1::uuid AND user_id = $2::uuid AND status = 'active'
		 )`, roomID, userID,
	).Scan(&ok)
	return ok, err
}

// UpsertRoomMembership: adds or un-revokes a membership row.
// Note: on conflict we only reset status → 'active' and clear revoked_at.
// We do NOT change the role on conflict — role changes go through a
// separate UpdateRole call to preserve audit semantics.
func (r *Repository) UpsertRoomMembership(ctx context.Context, ex pgxExecutor, roomID, groupID, userID, role string) error {
	_, err := ex.Exec(ctx,
		`INSERT INTO room_memberships (room_id, group_id, user_id, role, status)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'active')
		 ON CONFLICT (room_id, user_id) DO UPDATE SET
		    status = 'active',
		    revoked_at = NULL,
		    role = EXCLUDED.role`,
		roomID, groupID, userID, role)
	return err
}

func (r *Repository) UpdateRoomRole(ctx context.Context, ex pgxExecutor, roomID, userID, role string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE room_memberships SET role = $3
		 WHERE room_id = $1::uuid AND user_id = $2::uuid AND status = 'active'`,
		roomID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) RevokeRoomMembership(ctx context.Context, ex pgxExecutor, roomID, userID string) error {
	tag, err := ex.Exec(ctx,
		`UPDATE room_memberships
		    SET status = 'revoked', revoked_at = now()
		  WHERE room_id = $1::uuid AND user_id = $2::uuid AND status = 'active'`,
		roomID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) ListRoomMembers(ctx context.Context, ex pgxExecutor, roomID string, limit, offset int) ([]RoomMemberSummary, error) {
	rows, err := ex.Query(ctx,
		`SELECT rm.user_id::text, u.display_name, u.email,
		        rm.role::text, rm.status, rm.joined_at
		   FROM room_memberships rm
		   JOIN users u ON u.id = rm.user_id
		  WHERE rm.room_id = $1::uuid AND rm.status = 'active'
		  ORDER BY
		    CASE rm.role WHEN 'host' THEN 0 ELSE 1 END,
		    rm.joined_at ASC
		  LIMIT $2 OFFSET $3`,
		roomID, normalizeLimit(limit), offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoomMemberSummary
	for rows.Next() {
		var m RoomMemberSummary
		var roleStr string
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email,
			&roleStr, &m.Status, &m.JoinedAt); err != nil {
			return nil, err
		}
		m.Role = ParseRoomRole(roleStr)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repository) CountActiveHosts(ctx context.Context, ex pgxExecutor, roomID string) (int, error) {
	var n int
	err := ex.QueryRow(ctx,
		`SELECT count(*) FROM room_memberships
		  WHERE room_id = $1::uuid AND role = 'host' AND status = 'active'`,
		roomID).Scan(&n)
	return n, err
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

func IsUniqueViolation(err error) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == "23505"
}

func IsForeignKeyViolation(err error) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == "23503"
}