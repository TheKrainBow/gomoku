package main

import "testing"

func TestGameStopsBeforeTenthCaptureWhenThreatExists(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlack = false
	g := NewGame(settings)
	g.Start()

	// White already has 8 captures. If White plays at (3,4), it captures
	// the Black pair (4,4)-(5,4) thanks to the White stone at (6,4).
	g.state.CapturedWhite = 8
	g.state.Board.Set(4, 4, CellBlack)
	g.state.Board.Set(5, 4, CellBlack)
	g.state.Board.Set(6, 4, CellWhite)
	g.state.recomputeHashes()

	applied, reason := g.TryApplyMove(Move{X: 0, Y: 0})
	if !applied {
		t.Fatalf("expected move to be applied, got reason: %s", reason)
	}
	if g.state.Status != StatusWhiteWon {
		t.Fatalf("expected White to win via forced capture move, got status=%v", g.state.Status)
	}
	if len(g.state.WinningCapturePair) != 2 {
		t.Fatalf("expected 2 threatened stones, got %d", len(g.state.WinningCapturePair))
	}
	if !containsMove(g.state.WinningCapturePair, Move{X: 4, Y: 4}) || !containsMove(g.state.WinningCapturePair, Move{X: 5, Y: 4}) {
		t.Fatalf("expected threatened pair to be (4,4) and (5,4), got %+v", g.state.WinningCapturePair)
	}
	if g.state.Board.At(4, 4) != CellEmpty || g.state.Board.At(5, 4) != CellEmpty {
		t.Fatalf("expected threatened stones to be removed by forced capture move")
	}
	if g.state.Board.At(3, 4) != CellWhite {
		t.Fatalf("expected forced winning move at (3,4) to be played")
	}
	entries := g.history.All()
	if len(entries) != 2 {
		t.Fatalf("expected history to contain original + forced move, got %d", len(entries))
	}
	last := entries[len(entries)-1]
	if last.Player != PlayerWhite || !last.Move.Equals(Move{X: 3, Y: 4}) {
		t.Fatalf("expected last history move to be forced White capture at (3,4), got player=%v move=%+v", last.Player, last.Move)
	}
	if len(last.CapturedPositions) < 2 {
		t.Fatalf("expected forced history move to carry captured stones, got %+v", last.CapturedPositions)
	}
}

func TestGameDoesNotStopBeforeTenthCaptureWithoutEnoughCapturedPairs(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlack = false
	g := NewGame(settings)
	g.Start()

	g.state.CapturedWhite = 6
	g.state.Board.Set(4, 4, CellBlack)
	g.state.Board.Set(5, 4, CellBlack)
	g.state.Board.Set(6, 4, CellWhite)
	g.state.recomputeHashes()

	applied, reason := g.TryApplyMove(Move{X: 0, Y: 0})
	if !applied {
		t.Fatalf("expected move to be applied, got reason: %s", reason)
	}
	if g.state.Status != StatusRunning {
		t.Fatalf("expected game to keep running, got status=%v", g.state.Status)
	}
	if len(g.state.WinningCapturePair) != 0 {
		t.Fatalf("expected no threatened capture pair, got %+v", g.state.WinningCapturePair)
	}
}

func containsMove(moves []Move, target Move) bool {
	for _, move := range moves {
		if move.Equals(target) {
			return true
		}
	}
	return false
}
