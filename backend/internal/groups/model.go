package groups

import "time"

// Group is the domain model for a study group.
type Group struct {
	ID          string
	CreatedBy   string
	Name        string
	Slug        string
	Description string
	Visibility  string
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Membership is a single group_memberships row.
type Membership struct {
	ID        string
	GroupID   string
	UserID    string
	Role      string
	Status    string
	JoinedAt  *time.Time
	CreatedAt time.Time
}

// Member is a membership joined with the user's email, used for listings.
type Member struct {
	UserID   string
	Email    string
	Role     string
	Status   string
	JoinedAt *time.Time
}
