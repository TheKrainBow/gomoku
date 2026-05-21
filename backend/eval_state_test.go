package main

import "testing"

func TestEvaluateBoardReturnsRichSnapshotForOpenFour(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9

	board := NewBoard(settings.BoardSize)
	board.Set(1, 0, CellRed)
	board.Set(2, 0, CellRed)
	board.Set(3, 0, CellRed)
	board.Set(4, 0, CellRed)

	result := EvaluateBoard(board, PlayerRed, DefaultConfig())
	if result.Score <= 0 {
		t.Fatalf("expected positive score for red open four, got %d", result.Score)
	}
	if result.Summary.Open4Red == 0 {
		t.Fatalf("expected open four summary for red, got %#v", result.Summary)
	}
	if !result.Summary.IsTactical {
		t.Fatalf("expected open four position to be tactical")
	}
	if result.ThreatCount == 0 {
		t.Fatalf("expected compact threat list for tactical snapshot")
	}
	if !containsThreatType(result.Threats[:int(result.ThreatCount)], ThreatOpen4) {
		t.Fatalf("expected open four threat in compact threat list, got %#v", result.Threats[:int(result.ThreatCount)])
	}
}

func TestEvaluateBoardTreatsOpenThreeAsMustAnswerButNotTactical(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9

	board := NewBoard(settings.BoardSize)
	board.Set(2, 4, CellBlue)
	board.Set(3, 4, CellBlue)
	board.Set(4, 4, CellBlue)

	result := EvaluateBoard(board, PlayerBlue, DefaultConfig())
	if result.Summary.Open3Blue == 0 {
		t.Fatalf("expected open three summary for blue, got %#v", result.Summary)
	}
	if !result.Summary.MustAnswerForRed {
		t.Fatalf("expected lone open three to require an answer from red, got %#v", result.Summary)
	}
	if result.Summary.IsTactical {
		t.Fatalf("expected lone open three to stay out of tactical mode")
	}
	if result.ThreatCount == 0 {
		t.Fatalf("expected compact threat list to include open three on must-answer snapshot")
	}
	if !containsThreatType(result.Threats[:int(result.ThreatCount)], ThreatOpen3) {
		t.Fatalf("expected open three threat in compact threat list, got %#v", result.Threats[:int(result.ThreatCount)])
	}
}

func TestEvalStateApplyMoveUndoMatchesRebuildWithoutCaptures(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	cfg := DefaultConfig()

	board := NewBoard(settings.BoardSize)
	board.Set(3, 4, CellBlue)
	board.Set(4, 4, CellBlue)
	board.Set(6, 6, CellRed)
	initialBoard := board.Clone()

	state := BuildEvalStateFromBoard(board, PlayerBlue, 0, 0, cfg)
	initialSnapshot := state.Snapshot(&initialBoard)

	move := Move{X: 5, Y: 4}
	board.Set(move.X, move.Y, CellBlue)
	undo := state.ApplyMove(&board, MoveDelta{
		Move:   move,
		Player: PlayerBlue,
	})

	expected := BuildEvalStateFromBoard(board, PlayerRed, 0, 0, cfg)
	if state.ScoreOnly() != expected.ScoreOnly() {
		t.Fatalf("expected incremental score %d to match rebuild %d", state.ScoreOnly(), expected.ScoreOnly())
	}
	if state.Summary != expected.Summary {
		t.Fatalf("expected incremental summary %#v to match rebuild %#v", state.Summary, expected.Summary)
	}

	state.UndoMove(undo)
	if state.ScoreOnly() != initialSnapshot.Score {
		t.Fatalf("expected undo score %d to restore initial %d", state.ScoreOnly(), initialSnapshot.Score)
	}
	if state.Summary != initialSnapshot.Summary {
		t.Fatalf("expected undo summary %#v to restore initial %#v", state.Summary, initialSnapshot.Summary)
	}
}

func TestEvalStateApplyMoveUndoMatchesRebuildWithCapture(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	cfg := DefaultConfig()
	rules := NewRules(settings)

	board := NewBoard(settings.BoardSize)
	board.Set(1, 0, CellBlue)
	board.Set(2, 0, CellRed)
	board.Set(3, 0, CellRed)
	initialBoard := board.Clone()

	state := BuildEvalStateFromBoard(board, PlayerBlue, 0, 0, cfg)
	initialSnapshot := state.Snapshot(&initialBoard)

	move := Move{X: 4, Y: 0}
	board.Set(move.X, move.Y, CellBlue)
	captures := rules.FindCapturesInto(board, move, CellBlue, nil)
	if len(captures) != 2 {
		t.Fatalf("expected one capture pair (2 stones), got %d stones", len(captures))
	}
	delta := MoveDelta{
		Move:               move,
		Player:             PlayerBlue,
		CapturedCount:      uint8(len(captures)),
		CapturePairsGained: uint8(len(captures) / 2),
	}
	for i, captured := range captures {
		board.Remove(captured.X, captured.Y)
		delta.CapturedCells[i] = CellIndex(captured.Y*board.Size() + captured.X)
	}

	undo := state.ApplyMove(&board, delta)
	expected := BuildEvalStateFromBoard(board, PlayerRed, 2, 0, cfg)
	if state.ScoreOnly() != expected.ScoreOnly() {
		t.Fatalf("expected incremental capture score %d to match rebuild %d", state.ScoreOnly(), expected.ScoreOnly())
	}
	if state.Summary != expected.Summary {
		t.Fatalf("expected incremental capture summary %#v to match rebuild %#v", state.Summary, expected.Summary)
	}
	if state.BlueCaptures != 2 {
		t.Fatalf("expected blue capture counter to be 2 stones, got %d", state.BlueCaptures)
	}

	state.UndoMove(undo)
	if state.ScoreOnly() != initialSnapshot.Score {
		t.Fatalf("expected undo score %d to restore initial %d", state.ScoreOnly(), initialSnapshot.Score)
	}
	if state.Summary != initialSnapshot.Summary {
		t.Fatalf("expected undo summary %#v to restore initial %#v", state.Summary, initialSnapshot.Summary)
	}
	if state.BlueCaptures != 0 || state.RedCaptures != 0 {
		t.Fatalf("expected undo captures to restore 0/0, got %d/%d", state.BlueCaptures, state.RedCaptures)
	}
}

func TestEvaluateBoardReturnsCriticalCaptureThreats(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9

	board := NewBoard(settings.BoardSize)
	board.Set(1, 0, CellBlue)
	board.Set(2, 0, CellRed)
	board.Set(3, 0, CellRed)

	result := EvaluateBoardWithContext(board, PlayerBlue, 8, 0, DefaultConfig())
	if result.Summary.CriticalCapturesBlue == 0 {
		t.Fatalf("expected critical blue capture count, got %#v", result.Summary)
	}
	if !result.Summary.IsTactical {
		t.Fatalf("expected critical capture to mark the position tactical")
	}
	if result.CaptureThreatCount == 0 {
		t.Fatalf("expected explicit capture threats in tactical snapshot")
	}
	found := false
	for i := 0; i < int(result.CaptureThreatCount); i++ {
		threat := result.CaptureThreats[i]
		if threat.Owner == PlayerBlue && int(threat.Move) == 4 {
			found = true
			if !threat.GivesImmediateWin {
				t.Fatalf("expected capture at cell 4 to be immediate capture win: %#v", threat)
			}
		}
	}
	if !found {
		t.Fatalf("expected capture threat for move (4,0), got %#v", result.CaptureThreats[:int(result.CaptureThreatCount)])
	}
}

func TestEvalStateCaptureMovesMatchScanAcrossApplyUndo(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	cfg := DefaultConfig()

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(0, 0, CellBlue)
	state.Board.Set(1, 0, CellRed)
	state.recomputeHashes()

	evalState := BuildEvalStateFromBoard(state.Board, state.ToMove, 0, 0, cfg)
	initialMoves := findCaptureMoves(state, rules, PlayerBlue, &evalState)
	if len(initialMoves) != 0 {
		t.Fatalf("expected no initial blue capture moves, got %v", initialMoves)
	}

	move := Move{X: 2, Y: 0}
	state.Board.Set(move.X, move.Y, CellRed)
	delta := MoveDelta{Move: move, Player: PlayerRed}
	undo := evalState.ApplyMove(&state.Board, delta)
	state.ToMove = PlayerBlue
	state.recomputeHashes()

	got := findCaptureMoves(state, rules, PlayerBlue, &evalState)
	want := findCaptureMovesByScan(state, rules, PlayerBlue)
	if len(got) != len(want) || (len(got) == 1 && !got[0].Equals(want[0])) {
		t.Fatalf("expected incremental capture moves %v to match scan %v", got, want)
	}
	if len(got) != 1 || !got[0].Equals(Move{X: 3, Y: 0}) {
		t.Fatalf("expected blue capture move at (3,0), got %v", got)
	}

	evalState.UndoMove(undo)
	state.Board.Remove(move.X, move.Y)
	state.ToMove = PlayerRed
	state.recomputeHashes()
	restored := findCaptureMoves(state, rules, PlayerBlue, &evalState)
	if len(restored) != 0 {
		t.Fatalf("expected undo to restore no blue capture moves, got %v", restored)
	}
}

func TestEvaluateBoardCaptureProgressAffectsScore(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9

	board := NewBoard(settings.BoardSize)

	neutral := EvaluateBoardWithContext(board, PlayerRed, 0, 0, DefaultConfig())
	blueAhead := EvaluateBoardWithContext(board, PlayerRed, 8, 0, DefaultConfig())
	redAhead := EvaluateBoardWithContext(board, PlayerRed, 0, 8, DefaultConfig())

	if neutral.CaptureScore != 0 {
		t.Fatalf("expected neutral empty board capture score to be 0, got %d", neutral.CaptureScore)
	}
	if blueAhead.CaptureScore >= 0 {
		t.Fatalf("expected blue capture lead to favor blue (negative score), got %d", blueAhead.CaptureScore)
	}
	if redAhead.CaptureScore <= 0 {
		t.Fatalf("expected red capture lead to favor red (positive score), got %d", redAhead.CaptureScore)
	}
}

func TestEvaluateBoardPenalizesCapturablePairs(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9

	safe := NewBoard(settings.BoardSize)
	hanging := NewBoard(settings.BoardSize)
	hanging.Set(1, 4, CellBlue)
	hanging.Set(2, 4, CellBlue)
	hanging.Set(3, 4, CellRed)

	safeEval := EvaluateBoardWithContext(safe, PlayerRed, 0, 0, DefaultConfig())
	hangingEval := EvaluateBoardWithContext(hanging, PlayerRed, 0, 0, DefaultConfig())

	if hangingEval.CaptureScore <= safeEval.CaptureScore {
		t.Fatalf("expected hanging blue pair to favor red via capture score, safe=%d hanging=%d", safeEval.CaptureScore, hangingEval.CaptureScore)
	}
}

func containsThreatType(threats []Threat, typ ThreatType) bool {
	for _, threat := range threats {
		if threat.Type == typ {
			return true
		}
	}
	return false
}

func containsThreatExtension(threats []Threat, x, y int) bool {
	for _, threat := range threats {
		for _, pos := range threat.ExtensionSquares {
			if pos.X == x && pos.Y == y {
				return true
			}
		}
	}
	return false
}
