// internal/notes/model.go
package notes

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
)

const (
	VisibilityPrivate = "private"
	VisibilityGroup   = "group"
)

// Note is personal by default. GroupID/MaterialID/SessionID attach context;
// Visibility "group" additionally shares it with the group's active members.
type Note struct {
	ID         string
	UserID     string
	GroupID    *string
	MaterialID *string
	SessionID  *string
	Title      string
	Body       string
	Visibility string
	Pinned     bool
	Tags       []string
	AuthorName *string // filled by queries that join users
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListFilter drives both "my notes" and "group notes" listings.
type ListFilter struct {
	UserID     string // owner, for "my notes"
	GroupID    string
	MaterialID string
	SessionID  string
	Query      string // case-insensitive substring over title + body
	PinnedOnly bool
	Limit      int
	Offset     int
}