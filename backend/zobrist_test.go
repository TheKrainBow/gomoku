package main

import "testing"

func TestHashIncludesCapturesAndSide(t *testing.T) {
	settings := DefaultGameSettings()
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.Board.Set(0, 0, CellBlack)
	state.recomputeHashes()

	state2 := state.Clone()
	state2.CapturedBlack = 2
	state2.recomputeHashes()
	if state.Hash == state2.Hash {
		t.Fatalf("expected hash to differ for different capture counts")
	}

	state3 := state.Clone()
	state3.ToMove = otherPlayer(state3.ToMove)
	state3.recomputeHashes()
	if state.Hash == state3.Hash {
		t.Fatalf("expected hash to differ for different side to move")
	}
}

func TestApplyMoveUpdatesHash(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.Board.Set(0, 0, CellBlack)
	state.Board.Set(1, 0, CellWhite)
	state.Board.Set(2, 0, CellWhite)
	state.recomputeHashes()

	move := Move{X: 3, Y: 0}
	if !applyMove(&state, rules, move, state.ToMove) {
		t.Fatalf("expected move to be legal")
	}
	expected, _ := computeSymmetricHashes(state)
	if state.Hash != expected {
		t.Fatalf("hash mismatch after apply move: got %d want %d", state.Hash, expected)
	}
}

func TestApplyUndoRestoresHash(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.Board.Set(0, 0, CellBlack)
	state.Board.Set(1, 0, CellWhite)
	state.Board.Set(2, 0, CellWhite)
	state.recomputeHashes()
	originalHash := state.Hash
	prevCapturedBlack := state.CapturedBlack
	prevCapturedWhite := state.CapturedWhite
	prevToMove := state.ToMove

	move := Move{X: 3, Y: 0}
	cell := CellFromPlayer(prevToMove)
	captures := rules.FindCaptures(state.Board, move, cell)
	state.Board.Set(move.X, move.Y, cell)
	for _, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
	}
	capturedCount := len(captures)
	if prevToMove == PlayerBlack {
		state.CapturedBlack += capturedCount
	} else {
		state.CapturedWhite += capturedCount
	}
	state.ToMove = otherPlayer(prevToMove)
	UpdateHashAfterMove(&state, move, prevToMove, captures, prevToMove, prevCapturedBlack, prevCapturedWhite)

	state.Board.Remove(move.X, move.Y)
	for _, captured := range captures {
		state.Board.Set(captured.X, captured.Y, CellFromPlayer(otherPlayer(prevToMove)))
	}
	state.CapturedBlack = prevCapturedBlack
	state.CapturedWhite = prevCapturedWhite
	state.ToMove = prevToMove
	state.recomputeHashes()

	if state.Hash != originalHash {
		t.Fatalf("hash mismatch after undo: got %d want %d", state.Hash, originalHash)
	}
}
