// internal/chess/extra.go
// New pieces: lobby listing, cancelling an unjoined game, and claiming a timeout.
package chess

import (
	"context"
	"fmt"
	"time"
)

// GameSummaryResponse is the light row the lobby needs (no legal moves, no history).
type GameSummaryResponse struct {
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	RoomID           *string         `json:"roomId"`
	White            *PlayerResponse `json:"white"`
	Black            *PlayerResponse `json:"black"`
	InitialSeconds   int             `json:"initialSeconds"`
	IncrementSeconds int             `json:"incrementSeconds"`
	CreatedBy        string          `json:"createdBy"`
	CreatedAt        time.Time       `json:"createdAt"`
	Mine             bool            `json:"mine"`
}

// ListOpenAndMine returns, for one scope (a room, or group-level when roomID is nil):
// every game waiting for an opponent, plus the caller's own active games.
func (r *Repository) ListOpenAndMine(ctx context.Context, ex pgxExecutor, groupID string, roomID *string, userID string, limit int) ([]Game, error) {
	rows, err := ex.Query(ctx,
		`SELECT `+gameColsWithNames+`
		   FROM chess_games g
		   LEFT JOIN users w ON w.id = g.white_player_id
		   LEFT JOIN users b ON b.id = g.black_player_id
		  WHERE g.group_id = $1::uuid
		    AND (($2::uuid IS NULL AND g.room_id IS NULL) OR g.room_id = $2::uuid)
		    AND (g.status = 'pending'
		         OR (g.status = 'active' AND (g.white_player_id = $3::uuid OR g.black_player_id = $3::uuid)))
		  ORDER BY g.created_at DESC
		  LIMIT $4`, groupID, roomID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Game{}
	for rows.Next() {
		g, err := scanGame(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// DeletePending removes an unjoined game its creator no longer wants.
func (r *Repository) DeletePending(ctx context.Context, ex pgxExecutor, gameID, userID string) (bool, error) {
	tag, err := ex.Exec(ctx,
		`DELETE FROM chess_games
		  WHERE id = $1::uuid AND status = 'pending' AND created_by = $2::uuid`, gameID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Service) List(ctx context.Context, actorID, groupID string, roomID *string) ([]GameSummaryResponse, error) {
	if !uuidRe.MatchString(groupID) {
		return nil, ErrNotFound
	}
	if err := s.requireGroupMember(ctx, s.db, groupID, actorID); err != nil {
		return nil, err
	}
	if err := s.requireRoomAccess(ctx, s.db, groupID, roomID, actorID, false); err != nil {
		return nil, err
	}
	games, err := s.repo.ListOpenAndMine(ctx, s.db, groupID, roomID, actorID, 50)
	if err != nil {
		return nil, err
	}
	out := make([]GameSummaryResponse, 0, len(games))
	for i := range games {
		g := &games[i]
		out = append(out, GameSummaryResponse{
			ID: g.ID, Status: g.Status, RoomID: g.RoomID,
			White: toPlayerResponse(g.White), Black: toPlayerResponse(g.Black),
			InitialSeconds: g.InitialSeconds, IncrementSeconds: g.IncrementSeconds,
			CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt,
			Mine: playerColor(g, actorID) != "",
		})
	}
	return out, nil
}

// Cancel deletes a pending game. Only its creator may.
func (s *Service) Cancel(ctx context.Context, actorID, groupID, gameID string) error {
	game, err := s.accessGame(ctx, s.db, groupID, actorID, gameID, false)
	if err != nil {
		return err
	}
	if game.Status != StatusPending {
		return ErrGameNotActive
	}
	if game.CreatedBy != actorID {
		return ErrForbidden
	}
	ok, err := s.repo.DeletePending(ctx, s.db, gameID, actorID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGameNotActive
	}
	return nil
}

// ClaimTimeout ends a game whose side-to-move has run out of time. Without this,
// a player who simply stops moving would keep the game "active" forever, because
// flag fall is otherwise only checked when a move arrives. Either player may call it.
func (s *Service) ClaimTimeout(ctx context.Context, actorID, groupID, gameID string) (*GameStateResponse, error) {
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
	if playerColor(game, actorID) == "" {
		return nil, ErrNotAPlayer
	}
	cfg := ClockConfig{InitialSeconds: game.InitialSeconds, IncrementSeconds: game.IncrementSeconds}
	if !cfg.Timed() || game.ClockStartedAt == nil {
		return nil, fmt.Errorf("%w: this game has no clock", ErrInvalid)
	}
	g, err := s.engine.FromFEN(game.FEN)
	if err != nil {
		return nil, err
	}
	turn := s.engine.Turn(g)
	if !cfg.Flagged(moverRemaining(game, turn), time.Since(*game.ClockStartedAt)) {
		return nil, fmt.Errorf("%w: the clock has not run out", ErrInvalid)
	}

	result, method := flagOutcome(turn)
	game.Status = StatusCompleted
	game.Result = &result
	game.ResultMethod = &method
	now := time.Now()
	game.CompletedAt = &now
	game.DrawOffer = nil
	setMoverRemaining(game, turn, 0)
	if err := s.repo.SaveGame(ctx, tx, game); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.broadcastCompletion(ctx, game)
	return s.buildState(ctx, game, nil)
}