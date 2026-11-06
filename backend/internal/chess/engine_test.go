// internal/chess/engine_test.go
package chess

import (
	"strings"
	"testing"

	rules "github.com/corentings/chess/v2"
)

func promo(t *testing.T, s string) PieceType {
	t.Helper()
	p, ok := ParsePromotion(s)
	if !ok {
		t.Fatalf("unexpected bad promotion %q", s)
	}
	return p
}

func mustGame(t *testing.T, fen string) *rules.Game {
	t.Helper()
	g, err := NewEngine().FromFEN(fen)
	if err != nil {
		t.Fatalf("FromFEN(%q): %v", fen, err)
	}
	return g
}

func TestValidMove(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, e.InitialFEN())

	am, err := e.ApplyMove(g, "e2", "e4", promo(t, ""))
	if err != nil {
		t.Fatalf("e4: %v", err)
	}
	if am.UCI != "e2e4" || am.SAN != "e4" {
		t.Fatalf("e4 notation = %q / %q", am.UCI, am.SAN)
	}
	if am.GivesCheck {
		t.Fatal("e4 should not give check")
	}
	if am.Result != "" || am.Method != "" {
		t.Fatalf("e4 should not end the game, got %q/%q", am.Result, am.Method)
	}
	if e.Turn(g) != ColorBlack {
		t.Fatalf("after e4 it should be black to move, got %q", e.Turn(g))
	}
}

func TestIllegalMove(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, e.InitialFEN())

	if _, err := e.ApplyMove(g, "e2", "e5", promo(t, "")); err != ErrIllegalMove {
		t.Fatalf("e2e5 should be illegal, got %v", err)
	}
}

func TestCheckmate(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, e.InitialFEN())

	// Fool's mate: 1.f3 e5 2.g4 Qh4#.
	for _, mv := range []struct{ o, d string }{
		{"f2", "f3"}, {"e7", "e5"}, {"g2", "g4"}, {"d8", "h4"},
	} {
		if _, err := e.ApplyMove(g, mv.o, mv.d, promo(t, "")); err != nil {
			t.Fatalf("%s%s: %v", mv.o, mv.d, err)
		}
	}

	// Re-apply the final move on a fresh game to read its result directly.
	g2 := mustGame(t, e.InitialFEN())
	for _, mv := range []struct{ o, d string }{
		{"f2", "f3"}, {"e7", "e5"}, {"g2", "g4"},
	} {
		if _, err := e.ApplyMove(g2, mv.o, mv.d, promo(t, "")); err != nil {
			t.Fatalf("%s%s: %v", mv.o, mv.d, err)
		}
	}
	last, err := e.ApplyMove(g2, "d8", "h4", promo(t, ""))
	if err != nil {
		t.Fatalf("Qh4#: %v", err)
	}
	if last.Result != ResultBlack || last.Method != MethodCheckmate {
		t.Fatalf("fool's mate result = %q/%q", last.Result, last.Method)
	}
	if !last.GivesCheck {
		t.Fatal("a checkmating move must be marked as giving check")
	}
}

func TestStalemate(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, "k1K5/8/8/8/8/8/8/1Q6 w - - 0 1")

	am, err := e.ApplyMove(g, "b1", "b6", promo(t, ""))
	if err != nil {
		t.Fatalf("Qb6: %v", err)
	}
	if am.Result != ResultDraw || am.Method != MethodStalemate {
		t.Fatalf("stalemate result = %q/%q", am.Result, am.Method)
	}
}

func TestCastling(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, "r3k2r/pppppppp/8/8/8/8/PPPPPPPP/R3K2R w KQkq - 0 1")

	am, err := e.ApplyMove(g, "e1", "g1", promo(t, ""))
	if err != nil {
		t.Fatalf("O-O: %v", err)
	}
	if am.UCI != "e1g1" || am.SAN != "O-O" {
		t.Fatalf("castling notation = %q / %q", am.UCI, am.SAN)
	}
}

func TestEnPassant(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, "4k3/3p4/8/4P3/8/8/8/4K3 b - - 0 1")

	if _, err := e.ApplyMove(g, "d7", "d5", promo(t, "")); err != nil {
		t.Fatalf("d7d5: %v", err)
	}
	am, err := e.ApplyMove(g, "e5", "d6", promo(t, ""))
	if err != nil {
		t.Fatalf("exd6 e.p.: %v", err)
	}
	if am.UCI != "e5d6" {
		t.Fatalf("en passant UCI = %q", am.UCI)
	}
}

func TestPromotion(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, "k7/7P/8/8/8/8/8/7K w - - 0 1")

	if _, err := e.ApplyMove(g, "h7", "h8", promo(t, "")); err != ErrPromotionRequired {
		t.Fatalf("h8 without a piece should require promotion, got %v", err)
	}

	g2 := mustGame(t, "k7/7P/8/8/8/8/8/7K w - - 0 1")
	am, err := e.ApplyMove(g2, "h7", "h8", promo(t, "q"))
	if err != nil {
		t.Fatalf("h8=Q: %v", err)
	}
	if am.UCI != "h7h8q" || !strings.HasPrefix(am.SAN, "h8=Q") {
		t.Fatalf("promotion notation = %q / %q", am.UCI, am.SAN)
	}
}

func TestInsufficientMaterial(t *testing.T) {
	// King vs king is an automatic draw, detected on game construction.
	g := mustGame(t, "8/2k5/8/8/8/3K4/8/8 w - - 0 1")
	if g.Outcome() != rules.Draw || g.Method() != rules.InsufficientMaterial {
		t.Fatalf("king vs king = %q/%q", g.Outcome(), g.Method())
	}
}

func TestLegalDests(t *testing.T) {
	e := NewEngine()
	g := mustGame(t, e.InitialFEN())

	dests := e.LegalDests(g)
	if len(dests) != 10 {
		t.Fatalf("starting position has 10 movable squares, got %d", len(dests))
	}
	total := 0
	for _, ds := range dests {
		total += len(ds)
	}
	if total != 20 {
		t.Fatalf("starting position has 20 legal moves, got %d", total)
	}
}
