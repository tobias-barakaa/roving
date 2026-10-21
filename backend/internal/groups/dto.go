package groups

import "time"

type createGroupRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
}

type updateGroupRequest struct {
	Name        *string `json:"name"`
	Slug        *string `json:"slug"` // immutable: rejected by the handler
	Description *string `json:"description"`
	Visibility  *string `json:"visibility"`
}

type changeRoleRequest struct {
	Role string `json:"role"`
}

type groupResponse struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility"`
	ArchivedAt  *time.Time `json:"archivedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type memberResponse struct {
	UserID   string     `json:"userId"`
	Email    string     `json:"email"`
	Role     string     `json:"role"`
	Status   string     `json:"status"`
	JoinedAt *time.Time `json:"joinedAt"`
}

type groupListResponse struct {
	Items      []groupResponse `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type memberListResponse struct {
	Items      []memberResponse `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func newGroupResponse(g *Group) groupResponse {
	return groupResponse{
		ID:          g.ID,
		Name:        g.Name,
		Slug:        g.Slug,
		Description: g.Description,
		Visibility:  g.Visibility,
		ArchivedAt:  g.ArchivedAt,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}
}

func newMemberResponse(m *Member) memberResponse {
	return memberResponse{
		UserID:   m.UserID,
		Email:    m.Email,
		Role:     m.Role,
		Status:   m.Status,
		JoinedAt: m.JoinedAt,
	}
}
