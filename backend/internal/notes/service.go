// internal/notes/service.go
package notes

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
}

func NewService(repo *Repository, db *pgxpool.Pool) *Service {
	return &Service{repo: repo, db: db}
}

// ==================== create ====================

func (s *Service) Create(ctx context.Context, actorID string, req CreateNoteRequest) (*Note, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" && strings.TrimSpace(req.Body) == "" {
		return nil, fmt.Errorf("%w: a note needs a title or a body", ErrInvalid)
	}
	vis := req.Visibility
	if vis == "" {
		vis = VisibilityPrivate
	}
	tags, err := normalizeTags(req.Tags)
	if err != nil {
		return nil, err
	}

	if (req.MaterialID != nil || req.SessionID != nil) && req.GroupID == nil {
		return nil, fmt.Errorf("%w: materialId and sessionId require groupId", ErrInvalid)
	}
	if vis == VisibilityGroup && req.GroupID == nil {
		return nil, fmt.Errorf("%w: sharing with a group requires groupId", ErrInvalid)
	}

	if req.GroupID != nil {
		groupID := *req.GroupID
		if !uuidRe.MatchString(groupID) {
			return nil, ErrInvalid
		}
		ok, err := s.repo.IsActiveMember(ctx, s.db, groupID, actorID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotFound // anti-enumeration
		}
		if req.MaterialID != nil {
			ok, err := s.repo.MaterialInGroup(ctx, s.db, *req.MaterialID, groupID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%w: material does not belong to this group", ErrInvalid)
			}
		}
		if req.SessionID != nil {
			ok, err := s.repo.SessionInGroup(ctx, s.db, *req.SessionID, groupID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%w: session does not belong to this group", ErrInvalid)
			}
		}
	}

	n := &Note{
		UserID:     actorID,
		GroupID:    req.GroupID,
		MaterialID: req.MaterialID,
		SessionID:  req.SessionID,
		Title:      title,
		Body:       req.Body,
		Visibility: vis,
		Pinned:     req.Pinned,
		Tags:       tags,
	}
	if err := s.repo.Insert(ctx, s.db, n); err != nil {
		return nil, mapDBErr(err)
	}
	return n, nil
}

// ==================== read ====================

// Get returns the note to its owner, or to active members of its group when
// it is group-visible and its author is still an active member.
// Anyone else gets NotFound.
func (s *Service) Get(ctx context.Context, actorID, noteID string) (*Note, error) {
	if !uuidRe.MatchString(noteID) {
		return nil, ErrNotFound
	}
	n, err := s.repo.Get(ctx, s.db, noteID)
	if err != nil {
		return nil, err
	}
	if n.UserID == actorID {
		return n, nil
	}
	if n.Visibility != VisibilityGroup || n.GroupID == nil {
		return nil, ErrNotFound
	}
	viewerOK, err := s.repo.IsActiveMember(ctx, s.db, *n.GroupID, actorID)
	if err != nil {
		return nil, err
	}
	authorOK, err := s.repo.IsActiveMember(ctx, s.db, *n.GroupID, n.UserID)
	if err != nil {
		return nil, err
	}
	if !viewerOK || !authorOK {
		return nil, ErrNotFound
	}
	return n, nil
}

// ListMine lists the caller's own notes.
func (s *Service) ListMine(ctx context.Context, actorID string, f ListFilter) ([]Note, error) {
	f.UserID = actorID
	if err := validateFilter(&f); err != nil {
		return nil, err
	}
	return s.repo.ListMine(ctx, s.db, f)
}

// ListShared lists notes members have shared with the group.
func (s *Service) ListShared(ctx context.Context, actorID string, f ListFilter) ([]Note, error) {
	if !uuidRe.MatchString(f.GroupID) {
		return nil, ErrNotFound
	}
	ok, err := s.repo.IsActiveMember(ctx, s.db, f.GroupID, actorID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if err := validateFilter(&f); err != nil {
		return nil, err
	}
	return s.repo.ListShared(ctx, s.db, f)
}

// ==================== update / delete (owner only) ====================

func (s *Service) Update(ctx context.Context, actorID, noteID string, req UpdateNoteRequest) error {
	if !uuidRe.MatchString(noteID) {
		return ErrNotFound
	}
	n, err := s.repo.Get(ctx, s.db, noteID)
	if err != nil {
		return err
	}
	if n.UserID != actorID {
		return ErrNotFound // others cannot even tell it exists
	}

	f := UpdateFields{
		Title:      trimPtr(req.Title),
		Body:       req.Body,
		Visibility: req.Visibility,
		Pinned:     req.Pinned,
	}
	if req.Tags != nil {
		tags, err := normalizeTags(*req.Tags)
		if err != nil {
			return err
		}
		f.Tags = tags
	}
	if f.Visibility != nil && *f.Visibility == VisibilityGroup && n.GroupID == nil {
		return fmt.Errorf("%w: only notes attached to a group can be shared", ErrInvalid)
	}

	// The result must still have some content.
	title, body := n.Title, n.Body
	if f.Title != nil {
		title = *f.Title
	}
	if f.Body != nil {
		body = *f.Body
	}
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: a note needs a title or a body", ErrInvalid)
	}

	if err := s.repo.Update(ctx, s.db, noteID, actorID, f); err != nil {
		return mapDBErr(err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, actorID, noteID string) error {
	if !uuidRe.MatchString(noteID) {
		return ErrNotFound
	}
	return s.repo.Delete(ctx, s.db, noteID, actorID)
}

// ==================== helpers ====================

func validateFilter(f *ListFilter) error {
	for _, id := range []string{f.GroupID, f.MaterialID, f.SessionID} {
		if id != "" && !uuidRe.MatchString(id) {
			return fmt.Errorf("%w: malformed id filter", ErrInvalid)
		}
	}
	f.Query = strings.TrimSpace(f.Query)
	if len([]rune(f.Query)) > 100 {
		return fmt.Errorf("%w: search text is too long", ErrInvalid)
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return nil
}

// normalizeTags lowercases, trims, dedupes, and bounds tags. Never returns nil.
func normalizeTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if len([]rune(t)) > 30 {
			return nil, fmt.Errorf("%w: tags are limited to 30 characters", ErrInvalid)
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > 10 {
		return nil, fmt.Errorf("%w: at most 10 tags", ErrInvalid)
	}
	return out, nil
}

func mapDBErr(err error) error {
	if IsCheckViolation(err) || IsForeignKeyViolation(err) {
		return fmt.Errorf("%w: value rejected by database constraints", ErrInvalid)
	}
	return err
}

func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	return &t
}