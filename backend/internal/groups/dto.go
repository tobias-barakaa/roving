// internal/groups/dto.go
package groups

import "time"

// --- requests ---

type CreateGroupRequest struct {
	Name        string `json:"name"        binding:"required,min=1,max=100"`
	// Slug        string `json:"slug"        binding:"required,min=3,max=60"`
	Description string `json:"description" binding:"max=2000"`
	Visibility  string `json:"visibility"  binding:"required,oneof=private public"`
}

type UpdateGroupRequest struct {
	Name        *string `json:"name"        binding:"omitempty,min=1,max=100"`
	Description *string `json:"description" binding:"omitempty,max=2000"`
	Visibility  *string `json:"visibility"  binding:"omitempty,oneof=private public"`
}

type InviteMemberRequest struct {
	UserID string `json:"userId" binding:"required,uuid"`
	Role   string `json:"role"   binding:"required,oneof=member moderator admin"`
}

type ChangeRoleRequest struct {
	Role string `json:"role" binding:"required,oneof=member moderator admin"`
}

type TransferOwnershipRequest struct {
	NewOwnerUserID string `json:"newOwnerUserId" binding:"required,uuid"`
}

// type ListMembersQuery struct {
// 	Limit  int    `form:"limit"  binding:"omitempty,min=1,max=200"`
// 	Offset int    `form:"offset" binding:"min=0"`
// 	Role   string `form:"role" binding:"omitempty,oneof=owner admin moderator member"`
// }


type ListMembersQuery struct {
	Limit  int    `form:"limit"  binding:"omitempty,min=1,max=200"`
	Offset int    `form:"offset" binding:"min=0"`
	Role   string `form:"role"   binding:"omitempty,oneof=owner admin moderator member"`
	Status string `form:"status" binding:"omitempty,oneof=active pending suspended left"`  // NEW
}

// --- responses ---

type GroupResponse struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility"`
	CreatedBy   string     `json:"createdBy"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	// Only filled on detail / list responses:
	MemberCount int  `json:"memberCount,omitempty"`
	MyRole      Role `json:"myRole,omitempty"` // caller's role in this group
}

type MemberResponse struct {
	UserID      string     `json:"userId"`
	DisplayName string     `json:"displayName"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	JoinedAt    *time.Time `json:"joinedAt,omitempty"`
}

// --- mapping ---

func toGroupResponse(g *Group) GroupResponse {
	return GroupResponse{
		ID:          g.ID,
		Name:        g.Name,
		Slug:        g.Slug,
		Description: g.Description,
		Visibility:  g.Visibility,
		CreatedBy:   g.CreatedBy,
		ArchivedAt:  g.ArchivedAt,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}
}

func toMemberResponse(m MemberSummary) MemberResponse {
	return MemberResponse{
		UserID:      m.UserID,
		DisplayName: m.DisplayName,
		Email:       m.Email,
		Role:        m.Role.String(),
		Status:      m.Status,
		JoinedAt:    m.JoinedAt,
	}
}



// RequestToJoinRequest is what a user sends when asking to join a private
// (or public) group. Public groups should normally auto-activate — but we
// still route through the same endpoint so the client doesn't have to
// special-case the two visibilities. The service decides.
type RequestToJoinRequest struct {
	// Reserved for Path B. Ignored today.
	Message string `json:"message" binding:"omitempty,max=1000"`
}

// ApproveJoinRequest is optionally parameterized — if the applier is a
// pending member who requested in (invited_by IS NULL), the admin may
// promote them to a role other than 'member' directly at approval time.
// Default is 'member'. Owner is never assignable here.
type ApproveJoinRequest struct {
	Role string `json:"role" binding:"omitempty,oneof=member moderator admin"`
}

// ListPendingQuery lets the UI fetch "pending invites" (invited_by NOT NULL)
// versus "pending requests" (invited_by IS NULL) as separate lists, or both
// together. It reuses ListMembersQuery's shape.
type ListPendingQuery struct {
	// "invites" | "requests" | "" (both)
	Kind   string `form:"kind"   binding:"omitempty,oneof=invites requests"`
	Limit  int    `form:"limit"  binding:"omitempty,min=1,max=200"`
	Offset int    `form:"offset" binding:"min=0"`
}

// --- responses ---

// PendingEntryResponse is the shape returned by ListPendingInvites and
// ListJoinRequests. It includes the group so a user can see "you were
// invited to <group>" without a second round-trip.
type PendingEntryResponse struct {
	Group       GroupResponse `json:"group"`
	UserID      string        `json:"userId"`
	DisplayName string        `json:"displayName"`
	Email       string        `json:"email"`
	Role        string        `json:"role"`        // role they'll have on approval
	InvitedBy   *string       `json:"invitedBy,omitempty"`
	CreatedAt   time.Time     `json:"createdAt"`
}

type PendingEntry struct {
	Group       Group
	UserID      string
	DisplayName string
	Email       string
	Role        Role
	InvitedBy   *string
	CreatedAt   time.Time
}

func toPendingEntryResponse(e PendingEntry) PendingEntryResponse {
	return PendingEntryResponse{
		Group:       toGroupResponse(&e.Group),
		UserID:      e.UserID,
		DisplayName: e.DisplayName,
		Email:       e.Email,
		Role:        e.Role.String(),
		InvitedBy:   e.InvitedBy,
		CreatedAt:   e.CreatedAt,
	}
}

type InviteByEmailRequest struct {
	Email string `json:"email" binding:"required,email"`
	Role  string `json:"role"  binding:"required,oneof=member moderator admin"`
}


// package groups

// import "time"

// type createGroupRequest struct {
// 	Name        string `json:"name"`
// 	Slug        string `json:"slug"`
// 	Description string `json:"description"`
// 	Visibility  string `json:"visibility"`
// }

// type updateGroupRequest struct {
// 	Name        *string `json:"name"`
// 	Slug        *string `json:"slug"` // immutable: rejected by the handler
// 	Description *string `json:"description"`
// 	Visibility  *string `json:"visibility"`
// }

// type changeRoleRequest struct {
// 	Role string `json:"role"`
// }

// type groupResponse struct {
// 	ID          string     `json:"id"`
// 	Name        string     `json:"name"`
// 	Slug        string     `json:"slug"`
// 	Description string     `json:"description"`
// 	Visibility  string     `json:"visibility"`
// 	ArchivedAt  *time.Time `json:"archivedAt"`
// 	CreatedAt   time.Time  `json:"createdAt"`
// 	UpdatedAt   time.Time  `json:"updatedAt"`
// }

// type memberResponse struct {
// 	UserID   string     `json:"userId"`
// 	Email    string     `json:"email"`
// 	Role     string     `json:"role"`
// 	Status   string     `json:"status"`
// 	JoinedAt *time.Time `json:"joinedAt"`
// }

// type groupListResponse struct {
// 	Items      []groupResponse `json:"items"`
// 	NextCursor string          `json:"nextCursor,omitempty"`
// }

// type memberListResponse struct {
// 	Items      []memberResponse `json:"items"`
// 	NextCursor string           `json:"nextCursor,omitempty"`
// }

// func newGroupResponse(g *Group) groupResponse {
// 	return groupResponse{
// 		ID:          g.ID,
// 		Name:        g.Name,
// 		Slug:        g.Slug,
// 		Description: g.Description,
// 		Visibility:  g.Visibility,
// 		ArchivedAt:  g.ArchivedAt,
// 		CreatedAt:   g.CreatedAt,
// 		UpdatedAt:   g.UpdatedAt,
// 	}
// }

// func newMemberResponse(m *Member) memberResponse {
// 	return memberResponse{
// 		UserID:   m.UserID,
// 		Email:    m.Email,
// 		Role:     m.Role,
// 		Status:   m.Status,
// 		JoinedAt: m.JoinedAt,
// 	}
// }
