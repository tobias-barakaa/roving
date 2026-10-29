// internal/rooms/service.go
package rooms

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
}

func NewService(repo *Repository, db *pgxpool.Pool) *Service {
	return &Service{repo: repo, db: db}
}

// ==================== Create ====================

func (s *Service) Create(ctx context.Context, actorID, groupID string, req CreateRoomRequest) (*Room, error) {
	if !ValidAccess(req.Access) {
		return nil, ErrInvalidAccess
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Moderator+ may create rooms. If you want admin-only, change RoleAdmin.
	if _, err := s.requireGroupRole(ctx, tx, groupID, actorID, GroupRoleModerator); err != nil {
		return nil, err
	}

	room := &Room{
		GroupID:     groupID,
		CreatedBy:   actorID,
		Name:        strings.TrimSpace(req.Name),
		Description: req.Description,
		Access:      req.Access,
		Capacity:    req.Capacity,
	}
	if err := s.repo.Insert(ctx, tx, room); err != nil {
		if IsUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	// If the room is restricted, the creator becomes the first host.
	// Rationale: someone must be able to invite others into a restricted room;
	// otherwise it's a locked vault. If you disagree, delete this block.
	if room.Access == AccessRestricted {
		if err := s.repo.UpsertRoomMembership(ctx, tx, room.ID, groupID, actorID, "host"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return room, nil
}

// ==================== Read ====================

// Get returns the room detail plus the caller's resolved permissions.
// If the caller is a group member but cannot enter (restricted + not a
// room member), the room is still returned with CanEnter=false so the
// frontend can render the lock state. This is deliberate: the caller
// *knows* the room exists because it's in the group list; hiding it here
// would be inconsistent.
func (s *Service) Get(ctx context.Context, actorID, groupID, roomID string) (*RoomDetail, error) {
	room, err := s.repo.GetByIDGroup(ctx, s.db, roomID, groupID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	groupRole, err := s.requireGroupRole(ctx, s.db, groupID, actorID, GroupRoleMember)
	if err != nil {
		return nil, err
	}

	canEnter := room.Access == AccessGroup
	var roomRole RoomRole
	// Look up room role (works for both access modes; useful for hosts even
	// on 'group' rooms).
	if m, err := s.repo.GetRoomMembership(ctx, s.db, roomID, actorID); err == nil && m.Status == "active" {
		roomRole = m.Role
		if room.Access == AccessRestricted {
			canEnter = true
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Group admins/owner may enter any room (moderation override).
	if groupRole >= GroupRoleAdmin {
		canEnter = true
	}

	// Group name for display.
	var groupName string
	if err := s.db.QueryRow(ctx,
		`SELECT name FROM study_groups WHERE id = $1::uuid`, groupID,
	).Scan(&groupName); err != nil {
		return nil, err
	}

	return &RoomDetail{
		Room:       *room,
		GroupName:  groupName,
		MyRole:     groupRole,
		MyRoomRole: roomRole,
		CanEnter:   canEnter,
	}, nil
}

func (s *Service) List(ctx context.Context, actorID, groupID string, f ListFilter) ([]Room, error) {
	if _, err := s.requireGroupRole(ctx, s.db, groupID, actorID, GroupRoleMember); err != nil {
		return nil, err
	}
	f.GroupID = groupID
	return s.repo.ListByGroup(ctx, s.db, f)
}

// ==================== Update / archive ====================

func (s *Service) Update(ctx context.Context, actorID, groupID, roomID string, req UpdateRoomRequest) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Update requires *either* group-admin+ OR room-host. This is the only
	// place in rooms where room-role alone suffices for a mutation.
	if _, err := s.requireRoomWriteAccess(ctx, tx, groupID, roomID, actorID); err != nil {
		return err
	}

	if req.Access != nil && !ValidAccess(*req.Access) {
		return ErrInvalidAccess
	}

	fields := UpdateFields{
		Name:        req.Name,
		Description: req.Description,
		Access:      req.Access,
		Capacity:    req.Capacity,
	}
	if err := s.repo.Update(ctx, tx, roomID, groupID, fields); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrArchived
		}
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Archive(ctx context.Context, actorID, groupID, roomID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireGroupRole(ctx, tx, groupID, actorID, GroupRoleAdmin); err != nil {
		return err
	}
	if err := s.repo.Archive(ctx, tx, roomID, groupID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Unarchive(ctx context.Context, actorID, groupID, roomID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireGroupRole(ctx, tx, groupID, actorID, GroupRoleAdmin); err != nil {
		return err
	}
	if err := s.repo.Unarchive(ctx, tx, roomID, groupID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ==================== Room membership (host management) ====================

// GrantAccess lets a host (or group admin) explicitly add someone to a
// restricted room. For an 'group' room, this is a no-op (everyone can
// already enter), so we return an error to make it explicit.
func (s *Service) GrantAccess(ctx context.Context, actorID, groupID, roomID string, req GrantRoomAccessRequest) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRoomWriteAccess(ctx, tx, groupID, roomID, actorID); err != nil {
		return err
	}

	room, err := s.repo.GetByIDGroup(ctx, tx, roomID, groupID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if room.Access != AccessRestricted {
		return ErrInvalidAccess // caller tried to grant to a group-room
	}

	role := ParseRoomRole(req.Role)
	if role != RoomRoleHost && role != RoomRoleMember {
		return ErrInvalidAccess
	}

	// The target must be an active group member. The DB FK enforces this,
	// but we check explicitly to return a clean ErrNotMember.
	var ok bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM group_memberships
		                 WHERE group_id = $1::uuid AND user_id = $2::uuid
		                   AND status = 'active')`,
		groupID, req.UserID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotMember
	}

	if err := s.repo.UpsertRoomMembership(ctx, tx, roomID, groupID, req.UserID, role.String()); err != nil {
		if IsForeignKeyViolation(err) {
			return ErrNotMember
		}
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ChangeRole(ctx context.Context, actorID, groupID, roomID, targetUserID string, req ChangeRoomRoleRequest) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	actorRole, err := s.requireRoomWriteAccess(ctx, tx, groupID, roomID, actorID)
	if err != nil {
		return err
	}

	newRole := ParseRoomRole(req.Role)
	if newRole != RoomRoleHost && newRole != RoomRoleMember {
		return ErrInvalidAccess
	}

	// Room hosts can only change member→host, not demote other hosts unless
	// they are also group admin+.
	if actorRole == RoomRoleHost && newRole == RoomRoleMember {
		// A host demoting another host is allowed only if they can prove
		// there will remain ≥ 1 active host after the change.
		// Easiest: forbid host-demotes-host at the room level; require group admin.
		gr, err := s.requireGroupRole(ctx, tx, groupID, actorID, GroupRoleMember)
		if err != nil {
			return err
		}
		if gr < GroupRoleAdmin {
			return ErrNotHost
		}
	}

	target, err := s.repo.GetRoomMembership(ctx, tx, roomID, targetUserID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if target.Status != "active" {
		return ErrForbidden
	}
	if target.UserID == actorID && newRole != target.Role {
		// Self-demotion from host → member is allowed only if ≥ 2 active hosts.
		if target.Role == RoomRoleHost {
			n, err := s.repo.CountActiveHosts(ctx, tx, roomID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrNotHost // would leave restricted room hostless
			}
		}
	}

	if err := s.repo.UpdateRoomRole(ctx, tx, roomID, targetUserID, newRole.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) RevokeAccess(ctx context.Context, actorID, groupID, roomID, targetUserID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRoomWriteAccess(ctx, tx, groupID, roomID, actorID); err != nil {
		return err
	}

	// Don't let the last host revoke themselves out of a restricted room.
	target, err := s.repo.GetRoomMembership(ctx, tx, roomID, targetUserID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if target.Role == RoomRoleHost {
		n, err := s.repo.CountActiveHosts(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrNotHost
		}
	}

	if err := s.repo.RevokeRoomMembership(ctx, tx, roomID, targetUserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ListMembers(ctx context.Context, actorID, groupID, roomID string, limit, offset int) ([]RoomMemberSummary, error) {
	// Both the group-member gate AND the room-access gate apply: if you
	// can't enter the room, you can't see who's in it.
	if _, err := s.requireRoomAccess(ctx, s.db, groupID, roomID, actorID); err != nil {
		return nil, err
	}
	return s.repo.ListRoomMembers(ctx, s.db, roomID, limit, offset)
}

// ==================== authorization gates ====================

// requireGroupRole is identical to the one in groups/scheduling.
// Non-member → ErrNotFound (anti-enumeration).
func (s *Service) requireGroupRole(ctx context.Context, ex pgxExecutor, groupID, userID string, need GroupRole) (GroupRole, error) {
	var roleStr, status string
	err := ex.QueryRow(ctx,
		`SELECT role::text, status
		   FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid
		  FOR SHARE`,
		groupID, userID).Scan(&roleStr, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupRoleNone, ErrNotFound
	}
	if err != nil {
		return GroupRoleNone, err
	}
	if status != "active" {
		return GroupRoleNone, ErrForbidden
	}
	r := ParseGroupRole(roleStr)
	if r < need {
		return GroupRoleNone, ErrForbidden
	}
	return r, nil
}

// requireRoomAccess is gate 1 + gate 2. Used by anything that means
// "the caller is trying to be inside the room" (read members, entry,
// future WebSocket join).
func (s *Service) requireRoomAccess(ctx context.Context, ex pgxExecutor, groupID, roomID, userID string) (GroupRole, error) {
	groupRole, err := s.requireGroupRole(ctx, ex, groupID, userID, GroupRoleMember)
	if err != nil {
		return GroupRoleNone, err
	}

	var access string
	err = ex.QueryRow(ctx,
		`SELECT access FROM study_rooms
		  WHERE id = $1::uuid AND group_id = $2::uuid AND archived_at IS NULL`,
		roomID, groupID).Scan(&access)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupRoleNone, ErrNotFound
	}
	if err != nil {
		return GroupRoleNone, err
	}

	if access == AccessGroup {
		return groupRole, nil
	}

	// Restricted. Group admin+ bypasses.
	if groupRole >= GroupRoleAdmin {
		return groupRole, nil
	}

	ok, err := s.repo.HasActiveRoomMembership(ctx, ex, roomID, userID)
	if err != nil {
		return GroupRoleNone, err
	}
	if !ok {
		return GroupRoleNone, ErrForbidden
	}
	return groupRole, nil
}

// requireRoomWriteAccess answers: "may the caller modify this room?".
// Group admin+ OR room-host.
func (s *Service) requireRoomWriteAccess(ctx context.Context, ex pgxExecutor, groupID, roomID, userID string) (RoomRole, error) {
	groupRole, err := s.requireGroupRole(ctx, ex, groupID, userID, GroupRoleMember)
	if err != nil {
		return RoomRoleNone, err
	}
	if groupRole >= GroupRoleAdmin {
		return RoomRoleHost, nil // admins act with host-equivalent power
	}

	// Must have active room membership with role='host'.
	m, err := s.repo.GetRoomMembership(ctx, ex, roomID, userID)
	if errors.Is(err, ErrNotFound) {
		return RoomRoleNone, ErrNotHost
	}
	if err != nil {
		return RoomRoleNone, err
	}
	if m.Status != "active" || m.Role != RoomRoleHost {
		return RoomRoleNone, ErrNotHost
	}
	return RoomRoleHost, nil
}


func (s *Service) RequireRoomAccess(ctx context.Context, groupID, roomID, userID string) (GroupRole, error) {
	return s.requireRoomAccess(ctx, s.db, groupID, roomID, userID)
}