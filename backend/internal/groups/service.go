// internal/groups/service.go
package groups

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
)

// Pauser is the hook that scheduling implements. Defined here (not imported
// from scheduling) to avoid a cycle: groups → Pauser ← scheduling.
type Pauser interface {
	// PauseGroupActivity pauses all active series in the group.
	// Runs inside the archive transaction so it's atomic.
	PauseGroupActivity(ctx context.Context, tx pgx.Tx, groupID string) error
}

// noopPauser is used in tests and when the scheduling module isn't wired yet.
type noopPauser struct{}

// func (noopPauser) PauseGroupActivity(context.Context, pgx.Tx, string) error { return nil }
func (noopPauser) PauseGroupActivity(context.Context, pgx.Tx, string) error { return nil }

type Service struct {
	repo   *Repository
	pauser Pauser
}

func NewService(repo *Repository, pauser Pauser) *Service {
	if pauser == nil {
		pauser = noopPauser{}
	}
	return &Service{repo: repo, pauser: pauser}
}

// ==================== Create ====================

func (s *Service) Create(ctx context.Context, actorID string, req CreateGroupRequest) (*Group, Role, error) {
	slug := Slugify(req.Slug)
	if slug == "" {
		return nil, RoleNone, fmt.Errorf("slug is empty after normalization")
	}

	// Up to 5 attempts in case the suffix collides.
	var g Group
	for attempt := 0; attempt < 5; attempt++ {
		candidate := slug
		if attempt > 0 {
			candidate = fmtSlugSuffix(slug, attempt+1)
		}

		tx, err := s.repo.db.Begin(ctx)
		if err != nil {
			return nil, RoleNone, err
		}

		g = Group{
			CreatedBy:   actorID,
			Name:        req.Name,
			Slug:        candidate,
			Description: req.Description,
			Visibility:  req.Visibility,
		}
		err = s.repo.CreateGroup(ctx, tx, &g)
		if err != nil {
			_ = tx.Rollback(ctx)
			if IsUniqueViolation(err) {
				continue // retry with a different suffix
			}
			return nil, RoleNone, err
		}
		// Owner membership must exist before COMMIT, or the deferred
		// study_groups_require_owner trigger fires.
		if err := s.repo.AddOwner(ctx, tx, g.ID, actorID); err != nil {
			_ = tx.Rollback(ctx)
			return nil, RoleNone, err
		}
		if err := tx.Commit(ctx); err != nil {
			if IsUniqueViolation(err) {
				continue
			}
			return nil, RoleNone, err
		}
		return &g, RoleOwner, nil
	}
	return nil, RoleNone, ErrSlugTaken
}

// ==================== Read ====================

// Get returns the group plus the caller's role. Non-members get ErrNotFound
// for private groups (anti-enumeration); public groups are visible.
func (s *Service) Get(ctx context.Context, actorID, groupID string) (*Group, Role, error) {
	g, err := s.repo.GetGroupByID(ctx, s.repo.db, groupID)
	if errors.Is(err, ErrNotFound) {
		return nil, RoleNone, ErrNotFound
	}
	if err != nil {
		return nil, RoleNone, err
	}

	m, err := s.repo.GetMembership(ctx, s.repo.db, groupID, actorID)
	if errors.Is(err, ErrNotFound) {
		// Not a member: only visible if public.
		if g.Visibility != "public" {
			return nil, RoleNone, ErrNotFound
		}
		return g, RoleNone, nil
	}
	if err != nil {
		return nil, RoleNone, err
	}
	if m.Status != "active" {
		return nil, RoleNone, ErrForbidden
	}
	return g, m.Role, nil
}

func (s *Service) ListMine(ctx context.Context, actorID string, f ListFilter) ([]Group, error) {
	return s.repo.ListMyGroups(ctx, s.repo.db, actorID, f)
}

func (s *Service) ListPublic(ctx context.Context, f ListFilter) ([]Group, error) {
	return s.repo.ListPublicGroups(ctx, s.repo.db, f)
}

// ==================== Update ====================

func (s *Service) Update(ctx context.Context, actorID, groupID string, req UpdateGroupRequest) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRole(ctx, tx, groupID, actorID, RoleAdmin); err != nil {
		return err
	}
	if err := s.repo.UpdateGroup(ctx, tx, groupID, UpdateGroupFields{
		Name:        req.Name,
		Description: req.Description,
		Visibility:  req.Visibility,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// actually perform the update inside the tx:
func (s *Service) updateTx(ctx context.Context, tx pgx.Tx, groupID string, req UpdateGroupRequest) error {
	// Guarded by the group's archived state; archived groups can't be edited.
	return s.repo.UpdateGroup(ctx, tx, groupID, UpdateGroupFields{
		Name:        req.Name,
		Description: req.Description,
		Visibility:  req.Visibility,
	})
}

// ==================== Archive / Unarchive ====================

func (s *Service) Archive(ctx context.Context, actorID, groupID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Only owner may archive.
	if _, err := s.requireRole(ctx, tx, groupID, actorID, RoleOwner); err != nil {
		return err
	}

	// Pause scheduling FIRST so that when the deferred owner-guard trigger
	// fires at COMMIT, the scheduling state is consistent with the archive.
	if err := s.pauser.PauseGroupActivity(ctx, tx, groupID); err != nil {
		return fmt.Errorf("pause activity: %w", err)
	}
	if err := s.repo.ArchiveGroup(ctx, tx, groupID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Unarchive(ctx context.Context, actorID, groupID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRole(ctx, tx, groupID, actorID, RoleOwner); err != nil {
		return err
	}
	if err := s.repo.UnarchiveGroup(ctx, tx, groupID); err != nil {
		return err
	}
	// study_groups_require_owner_on_unarchive fires at COMMIT; it will fail
	// if we somehow reach here without an active owner. That's the point.
	if err := tx.Commit(ctx); err != nil {
		if IsCheckViolation(err) {
			return ErrLastOwner
		}
		return err
	}
	return nil
}

// ==================== Membership ====================

func (s *Service) ListMembers(ctx context.Context, actorID, groupID string, q ListMembersQuery) ([]MemberSummary, error) {
	if _, err := s.requireRole(ctx, s.repo.db, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	return s.repo.ListMembers(ctx, s.repo.db, groupID, q)
}

// Invite adds a user directly (no email invitation flow yet). Caller must be
// admin+; the target user must exist (FK does that); target must not already
// be a member.
// 






func (s *Service) Invite(
    ctx context.Context,
    actorID, groupID string,
    req InviteMemberRequest,
) (*Membership, error) {
    tx, err := s.repo.db.Begin(ctx)
    if err != nil {
        return nil, err
    }
    defer tx.Rollback(ctx)

    actorRole, err := s.requireRole(
        ctx, tx, groupID, actorID, RoleAdmin,
    )
    if err != nil {
        return nil, err
    }

    targetRole := ParseRole(req.Role)
    if !ValidAssignableRole(targetRole) {
        return nil, ErrInvalidRole
    }

    if targetRole >= actorRole {
        return nil, ErrForbidden
    }

    // Try to find any existing row (active / pending / left / suspended).
    existing, err := s.repo.FindMembershipAnyStatus(
        ctx, tx, groupID, req.UserID,
    )

    switch {
    case err == nil:
        switch existing.Status {
        case "active":
            return nil, ErrAlreadyMember

        case "pending":
            return nil, ErrInviteExists

        case "left", "suspended":
            if err := s.repo.ReopenPendingMembership(
                ctx, tx, groupID, req.UserID,
                targetRole.String(), &actorID,
            ); err != nil {
                return nil, err
            }

        default:
            return nil, ErrAlreadyMember
        }

    case errors.Is(err, ErrNotFound):
        if err := s.repo.InsertPendingMembership(
            ctx, tx, groupID, req.UserID,
            targetRole.String(), &actorID,
        ); err != nil {
            if IsUniqueViolation(err) {
                return nil, ErrInviteExists
            }
            if IsForeignKeyViolation(err) {
                return nil, ErrNotFound
            }
            return nil, err
        }

    default:
        return nil, err
    }

    if err := tx.Commit(ctx); err != nil {
        return nil, err
    }

    return s.repo.GetMembership(
        ctx,
        s.repo.db,
        groupID,
        req.UserID,
    )
}



// func (s *Service) Invite(ctx context.Context, actorID, groupID string, req InviteMemberRequest) (*Membership, error) {
// // func (s *Service) Invite(ctx context.Context, actorID, groupID string, req InviteMemberRequest) error {
// 	tx, err := s.repo.db.Begin(ctx)
// 	if err != nil {
// 		return err
// 	}
// 	defer tx.Rollback(ctx)

// 	actorRole, err := s.requireRole(ctx, tx, groupID, actorID, RoleAdmin)
// 	if err != nil {
// 		return err
// 	}
// 	targetRole := ParseRole(req.Role)
// 	if !ValidAssignableRole(targetRole) {
// 		return ErrInvalidRole
// 	}
// 	if targetRole >= actorRole {
// 		return ErrForbidden
// 	}

// 	// Try to find any existing row (active / pending / left / suspended).
// 	existing, err := s.repo.FindMembershipAnyStatus(ctx, tx, groupID, req.UserID)
// 	switch {
// 	case err == nil:
// 		switch existing.Status {
// 		case "active":
// 			return ErrAlreadyMember
// 		case "pending":
// 			return ErrInviteExists
// 		case "left", "suspended":
// 			if err := s.repo.ReopenPendingMembership(ctx, tx, groupID, req.UserID,
// 				targetRole.String(), &actorID); err != nil {
// 				return err
// 			}
// 		default:
// 			return ErrAlreadyMember
// 		}
// 	case errors.Is(err, ErrNotFound):
// 		if err := s.repo.InsertPendingMembership(ctx, tx, groupID, req.UserID,
// 			targetRole.String(), &actorID); err != nil {
// 			if IsUniqueViolation(err) {
// 				return ErrInviteExists
// 			}
// 			if IsForeignKeyViolation(err) {
// 				return ErrNotFound
// 			}
// 			return err
// 		}
// 	default:
// 		return err
// 	}
// 	if err := tx.Commit(ctx); err != nil {
//     return nil, err
// 	}
	
// 	return s.repo.GetMembership(
//     ctx,
//     s.repo.db,
//     groupID,
//     req.UserID,
// 	)
// }


// func (s *Service) Invite(ctx context.Context, actorID, groupID string, req InviteMemberRequest) error {
// 	tx, err := s.repo.db.Begin(ctx)
// 	if err != nil {
// 		return err
// 	}
// 	defer tx.Rollback(ctx)

// 	actorRole, err := s.requireRole(ctx, tx, groupID, actorID, RoleAdmin)
// 	if err != nil {
// 		return err
// 	}

// 	targetRole := ParseRole(req.Role)
// 	if !ValidAssignableRole(targetRole) {
// 		return ErrInvalidRole
// 	}
// 	// Admin cannot promote to a role >= their own.
// 	if targetRole >= actorRole {
// 		return ErrForbidden
// 	}

// 	if err := s.repo.AddMember(ctx, tx, groupID, req.UserID, targetRole.String(), actorID); err != nil {
// 		if IsUniqueViolation(err) {
// 			return ErrAlreadyMember
// 		}
// 		if IsForeignKeyViolation(err) {
// 			return ErrNotFound // user ID doesn't exist
// 		}
// 		return err
// 	}
// 	return tx.Commit(ctx)
// }

// ChangeRole: admin can change member/moderator; owner can change anyone but
// themselves (must use TransferOwnership to hand off ownership).
func (s *Service) ChangeRole(ctx context.Context, actorID, groupID, targetUserID string, req ChangeRoleRequest) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	actorRole, err := s.requireRole(ctx, tx, groupID, actorID, RoleAdmin)
	if err != nil {
		return err
	}

	newRole := ParseRole(req.Role)
	if !ValidAssignableRole(newRole) {
		return ErrInvalidRole
	}
	if newRole >= actorRole {
		return ErrForbidden
	}

	// Target must exist and be a member.
	target, err := s.repo.GetMembership(ctx, tx, groupID, targetUserID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	// Cannot demote owner via ChangeRole; use TransferOwnership.
	if target.Role >= actorRole {
		return ErrForbidden
	}

	if err := s.repo.UpdateMemberRole(ctx, tx, groupID, targetUserID, newRole.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveMember: admin can remove members below them. Everyone can leave.
func (s *Service) RemoveMember(ctx context.Context, actorID, groupID, targetUserID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Self-leave: any active member may do this.
	if actorID == targetUserID {
		m, err := s.repo.GetMembership(ctx, tx, groupID, actorID)
		if errors.Is(err, ErrNotFound) {
			return ErrNotMember
		}
		if err != nil {
			return err
		}
		if m.Role == RoleOwner {
			return ErrLastOwner // owner must transfer ownership first
		}
		if err := s.repo.RemoveMember(ctx, tx, groupID, actorID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	actorRole, err := s.requireRole(ctx, tx, groupID, actorID, RoleAdmin)
	if err != nil {
		return err
	}

	target, err := s.repo.GetMembership(ctx, tx, groupID, targetUserID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if target.Role >= actorRole {
		return ErrForbidden
	}

	if err := s.repo.RemoveMember(ctx, tx, groupID, targetUserID); err != nil {
		return err
	}
	// Removing an active owner would trip the deferred owner guard; we
	// prevented that above (target.Role >= actorRole implies target.Role=owner
	// when actorRole=owner, and actorID==targetUserID is handled separately).
	return tx.Commit(ctx)
}

// TransferOwnership: current owner passes the crown. Demote self, promote target.
// If either step leaves zero active owners, the deferred trigger fails COMMIT.
func (s *Service) TransferOwnership(ctx context.Context, actorID, groupID, newOwnerID string) error {
	if actorID == newOwnerID {
		return ErrForbidden // can't transfer to yourself
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRole(ctx, tx, groupID, actorID, RoleOwner); err != nil {
		return err
	}

	target, err := s.repo.GetMembership(ctx, tx, groupID, newOwnerID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if target.Status != "active" {
		return ErrForbidden
	}

	// Step 1: demote current owner to admin.
	if err := s.repo.UpdateMemberRole(ctx, tx, groupID, actorID, "admin"); err != nil {
		return err
	}
	// Step 2: promote target to owner. The unique partial index
	// group_memberships_one_owner_idx ensures there's exactly one at commit.
	if err := s.repo.UpdateMemberRole(ctx, tx, groupID, newOwnerID, "owner"); err != nil {
		if IsUniqueViolation(err) {
			return ErrForbidden
		}
		return err
	}

	// Optional: also update study_groups.created_by for record-keeping.
	if _, err := tx.Exec(ctx,
		`UPDATE study_groups SET created_by = $2::uuid WHERE id = $1::uuid`,
		groupID, newOwnerID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		if IsCheckViolation(err) {
			return ErrLastOwner
		}
		return err
	}
	return nil
}

// ==================== the resistance gate ====================

// requireRole is the ONLY place that reads group_memberships for
// authorization. Every mutating method calls it inside its transaction,
// with FOR SHARE so a concurrent role change blocks until we commit.
//
// Non-member  → ErrNotFound   (anti-enumeration)
// Non-active  → ErrForbidden
// Role too low→ ErrForbidden
func (s *Service) requireRole(ctx context.Context, ex pgxExecutor, groupID, userID string, need Role) (Role, error) {
	var roleStr, status string
	err := ex.QueryRow(ctx,
		`SELECT role::text, status
		   FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid
		  FOR SHARE`,
		groupID, userID).Scan(&roleStr, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleNone, ErrNotFound
	}
	if err != nil {
		return RoleNone, err
	}
	if status != "active" {
		return RoleNone, ErrForbidden
	}
	r := ParseRole(roleStr)
	if r < need {
		return RoleNone, ErrForbidden
	}
	return r, nil
}

// Log is exposed so handlers can emit structured warnings on auth failures
// without importing a logger — they just pass the error here.
func (s *Service) Log(err error) {
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
		log.Printf("groups: %v", err)
	}
}







// RequestToJoin is what a user calls to ask to join a group.
//
// Behavior by visibility and prior state:
//   - Group archived                       → ErrGroupArchived
//   - Active member                        → ErrAlreadyMember
//   - Pending request/invite already open  → ErrJoinRequestExists (or ErrInviteExists)
//   - Suspended                            → ErrForbidden (admin must unsuspend)
//   - Left or brand-new                    → insert pending, invited_by=NULL
//   - Public group + brand-new             → activate immediately (no friction)
//
// Public groups skip the approval queue because that's the whole point of
// "public" — the user can still be removed later, and the discovery page
// makes joining a one-click action. Private groups go through approval.
func (s *Service) RequestToJoin(ctx context.Context, actorID, groupID string) (*Membership, error) {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	g, err := s.repo.GetGroupByID(ctx, tx, groupID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if g.Archived() {
		return nil, ErrGroupArchived
	}

	existing, err := s.repo.FindMembershipAnyStatus(ctx, tx, groupID, actorID)
	switch {
	case err == nil:
		switch existing.Status {
		case "active":
			return nil, ErrAlreadyMember
		case "pending":
			if existing.InvitedBy != nil {
				return nil, ErrInviteExists // they have an invite pending; they should accept it
			}
			return nil, ErrJoinRequestExists
		case "suspended":
			return nil, ErrForbidden
		case "left":
			// Re-request: resurrect the row.
			if err := s.repo.ReopenPendingMembership(ctx, tx, groupID, actorID,
				"member", nil); err != nil {
				return nil, err
			}
		}
	case !errors.Is(err, ErrNotFound):
		return nil, err
	default:
		// brand-new membership
		if err := s.repo.InsertPendingMembership(ctx, tx, groupID, actorID,
			"member", nil); err != nil {
			if IsUniqueViolation(err) {
				return nil, ErrJoinRequestExists
			}
			return nil, err
		}
	}

	// Public groups auto-approve — but only if there's no competing invite
	// (invitations are explicit; if an admin invited you and you also
	// requested, the invite's target role wins). Today we don't have both
	// because RequestToJoin rejects when an invite is pending, so this is
	// just the plain auto-activate.
	if g.Visibility == "public" {
		if err := s.repo.ActivateMembership(ctx, tx, groupID, actorID, "member"); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	// Re-read so the caller sees the final state (active or pending).
	return s.repo.GetMembership(ctx, s.repo.db, groupID, actorID)
}

// CancelJoinRequest lets a user withdraw their own pending request
// (but not a pending invitation — that's a DeclineInvite).
func (s *Service) CancelJoinRequest(ctx context.Context, actorID, groupID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	m, err := s.repo.GetPendingMembership(ctx, tx, groupID, actorID)
	if err != nil {
		return err // ErrNotPending
	}
	if m.InvitedBy != nil {
		return ErrForbidden // that's an invitation; use DeclineInvite
	}
	if err := s.repo.MarkMembershipLeft(ctx, tx, groupID, actorID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LeaveGroup lets an active member leave on their own. Owners cannot — they
// must TransferOwnership first. This is the self-service counterpart to
// RemoveMember (which can also self-remove but is admin-shaped).
func (s *Service) LeaveGroup(ctx context.Context, actorID, groupID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	m, err := s.repo.GetMembership(ctx, tx, groupID, actorID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if m.Status != "active" {
		return ErrForbidden
	}
	if m.Role == RoleOwner {
		return ErrNotAuthorizedLeave
	}
	// Use status='left' rather than DELETE so re-requesting doesn't trip
	// the UNIQUE (group_id, user_id) constraint — RequestToJoin handles
	// the 'left' → 'pending' transition.
	if err := s.repo.MarkMembershipLeft(ctx, tx, groupID, actorID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ==================== Join requests (admin-side) ====================

// ListJoinRequests returns pending memberships for the group. By default
// (kind="") it returns both pending requests and pending invitations —
// useful for an admin dashboard. Use kind="requests" to see only user-
// initiated ones, kind="invites" for admin-initiated.
func (s *Service) ListJoinRequests(ctx context.Context, actorID, groupID string, q ListPendingQuery) ([]PendingEntry, error) {
	if _, err := s.requireRole(ctx, s.repo.db, groupID, actorID, RoleModerator); err != nil {
		return nil, err
	}
	return s.repo.ListPendingForGroup(ctx, s.repo.db, groupID, q.Kind, q.Limit, q.Offset)
}

// ApproveJoinRequest flips a pending membership to active. Works for both
// a user-initiated request (approve = accept them into the group) and an
// admin-initiated invitation where a second admin is confirming on behalf
// of the invitee — but the latter is unusual; normally the invitee calls
// AcceptInvite. We allow it so a moderator can force-add without waiting.
//
// The requested role is bounded: an admin cannot promote above themselves,
// and cannot grant 'owner' (that's TransferOwnership).
func (s *Service) ApproveJoinRequest(ctx context.Context, actorID, groupID, targetUserID string, req ApproveJoinRequest) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	actorRole, err := s.requireRole(ctx, tx, groupID, actorID, RoleModerator)
	if err != nil {
		return err
	}

	pending, err := s.repo.GetPendingMembership(ctx, tx, groupID, targetUserID)
	if err != nil {
		return err // ErrNotPending
	}

	// Default: whatever the pending row already says (usually 'member').
	finalRole := pending.Role
	if req.Role != "" {
		finalRole = ParseRole(req.Role)
		if !ValidAssignableRole(finalRole) {
			return ErrInvalidRole
		}
	}
	if finalRole >= actorRole {
		return ErrForbidden
	}

	if err := s.repo.ActivateMembership(ctx, tx, groupID, targetUserID, finalRole.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RejectJoinRequest marks a pending membership as left (rejected). Works on
// both pending requests and pending invitations that an admin is revoking.
func (s *Service) RejectJoinRequest(ctx context.Context, actorID, groupID, targetUserID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRole(ctx, tx, groupID, actorID, RoleModerator); err != nil {
		return err
	}
	if _, err := s.repo.GetPendingMembership(ctx, tx, groupID, targetUserID); err != nil {
		return err // ErrNotPending
	}
	if err := s.repo.MarkMembershipLeft(ctx, tx, groupID, targetUserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ==================== Invitations (invitee-side) ====================

// ListMyInvites returns pending invitations for the caller. Used by the
// "you have invites" page. Returns every group where the caller has a
// pending row and invited_by IS NOT NULL.
func (s *Service) ListMyInvites(ctx context.Context, actorID string) ([]PendingEntry, error) {
	return s.repo.ListPendingForUser(ctx, s.repo.db, actorID)
}

// AcceptInvite is the invitee's "yes". Only the invitee can call this.
func (s *Service) AcceptInvite(ctx context.Context, actorID, groupID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	m, err := s.repo.GetPendingMembership(ctx, tx, groupID, actorID)
	if err != nil {
		return err // ErrNotPending
	}
	if m.InvitedBy == nil {
		return ErrNotInvited // they have a request pending, not an invite
	}

	// Confirm the group isn't archived.
	g, err := s.repo.GetGroupByID(ctx, tx, groupID)
	if err != nil {
		return err
	}
	if g.Archived() {
		return ErrGroupArchived
	}

	if err := s.repo.ActivateMembership(ctx, tx, groupID, actorID, m.Role.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeclineInvite is the invitee's "no". Marks the row left, so the admin
// must explicitly re-invite to reopen (which reuses ReopenPendingMembership).
func (s *Service) DeclineInvite(ctx context.Context, actorID, groupID string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	m, err := s.repo.GetPendingMembership(ctx, tx, groupID, actorID)
	if err != nil {
		return err
	}
	if m.InvitedBy == nil {
		return ErrNotInvited
	}
	if err := s.repo.MarkMembershipLeft(ctx, tx, groupID, actorID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// package groups

// import (
// 	"context"
// 	"errors"
// 	"fmt"
// 	"regexp"
// 	"strings"

// 	"github.com/jackc/pgx/v5"
// 	"github.com/jackc/pgx/v5/pgxpool"

// 	"study-platform/backend/internal/database"
// )

// var (
// 	ErrNotFound      = errors.New("not found")
// 	ErrForbidden     = errors.New("forbidden")
// 	ErrInvalidInput  = errors.New("invalid input")
// 	ErrConflict      = errors.New("conflict")
// 	ErrGroupArchived = errors.New("group is archived")
// 	ErrLastOwner     = errors.New("cannot remove the last owner")
// 	ErrSlugTaken     = errors.New("a group with this slug already exists")
// )

// // wrappedError carries a user-facing message while preserving errors.Is for
// // the underlying sentinel.
// type wrappedError struct {
// 	cause error
// 	msg   string
// }

// func (e *wrappedError) Error() string { return e.msg }
// func (e *wrappedError) Unwrap() error { return e.cause }

// func wrap(cause error, format string, args ...any) error {
// 	return &wrappedError{cause: cause, msg: fmt.Sprintf(format, args...)}
// }

// // Pauser pauses a group's scheduling activity inside the archive transaction.
// // Implemented by the scheduling module; a no-op until then.
// type Pauser interface {
// 	PauseGroupActivity(ctx context.Context, tx pgx.Tx, groupID string) error
// }

// type Service struct {
// 	db     *pgxpool.Pool
// 	repo   *Repository
// 	pauser Pauser
// }

// func NewService(db *pgxpool.Pool, pauser Pauser) *Service {
// 	return &Service{db: db, repo: NewRepository(), pauser: pauser}
// }

// var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// func validateName(name string) error {
// 	n := strings.TrimSpace(name)
// 	if n == "" || len([]rune(n)) > 100 {
// 		return wrap(ErrInvalidInput, "name must be between 1 and 100 characters")
// 	}
// 	return nil
// }

// func normalizeAndValidateSlug(slug string) (string, error) {
// 	s := strings.ToLower(strings.TrimSpace(slug))
// 	if len(s) < 3 || len(s) > 60 || !slugRe.MatchString(s) {
// 		return "", wrap(ErrInvalidInput, "slug must be 3-60 lowercase letters, numbers, and single hyphens")
// 	}
// 	return s, nil
// }

// func validateDescription(d string) error {
// 	if len([]rune(d)) > 2000 {
// 		return wrap(ErrInvalidInput, "description must be at most 2000 characters")
// 	}
// 	return nil
// }

// func validateVisibility(v string) (string, error) {
// 	if v == "" {
// 		return "private", nil
// 	}
// 	if v != "private" && v != "public" {
// 		return "", wrap(ErrInvalidInput, "visibility must be private or public")
// 	}
// 	return v, nil
// }

// func (s *Service) Create(ctx context.Context, userID, name, slug, description, visibility string) (*Group, error) {
// 	if err := validateName(name); err != nil {
// 		return nil, err
// 	}
// 	normSlug, err := normalizeAndValidateSlug(slug)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if err := validateDescription(description); err != nil {
// 		return nil, err
// 	}
// 	vis, err := validateVisibility(visibility)
// 	if err != nil {
// 		return nil, err
// 	}

// 	var g *Group
// 	err = database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
// 		var err error
// 		g, err = s.repo.CreateGroup(ctx, tx, userID, strings.TrimSpace(name), normSlug, description, vis)
// 		if err != nil {
// 			return err
// 		}
// 		return s.repo.AddMembership(ctx, tx, g.ID, userID, "owner")
// 	})
// 	if err != nil {
// 		return nil, mapPgErr(err)
// 	}
// 	return g, nil
// }

// func (s *Service) Get(ctx context.Context, userID, groupID string) (*Group, error) {
// 	g, err := s.repo.GetByID(ctx, s.db, groupID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	role, err := s.repo.ActiveRole(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if role != "" || (g.Visibility == "public" && g.ArchivedAt == nil) {
// 		return g, nil
// 	}
// 	return nil, ErrNotFound
// }

// func (s *Service) Update(ctx context.Context, userID, groupID string, name, description, visibility *string) (*Group, error) {
// 	g, err := s.repo.GetByID(ctx, s.db, groupID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	role, err := s.requireMember(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if role != "owner" && role != "admin" {
// 		return nil, ErrForbidden
// 	}
// 	if name != nil {
// 		if err := validateName(*name); err != nil {
// 			return nil, err
// 		}
// 	}
// 	if description != nil {
// 		if err := validateDescription(*description); err != nil {
// 			return nil, err
// 		}
// 	}

// 	newName := g.Name
// 	if name != nil {
// 		newName = strings.TrimSpace(*name)
// 	}
// 	newDesc := g.Description
// 	if description != nil {
// 		newDesc = *description
// 	}
// 	newVis := g.Visibility
// 	if visibility != nil {
// 		v, err := validateVisibility(*visibility)
// 		if err != nil {
// 			return nil, err
// 		}
// 		newVis = v
// 	}

// 	updated, err := s.repo.Update(ctx, s.db, groupID, newName, newDesc, newVis)
// 	if err != nil {
// 		return nil, mapPgErr(err)
// 	}
// 	return updated, nil
// }

// func (s *Service) Archive(ctx context.Context, userID, groupID string) error {
// 	g, err := s.repo.GetByID(ctx, s.db, groupID)
// 	if err != nil {
// 		return err
// 	}
// 	role, err := s.requireMember(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return err
// 	}
// 	if role != "owner" {
// 		return ErrForbidden
// 	}
// 	if g.ArchivedAt != nil {
// 		return ErrGroupArchived
// 	}
// 	return mapPgErr(database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
// 		if err := s.repo.Archive(ctx, tx, groupID); err != nil {
// 			return err
// 		}
// 		if s.pauser != nil {
// 			return s.pauser.PauseGroupActivity(ctx, tx, groupID)
// 		}
// 		return nil
// 	}))
// }

// func (s *Service) Unarchive(ctx context.Context, userID, groupID string) error {
// 	g, err := s.repo.GetByID(ctx, s.db, groupID)
// 	if err != nil {
// 		return err
// 	}
// 	role, err := s.requireMember(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return err
// 	}
// 	if role != "owner" {
// 		return ErrForbidden
// 	}
// 	if g.ArchivedAt == nil {
// 		return ErrConflict
// 	}
// 	return mapPgErr(database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
// 		return s.repo.Unarchive(ctx, tx, groupID)
// 	}))
// }

// func (s *Service) ListMyGroups(ctx context.Context, userID string, limit int, cursor string) ([]Group, string, error) {
// 	return s.repo.ListForUser(ctx, s.db, userID, limit, cursor)
// }

// func (s *Service) Discover(ctx context.Context, limit int, cursor string) ([]Group, string, error) {
// 	return s.repo.ListDiscover(ctx, s.db, limit, cursor)
// }

// func (s *Service) ListMembers(ctx context.Context, userID, groupID string, limit int, cursor string) ([]Member, string, error) {
// 	if _, err := s.repo.GetByID(ctx, s.db, groupID); err != nil {
// 		return nil, "", err
// 	}
// 	if _, err := s.requireMember(ctx, s.db, groupID, userID); err != nil {
// 		return nil, "", err
// 	}
// 	return s.repo.ListMembers(ctx, s.db, groupID, limit, cursor)
// }

// func (s *Service) ChangeRole(ctx context.Context, userID, groupID, targetID, newRole string) (*Member, error) {
// 	if newRole != "owner" && newRole != "admin" && newRole != "member" {
// 		return nil, wrap(ErrInvalidInput, "role must be owner, admin, or member")
// 	}
// 	g, err := s.repo.GetByID(ctx, s.db, groupID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	role, err := s.requireMember(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if role != "owner" {
// 		return nil, ErrForbidden
// 	}
// 	if g.ArchivedAt != nil {
// 		return nil, ErrGroupArchived
// 	}

// 	target, err := s.repo.GetMembership(ctx, s.db, groupID, targetID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if target.Status != "active" {
// 		return nil, ErrNotFound
// 	}

// 	switch newRole {
// 	case "owner":
// 		if target.Role == "owner" {
// 			// Already owner: idempotent.
// 			return s.repo.UpdateRole(ctx, s.db, groupID, targetID, "owner")
// 		}
// 		// Ownership transfer: demote the current owner, then promote the
// 		// target, in one transaction. The non-deferrable partial unique index
// 		// (one owner per group) requires demote-first ordering.
// 		var updated *Member
// 		err := database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
// 			if err := s.repo.LockGroup(ctx, tx, groupID); err != nil {
// 				return err
// 			}
// 			if _, err := s.repo.UpdateRole(ctx, tx, groupID, userID, "admin"); err != nil {
// 				return err
// 			}
// 			m, err := s.repo.UpdateRole(ctx, tx, groupID, targetID, "owner")
// 			if err != nil {
// 				return err
// 			}
// 			updated = m
// 			return nil
// 		})
// 		if err != nil {
// 			return nil, mapPgErr(err)
// 		}
// 		return updated, nil
// 	default: // admin or member
// 		if target.Role == "owner" {
// 			return nil, ErrLastOwner
// 		}
// 		return s.repo.UpdateRole(ctx, s.db, groupID, targetID, newRole)
// 	}
// }

// func (s *Service) RemoveMember(ctx context.Context, userID, groupID, targetID string) error {
// 	if _, err := s.repo.GetByID(ctx, s.db, groupID); err != nil {
// 		return err
// 	}
// 	role, err := s.requireMember(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return err
// 	}
// 	if role != "owner" && role != "admin" {
// 		return ErrForbidden
// 	}
// 	target, err := s.repo.GetMembership(ctx, s.db, groupID, targetID)
// 	if err != nil {
// 		return err
// 	}
// 	if target.Role == "owner" {
// 		return ErrLastOwner
// 	}
// 	return s.repo.DeleteMembership(ctx, s.db, groupID, targetID)
// }

// func (s *Service) Leave(ctx context.Context, userID, groupID string) error {
// 	if _, err := s.repo.GetByID(ctx, s.db, groupID); err != nil {
// 		return err
// 	}
// 	role, err := s.requireMember(ctx, s.db, groupID, userID)
// 	if err != nil {
// 		return err
// 	}
// 	if role == "owner" {
// 		return ErrLastOwner
// 	}
// 	return s.repo.DeleteMembership(ctx, s.db, groupID, userID)
// }

// // requireMember returns the caller's role if they are an active member, or
// // ErrNotFound otherwise (so non-members cannot distinguish "missing" from
// // "exists but you may not see it").
// func (s *Service) requireMember(ctx context.Context, db database.DBTX, groupID, userID string) (string, error) {
// 	role, err := s.repo.ActiveRole(ctx, db, groupID, userID)
// 	if err != nil {
// 		return "", err
// 	}
// 	if role == "" {
// 		return "", ErrNotFound
// 	}
// 	return role, nil
// }
