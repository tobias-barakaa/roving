// internal/notifications/service.go
package notifications

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ReminderLead is how long before a session starts its reminder is created.
const ReminderLead = 15 * time.Minute

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
}

func NewService(repo *Repository, db *pgxpool.Pool) *Service {
	return &Service{repo: repo, db: db}
}

// ==================== producing (used by other modules) ====================

// Notify sends one notification to the given users.
func (s *Service) Notify(ctx context.Context, userIDs []string, n NewNotification) error {
	if !IsKnownKind(n.Kind) {
		return ErrInvalid
	}
	n.Title = truncate(strings.TrimSpace(n.Title), 200)
	n.Body = truncate(n.Body, 2000)
	if n.Title == "" {
		return ErrInvalid
	}
	_, err := s.repo.Insert(ctx, s.db, userIDs, n)
	return err
}

// NotifyGroup sends to every active member of a group except exceptUserID
// (normally the actor who caused the event). The flat signature lets other
// packages declare a tiny interface without importing this one.
func (s *Service) NotifyGroup(ctx context.Context, groupID, exceptUserID, kind, title, body string,
	data map[string]any, dedupeKey string) error {
	if !uuidRe.MatchString(groupID) {
		return ErrInvalid
	}
	if exceptUserID != "" && !uuidRe.MatchString(exceptUserID) {
		return ErrInvalid
	}
	ids, err := s.repo.GroupMemberIDs(ctx, s.db, groupID, exceptUserID)
	if err != nil {
		return err
	}
	g := groupID
	return s.Notify(ctx, ids, NewNotification{
		GroupID:   &g,
		Kind:      kind,
		Title:     title,
		Body:      body,
		Data:      data,
		DedupeKey: dedupeKey,
	})
}

// ==================== inbox ====================

// List returns a page of the user's notifications, newest first. nextBefore is
// non-nil when more exist: pass it back as `before` to get the next page.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Notification, *time.Time, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	f.Limit = limit + 1
	rows, err := s.repo.List(ctx, s.db, f)
	if err != nil {
		return nil, nil, err
	}
	var next *time.Time
	if len(rows) > limit {
		rows = rows[:limit]
		t := rows[len(rows)-1].CreatedAt
		next = &t
	}
	return rows, next, nil
}

func (s *Service) UnreadCount(ctx context.Context, userID string) (int, error) {
	return s.repo.UnreadCount(ctx, s.db, userID)
}

func (s *Service) MarkRead(ctx context.Context, userID, id string) error {
	if !uuidRe.MatchString(id) {
		return ErrNotFound
	}
	return s.repo.MarkRead(ctx, s.db, id, userID)
}

func (s *Service) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	return s.repo.MarkAllRead(ctx, s.db, userID)
}

// ==================== preferences ====================

// Preferences returns every known kind, defaulting to enabled.
func (s *Service) Preferences(ctx context.Context, userID string) ([]Preference, error) {
	stored, err := s.repo.ListPreferences(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	byKind := make(map[string]bool, len(stored))
	for _, p := range stored {
		byKind[p.Kind] = p.InApp
	}
	out := make([]Preference, 0, len(KnownKinds))
	for _, k := range KnownKinds {
		on, ok := byKind[k]
		if !ok {
			on = true
		}
		out = append(out, Preference{Kind: k, InApp: on})
	}
	return out, nil
}

func (s *Service) SetPreference(ctx context.Context, userID, kind string, inApp bool) error {
	if !IsKnownKind(kind) {
		return ErrInvalid
	}
	return s.repo.SetPreference(ctx, s.db, userID, kind, inApp)
}

// ==================== background worker ====================

// RunWorker creates session reminders every tick and purges old rows hourly.
// Call as: go svc.RunWorker(ctx, time.Minute)
func (s *Service) RunWorker(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	var lastPurge time.Time
	for {
		s.runOnce(ctx, &lastPurge)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) runOnce(ctx context.Context, lastPurge *time.Time) {
	if _, err := s.repo.EnqueueSessionReminders(ctx, s.db, ReminderLead.Seconds()); err != nil {
		log.Printf("notifications: session reminders: %v", err)
	}
	if time.Since(*lastPurge) >= time.Hour {
		if _, err := s.repo.Purge(ctx, s.db); err != nil {
			log.Printf("notifications: purge: %v", err)
		}
		*lastPurge = time.Now()
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}