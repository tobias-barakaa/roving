package groups

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"study-platform/backend/internal/database"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidInput  = errors.New("invalid input")
	ErrConflict      = errors.New("conflict")
	ErrGroupArchived = errors.New("group is archived")
	ErrLastOwner     = errors.New("cannot remove the last owner")
	ErrSlugTaken     = errors.New("a group with this slug already exists")
)

// wrappedError carries a user-facing message while preserving errors.Is for
// the underlying sentinel.
type wrappedError struct {
	cause error
	msg   string
}

func (e *wrappedError) Error() string { return e.msg }
func (e *wrappedError) Unwrap() error { return e.cause }

func wrap(cause error, format string, args ...any) error {
	return &wrappedError{cause: cause, msg: fmt.Sprintf(format, args...)}
}

// Pauser pauses a group's scheduling activity inside the archive transaction.
// Implemented by the scheduling module; a no-op until then.
type Pauser interface {
	PauseGroupActivity(ctx context.Context, tx pgx.Tx, groupID string) error
}

type Service struct {
	db     *pgxpool.Pool
	repo   *Repository
	pauser Pauser
}

func NewService(db *pgxpool.Pool, pauser Pauser) *Service {
	return &Service{db: db, repo: NewRepository(), pauser: pauser}
}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func validateName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" || len([]rune(n)) > 100 {
		return wrap(ErrInvalidInput, "name must be between 1 and 100 characters")
	}
	return nil
}

func normalizeAndValidateSlug(slug string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(slug))
	if len(s) < 3 || len(s) > 60 || !slugRe.MatchString(s) {
		return "", wrap(ErrInvalidInput, "slug must be 3-60 lowercase letters, numbers, and single hyphens")
	}
	return s, nil
}

func validateDescription(d string) error {
	if len([]rune(d)) > 2000 {
		return wrap(ErrInvalidInput, "description must be at most 2000 characters")
	}
	return nil
}

func validateVisibility(v string) (string, error) {
	if v == "" {
		return "private", nil
	}
	if v != "private" && v != "public" {
		return "", wrap(ErrInvalidInput, "visibility must be private or public")
	}
	return v, nil
}

func (s *Service) Create(ctx context.Context, userID, name, slug, description, visibility string) (*Group, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	normSlug, err := normalizeAndValidateSlug(slug)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(description); err != nil {
		return nil, err
	}
	vis, err := validateVisibility(visibility)
	if err != nil {
		return nil, err
	}

	var g *Group
	err = database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		g, err = s.repo.CreateGroup(ctx, tx, userID, strings.TrimSpace(name), normSlug, description, vis)
		if err != nil {
			return err
		}
		return s.repo.AddMembership(ctx, tx, g.ID, userID, "owner")
	})
	if err != nil {
		return nil, mapPgErr(err)
	}
	return g, nil
}

func (s *Service) Get(ctx context.Context, userID, groupID string) (*Group, error) {
	g, err := s.repo.GetByID(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	role, err := s.repo.ActiveRole(ctx, s.db, groupID, userID)
	if err != nil {
		return nil, err
	}
	if role != "" || (g.Visibility == "public" && g.ArchivedAt == nil) {
		return g, nil
	}
	return nil, ErrNotFound
}

func (s *Service) Update(ctx context.Context, userID, groupID string, name, description, visibility *string) (*Group, error) {
	g, err := s.repo.GetByID(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	role, err := s.requireMember(ctx, s.db, groupID, userID)
	if err != nil {
		return nil, err
	}
	if role != "owner" && role != "admin" {
		return nil, ErrForbidden
	}
	if name != nil {
		if err := validateName(*name); err != nil {
			return nil, err
		}
	}
	if description != nil {
		if err := validateDescription(*description); err != nil {
			return nil, err
		}
	}

	newName := g.Name
	if name != nil {
		newName = strings.TrimSpace(*name)
	}
	newDesc := g.Description
	if description != nil {
		newDesc = *description
	}
	newVis := g.Visibility
	if visibility != nil {
		v, err := validateVisibility(*visibility)
		if err != nil {
			return nil, err
		}
		newVis = v
	}

	updated, err := s.repo.Update(ctx, s.db, groupID, newName, newDesc, newVis)
	if err != nil {
		return nil, mapPgErr(err)
	}
	return updated, nil
}

func (s *Service) Archive(ctx context.Context, userID, groupID string) error {
	g, err := s.repo.GetByID(ctx, s.db, groupID)
	if err != nil {
		return err
	}
	role, err := s.requireMember(ctx, s.db, groupID, userID)
	if err != nil {
		return err
	}
	if role != "owner" {
		return ErrForbidden
	}
	if g.ArchivedAt != nil {
		return ErrGroupArchived
	}
	return mapPgErr(database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.repo.Archive(ctx, tx, groupID); err != nil {
			return err
		}
		if s.pauser != nil {
			return s.pauser.PauseGroupActivity(ctx, tx, groupID)
		}
		return nil
	}))
}

func (s *Service) Unarchive(ctx context.Context, userID, groupID string) error {
	g, err := s.repo.GetByID(ctx, s.db, groupID)
	if err != nil {
		return err
	}
	role, err := s.requireMember(ctx, s.db, groupID, userID)
	if err != nil {
		return err
	}
	if role != "owner" {
		return ErrForbidden
	}
	if g.ArchivedAt == nil {
		return ErrConflict
	}
	return mapPgErr(database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		return s.repo.Unarchive(ctx, tx, groupID)
	}))
}

func (s *Service) ListMyGroups(ctx context.Context, userID string, limit int, cursor string) ([]Group, string, error) {
	return s.repo.ListForUser(ctx, s.db, userID, limit, cursor)
}

func (s *Service) Discover(ctx context.Context, limit int, cursor string) ([]Group, string, error) {
	return s.repo.ListDiscover(ctx, s.db, limit, cursor)
}

func (s *Service) ListMembers(ctx context.Context, userID, groupID string, limit int, cursor string) ([]Member, string, error) {
	if _, err := s.repo.GetByID(ctx, s.db, groupID); err != nil {
		return nil, "", err
	}
	if _, err := s.requireMember(ctx, s.db, groupID, userID); err != nil {
		return nil, "", err
	}
	return s.repo.ListMembers(ctx, s.db, groupID, limit, cursor)
}

func (s *Service) ChangeRole(ctx context.Context, userID, groupID, targetID, newRole string) (*Member, error) {
	if newRole != "owner" && newRole != "admin" && newRole != "member" {
		return nil, wrap(ErrInvalidInput, "role must be owner, admin, or member")
	}
	g, err := s.repo.GetByID(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	role, err := s.requireMember(ctx, s.db, groupID, userID)
	if err != nil {
		return nil, err
	}
	if role != "owner" {
		return nil, ErrForbidden
	}
	if g.ArchivedAt != nil {
		return nil, ErrGroupArchived
	}

	target, err := s.repo.GetMembership(ctx, s.db, groupID, targetID)
	if err != nil {
		return nil, err
	}
	if target.Status != "active" {
		return nil, ErrNotFound
	}

	switch newRole {
	case "owner":
		if target.Role == "owner" {
			// Already owner: idempotent.
			return s.repo.UpdateRole(ctx, s.db, groupID, targetID, "owner")
		}
		// Ownership transfer: demote the current owner, then promote the
		// target, in one transaction. The non-deferrable partial unique index
		// (one owner per group) requires demote-first ordering.
		var updated *Member
		err := database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
			if err := s.repo.LockGroup(ctx, tx, groupID); err != nil {
				return err
			}
			if _, err := s.repo.UpdateRole(ctx, tx, groupID, userID, "admin"); err != nil {
				return err
			}
			m, err := s.repo.UpdateRole(ctx, tx, groupID, targetID, "owner")
			if err != nil {
				return err
			}
			updated = m
			return nil
		})
		if err != nil {
			return nil, mapPgErr(err)
		}
		return updated, nil
	default: // admin or member
		if target.Role == "owner" {
			return nil, ErrLastOwner
		}
		return s.repo.UpdateRole(ctx, s.db, groupID, targetID, newRole)
	}
}

func (s *Service) RemoveMember(ctx context.Context, userID, groupID, targetID string) error {
	if _, err := s.repo.GetByID(ctx, s.db, groupID); err != nil {
		return err
	}
	role, err := s.requireMember(ctx, s.db, groupID, userID)
	if err != nil {
		return err
	}
	if role != "owner" && role != "admin" {
		return ErrForbidden
	}
	target, err := s.repo.GetMembership(ctx, s.db, groupID, targetID)
	if err != nil {
		return err
	}
	if target.Role == "owner" {
		return ErrLastOwner
	}
	return s.repo.DeleteMembership(ctx, s.db, groupID, targetID)
}

func (s *Service) Leave(ctx context.Context, userID, groupID string) error {
	if _, err := s.repo.GetByID(ctx, s.db, groupID); err != nil {
		return err
	}
	role, err := s.requireMember(ctx, s.db, groupID, userID)
	if err != nil {
		return err
	}
	if role == "owner" {
		return ErrLastOwner
	}
	return s.repo.DeleteMembership(ctx, s.db, groupID, userID)
}

// requireMember returns the caller's role if they are an active member, or
// ErrNotFound otherwise (so non-members cannot distinguish "missing" from
// "exists but you may not see it").
func (s *Service) requireMember(ctx context.Context, db database.DBTX, groupID, userID string) (string, error) {
	role, err := s.repo.ActiveRole(ctx, db, groupID, userID)
	if err != nil {
		return "", err
	}
	if role == "" {
		return "", ErrNotFound
	}
	return role, nil
}
