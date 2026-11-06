// internal/chess/repository.go
package chess

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxExecutor is satisfied by both *pgxpool.Pool and pgx.Tx, so repository
// queries run identically on the pool or inside a transaction.
type pgxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// gameColsWithNames selects through aliases g (game), w (white user) and b
// (black user) so player display names ride along with the row.
const gameColsWithNames = `g.id::text, g.group_id::text, g.room_id::text,
    g.white_player_id::text, g.black_player_id::text,
    g.status, g.result, g.result_method,
    g.fen, g.version,
    g.initial_seconds, g.increment_seconds,
    g.white_remaining_ms, g.black_remaining_ms,
    g.clock_started_at, g.draw_offer,
    g.created_by::text, g.created_at, g.started_at, g.completed_at, g.updated_at,
    w.display_name, b.display_name`

func scanGame(row pgx.Row) (*Game, error) {
	var g Game
	var whiteName, blackName *string
	err := row.Scan(
		&g.ID, &g.GroupID, &g.RoomID,
		&g.WhitePlayerID, &g.BlackPlayerID,
		&g.Status, &g.Result, &g.ResultMethod,
		&g.FEN, &g.Version,
		&g.InitialSeconds, &g.IncrementSeconds,
		&g.WhiteRemainingMs, &g.BlackRemainingMs,
		&g.ClockStartedAt, &g.DrawOffer,
		&g.CreatedBy, &g.CreatedAt, &g.StartedAt, &g.CompletedAt, &g.UpdatedAt,
		&whiteName, &blackName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if g.WhitePlayerID != nil && whiteName != nil {
		g.White = &Player{UserID: *g.WhitePlayerID, DisplayName: *whiteName}
	}
	if g.BlackPlayerID != nil && blackName != nil {
		g.Black = &Player{UserID: *g.BlackPlayerID, DisplayName: *blackName}
	}
	return &g, nil
}

// CreateGame inserts a new (pending) game and returns it with player names.
func (r *Repository) CreateGame(ctx context.Context, ex pgxExecutor, g *Game) (*Game, error) {
	return scanGame(ex.QueryRow(ctx,
		`WITH ins AS (
		     INSERT INTO chess_games
		         (group_id, room_id, white_player_id, black_player_id, status,
		          fen, version, initial_seconds, increment_seconds,
		          white_remaining_ms, black_remaining_ms, created_by)
		     VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12::uuid)
		     RETURNING *)
		 SELECT `+gameColsWithNames+`
		   FROM ins g
		   LEFT JOIN users w ON w.id = g.white_player_id
		   LEFT JOIN users b ON b.id = g.black_player_id`,
		g.GroupID, g.RoomID, g.WhitePlayerID, g.BlackPlayerID, g.Status,
		g.FEN, g.Version, g.InitialSeconds, g.IncrementSeconds,
		g.WhiteRemainingMs, g.BlackRemainingMs, g.CreatedBy,
	))
}

// GetGame loads a game. forUpdate takes a row lock so concurrent move/join
// operations serialise.
func (r *Repository) GetGame(ctx context.Context, ex pgxExecutor, id string, forUpdate bool) (*Game, error) {
	q := `SELECT ` + gameColsWithNames + `
	        FROM chess_games g
	        LEFT JOIN users w ON w.id = g.white_player_id
	        LEFT JOIN users b ON b.id = g.black_player_id
	       WHERE g.id = $1::uuid`
	if forUpdate {
		q += ` FOR UPDATE OF g`
	}
	return scanGame(ex.QueryRow(ctx, q, id))
}

// SaveGame persists the mutable fields of a loaded game.
func (r *Repository) SaveGame(ctx context.Context, ex pgxExecutor, g *Game) error {
	_, err := ex.Exec(ctx,
		`UPDATE chess_games SET
		     status = $2, result = $3, result_method = $4,
		     fen = $5, version = $6,
		     white_remaining_ms = $7, black_remaining_ms = $8,
		     clock_started_at = $9, draw_offer = $10,
		     started_at = $11, completed_at = $12
		 WHERE id = $1::uuid`,
		g.ID, g.Status, g.Result, g.ResultMethod, g.FEN, g.Version,
		g.WhiteRemainingMs, g.BlackRemainingMs, g.ClockStartedAt, g.DrawOffer,
		g.StartedAt, g.CompletedAt)
	return err
}

// JoinGame is not used directly; the service loads the game FOR UPDATE, mutates
// it and calls SaveGame so join and move share the same lock discipline.

// InsertMove records a half-move and fills in its id and created_at.
func (r *Repository) InsertMove(ctx context.Context, ex pgxExecutor, m *Move) error {
	return ex.QueryRow(ctx,
		`INSERT INTO chess_moves
		     (game_id, player_id, color, move_number, ply, san, uci, fen_after, gives_check)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		m.GameID, m.PlayerID, m.Color, m.MoveNumber, m.Ply, m.SAN, m.UCI, m.FENAfter, m.GivesCheck,
	).Scan(&m.ID, &m.CreatedAt)
}

// ListMoves returns the game's move history in play order.
func (r *Repository) ListMoves(ctx context.Context, ex pgxExecutor, gameID string) ([]Move, error) {
	rows, err := ex.Query(ctx,
		`SELECT m.id, m.game_id::text, m.player_id::text, u.display_name, m.color,
		        m.move_number, m.ply, m.san, m.uci, m.fen_after, m.gives_check, m.created_at
		   FROM chess_moves m
		   JOIN users u ON u.id = m.player_id
		  WHERE m.game_id = $1::uuid
		  ORDER BY m.ply`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Move{}
	for rows.Next() {
		var m Move
		if err := rows.Scan(&m.ID, &m.GameID, &m.PlayerID, &m.PlayerName, &m.Color,
			&m.MoveNumber, &m.Ply, &m.SAN, &m.UCI, &m.FENAfter, &m.GivesCheck, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------- access lookups into other modules' tables ----------

func (r *Repository) MemberStatus(ctx context.Context, ex pgxExecutor, groupID, userID string) (string, error) {
	var status string
	err := ex.QueryRow(ctx,
		`SELECT status FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid
		  FOR SHARE`, groupID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

func (r *Repository) GetRoom(ctx context.Context, ex pgxExecutor, id string) (*Room, error) {
	var rm Room
	err := ex.QueryRow(ctx,
		`SELECT id::text, group_id::text, access, archived_at IS NOT NULL
		   FROM study_rooms WHERE id = $1::uuid`, id,
	).Scan(&rm.ID, &rm.GroupID, &rm.Access, &rm.Archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rm, nil
}

func (r *Repository) HasRoomMembership(ctx context.Context, ex pgxExecutor, roomID, userID string) (bool, error) {
	var ok bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM room_memberships
		                 WHERE room_id = $1::uuid AND user_id = $2::uuid AND status = 'active')`,
		roomID, userID).Scan(&ok)
	return ok, err
}

// Room is the slice of study_rooms the access gate needs.
type Room struct {
	ID       string
	GroupID  string
	Access   string // "group" | "restricted"
	Archived bool
}

// ---------- helpers ----------

func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func mapDBErr(err error) error {
	if IsCheckViolation(err) || IsForeignKeyViolation(err) {
		return ErrInvalid
	}
	return err
}
