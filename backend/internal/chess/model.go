// internal/chess/model.go
package chess

import (
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrForbidden         = errors.New("forbidden")
	ErrInvalid           = errors.New("invalid request")
	ErrIllegalMove       = errors.New("illegal move")
	ErrNotYourTurn       = errors.New("not your turn")
	ErrStaleMove         = errors.New("stale move")
	ErrGameNotActive     = errors.New("game is not active")
	ErrGameFull          = errors.New("game already has two players")
	ErrAlreadyJoined     = errors.New("you are already in this game")
	ErrNotAPlayer        = errors.New("you are not a player in this game")
	ErrPromotionRequired = errors.New("a promotion piece is required")
	ErrFlagFall          = errors.New("time expired")
)

// Game lifecycle.
const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusCompleted = "completed"
)

// PGN-style results.
const (
	ResultWhite = "1-0"
	ResultBlack = "0-1"
	ResultDraw  = "1/2-1/2"
)

// How a game ended.
const (
	MethodCheckmate            = "checkmate"
	MethodStalemate            = "stalemate"
	MethodResignation          = "resignation"
	MethodDrawAgreement        = "draw_agreement"
	MethodThreefoldRepetition  = "threefold_repetition"
	MethodFivefoldRepetition   = "fivefold_repetition"
	MethodFiftyMove            = "fifty_move"
	MethodSeventyFiveMove      = "seventy_five_move"
	MethodInsufficientMaterial = "insufficient_material"
	MethodFlag                 = "flag"
)

// Colors, matching the library's FEN notation.
const (
	ColorWhite = "w"
	ColorBlack = "b"
)

// Role mirrors groups.Role numerically (kept separate to avoid an import cycle).
type Role int

const (
	RoleNone Role = iota
	RoleMember
	RoleModerator
	RoleAdmin
	RoleOwner
)

func ParseRole(s string) Role {
	switch s {
	case "owner":
		return RoleOwner
	case "admin":
		return RoleAdmin
	case "moderator":
		return RoleModerator
	case "member":
		return RoleMember
	default:
		return RoleNone
	}
}

// Player is the slice of a user that responses need for display.
type Player struct {
	UserID      string
	DisplayName string
}

// Game is one chess game, including its authoritative FEN and clock state.
type Game struct {
	ID               string
	GroupID          string
	RoomID           *string
	WhitePlayerID    *string
	BlackPlayerID    *string
	White            *Player // display data, populated by repository joins
	Black            *Player
	Status           string
	Result           *string
	ResultMethod     *string
	FEN              string
	Version          int // ply count; also the optimistic-concurrency guard
	InitialSeconds   int
	IncrementSeconds int
	WhiteRemainingMs *int64
	BlackRemainingMs *int64
	ClockStartedAt   *time.Time
	DrawOffer        *string
	CreatedBy        string
	CreatedAt        time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	UpdatedAt        time.Time
}

// Move is one persisted half-move.
type Move struct {
	ID         int64
	GameID     string
	PlayerID   string
	PlayerName string
	Color      string
	MoveNumber int
	Ply        int
	SAN        string
	UCI        string
	FENAfter   string
	GivesCheck bool
	CreatedAt  time.Time
}
