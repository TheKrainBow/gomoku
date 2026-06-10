package main

import "testing"

func TestForbiddenMovesForStateIncludesDoubleThreeCells(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = true
	settings.ForbidDoubleThreeRed = false

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.Board.Set(4, 3, CellBlue)
	state.Board.Set(4, 5, CellBlue)

	moves := forbiddenMovesForState(state, settings)
	if !containsMove(moves, Move{X: 4, Y: 4}) {
		t.Fatalf("expected double-three cell (4,4) to be forbidden, got %+v", moves)
	}
}

func TestMustPlayMovesForStateReturnsForcedCaptures(t *testing.T) {
	state := DefaultGameState(DefaultGameSettings())
	state.Status = StatusRunning
	state.MustCapture = true
	state.ForcedCaptureMoves = []Move{{X: 6, Y: 10}, {X: 2, Y: 4}}

	moves := mustPlayMovesForState(state)
	if len(moves) != 2 || !containsMove(moves, Move{X: 6, Y: 10}) || !containsMove(moves, Move{X: 2, Y: 4}) {
		t.Fatalf("expected forced capture moves to be exposed, got %+v", moves)
	}
}
