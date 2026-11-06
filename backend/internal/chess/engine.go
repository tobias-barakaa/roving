// internal/chess/engine.go
package chess

import (
	rules "github.com/corentings/chess/v2"
)

// Engine is the only place that imports the rules library. It presents the
// rest of the module with a small, testable surface: FEN in, legal-move
// validation, and authoritative FEN + notation out.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// PieceType is the promotion piece type of the rules library, re-exported so
// callers never have to import the library themselves.
type PieceType = rules.PieceType

// InitialFEN returns the standard starting position.
func (e *Engine) InitialFEN() string { return rules.StartingPosition().String() }

// FromFEN rebuilds a rules game from an authoritative full FEN.
func (e *Engine) FromFEN(fen string) (*rules.Game, error) {
	opt, err := rules.FEN(fen)
	if err != nil {
		return nil, err
	}
	return rules.NewGame(opt), nil
}

// Turn returns the side to move ("w" or "b").
func (e *Engine) Turn(g *rules.Game) string { return g.Position().Turn().String() }

// FEN returns the authoritative full FEN of the current position.
func (e *Engine) FEN(g *rules.Game) string { return g.Position().String() }

// LegalDests returns Chessground-shaped destinations: origin square → list of
// destination squares. Promotion produces several library moves for the same
// origin/destination pair, so destinations are de-duplicated (Chessground
// cannot express a promotion choice inside dests).
func (e *Engine) LegalDests(g *rules.Game) map[string][]string {
	out := map[string][]string{}
	for _, mv := range g.ValidMoves() {
		from := mv.S1().String()
		to := mv.S2().String()
		if !containsString(out[from], to) {
			out[from] = append(out[from], to)
		}
	}
	return out
}

// AppliedMove carries the notation and resulting outcome of a successful move.
type AppliedMove struct {
	UCI        string
	SAN        string
	GivesCheck bool
	Result     string // "" while ongoing, else "1-0", "0-1" or "1/2-1/2"
	Method     string // "" while ongoing, else a Method* constant
}

// ApplyMove validates and applies a proposed move. It returns ErrIllegalMove
// when the move is not among the position's legal moves, or
// ErrPromotionRequired when a pawn reaches the last rank without a piece.
func (e *Engine) ApplyMove(g *rules.Game, orig, dest string, promo PieceType) (*AppliedMove, error) {
	s1 := rules.SquareFromString(orig)
	if s1 == rules.NoSquare {
		return nil, ErrIllegalMove
	}
	s2 := rules.SquareFromString(dest)
	if s2 == rules.NoSquare {
		return nil, ErrIllegalMove
	}

	// A pawn stepping onto the last rank must promote. Give a clearer error
	// than "illegal move" when the promotion piece is missing.
	if promo == rules.NoPieceType {
		if p := g.Position().Board().Piece(s1); p != rules.NoPiece && p.Type() == rules.Pawn {
			r := s2.Rank()
			if (p.Color() == rules.White && r == rules.Rank8) ||
				(p.Color() == rules.Black && r == rules.Rank1) {
				return nil, ErrPromotionRequired
			}
		}
	}

	for _, mv := range g.ValidMoves() {
		if mv.S1() == s1 && mv.S2() == s2 && mv.Promo() == promo {
			pos := g.Position()
			am := &AppliedMove{
				UCI:        rules.UCINotation{}.Encode(pos, &mv),
				SAN:        rules.AlgebraicNotation{}.Encode(pos, &mv),
				GivesCheck: mv.HasTag(rules.Check),
			}
			if err := g.Move(&mv, nil); err != nil {
				return nil, err
			}
			am.Result, am.Method = e.outcome(g)
			return am, nil
		}
	}
	return nil, ErrIllegalMove
}

func (e *Engine) outcome(g *rules.Game) (string, string) {
	switch g.Outcome() {
	case rules.WhiteWon:
		return ResultWhite, e.method(g.Method())
	case rules.BlackWon:
		return ResultBlack, e.method(g.Method())
	case rules.Draw:
		return ResultDraw, e.method(g.Method())
	default:
		return "", ""
	}
}

func (e *Engine) method(m rules.Method) string {
	switch m {
	case rules.Checkmate:
		return MethodCheckmate
	case rules.Stalemate:
		return MethodStalemate
	case rules.Resignation:
		return MethodResignation
	case rules.DrawOffer:
		return MethodDrawAgreement
	case rules.ThreefoldRepetition:
		return MethodThreefoldRepetition
	case rules.FivefoldRepetition:
		return MethodFivefoldRepetition
	case rules.FiftyMoveRule:
		return MethodFiftyMove
	case rules.SeventyFiveMoveRule:
		return MethodSeventyFiveMove
	case rules.InsufficientMaterial:
		return MethodInsufficientMaterial
	default:
		return ""
	}
}

// ParsePromotion maps "", "q", "r", "b" or "n" to the library's piece type.
func ParsePromotion(s string) (PieceType, bool) {
	switch s {
	case "":
		return rules.NoPieceType, true
	case "q":
		return rules.Queen, true
	case "r":
		return rules.Rook, true
	case "b":
		return rules.Bishop, true
	case "n":
		return rules.Knight, true
	default:
		return rules.NoPieceType, false
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
