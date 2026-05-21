package main

import "testing"

func TestGameApplyBoardSnapshotSetsBoardState(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BlueType = PlayerHuman
	settings.RedType = PlayerHuman

	g := NewGame(settings)
	g.Start()

	ok, reason := g.ApplyBoardSnapshot(applyBoardPayload{
		Cells: []applyBoardCellPayload{
			{X: 9, Y: 9, Cell: "CellBlue", MoveNumber: 1},
			{X: 10, Y: 9, Cell: "CellRed", MoveNumber: 2},
			{X: 9, Y: 10, Cell: "CellBlue", MoveNumber: 3},
		},
		NextPlayer:    2,
		CapturedBlue: 2,
		CapturedRed: 4,
	})
	if !ok {
		t.Fatalf("expected apply board snapshot to succeed: %s", reason)
	}

	if g.state.Board.At(9, 9) != CellBlue || g.state.Board.At(10, 9) != CellRed || g.state.Board.At(9, 10) != CellBlue {
		t.Fatalf("expected board to be populated from payload")
	}
	if g.state.ToMove != PlayerRed {
		t.Fatalf("expected red to move, got %v", g.state.ToMove)
	}
	if g.state.CapturedBlue != 2 || g.state.CapturedRed != 4 {
		t.Fatalf("expected capture counters to be set, got blue=%d red=%d", g.state.CapturedBlue, g.state.CapturedRed)
	}
	if g.history.Size() != 3 {
		t.Fatalf("expected synthetic history to contain 3 entries, got %d", g.history.Size())
	}
	if !g.state.HasLastMove || !g.state.LastMove.Equals(Move{X: 9, Y: 10}) {
		t.Fatalf("expected last move to follow highest move number, got %+v", g.state.LastMove)
	}
	if g.state.Status != StatusRunning {
		t.Fatalf("expected running status after apply, got %v", g.state.Status)
	}
}
