// internal/scheduling/dto.go
package scheduling

import "time"

// --- series ---

type CreateSeriesRequest struct {
	RoomID          *string `json:"roomId"          binding:"omitempty,uuid"`
	Title           string  `json:"title"           binding:"required,min=1,max=150"`
	Description     string  `json:"description"     binding:"max=2000"`
	Timezone        string  `json:"timezone"        binding:"required"`
	RecurrenceRule  string  `json:"recurrenceRule"  binding:"required,max=500"`
	LocalStartTime  string  `json:"localStartTime"  binding:"required"` // "HH:MM" or "HH:MM:SS"
	DurationSeconds int     `json:"durationSeconds" binding:"required,min=60,max=86400"`
	StartsOn        string  `json:"startsOn"        binding:"required"` // "YYYY-MM-DD"
	EndsOn          *string `json:"endsOn"          binding:"omitempty"` // "YYYY-MM-DD"
}

type 








struct {
	RoomID          *string `json:"roomId"          binding:"omitempty,uuid"`
	Title           *string `json:"title"           binding:"omitempty,min=1,max=150"`
	Description     *string `json:"description"     binding:"omitempty,max=2000"`
	LocalStartTime  *string `json:"localStartTime"`
	DurationSeconds *int    `json:"durationSeconds" binding:"omitempty,min=60,max=86400"`
}

type SeriesResponse struct {
	ID              string     `json:"id"`
	GroupID         string     `json:"groupId"`
	RoomID          *string    `json:"roomId,omitempty"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Timezone        string     `json:"timezone"`
	RecurrenceRule  string     `json:"recurrenceRule"`
	LocalStartTime  string     `json:"localStartTime"`
	DurationSeconds int        `json:"durationSeconds"`
	StartsOn        string     `json:"startsOn"`
	EndsOn          *string    `json:"endsOn,omitempty"`
	Status          string     `json:"status"`
	GeneratedUntil  *time.Time `json:"generatedUntil,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// --- sessions ---

type CreateSessionRequest struct {
	RoomID      *string `json:"roomId"      binding:"omitempty,uuid"`
	Title       string  `json:"title"       binding:"required,min=1,max=150"`
	Description string  `json:"description" binding:"max=2000"`
	StartsAt    string  `json:"startsAt"    binding:"required"` // RFC 3339
	EndsAt      string  `json:"endsAt"      binding:"required"`
}

type UpdateSessionRequest struct {
	Title       *string `json:"title"       binding:"omitempty,min=1,max=150"`
	Description *string `json:"description" binding:"omitempty,max=2000"`
	StartsAt    *string `json:"startsAt"`
	EndsAt      *string `json:"endsAt"`
	RoomID      *string `json:"roomId"      binding:"omitempty,uuid"`
}

type SessionResponse struct {
	ID          string     `json:"id"`
	GroupID     string     `json:"groupId"`
	RoomID      *string    `json:"roomId,omitempty"`
	RoomName    *string    `json:"roomName,omitempty"`
	SeriesID    *string    `json:"seriesId,omitempty"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      time.Time  `json:"endsAt"`
	Status      string     `json:"status"`
	CancelledAt *time.Time `json:"cancelledAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	MyRole      Role       `json:"myRole,omitempty"`
}

func toSeriesResponse(s *Series) SeriesResponse {
	r := SeriesResponse{
		ID:              s.ID,
		GroupID:         s.GroupID,
		RoomID:          s.RoomID,
		Title:           s.Title,
		Description:     s.Description,
		Timezone:        s.Timezone,
		RecurrenceRule:  s.RecurrenceRule,
		LocalStartTime:  s.LocalStartTime,
		DurationSeconds: s.DurationSeconds,
		StartsOn:        s.StartsOn.Format("2006-01-02"),
		Status:          s.Status,
		GeneratedUntil:  s.GeneratedUntil,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
	if s.EndsOn != nil {
		ds := s.EndsOn.Format("2006-01-02")
		r.EndsOn = &ds
	}
	return r
}

func toSessionResponse(d *SessionDetail) SessionResponse {
	return SessionResponse{
		ID:          d.ID,
		GroupID:     d.GroupID,
		RoomID:      d.RoomID,
		RoomName:    d.RoomName,
		SeriesID:    d.SeriesID,
		Title:       d.Title,
		Description: d.Description,
		StartsAt:    d.StartsAt,
		EndsAt:      d.EndsAt,
		Status:      d.Status,
		CancelledAt: d.CancelledAt,
		CreatedAt:   d.CreatedAt,
		MyRole:      d.MyRole,
	}
}