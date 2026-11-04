// internal/notifications/dto.go
package notifications

import (
	"encoding/json"
	"time"
)

type NotificationResponse struct {
	ID        string          `json:"id"`
	GroupID   *string         `json:"groupId,omitempty"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Data      json.RawMessage `json:"data"`
	ReadAt    *time.Time      `json:"readAt,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type PreferenceResponse struct {
	Kind  string `json:"kind"`
	InApp bool   `json:"inApp"`
}

type SetPreferenceRequest struct {
	InApp *bool `json:"inApp" binding:"required"`
}

func toResponse(n *Notification) NotificationResponse {
	data := n.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	return NotificationResponse{
		ID:        n.ID,
		GroupID:   n.GroupID,
		Kind:      n.Kind,
		Title:     n.Title,
		Body:      n.Body,
		Data:      data,
		ReadAt:    n.ReadAt,
		CreatedAt: n.CreatedAt,
	}
}