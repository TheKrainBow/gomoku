package main

import "testing"

func TestEvaluateMustBlockOpenFour(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	board := state.Board
	// Opponent (white) has open four: .OOOO.
	board.Set(1, 0, CellWhite)
	board.Set(2, 0, CellWhite)
	board.Set(3, 0, CellWhite)
	board.Set(4, 0, CellWhite)
	state.Board = board

	score := EvaluateBoard(state.Board, PlayerBlack, DefaultConfig())
	if score > -800000.0 {
		t.Fatalf("expected strong negative score for must-block open four, got %f", score)
	}
}

func TestEvaluateImmediateWinOpenFour(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	board := state.Board
	// Me (black) has open four: .MMMM.
	board.Set(1, 0, CellBlack)
	board.Set(2, 0, CellBlack)
	board.Set(3, 0, CellBlack)
	board.Set(4, 0, CellBlack)
	state.Board = board

	score := EvaluateBoard(state.Board, PlayerBlack, DefaultConfig())
	if score < 800000.0 {
		t.Fatalf("expected strong positive score for open four, got %f", score)
	}
}

func TestEvaluateWinFive(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	board := state.Board
	board.Set(0, 0, CellBlack)
	board.Set(1, 0, CellBlack)
	board.Set(2, 0, CellBlack)
	board.Set(3, 0, CellBlack)
	board.Set(4, 0, CellBlack)
	state.Board = board

	score := EvaluateBoard(state.Board, PlayerBlack, DefaultConfig())
	if score < evalInf {
		t.Fatalf("expected win score for five in row, got %f", score)
	}
}
