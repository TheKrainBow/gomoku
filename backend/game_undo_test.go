package main

import "testing"

func TestGameUndoToHistoryIndexRestoresBoardAndTurn(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BlueType = PlayerHuman
	settings.RedType = PlayerHuman

	g := NewGame(settings)
	g.Start()

	moves := []Move{
		{X: 9, Y: 9},
		{X: 10, Y: 9},
		{X: 9, Y: 10},
		{X: 10, Y: 10},
	}
	for _, move := range moves {
		applied, reason := g.TryApplyMove(move)
		if !applied {
			t.Fatalf("expected move %+v to apply: %s", move, reason)
		}
	}

	if ok, reason := g.UndoToHistoryIndex(1); !ok {
		t.Fatalf("expected undo to succeed: %s", reason)
	}

	if g.history.Size() != 2 {
		t.Fatalf("expected 2 history entries after undo, got %d", g.history.Size())
	}
	if g.state.Board.At(9, 9) != CellBlue || g.state.Board.At(10, 9) != CellRed {
		t.Fatalf("expected first two stones to remain on board")
	}
	if g.state.Board.At(9, 10) != CellEmpty || g.state.Board.At(10, 10) != CellEmpty {
		t.Fatalf("expected later stones to be removed from board")
	}
	if g.state.ToMove != PlayerBlue {
		t.Fatalf("expected blue to move after undo, got %v", g.state.ToMove)
	}
	if !g.state.HasLastMove || !g.state.LastMove.Equals(Move{X: 10, Y: 9}) {
		t.Fatalf("expected last move to be restored to second move, got %+v", g.state.LastMove)
	}
	if g.state.Status != StatusRunning {
		t.Fatalf("expected game to be running after undo, got %v", g.state.Status)
	}
}

func TestGameUndoToHistoryIndexRestoresCapturedStones(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.BlueType = PlayerHuman
	settings.RedType = PlayerHuman
	settings.ForbidDoubleThreeBlue = false

	g := NewGame(settings)
	g.Start()

	moves := []Move{
		{X: 0, Y: 1},
		{X: 1, Y: 1},
		{X: 0, Y: 2},
		{X: 2, Y: 1},
		{X: 3, Y: 1},
	}
	for _, move := range moves {
		applied, reason := g.TryApplyMove(move)
		if !applied {
			t.Fatalf("expected move %+v to apply: %s", move, reason)
		}
	}

	if g.state.Board.At(1, 1) != CellEmpty || g.state.Board.At(2, 1) != CellEmpty {
		t.Fatalf("expected red stones to be captured before undo")
	}
	if g.state.CapturedBlue != 2 {
		t.Fatalf("expected blue capture count to be 2 before undo, got %d", g.state.CapturedBlue)
	}

	if ok, reason := g.UndoToHistoryIndex(3); !ok {
		t.Fatalf("expected undo to succeed: %s", reason)
	}

	if g.state.Board.At(3, 1) != CellEmpty {
		t.Fatalf("expected capturing stone to be removed after undo")
	}
	if g.state.Board.At(1, 1) != CellRed || g.state.Board.At(2, 1) != CellRed {
		t.Fatalf("expected captured stones to be restored after undo")
	}
	if g.state.CapturedBlue != 0 {
		t.Fatalf("expected blue capture count to be restored to 0 after undo, got %d", g.state.CapturedBlue)
	}
	if g.state.ToMove != PlayerBlue {
		t.Fatalf("expected blue to replay the undone move, got %v", g.state.ToMove)
	}
}
