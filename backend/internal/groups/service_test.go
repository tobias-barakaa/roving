package groups

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testDB connects to a real PostgreSQL test database. It skips unless
// TEST_DB_URL is set. The test role must be able to TRUNCATE (owner/migrator).
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set; skipping database-backed tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Every table references users, so CASCADE gives a clean slate.
	if _, err := pool.Exec(context.Background(), `TRUNCATE users CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

var emailSeq int

// newUser inserts a minimal user row (lowercase unique email, dummy hash).
func newUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	emailSeq++
	email := fmt.Sprintf("user-%d-%d@example.com", time.Now().UnixNano(), emailSeq)
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func addMember(t *testing.T, pool *pgxpool.Pool, groupID, userID, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO group_memberships (group_id, user_id, role, status, joined_at)
		 VALUES ($1::uuid, $2::uuid, $3, 'active', now())`, groupID, userID, role); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

func createSeries(t *testing.T, pool *pgxpool.Pool, groupID, userID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO study_session_series (group_id, created_by, title, timezone, recurrence_rule, local_start_time, duration_seconds, starts_on)
		 VALUES ($1::uuid, $2::uuid, 'Study', 'UTC', 'FREQ=WEEKLY;BYDAY=MO', '09:00:00', 3600, CURRENT_DATE)
		 RETURNING id::text`, groupID, userID).Scan(&id); err != nil {
		t.Fatalf("create series: %v", err)
	}
	return id
}

func createSession(t *testing.T, pool *pgxpool.Pool, groupID, userID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO study_sessions (group_id, created_by, title, starts_at, ends_at)
		 VALUES ($1::uuid, $2::uuid, 'Session', now() + interval '1 day', now() + interval '1 day' + interval '1 hour')
		 RETURNING id::text`, groupID, userID).Scan(&id); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id
}

// sqlPauser performs the scheduling updates the real module will eventually do.
type sqlPauser struct{}

func (sqlPauser) PauseGroupActivity(ctx context.Context, tx pgx.Tx, groupID string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE study_session_series SET status = 'paused' WHERE group_id = $1::uuid AND status = 'active'`, groupID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE study_sessions SET status = 'cancelled', cancelled_at = now() WHERE group_id = $1::uuid AND status = 'scheduled' AND starts_at > now()`, groupID)
	return err
}

type failingPauser struct{ err error }

func (p failingPauser) PauseGroupActivity(_ context.Context, _ pgx.Tx, _ string) error {
	return p.err
}

// ---------- create / read ----------

func TestCreateGroupInsertsOwner(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)

	g, err := svc.Create(ctx, owner, "My Group", "my-group", "desc", "private")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var role, status string
	var joinedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT role, status, joined_at FROM group_memberships WHERE group_id = $1::uuid AND user_id = $2::uuid`,
		g.ID, owner).Scan(&role, &status, &joinedAt); err != nil {
		t.Fatalf("query membership: %v", err)
	}
	if role != "owner" || status != "active" || joinedAt == nil {
		t.Fatalf("role=%q status=%q joinedAt=%v", role, status, joinedAt)
	}
}

func TestCreateGroupInvalidSlug(t *testing.T) {
	pool := testDB(t)
	svc := NewService(pool, nil)
	owner := newUser(t, pool)

	if _, err := svc.Create(context.Background(), owner, "G", "Bad Slug!", "", "private"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestCreateGroupDuplicateSlug(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)

	if _, err := svc.Create(ctx, owner, "G", "dup-slug", "", "private"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(ctx, owner, "G", "dup-slug", "", "private"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("expected ErrSlugTaken, got %v", err)
	}
}

func TestGetPrivateNonMemberNotFound(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	outsider := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "private-get", "", "private")

	if _, err := svc.Get(ctx, outsider, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetPublicNonMemberOK(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	outsider := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "public-get", "", "public")

	got, err := svc.Get(ctx, outsider, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != g.ID {
		t.Fatalf("unexpected group")
	}
}

// ---------- update ----------

func TestUpdateMemberForbidden(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	member := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "update-forbidden", "", "private")
	addMember(t, pool, g.ID, member, "member")

	newName := "Changed"
	if _, err := svc.Update(ctx, member, g.ID, &newName, nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestUpdateDoesNotChangeSlug(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "slug-immutable", "", "private")

	newName := "Renamed"
	updated, err := svc.Update(ctx, owner, g.ID, &newName, nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Slug != "slug-immutable" {
		t.Fatalf("slug changed to %q", updated.Slug)
	}
}

// ---------- archive / unarchive ----------

func TestArchivePausesSeriesAndCancelsSessions(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, sqlPauser{})
	owner := newUser(t, pool)

	g, err := svc.Create(ctx, owner, "G", "archive-test", "", "private")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	seriesID := createSeries(t, pool, g.ID, owner)
	sessID := createSession(t, pool, g.ID, owner)

	if err := svc.Archive(ctx, owner, g.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	var seriesStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM study_session_series WHERE id = $1::uuid`, seriesID).Scan(&seriesStatus); err != nil {
		t.Fatal(err)
	}
	if seriesStatus != "paused" {
		t.Fatalf("series status = %q, want paused", seriesStatus)
	}

	var sessStatus string
	var cancelledAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT status, cancelled_at FROM study_sessions WHERE id = $1::uuid`, sessID).Scan(&sessStatus, &cancelledAt); err != nil {
		t.Fatal(err)
	}
	if sessStatus != "cancelled" || cancelledAt == nil {
		t.Fatalf("session status=%q cancelledAt=%v, want cancelled+set", sessStatus, cancelledAt)
	}
}

func TestArchivePauserErrorRollsBack(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, failingPauser{err: errors.New("boom")})
	owner := newUser(t, pool)

	g, err := svc.Create(ctx, owner, "G", "archive-rollback", "", "private")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Archive(ctx, owner, g.ID); err == nil {
		t.Fatalf("expected error from pauser")
	}

	var archivedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT archived_at FROM study_groups WHERE id = $1::uuid`, g.ID).Scan(&archivedAt); err != nil {
		t.Fatal(err)
	}
	if archivedAt != nil {
		t.Fatalf("archive must roll back when the pauser fails")
	}
}

func TestArchiveNonOwnerForbidden(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	member := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "archive-forbidden", "", "private")
	addMember(t, pool, g.ID, member, "member")

	if err := svc.Archive(ctx, member, g.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestUnarchiveRequiresOwner(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "unarchive-test", "", "private")

	if err := svc.Archive(ctx, owner, g.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := svc.Unarchive(ctx, owner, g.ID); err != nil {
		t.Fatalf("unarchive: %v", err)
	}

	var archivedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT archived_at FROM study_groups WHERE id = $1::uuid`, g.ID).Scan(&archivedAt); err != nil {
		t.Fatal(err)
	}
	if archivedAt != nil {
		t.Fatalf("expected unarchived")
	}
}

// ---------- roles and membership ----------

func TestChangeRoleOwnershipTransfer(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	member := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "transfer-test", "", "private")
	addMember(t, pool, g.ID, member, "member")

	if _, err := svc.ChangeRole(ctx, owner, g.ID, member, "owner"); err != nil {
		t.Fatalf("change role: %v", err)
	}

	var ownerRole, memberRole string
	_ = pool.QueryRow(ctx, `SELECT role FROM group_memberships WHERE group_id = $1::uuid AND user_id = $2::uuid`, g.ID, owner).Scan(&ownerRole)
	_ = pool.QueryRow(ctx, `SELECT role FROM group_memberships WHERE group_id = $1::uuid AND user_id = $2::uuid`, g.ID, member).Scan(&memberRole)
	if ownerRole != "admin" || memberRole != "owner" {
		t.Fatalf("owner role=%q (want admin), member role=%q (want owner)", ownerRole, memberRole)
	}
}

func TestChangeRoleDemoteLastOwner(t *testing.T) {
	pool := testDB(t)
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	g, _ := svc.Create(context.Background(), owner, "G", "demote-test", "", "private")

	if _, err := svc.ChangeRole(context.Background(), owner, g.ID, owner, "member"); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("expected ErrLastOwner, got %v", err)
	}
}

func TestRemoveLastOwner(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	admin := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "remove-owner-test", "", "private")
	addMember(t, pool, g.ID, admin, "admin")

	if err := svc.RemoveMember(ctx, admin, g.ID, owner); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("expected ErrLastOwner, got %v", err)
	}
}

func TestLeaveOwnerForbidden(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "leave-owner-test", "", "private")

	if err := svc.Leave(ctx, owner, g.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("expected ErrLastOwner, got %v", err)
	}
}

func TestRemoveMemberByAdmin(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	admin := newUser(t, pool)
	member := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "remove-member-test", "", "private")
	addMember(t, pool, g.ID, admin, "admin")
	addMember(t, pool, g.ID, member, "member")

	if err := svc.RemoveMember(ctx, admin, g.ID, member); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var count int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM group_memberships WHERE group_id = $1::uuid AND user_id = $2::uuid`, g.ID, member).Scan(&count)
	if count != 0 {
		t.Fatalf("membership still present")
	}
}

// ---------- listing ----------

func TestDiscoverExcludesPrivate(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	_, _ = svc.Create(ctx, owner, "Private", "discover-private", "", "private")
	_, _ = svc.Create(ctx, owner, "Public", "discover-public", "", "public")

	groups, _, err := svc.Discover(ctx, 100, "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	found := false
	for _, g := range groups {
		if g.Visibility != "public" {
			t.Fatalf("discover returned non-public group %q", g.Slug)
		}
		if g.Slug == "discover-public" {
			found = true
		}
	}
	if !found {
		t.Fatalf("public group not discoverable")
	}
}

func TestListMembersPagination(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := NewService(pool, nil)
	owner := newUser(t, pool)
	g, _ := svc.Create(ctx, owner, "G", "members-pagination", "", "private")
	addMember(t, pool, g.ID, newUser(t, pool), "member")
	addMember(t, pool, g.ID, newUser(t, pool), "member")

	page1, next, err := svc.ListMembers(ctx, owner, g.ID, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page1) != 2 || next == "" {
		t.Fatalf("page1 len=%d next=%q", len(page1), next)
	}

	page2, next2, err := svc.ListMembers(ctx, owner, g.ID, 2, next)
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	if len(page2) != 1 || next2 != "" {
		t.Fatalf("page2 len=%d next=%q", len(page2), next2)
	}
}

// ---------- handler validation (DB-free) ----------

func TestHandlerUpdateRejectsSlug(t *testing.T) {
	h := &Handler{requireAuth: func(c *gin.Context) { c.Set("userID", "00000000-0000-0000-0000-000000000001") }}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "00000000-0000-0000-0000-000000000001"}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/groups/x", strings.NewReader(`{"slug":"new-slug"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.update(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandlerMalformedUUID(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/groups/not-a-uuid", nil)

	h.get(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
