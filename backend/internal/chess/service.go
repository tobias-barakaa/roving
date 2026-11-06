// internal/chess/service.go
package chess

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var squareRe = regexp.MustCompile(`^[a-h][1-8]$`)

const (
	maxInitialSeconds   = 7 * 24 * 60 * 60 // 7 days
	maxIncrementSeconds = 60 * 60          // 1 hour
)

type Service struct {
	repo   *Repository
	db     *pgxpool.Pool
	engine *Engine
	pub    Publisher // optional; attached by the WebSocket layer
}

func NewService(repo *Repository, db *pgxpool.Pool, engine *Engine) *Service {
	return &Service{repo: repo, db: db, engine: engine}
}

// SetPublisher attaches the real-time fan-out. Call once at startup.
func (s *Service) SetPublisher(p Publisher) { s.pub = p }

func (s *Service) broadcast(gameID string, ev wsEvent) {
	if s.pub != nil {
		s.pub.Broadcast(gameID, ev)
	}
}

// ==================== create ====================

func (s *Service) Create(ctx context.Context, actorID, groupID string, req CreateGameRequest) (*GameStateResponse, error) {
	if !uuidRe.MatchString(groupID) {
		return nil, ErrNotFound
	}
	if req.InitialSeconds < 0 || req.IncrementSeconds < 0 {
		return nil, fmt.Errorf("%w: clock values must be non-negative", ErrInvalid)
	}
	if req.InitialSeconds > maxInitialSeconds || req.IncrementSeconds > maxIncrementSeconds {
		return nil, fmt.Errorf("%w: clock values out of range", ErrInvalid)
	}
	if req.InitialSeconds == 0 && req.IncrementSeconds != 0 {
		return nil, fmt.Errorf("%w: an untimed game cannot have an increment", ErrInvalid)
	}

	color, err := resolveColor(req.Color)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := s.requireGroupMember(ctx, tx, groupID, actorID); err != nil {
		return nil, err
	}
	if err := s.requireRoomAccess(ctx, tx, groupID, req.RoomID, actorID, true); err != nil {
		return nil, err
	}

	cfg := ClockConfig{InitialSeconds: req.InitialSeconds, IncrementSeconds: req.IncrementSeconds}
	g := &Game{
		GroupID:          groupID,
		RoomID:           req.RoomID,
		Status:           StatusPending,
		FEN:              s.engine.InitialFEN(),
		Version:          0,
		InitialSeconds:   req.InitialSeconds,
		IncrementSeconds: req.IncrementSeconds,
		WhiteRemainingMs: cfg.InitialMs(),
		BlackRemainingMs: cfg.InitialMs(),
		CreatedBy:        actorID,
	}
	if color == ColorWhite {
		g.WhitePlayerID = &actorID
	} else {
		g.BlackPlayerID = &actorID
	}

	created, err := s.repo.CreateGame(ctx, tx, g)
	if err != nil {
		return nil, mapDBErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.buildState(ctx, created, nil)
}

// ==================== read ====================

func (s *Service) Get(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
	game, err := s.accessGame(ctx, s.db, groupID, actorID, gameID, false)
	if err != nil {
		return nil, err
	}
	moves, err := s.repo.ListMoves(ctx, s.db, game.ID)
	if err != nil {
		return nil, err
	}
	return s.buildState(ctx, game, moves)
}

func (s *Service) LegalMoves(ctx context.Context, actorID, groupID, gameID string) (*LegalMovesResponse, error) {
	game, err := s.accessGame(ctx, s.db, groupID, actorID, gameID, false)
	if err != nil {
		return nil, err
	}
	moves, err := s.repo.ListMoves(ctx, s.db, game.ID)
	if err != nil {
		return nil, err
	}
	state, err := s.buildState(ctx, game, moves)
	if err != nil {
		return nil, err
	}
	return &LegalMovesResponse{FEN: state.FEN, TurnColor: state.TurnColor, Check: state.Check, Dests: state.Dests}, nil
}

// ==================== join ====================

func (s *Service) Join(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	game, err := s.accessGame(ctx, tx, groupID, actorID, gameID, true)
	if err != nil {
		return nil, err
	}
	if game.Status != StatusPending {
		if game.Status == StatusActive {
			return nil, ErrGameFull
		}
		return nil, ErrGameNotActive
	}
	if playerColor(game, actorID) != "" {
		return nil, ErrAlreadyJoined
	}
	// Fill whichever seat is open (the creator may have chosen black).
	if game.WhitePlayerID == nil {
		game.WhitePlayerID = &actorID
	} else if game.BlackPlayerID == nil {
		game.BlackPlayerID = &actorID
	} else {
		return nil, ErrGameFull
	}

	game.Status = StatusActive
	now := time.Now()
	game.StartedAt = &now
	game.ClockStartedAt = &now

	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	state, err := s.buildState(ctx, game, nil)
	if err != nil {
		return nil, err
	}
	s.broadcast(gameID, wsEvent{Type: "game.state", State: state})
	return state, nil
}

// ==================== move ====================

func (s *Service) Move(ctx context.Context, actorID, groupID, gameID string, req MoveRequest) (*MoveSubmitResponse, error) {
	if !squareRe.MatchString(req.Orig) || !squareRe.MatchString(req.Dest) {
		return nil, fmt.Errorf("%w: invalid square", ErrInvalid)
	}
	promo, ok := ParsePromotion(req.Promotion)
	if !ok {
		return nil, fmt.Errorf("%w: invalid promotion piece", ErrInvalid)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	game, err := s.accessGame(ctx, tx, groupID, actorID, gameID, true)
	if err != nil {
		return nil, err
	}
	if game.Status != StatusActive {
		return nil, ErrGameNotActive
	}
	color := playerColor(game, actorID)
	if color == "" {
		return nil, ErrNotAPlayer
	}

	g, err := s.engine.FromFEN(game.FEN)
	if err != nil {
		return nil, err
	}
	if s.engine.Turn(g) != color {
		return nil, ErrNotYourTurn
	}
	if req.Version != game.Version {
		return nil, ErrStaleMove
	}

	cfg := ClockConfig{InitialSeconds: game.InitialSeconds, IncrementSeconds: game.IncrementSeconds}
	var elapsed time.Duration
	if cfg.Timed() && game.ClockStartedAt != nil {
		elapsed = time.Since(*game.ClockStartedAt)
	}
	moverRemaining := moverRemaining(game, color)

	// Flag fall: the mover ran out of time before completing their move. The
	// move is not applied, but the game completes and both players are told.
	if cfg.Flagged(moverRemaining, elapsed) {
		result, method := flagOutcome(color)
		game.Status = StatusCompleted
		game.Result = &result
		game.ResultMethod = &method
		now := time.Now()
		game.CompletedAt = &now
		game.DrawOffer = nil
		setMoverRemaining(game, color, 0)
		if err := s.repo.SaveGame(ctx, tx, game); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		s.broadcastCompletion(ctx, game)
		return nil, ErrFlagFall
	}

	am, err := s.engine.ApplyMove(g, req.Orig, req.Dest, promo)
	if err != nil {
		return nil, err
	}

	if cfg.Timed() {
		setMoverRemaining(game, color, cfg.Apply(moverRemaining, elapsed))
		now := time.Now()
		game.ClockStartedAt = &now
	}

	game.FEN = s.engine.FEN(g)
	game.Version = req.Version + 1
	game.DrawOffer = nil
	if am.Result != "" {
		game.Status = StatusCompleted
		game.Result = &am.Result
		game.ResultMethod = &am.Method
		now := time.Now()
		game.CompletedAt = &now
	}
	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}

	move := &Move{
		GameID:     game.ID,
		PlayerID:   actorID,
		Color:      color,
		MoveNumber: (game.Version + 1) / 2,
		Ply:        game.Version,
		SAN:        am.SAN,
		UCI:        am.UCI,
		FENAfter:   game.FEN,
		GivesCheck: am.GivesCheck,
	}
	if err := s.repo.InsertMove(ctx, tx, move); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	moves, err := s.repo.ListMoves(ctx, s.db, game.ID)
	if err != nil {
		return nil, err
	}
	state, err := s.buildState(ctx, game, moves)
	if err != nil {
		return nil, err
	}

	// Both players get the same authoritative state. The mover also receives it
	// here; the opponent receives it over the WebSocket.
	mr := toMoveResponse(*move)
	s.broadcast(gameID, wsEvent{Type: "game.move", Move: &mr, State: state})
	if game.Status == StatusCompleted {
		s.broadcast(gameID, wsEvent{Type: "game.over", Result: *game.Result, ResultMethod: *game.ResultMethod, State: state})
	}

	return &MoveSubmitResponse{Accepted: true, Move: &mr, State: state}, nil
}

// ==================== resignation ====================

func (s *Service) Resign(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	game, err := s.accessGame(ctx, tx, groupID, actorID, gameID, true)
	if err != nil {
		return nil, err
	}
	if game.Status != StatusActive {
		return nil, ErrGameNotActive
	}
	color := playerColor(game, actorID)
	if color == "" {
		return nil, ErrNotAPlayer
	}

	result := ResultWhite
	if color == ColorWhite {
		result = ResultBlack
	}
	method := MethodResignation
	game.Status = StatusCompleted
	game.Result = &result
	game.ResultMethod = &method
	now := time.Now()
	game.CompletedAt = &now
	game.DrawOffer = nil

	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.broadcastCompletion(ctx, game)
	state, err := s.buildState(ctx, game, nil)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// ==================== draw offers ====================

func (s *Service) OfferDraw(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	game, err := s.accessGame(ctx, tx, groupID, actorID, gameID, true)
	if err != nil {
		return nil, err
	}
	if game.Status != StatusActive {
		return nil, ErrGameNotActive
	}
	color := playerColor(game, actorID)
	if color == "" {
		return nil, ErrNotAPlayer
	}
	if game.DrawOffer != nil {
		return nil, fmt.Errorf("%w: a draw offer is already pending", ErrInvalid)
	}

	game.DrawOffer = &color
	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.broadcast(gameID, wsEvent{Type: "draw.offer", Color: color, UserID: actorID})
	state, err := s.buildState(ctx, game, nil)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func (s *Service) AcceptDraw(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	game, err := s.accessGame(ctx, tx, groupID, actorID, gameID, true)
	if err != nil {
		return nil, err
	}
	if game.Status != StatusActive {
		return nil, ErrGameNotActive
	}
	color := playerColor(game, actorID)
	if color == "" {
		return nil, ErrNotAPlayer
	}
	if game.DrawOffer == nil || *game.DrawOffer == color {
		return nil, fmt.Errorf("%w: no draw offer to accept", ErrInvalid)
	}

	result := ResultDraw
	method := MethodDrawAgreement
	game.Status = StatusCompleted
	game.Result = &result
	game.ResultMethod = &method
	now := time.Now()
	game.CompletedAt = &now
	game.DrawOffer = nil

	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.broadcastCompletion(ctx, game)
	state, err := s.buildState(ctx, game, nil)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func (s *Service) DeclineDraw(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	game, err := s.accessGame(ctx, tx, groupID, actorID, gameID, true)
	if err != nil {
		return nil, err
	}
	if game.Status != StatusActive {
		return nil, ErrGameNotActive
	}
	color := playerColor(game, actorID)
	if color == "" {
		return nil, ErrNotAPlayer
	}
	if game.DrawOffer == nil || *game.DrawOffer == color {
		return nil, fmt.Errorf("%w: no draw offer to decline", ErrInvalid)
	}

	game.DrawOffer = nil
	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.broadcast(gameID, wsEvent{Type: "draw.decline"})
	state, err := s.buildState(ctx, game, nil)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// ==================== connect (WebSocket authorization) ====================

// Connect authorizes a WebSocket connection (the caller must be a player of
// this group's game) and returns the authoritative snapshot to send first.
func (s *Service) Connect(ctx context.Context, userID, groupID, gameID string) (*GameStateResponse, error) {
	game, err := s.accessGame(ctx, s.db, groupID, userID, gameID, false)
	if err != nil {
		return nil, err
	}
	if playerColor(game, userID) == "" {
		return nil, ErrNotAPlayer
	}
	moves, err := s.repo.ListMoves(ctx, s.db, game.ID)
	if err != nil {
		return nil, err
	}
	return s.buildState(ctx, game, moves)
}

// ==================== state assembly ====================

// buildState reconstructs the authoritative position from the stored FEN and
// returns the single snapshot the frontend renders. moves may be nil when the
// caller does not need history (a subsequent ListMoves is skipped).
func (s *Service) buildState(ctx context.Context, game *Game, moves []Move) (*GameStateResponse, error) {
	g, err := s.engine.FromFEN(game.FEN)
	if err != nil {
		return nil, err
	}
	turn := s.engine.Turn(g)

	dests := map[string][]string{}
	if game.Status == StatusActive {
		dests = s.engine.LegalDests(g)
	}

	var check *string
	if len(moves) > 0 && moves[len(moves)-1].GivesCheck {
		check = &turn // the side to move is the checked side
	}

	return toStateResponse(game, moves, turn, dests, check), nil
}

func (s *Service) broadcastCompletion(ctx context.Context, game *Game) {
	state, err := s.buildState(ctx, game, nil)
	if err != nil {
		return
	}
	s.broadcast(game.ID, wsEvent{Type: "game.over", Result: *game.Result, ResultMethod: *game.ResultMethod, State: state})
	s.broadcast(game.ID, wsEvent{Type: "game.state", State: state})
}

// ==================== access gates ====================

func (s *Service) accessGame(ctx context.Context, ex pgxExecutor, groupID, userID, gameID string, write bool) (*Game, error) {
	if !uuidRe.MatchString(groupID) || !uuidRe.MatchString(gameID) {
		return nil, ErrNotFound
	}
	if err := s.requireGroupMember(ctx, ex, groupID, userID); err != nil {
		return nil, err
	}
	game, err := s.repo.GetGame(ctx, ex, gameID, write)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(game.GroupID, groupID) {
		return nil, ErrNotFound
	}
	if err := s.requireRoomAccess(ctx, ex, groupID, game.RoomID, userID, write); err != nil {
		return nil, err
	}
	return game, nil
}

func (s *Service) requireGroupMember(ctx context.Context, ex pgxExecutor, groupID, userID string) error {
	status, err := s.repo.MemberStatus(ctx, ex, groupID, userID)
	if err != nil {
		return err // ErrNotFound for non-members (anti-enumeration)
	}
	if status != "active" {
		return ErrForbidden
	}
	return nil
}

func (s *Service) requireRoomAccess(ctx context.Context, ex pgxExecutor, groupID string, roomID *string, userID string, write bool) error {
	if roomID == nil {
		return nil
	}
	if !uuidRe.MatchString(*roomID) {
		return ErrNotFound
	}
	room, err := s.repo.GetRoom(ctx, ex, *roomID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(room.GroupID, groupID) {
		return ErrNotFound
	}
	if room.Access == "restricted" {
		ok, err := s.repo.HasRoomMembership(ctx, ex, room.ID, userID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	if write && room.Archived {
		return fmt.Errorf("%w: this room is archived", ErrInvalid)
	}
	return nil
}

// ==================== helpers ====================

func playerColor(g *Game, userID string) string {
	if g.WhitePlayerID != nil && *g.WhitePlayerID == userID {
		return ColorWhite
	}
	if g.BlackPlayerID != nil && *g.BlackPlayerID == userID {
		return ColorBlack
	}
	return ""
}

func moverRemaining(g *Game, color string) int64 {
	if color == ColorWhite && g.WhiteRemainingMs != nil {
		return *g.WhiteRemainingMs
	}
	if color == ColorBlack && g.BlackRemainingMs != nil {
		return *g.BlackRemainingMs
	}
	return 0
}

func setMoverRemaining(g *Game, color string, ms int64) {
	if color == ColorWhite {
		g.WhiteRemainingMs = &ms
	} else {
		g.BlackRemainingMs = &ms
	}
}

// flagOutcome returns the result when the given color loses (flag or
// resignation): the opponent wins.
func flagOutcome(color string) (string, string) {
	if color == ColorWhite {
		return ResultBlack, MethodFlag
	}
	return ResultWhite, MethodFlag
}

func resolveColor(c string) (string, error) {
	switch c {
	case "", "white":
		return ColorWhite, nil
	case "black":
		return ColorBlack, nil
	case "random":
		var b [1]byte
		if _, err := crand.Read(b[:]); err != nil {
			return "", err
		}
		if b[0]%2 == 0 {
			return ColorWhite, nil
		}
		return ColorBlack, nil
	default:
		return "", fmt.Errorf("%w: invalid color", ErrInvalid)
	}
}
