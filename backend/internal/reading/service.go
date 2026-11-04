// internal/reading/service.go
package reading

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Notifier is satisfied by *notifications.Service; declared here so the two
// packages never import each other.
type Notifier interface {
	NotifyGroup(ctx context.Context, groupID, exceptUserID, kind, title, body string,
		data map[string]any, dedupeKey string) error
}

type Service struct {
	repo   *Repository
	db     *pgxpool.Pool
	notify Notifier // may be nil
}

func NewService(repo *Repository, db *pgxpool.Pool, notify Notifier) *Service {
	return &Service{repo: repo, db: db, notify: notify}
}

// ==================== materials ====================

func (s *Service) CreateMaterial(ctx context.Context, actorID, groupID string, req CreateMaterialRequest) (*Material, error) {
	if req.URL != nil && !isHTTPURL(*req.URL) {
		return nil, fmt.Errorf("%w: url must start with http:// or https://", ErrInvalid)
	}
	m := &Material{
		GroupID:     groupID,
		AddedBy:     actorID,
		Title:       strings.TrimSpace(req.Title),
		Kind:        defaultStr(req.Kind, "other"),
		Author:      strings.TrimSpace(req.Author),
		URL:         req.URL,
		Description: req.Description,
		Unit:        defaultStr(req.Unit, "pages"),
		TotalUnits:  req.TotalUnits,
	}
	if m.Title == "" {
		return nil, fmt.Errorf("%w: title is required", ErrInvalid)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireGroupRole(ctx, tx, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	if err := s.repo.InsertMaterial(ctx, tx, m); err != nil {
		return nil, mapDBErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Best effort: a notification failure must never fail the request.
	if s.notify != nil {
		if err := s.notify.NotifyGroup(ctx, groupID, actorID, "material_added",
			"New reading: "+m.Title, "",
			map[string]any{"groupId": groupID, "materialId": m.ID},
			"material_added:"+m.ID); err != nil {
			log.Printf("reading: notify material_added: %v", err)
		}
	}
	return m, nil
}

func (s *Service) GetMaterial(ctx context.Context, actorID, groupID, materialID string) (*MaterialView, error) {
	if _, err := s.requireGroupRole(ctx, s.db, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	if _, err := s.loadMaterial(ctx, s.db, groupID, materialID); err != nil {
		return nil, err
	}
	return s.repo.GetMaterialView(ctx, s.db, materialID, actorID)
}

func (s *Service) ListMaterials(ctx context.Context, actorID, groupID string, includeArchived bool, limit, offset int) ([]MaterialView, error) {
	if _, err := s.requireGroupRole(ctx, s.db, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListMaterials(ctx, s.db, groupID, actorID, includeArchived, normalizeLimit(limit), offset)
}

func (s *Service) UpdateMaterial(ctx context.Context, actorID, groupID, materialID string, req UpdateMaterialRequest) error {
	if req.URL != nil && !isHTTPURL(*req.URL) {
		return fmt.Errorf("%w: url must start with http:// or https://", ErrInvalid)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	m, err := s.authorizeMaterialEdit(ctx, tx, actorID, groupID, materialID)
	if err != nil {
		return err
	}
	if req.TotalUnits != nil {
		max, err := s.repo.MaxCurrentUnit(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		if *req.TotalUnits < max {
			return fmt.Errorf("%w: totalUnits cannot be below the furthest reader (%d)", ErrInvalid, max)
		}
	}
	f := UpdateMaterialFields{
		Title:       trimPtr(req.Title),
		Author:      trimPtr(req.Author),
		URL:         req.URL,
		Description: req.Description,
		TotalUnits:  req.TotalUnits,
	}
	if f.Title != nil && *f.Title == "" {
		return fmt.Errorf("%w: title cannot be empty", ErrInvalid)
	}
	if err := s.repo.UpdateMaterial(ctx, tx, materialID, f); err != nil {
		return mapDBErr(err)
	}
	return tx.Commit(ctx)
}

// ArchiveMaterial hides a material from the default list. Progress is kept.
func (s *Service) ArchiveMaterial(ctx context.Context, actorID, groupID, materialID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := s.authorizeMaterialEdit(ctx, tx, actorID, groupID, materialID); err != nil {
		return err
	}
	if err := s.repo.ArchiveMaterial(ctx, tx, materialID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ==================== progress ====================

func (s *Service) UpdateProgress(ctx context.Context, actorID, groupID, materialID string, req UpdateProgressRequest) (*Progress, error) {
	if req.CurrentUnit == nil && req.Status == nil {
		return nil, fmt.Errorf("%w: send currentUnit and/or status", ErrInvalid)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireGroupRole(ctx, tx, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	m, err := s.loadMaterial(ctx, tx, groupID, materialID)
	if err != nil {
		return nil, err
	}
	if m.ArchivedAt != nil {
		return nil, fmt.Errorf("%w: material is archived", ErrInvalid)
	}

	cur, err := s.repo.GetProgress(ctx, tx, materialID, actorID)
	if err != nil {
		return nil, err
	}

	unit, status := 0, "not_started"
	if cur != nil {
		unit, status = cur.CurrentUnit, cur.Status
	}
	if req.CurrentUnit != nil {
		unit = *req.CurrentUnit
		if m.TotalUnits != nil && unit > *m.TotalUnits {
			return nil, fmt.Errorf("%w: currentUnit exceeds totalUnits (%d)", ErrInvalid, *m.TotalUnits)
		}
	}

	if req.Status != nil {
		status = *req.Status
		switch status {
		case "finished":
			if m.TotalUnits != nil {
				unit = *m.TotalUnits
			}
		case "not_started":
			unit = 0
		}
	} else {
		switch {
		case m.TotalUnits != nil && unit >= *m.TotalUnits:
			status = "finished"
		case unit > 0:
			status = "reading"
		default:
			status = "not_started"
		}
	}

	now := time.Now().UTC()
	p := &Progress{MaterialID: materialID, UserID: actorID, CurrentUnit: unit, Status: status}
	if status != "not_started" {
		if cur != nil && cur.StartedAt != nil {
			p.StartedAt = cur.StartedAt
		} else {
			p.StartedAt = &now
		}
	}
	if status == "finished" {
		if cur != nil && cur.FinishedAt != nil {
			p.FinishedAt = cur.FinishedAt
		} else {
			p.FinishedAt = &now
		}
	}

	if err := s.repo.UpsertProgress(ctx, tx, groupID, p); err != nil {
		return nil, mapDBErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) GetMyProgress(ctx context.Context, actorID, groupID, materialID string) (*Progress, error) {
	if _, err := s.requireGroupRole(ctx, s.db, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	if _, err := s.loadMaterial(ctx, s.db, groupID, materialID); err != nil {
		return nil, err
	}
	p, err := s.repo.GetProgress(ctx, s.db, materialID, actorID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = &Progress{MaterialID: materialID, UserID: actorID, Status: "not_started"}
	}
	return p, nil
}

func (s *Service) Roster(ctx context.Context, actorID, groupID, materialID string) ([]RosterEntry, error) {
	if _, err := s.requireGroupRole(ctx, s.db, groupID, actorID, RoleMember); err != nil {
		return nil, err
	}
	if _, err := s.loadMaterial(ctx, s.db, groupID, materialID); err != nil {
		return nil, err
	}
	return s.repo.ListRoster(ctx, s.db, materialID)
}

// ==================== gates & helpers ====================

// loadMaterial returns the material only if it belongs to groupID.
func (s *Service) loadMaterial(ctx context.Context, ex pgxExecutor, groupID, materialID string) (*Material, error) {
	if !uuidRe.MatchString(materialID) {
		return nil, ErrNotFound
	}
	m, err := s.repo.GetMaterial(ctx, ex, materialID)
	if err != nil {
		return nil, err
	}
	if m.GroupID != groupID {
		return nil, ErrNotFound
	}
	return m, nil
}

// authorizeMaterialEdit: the member who added it, or a moderator and above.
func (s *Service) authorizeMaterialEdit(ctx context.Context, ex pgxExecutor, actorID, groupID, materialID string) (*Material, error) {
	role, err := s.requireGroupRole(ctx, ex, groupID, actorID, RoleMember)
	if err != nil {
		return nil, err
	}
	m, err := s.loadMaterial(ctx, ex, groupID, materialID)
	if err != nil {
		return nil, err
	}
	if m.AddedBy != actorID && role < RoleModerator {
		return nil, ErrForbidden
	}
	return m, nil
}

// requireGroupRole mirrors scheduling.requireGroupRole. Non-member → NotFound
// (anti-enumeration). Non-active or role too low → Forbidden.
func (s *Service) requireGroupRole(ctx context.Context, ex pgxExecutor, groupID, userID string, need Role) (Role, error) {
	if !uuidRe.MatchString(groupID) {
		return RoleNone, ErrNotFound
	}
	var roleStr, status string
	err := ex.QueryRow(ctx,
		`SELECT role::text, status
		   FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid
		  FOR SHARE`,
		groupID, userID).Scan(&roleStr, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleNone, ErrNotFound
	}
	if err != nil {
		return RoleNone, err
	}
	if status != "active" {
		return RoleNone, ErrForbidden
	}
	r := ParseRole(roleStr)
	if r < need {
		return RoleNone, ErrForbidden
	}
	return r, nil
}

func mapDBErr(err error) error {
	if IsCheckViolation(err) || IsForeignKeyViolation(err) {
		return fmt.Errorf("%w: %v", ErrInvalid, "value rejected by database constraints")
	}
	return err
}

func isHTTPURL(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	return &t
}