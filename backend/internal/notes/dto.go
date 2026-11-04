// internal/notes/dto.go
package notes

import "time"

type CreateNoteRequest struct {
	GroupID    *string  `json:"groupId"    binding:"omitempty,uuid"`
	MaterialID *string  `json:"materialId" binding:"omitempty,uuid"`
	SessionID  *string  `json:"sessionId"  binding:"omitempty,uuid"`
	Title      string   `json:"title"      binding:"max=200"`
	Body       string   `json:"body"       binding:"max=50000"`
	Visibility string   `json:"visibility" binding:"omitempty,oneof=private group"`
	Pinned     bool     `json:"pinned"`
	Tags       []string `json:"tags"       binding:"omitempty,max=10"`
}

// UpdateNoteRequest: context (group/material/session) is fixed at creation.
type UpdateNoteRequest struct {
	Title      *string   `json:"title"      binding:"omitempty,max=200"`
	Body       *string   `json:"body"       binding:"omitempty,max=50000"`
	Visibility *string   `json:"visibility" binding:"omitempty,oneof=private group"`
	Pinned     *bool     `json:"pinned"`
	Tags       *[]string `json:"tags"       binding:"omitempty,max=10"`
}

type NoteResponse struct {
	ID         string    `json:"id"`
	GroupID    *string   `json:"groupId,omitempty"`
	MaterialID *string   `json:"materialId,omitempty"`
	SessionID  *string   `json:"sessionId,omitempty"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Visibility string    `json:"visibility"`
	Pinned     bool      `json:"pinned"`
	Tags       []string  `json:"tags"`
	AuthorName *string   `json:"authorName,omitempty"`
	IsMine     bool      `json:"isMine"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func toResponse(n *Note, viewerID string) NoteResponse {
	tags := n.Tags
	if tags == nil {
		tags = []string{}
	}
	return NoteResponse{
		ID: n.ID, GroupID: n.GroupID, MaterialID: n.MaterialID, SessionID: n.SessionID,
		Title: n.Title, Body: n.Body, Visibility: n.Visibility, Pinned: n.Pinned,
		Tags: tags, AuthorName: n.AuthorName, IsMine: n.UserID == viewerID,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}
}