// internal/chess/service_integration_test.go
//
// These tests exercise the full service (authorization, turn/stale guards,
// persistence and the concurrent-move serialisation) against a real
// PostgreSQL. They are skipped unless TEST_DATABASE_URL points at a database
// with the migrations applied, so `go test ./...` still passes without one.
package chess

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed chess tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func uniq() string {
	var b [6]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func insertUser(t *testing.T, db *pgxpool.Pool, email, name string) string {
	t.Helper()
	var id string
	err := db.QueryRow(context.Background(),
		`INSERT INTO users (email, display_name, password_hash)
		 VALUES ($1, $2, $3) RETURNING id::text`, email, name, "x").Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// setupTest creates a group with an owner plus two members, wires a service
// and truncates the chess tables. It returns the service and the key ids.
func setupTest(t *testing.T) (*Service, *pgxpool.Pool, string, string, string, string) {
	t.Helper()
	db := newTestPool(t)
	ctx := context.Background()

	suffix := uniq()
	owner := insertUser(t, db, "owner-"+suffix+"@example.com", "Owner"+suffix)
	white := insertUser(t, db, "white-"+suffix+"@example.com", "White"+suffix)
	black := insertUser(t, db, "black-"+suffix+"@example.com", "Black"+suffix)

	var groupID string
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO study_groups (created_by, name, slug, visibility)
		 VALUES ($1::uuid, $2, $3, 'private') RETURNING id::text`,
		owner, "Group "+suffix, "g-"+suffix).Scan(&groupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO group_memberships (group_id, user_id, role, status, joined_at)
		 VALUES ($1::uuid, $2::uuid, 'owner', 'active', now())`, groupID, owner); err != nil {
		t.Fatalf("insert owner membership: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit group: %v", err)
	}

	// Other two members are simple inserts (no deferred owner guard).
	for _, u := range []string{white, black} {
		if _, err := db.Exec(ctx,
			`INSERT INTO group_memberships (group_id, user_id, role, status, joined_at)
			 VALUES ($1::uuid, $2::uuid, 'member', 'active', now())`, groupID, u); err != nil {
			t.Fatalf("insert member: %v", err)
		}
	}

	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM chess_games WHERE group_id = $1::uuid`, groupID)
		_, _ = db.Exec(ctx, `DELETE FROM group_memberships WHERE group_id = $1::uuid`, groupID)
		_, _ = db.Exec(ctx, `DELETE FROM study_groups WHERE id = $1::uuid`, groupID)
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, owner, white, black)
	})

	repo := NewRepository(db)
	svc := NewService(repo, db, NewEngine())
	return svc, db, groupID, owner, white, black
}

func createAndJoin(t *testing.T, svc *Service, groupID, whiteID, blackID string) string {
	t.Helper()
	ctx := context.Background()
	state, err := svc.Create(ctx, whiteID, groupID, CreateGameRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if state.Status != StatusPending {
		t.Fatalf("new game status = %q", state.Status)
	}
	if _, err := svc.Join(ctx, blackID, groupID, state.ID); err != nil {
		t.Fatalf("join: %v", err)
	}
	return state.ID
}

func TestServiceHappyPathAndCheckmate(t *testing.T) {
	svc, _, groupID, _, white, black := setupTest(t)
	ctx := context.Background()
	gameID := createAndJoin(t, svc, groupID, white, black)

	// 1.e4
	res, err := svc.Move(ctx, white, groupID, gameID, MoveRequest{Orig: "e2", Dest: "e4", Version: 0})
	if err != nil {
		t.Fatalf("e4: %v", err)
	}
	if !res.Accepted || res.State.Version != 1 {
		t.Fatalf("e4 accepted/version = %v/%d", res.Accepted, res.State.Version)
	}

	// Fool's mate finish (black checkmates): 1...e5 2.g4 Qh4#.
	for _, mv := range []struct {
		actor, o, d string
		v           int
	}{
		{black, "e7", "e5", 1},
		{white, "g2", "g4", 2},
		{black, "d8", "h4", 3},
	} {
		if _, err := svc.Move(ctx, mv.actor, groupID, gameID, MoveRequest{Orig: mv.o, Dest: mv.d, Version: mv.v}); err != nil {
			t.Fatalf("%s%s: %v", mv.o, mv.d, err)
		}
	}

	state, err := svc.Get(ctx, white, groupID, gameID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Status != StatusCompleted || state.Result == nil || *state.Result != ResultBlack {
		t.Fatalf("final state = %q/%v", state.Status, state.Result)
	}
	if state.ResultMethod == nil || *state.ResultMethod != MethodCheckmate {
		t.Fatalf("final method = %v", state.ResultMethod)
	}
	if len(state.Moves) != 4 {
		t.Fatalf("move history length = %d", len(state.Moves))
	}
}

func TestServiceWrongTurn(t *testing.T) {
	svc, _, groupID, _, white, black := setupTest(t)
	ctx := context.Background()
	gameID := createAndJoin(t, svc, groupID, white, black)

	// Black cannot move first.
	if _, err := svc.Move(ctx, black, groupID, gameID, MoveRequest{Orig: "e7", Dest: "e5", Version: 0}); err != ErrNotYourTurn {
		t.Fatalf("black first move = %v, want ErrNotYourTurn", err)
	}
}

func TestServiceWrongPlayer(t *testing.T) {
	svc, _, groupID, owner, white, black := setupTest(t)
	ctx := context.Background()
	gameID := createAndJoin(t, svc, groupID, white, black)

	// The group owner is a member but not a player in this game.
	if _, err := svc.Move(ctx, owner, groupID, gameID, MoveRequest{Orig: "e2", Dest: "e4", Version: 0}); err != ErrNotAPlayer {
		t.Fatalf("non-player move = %v, want ErrNotAPlayer", err)
	}
}

func TestServiceStaleMove(t *testing.T) {
	svc, _, groupID, _, white, black := setupTest(t)
	ctx := context.Background()
	gameID := createAndJoin(t, svc, groupID, white, black)

	if _, err := svc.Move(ctx, white, groupID, gameID, MoveRequest{Orig: "e2", Dest: "e4", Version: 0}); err != nil {
		t.Fatalf("e4: %v", err)
	}
	// Replaying version 0 is stale (the game is now at version 1).
	if _, err := svc.Move(ctx, white, groupID, gameID, MoveRequest{Orig: "d2", Dest: "d4", Version: 0}); err != ErrStaleMove {
		t.Fatalf("stale move = %v, want ErrStaleMove", err)
	}
}

func TestServiceSimultaneousMoves(t *testing.T) {
	svc, _, groupID, _, white, black := setupTest(t)
	ctx := context.Background()
	gameID := createAndJoin(t, svc, groupID, white, black)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Move(ctx, white, groupID, gameID, MoveRequest{Orig: "e2", Dest: "e4", Version: 0})
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one concurrent move should succeed, got %d (%v)", successes, errs)
	}

	state, err := svc.Get(ctx, white, groupID, gameID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Version != 1 || len(state.Moves) != 1 {
		t.Fatalf("version/moves after race = %d/%d", state.Version, len(state.Moves))
	}
}

func TestServiceUnauthorizedConnect(t *testing.T) {
	svc, _, groupID, owner, white, black := setupTest(t)
	ctx := context.Background()
	gameID := createAndJoin(t, svc, groupID, white, black)

	// Owner is a group member but not a player, so they cannot open the game socket.
	if _, err := svc.Connect(ctx, owner, groupID, gameID); err != ErrNotAPlayer {
		t.Fatalf("owner connect = %v, want ErrNotAPlayer", err)
	}
}
