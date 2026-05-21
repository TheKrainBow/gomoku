package main

import (
	"testing"

	"gomoku-backend/internal/ai/threatlut"
)

func TestCollectThreatLUTImpactsFindsOpenThreeExtensions(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	evalState := BuildEvalStateFromBoard(state.Board, state.ToMove, 0, 0, DefaultConfig())

	impacts := collectThreatLUTImpacts(state, PlayerBlue, settings.BoardSize, &evalState)
	foundLeft := false
	foundRight := false
	for _, impact := range impacts {
		if impact.Pos == (Move{X: 1, Y: 4}) {
			foundLeft = impact.OffensiveScore > 0
		}
		if impact.Pos == (Move{X: 5, Y: 4}) {
			foundRight = impact.OffensiveScore > 0
		}
	}
	if !foundLeft || !foundRight {
		t.Fatalf("expected both open-three extensions in impacts, got %#v", impacts)
	}
}

func TestBuildThreatLUTCandidatesSkipsOccupiedAndStaysInBounds(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 7
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(0, 3, CellRed)
	state.Board.Set(1, 3, CellBlue)
	state.Board.Set(2, 3, CellBlue)
	state.Board.Set(3, 3, CellBlue)
	evalState := BuildEvalStateFromBoard(state.Board, state.ToMove, 0, 0, DefaultConfig())

	candidates := buildThreatLUTCandidates(state, PlayerBlue, settings.BoardSize, &evalState, DefaultConfig())
	for _, cand := range candidates {
		if !cand.move.IsValid(settings.BoardSize) {
			t.Fatalf("candidate out of bounds: %#v", cand)
		}
		if !state.Board.IsEmpty(cand.move.X, cand.move.Y) {
			t.Fatalf("candidate on occupied cell: %#v", cand)
		}
	}
}

func TestThreatLUTLookupCanonicalEdgeBlocking(t *testing.T) {
	key := threatlut.EncodeCanonicalWindow([]byte("..MM.OO"))
	entry, ok := threatlut.LookupThreatWindow(key)
	if !ok {
		t.Fatalf("expected lookup success")
	}
	if entry.EmptyMask == 0 {
		t.Fatalf("expected empty mask")
	}
}

func BenchmarkCollectThreatLUTImpacts(b *testing.B) {
	state, _, settings := buildRedToPlaySixVsFourCapturesFixedState()
	evalState := BuildEvalStateFromBoard(state.Board, state.ToMove, clampUint8(state.CapturedBlue), clampUint8(state.CapturedRed), DefaultConfig())
	for i := 0; i < b.N; i++ {
		_ = collectThreatLUTImpacts(state, state.ToMove, settings.BoardSize, &evalState)
	}
}
