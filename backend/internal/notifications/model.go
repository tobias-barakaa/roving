// internal/notifications/model.go
package notifications

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
)

// Notification kinds. Add new ones here so they show up in preferences.
const (
	KindSessionReminder = "session_reminder"
	KindMaterialAdded   = "material_added"
)

var KnownKinds = []string{KindSessionReminder, KindMaterialAdded}

func IsKnownKind(k string) bool {
	for _, known := range KnownKinds {
		if known == k {
			return true
		}
	}
	return false
}

type Notification struct {
	ID        string
	UserID    string
	GroupID   *string
	Kind      string
	Title     string
	Body      string
	Data      json.RawMessage
	ReadAt    *time.Time
	CreatedAt time.Time
}

// NewNotification is what producers hand to the service.
// DedupeKey makes a producer idempotent: the same key never creates a second
// row for the same user.
type NewNotification struct {
	GroupID   *string
	Kind      string
	Title     string
	Body      string
	Data      map[string]any
	DedupeKey string
}

type ListFilter struct {
	UserID     string
	UnreadOnly bool
	Before     *time.Time
	Limit      int
}

type Preference struct {
	Kind  string
	InApp bool
}