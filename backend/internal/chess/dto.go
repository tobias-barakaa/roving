// internal/chess/dto.go
package chess

import "time"

// ---------- requests ----------

// CreateGameRequest creates a game inside a group. Color is "", "white",
// "black" or "random". A zero InitialSeconds means untimed.
type CreateGameRequest struct {
	RoomID           *string `json:"roomId"`
	Color            string  `json:"color"`
	InitialSeconds   int     `json:"initialSeconds"`
	IncrementSeconds int     `json:"incrementSeconds"`
}

// MoveRequest is a proposed move from Chessground: origin/destination squares
// plus an optional promotion piece and the game version the client saw.
type MoveRequest struct {
	Orig      string `json:"orig"`
	Dest      string `json:"dest"`
	Promotion string `json:"promotion"`
	Version   int    `json:"version"`
}

// ---------- responses ----------

type PlayerResponse struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

type MoveResponse struct {
	Color      string `json:"color"`
	SAN        string `json:"san"`
	UCI        string `json:"uci"`
	MoveNumber int    `json:"moveNumber"`
	Ply        int    `json:"ply"`
	GivesCheck bool   `json:"givesCheck"`
}

type ClockResponse struct {
	InitialSeconds   int        `json:"initialSeconds"`
	IncrementSeconds int        `json:"incrementSeconds"`
	WhiteRemainingMs *int64     `json:"whiteRemainingMs"`
	BlackRemainingMs *int64     `json:"blackRemainingMs"`
	StartedAt        *time.Time `json:"startedAt"`
	TurnColor        string     `json:"turnColor"`
}

// GameStateResponse is the single authoritative snapshot the frontend renders
// from. It carries everything Chessground needs: full FEN, side to move, last
// move, check colour and legal destinations, plus clocks and move history.
type GameStateResponse struct {
	ID           string              `json:"id"`
	GroupID      string              `json:"groupId"`
	RoomID       *string             `json:"roomId"`
	Status       string              `json:"status"`
	Result       *string             `json:"result"`
	ResultMethod *string             `json:"resultMethod"`
	White        *PlayerResponse     `json:"white"`
	Black        *PlayerResponse     `json:"black"`
	FEN          string              `json:"fen"`
	TurnColor    string              `json:"turnColor"`
	LastMove     []string            `json:"lastMove"`
	Check        *string             `json:"check"`
	Dests        map[string][]string `json:"dests"`
	DrawOffer    *string             `json:"drawOffer"`
	Clock        ClockResponse       `json:"clock"`
	Moves        []MoveResponse      `json:"moves"`
	Version      int                 `json:"version"`
	CreatedAt    time.Time           `json:"createdAt"`
	StartedAt    *time.Time          `json:"startedAt"`
	CompletedAt  *time.Time          `json:"completedAt"`
}

// LegalMovesResponse is a lightweight position snapshot for move highlighting.
type LegalMovesResponse struct {
	FEN       string              `json:"fen"`
	TurnColor string              `json:"turnColor"`
	Check     *string             `json:"check"`
	Dests     map[string][]string `json:"dests"`
}

// MoveSubmitResponse wraps the accepted move with the full authoritative state.
// Accepted is false only for a flag fall (the move was not applied but the game
// completed); Reason is then "flag".
type MoveSubmitResponse struct {
	Accepted bool               `json:"accepted"`
	Reason   string             `json:"reason,omitempty"`
	Move     *MoveResponse      `json:"move,omitempty"`
	State    *GameStateResponse `json:"state"`
}

func toPlayerResponse(p *Player) *PlayerResponse {
	if p == nil {
		return nil
	}
	return &PlayerResponse{UserID: p.UserID, DisplayName: p.DisplayName}
}

func toMoveResponse(m Move) MoveResponse {
	return MoveResponse{
		Color:      m.Color,
		SAN:        m.SAN,
		UCI:        m.UCI,
		MoveNumber: m.MoveNumber,
		Ply:        m.Ply,
		GivesCheck: m.GivesCheck,
	}
}

// toStateResponse builds the authoritative snapshot. turn is the side to move,
// dests the legal destinations (empty unless active) and check the colour in
// check (or nil).
func toStateResponse(g *Game, moves []Move, turn string, dests map[string][]string, check *string) *GameStateResponse {
	resp := &GameStateResponse{
		ID:           g.ID,
		GroupID:      g.GroupID,
		RoomID:       g.RoomID,
		Status:       g.Status,
		Result:       g.Result,
		ResultMethod: g.ResultMethod,
		White:        toPlayerResponse(g.White),
		Black:        toPlayerResponse(g.Black),
		FEN:          g.FEN,
		TurnColor:    turn,
		Check:        check,
		Dests:        dests,
		DrawOffer:    g.DrawOffer,
		Version:      g.Version,
		CreatedAt:    g.CreatedAt,
		StartedAt:    g.StartedAt,
		CompletedAt:  g.CompletedAt,
		Clock: ClockResponse{
			InitialSeconds:   g.InitialSeconds,
			IncrementSeconds: g.IncrementSeconds,
			WhiteRemainingMs: g.WhiteRemainingMs,
			BlackRemainingMs: g.BlackRemainingMs,
			StartedAt:        g.ClockStartedAt,
			TurnColor:        turn,
		},
		Moves: make([]MoveResponse, 0, len(moves)),
	}

	// White/black are represented by the player ids even when the display-name
	// join missed (defensive: a player row always exists while the id is set).
	if resp.White == nil && g.WhitePlayerID != nil {
		resp.White = &PlayerResponse{UserID: *g.WhitePlayerID}
	}
	if resp.Black == nil && g.BlackPlayerID != nil {
		resp.Black = &PlayerResponse{UserID: *g.BlackPlayerID}
	}

	for _, m := range moves {
		resp.Moves = append(resp.Moves, toMoveResponse(m))
	}
	if n := len(moves); n > 0 && len(moves[n-1].UCI) >= 4 {
		u := moves[n-1].UCI
		resp.LastMove = []string{u[0:2], u[2:4]}
	}
	return resp
}
