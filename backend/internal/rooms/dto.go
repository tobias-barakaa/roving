// internal/rooms/dto.go
package rooms

import "time"

// --- requests ---

type CreateRoomRequest struct {
	Name        string `json:"name"        binding:"required,min=1,max=100"`
	Description string `json:"description" binding:"max=2000"`
	Access      string `json:"access"      binding:"required,oneof=group restricted"`
	Capacity    *int   `json:"capacity"    binding:"omitempty,min=1,max=1000"`
}

type UpdateRoomRequest struct {
	Name        *string `json:"name"        binding:"omitempty,min=1,max=100"`
	Description *string `json:"description" binding:"omitempty,max=2000"`
	Access      *string `json:"access"      binding:"omitempty,oneof=group restricted"`
	Capacity    *int    `json:"capacity"    binding:"omitempty,min=1,max=1000"`
}

type GrantRoomAccessRequest struct {
	UserID string `json:"userId" binding:"required,uuid"`
	Role   string `json:"role"   binding:"required,oneof=member host"`
}

type ChangeRoomRoleRequest struct {
	Role string `json:"role" binding:"required,oneof=member host"`
}

type ListRoomsQuery struct {
	IncludeArchived bool   `form:"includeArchived"`
	Access          string `form:"access" binding:"omitempty,oneof=group restricted"`
	Search          string `form:"search"`
	Limit           int    `form:"limit"  binding:"omitempty,min=1,max=200"`
	Offset          int    `form:"offset" binding:"min=0"`
}

// --- responses ---

type RoomResponse struct {
	ID          string     `json:"id"`
	GroupID     string     `json:"groupId"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Access      string     `json:"access"`
	Capacity    *int       `json:"capacity,omitempty"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	// Only populated on single-room reads / lists with detail:
	GroupName  string     `json:"groupName,omitempty"`
	MyRoomRole RoomRole   `json:"myRoomRole,omitempty"`
	CanEnter   bool       `json:"canEnter,omitempty"`
}

type RoomMemberResponse struct {
	UserID      string    `json:"userId"`
	DisplayName string    `json:"displayName"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	JoinedAt    time.Time `json:"joinedAt"`
}

// --- mapping ---

func toRoomResponse(r *Room) RoomResponse {
	return RoomResponse{
		ID:          r.ID,
		GroupID:     r.GroupID,
		Name:        r.Name,
		Description: r.Description,
		Access:      r.Access,
		Capacity:    r.Capacity,
		ArchivedAt:  r.ArchivedAt,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func toRoomDetailResponse(d *RoomDetail) RoomResponse {
	out := toRoomResponse(&d.Room)
	out.GroupName = d.GroupName
	out.MyRoomRole = d.MyRoomRole
	out.CanEnter = d.CanEnter
	return out
}

func toRoomMemberResponse(m RoomMemberSummary) RoomMemberResponse {
	return RoomMemberResponse{
		UserID:      m.UserID,
		DisplayName: m.DisplayName,
		Email:       m.Email,
		Role:        m.Role.String(),
		Status:      m.Status,
		JoinedAt:    m.JoinedAt,
	}
}