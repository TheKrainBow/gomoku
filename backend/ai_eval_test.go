package main

import (
	"fmt"
	"math"
	"testing"
)

func TestEvaluateMustBlockOpenFour(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	board := state.Board
	// Opponent (red) has open four: .OOOO.
	board.Set(1, 0, CellRed)
	board.Set(2, 0, CellRed)
	board.Set(3, 0, CellRed)
	board.Set(4, 0, CellRed)
	state.Board = board

	score := EvaluateBoardScore(state.Board, PlayerRed, DefaultConfig())
	if score <= 0.0 {
		t.Fatalf("expected positive score for red open four, got %f", score)
	}
}

func TestEvaluateImmediateWinOpenFour(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	board := state.Board
	// Me (blue) has open four: .MMMM.
	board.Set(1, 0, CellBlue)
	board.Set(2, 0, CellBlue)
	board.Set(3, 0, CellBlue)
	board.Set(4, 0, CellBlue)
	state.Board = board

	score := EvaluateBoardScore(state.Board, PlayerRed, DefaultConfig())
	if score >= 0.0 {
		t.Fatalf("expected negative score for blue open four, got %f", score)
	}
}

func TestEvaluateWinFive(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	board := state.Board
	board.Set(0, 0, CellBlue)
	board.Set(1, 0, CellBlue)
	board.Set(2, 0, CellBlue)
	board.Set(3, 0, CellBlue)
	board.Set(4, 0, CellBlue)
	state.Board = board

	score := EvaluateBoardScore(state.Board, PlayerRed, DefaultConfig())
	if score > -evalInf {
		t.Fatalf("expected blue win score, got %f", score)
	}
}

func TestEvalBoardAfterMoveMatchesFullDeltaWithoutCaptures(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellRed)
	before := EvaluateBoardScore(state.Board, PlayerRed, DefaultConfig())
	delta := EvalBoardAfterMove(state.Board, DefaultConfig(), PlayerBlue, 2, 4)
	state.Board.Set(2, 4, CellBlue)
	after := EvaluateBoardScore(state.Board, PlayerRed, DefaultConfig())
	if diff := math.Abs((before + delta) - after); diff > 1e-9 {
		t.Fatalf("expected delta API to match full evaluation, diff=%g", diff)
	}
}

func TestIncrementalBoardEvaluatorMatchesFullEvaluation(t *testing.T) {
	cases := []struct {
		name     string
		scenario string
		moves    int
	}{
		{name: "empty-10", scenario: benchmarkScenarioEmpty, moves: 10},
		{name: "empty-100", scenario: benchmarkScenarioEmpty, moves: 100},
		{name: "complex-10", scenario: benchmarkScenarioComplex, moves: 10},
		{name: "complex-100", scenario: benchmarkScenarioComplex, moves: 100},
		{name: "captures-10", scenario: benchmarkScenarioCaptures, moves: 10},
		{name: "captures-100", scenario: benchmarkScenarioCaptures, moves: 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evalCase := buildEvalReplayCase(t, tc.scenario, tc.moves)
			evalState := BuildEvalStateFromBoard(
				evalCase.initialBoard,
				PlayerRed,
				clampUint8(evalCase.initialCapturedBlue),
				clampUint8(evalCase.initialCapturedRed),
				evalCase.config,
			)
			initialScore := float64(evalState.ScoreOnly())
			stepScores := 0.0

			for stepIndex, step := range evalCase.steps {
				boardAfter := step.board.Clone()
				stepScores += applyReplayStepToEvalState(&evalState, &boardAfter, step)

				fullScore := float64(EvaluateBoardWithContext(
					step.board,
					PlayerRed,
					clampUint8(step.capturedBlue),
					clampUint8(step.capturedRed),
					evalCase.config,
				).Score)
				if diff := math.Abs(float64(evalState.ScoreOnly()) - fullScore); diff > 1e-9 {
					t.Fatalf("step %d score mismatch: incremental=%f full=%f diff=%g", stepIndex, float64(evalState.ScoreOnly()), fullScore, diff)
				}
			}

			finalScore := float64(EvaluateBoardWithContext(
				evalCase.finalBoard,
				PlayerRed,
				clampUint8(evalCase.finalCapturedBlue),
				clampUint8(evalCase.finalCapturedRed),
				evalCase.config,
			).Score)
			if diff := math.Abs((initialScore + stepScores) - finalScore); diff > 1e-9 {
				t.Fatalf("delta accumulation mismatch: initial=%f delta=%f final=%f diff=%g", initialScore, stepScores, finalScore, diff)
			}
		})
	}
}

const (
	benchmarkScenarioEmpty    = "empty"
	benchmarkScenarioComplex  = "complex"
	benchmarkScenarioCaptures = "captures"
)

type replayPlannedMove struct {
	move   Move
	player PlayerColor
}

type evalReplayStep struct {
	move          Move
	player        PlayerColor
	captures      []Move
	board         Board
	capturedBlue int
	capturedRed int
}

type evalReplayCase struct {
	initialBoard         Board
	finalBoard           Board
	steps                []evalReplayStep
	config               Config
	initialCapturedBlue int
	initialCapturedRed int
	finalCapturedBlue   int
	finalCapturedRed   int
}

func buildEvalReplayCase(tb testing.TB, scenario string, moveCount int) evalReplayCase {
	tb.Helper()

	cfg := DefaultConfig()
	settings := DefaultGameSettings()
	settings.BoardSize = 25
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	rules := NewRules(settings)

	applySeedScenario(tb, &state, rules, scenario)
	initialBoard := state.Board.Clone()
	initialCapturedBlue := state.CapturedBlue
	initialCapturedRed := state.CapturedRed

	orderedMoves := orderedScenarioMoves(settings.BoardSize, scenario)
	steps := make([]evalReplayStep, 0, moveCount)

	nextPlayer := state.ToMove
	for _, move := range orderedMoves {
		if len(steps) >= moveCount {
			break
		}
		captures, ok := applyReplayMove(&state, rules, move, nextPlayer)
		if !ok {
			continue
		}
		steps = append(steps, evalReplayStep{
			move:          move,
			player:        nextPlayer,
			captures:      captures,
			board:         state.Board.Clone(),
			capturedBlue: state.CapturedBlue,
			capturedRed: state.CapturedRed,
		})
		nextPlayer = otherPlayer(nextPlayer)
	}

	if len(steps) != moveCount {
		tb.Fatalf("scenario %q generated %d moves, expected %d", scenario, len(steps), moveCount)
	}

	return evalReplayCase{
		initialBoard:         initialBoard,
		finalBoard:           state.Board.Clone(),
		steps:                steps,
		config:               cfg,
		initialCapturedBlue: initialCapturedBlue,
		initialCapturedRed: initialCapturedRed,
		finalCapturedBlue:   state.CapturedBlue,
		finalCapturedRed:   state.CapturedRed,
	}
}

func applySeedScenario(tb testing.TB, state *GameState, rules Rules, scenario string) {
	tb.Helper()

	var seed []replayPlannedMove
	switch scenario {
	case benchmarkScenarioEmpty:
		return
	case benchmarkScenarioComplex:
		player := state.ToMove
		for _, move := range []Move{
			{8, 8, 0}, {9, 8, 0}, {10, 8, 0}, {11, 8, 0}, {12, 8, 0},
			{8, 9, 0}, {10, 9, 0}, {12, 9, 0}, {14, 9, 0}, {16, 9, 0},
			{7, 10, 0}, {8, 10, 0}, {9, 10, 0}, {10, 10, 0}, {11, 10, 0}, {12, 10, 0}, {13, 10, 0}, {14, 10, 0},
			{9, 11, 0}, {11, 11, 0}, {13, 11, 0}, {15, 11, 0},
			{8, 12, 0}, {9, 12, 0}, {10, 12, 0}, {12, 12, 0}, {13, 12, 0}, {14, 12, 0}, {15, 12, 0},
			{7, 13, 0}, {9, 13, 0}, {11, 13, 0}, {13, 13, 0}, {15, 13, 0},
			{8, 14, 0}, {10, 14, 0}, {12, 14, 0}, {14, 14, 0},
			{9, 15, 0}, {10, 15, 0}, {11, 15, 0}, {12, 15, 0}, {13, 15, 0},
		} {
			seed = append(seed, replayPlannedMove{move: move, player: player})
			player = otherPlayer(player)
		}
	case benchmarkScenarioCaptures:
		seed = captureScenarioSeeds(state.Board.Size())
	default:
		tb.Fatalf("unknown scenario %q", scenario)
	}

	for _, planned := range seed {
		if _, ok := applyReplayMove(state, rules, planned.move, planned.player); !ok {
			tb.Fatalf("failed to seed scenario %q with move %+v for player %v", scenario, planned.move, planned.player)
		}
	}
}

func orderedScenarioMoves(boardSize int, scenario string) []Move {
	moves := make([]Move, 0, boardSize*boardSize)
	switch scenario {
	case benchmarkScenarioEmpty:
		center := boardSize / 2
		for radius := 0; radius < boardSize; radius++ {
			min := center - radius
			max := center + radius
			if min < 0 {
				min = 0
			}
			if max >= boardSize {
				max = boardSize - 1
			}
			for y := min; y <= max; y++ {
				for x := min; x <= max; x++ {
					if maxAbs(x-center, y-center) != radius {
						continue
					}
					moves = append(moves, Move{X: x, Y: y})
				}
			}
		}
	case benchmarkScenarioComplex:
		center := boardSize / 2
		for radius := 0; radius < boardSize; radius++ {
			for dx := -radius; dx <= radius; dx++ {
				xTop := center + dx
				yTop := center - radius
				xBottom := center - dx
				yBottom := center + radius
				if inBoard(boardSize, xTop, yTop) {
					moves = append(moves, Move{X: xTop, Y: yTop})
				}
				if radius > 0 && inBoard(boardSize, xBottom, yBottom) {
					moves = append(moves, Move{X: xBottom, Y: yBottom})
				}
			}
			for dy := -radius + 1; dy <= radius-1; dy++ {
				xRight := center + radius
				yRight := center + dy
				xLeft := center - radius
				yLeft := center - dy
				if radius > 0 && inBoard(boardSize, xRight, yRight) {
					moves = append(moves, Move{X: xRight, Y: yRight})
				}
				if radius > 0 && inBoard(boardSize, xLeft, yLeft) {
					moves = append(moves, Move{X: xLeft, Y: yLeft})
				}
			}
		}
	case benchmarkScenarioCaptures:
		for _, planned := range captureScenarioSeeds(boardSize) {
			if planned.player == PlayerBlue {
				moves = append(moves, Move{X: planned.move.X + 3, Y: planned.move.Y})
			} else {
				moves = append(moves, Move{X: planned.move.X + 3, Y: planned.move.Y})
			}
		}
		moves = append(moves, orderedScenarioMoves(boardSize, benchmarkScenarioComplex)...)
		moves = append(moves, orderedScenarioMoves(boardSize, benchmarkScenarioEmpty)...)
	}
	return dedupeMoves(moves, boardSize)
}

func captureScenarioSeeds(boardSize int) []replayPlannedMove {
	seeds := make([]replayPlannedMove, 0, 45*3)
	motifIndex := 0
	for y := 2; y < boardSize-2 && motifIndex < 45; y += 2 {
		for x := 1; x < boardSize-4 && motifIndex < 45; x += 5 {
			if motifIndex%2 == 0 {
				seeds = append(seeds,
					replayPlannedMove{move: Move{X: x, Y: y}, player: PlayerBlue},
					replayPlannedMove{move: Move{X: x + 1, Y: y}, player: PlayerRed},
					replayPlannedMove{move: Move{X: x + 2, Y: y}, player: PlayerRed},
				)
			} else {
				seeds = append(seeds,
					replayPlannedMove{move: Move{X: x, Y: y}, player: PlayerRed},
					replayPlannedMove{move: Move{X: x + 1, Y: y}, player: PlayerBlue},
					replayPlannedMove{move: Move{X: x + 2, Y: y}, player: PlayerBlue},
				)
			}
			motifIndex++
		}
	}
	return seeds
}

func dedupeMoves(moves []Move, boardSize int) []Move {
	seen := make([]bool, boardSize*boardSize)
	out := make([]Move, 0, len(moves))
	for _, move := range moves {
		if !move.IsValid(boardSize) {
			continue
		}
		idx := move.Y*boardSize + move.X
		if seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, move)
	}
	return out
}

func inBoard(boardSize, x, y int) bool {
	return x >= 0 && y >= 0 && x < boardSize && y < boardSize
}

func maxAbs(a, b int) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}

var evalBenchmarkSink float64

func applyReplayMove(state *GameState, rules Rules, move Move, player PlayerColor) ([]Move, bool) {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return nil, false
	}

	cell := CellFromPlayer(player)
	state.Board.Set(move.X, move.Y, cell)
	captures := append([]Move(nil), rules.FindCaptures(state.Board, move, cell)...)
	for _, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
	}
	if len(captures) > 0 {
		if player == PlayerBlue {
			state.CapturedBlue += len(captures)
		} else {
			state.CapturedRed += len(captures)
		}
	}

	totalCaptured := state.CapturedBlue
	if player == PlayerRed {
		totalCaptured = state.CapturedRed
	}
	switch {
	case totalCaptured >= rules.CaptureWinStones():
		if player == PlayerBlue {
			state.Status = StatusBlueWon
		} else {
			state.Status = StatusRedWon
		}
	case rules.IsWin(state.Board, move):
		if player == PlayerBlue {
			state.Status = StatusBlueWon
		} else {
			state.Status = StatusRedWon
		}
	case rules.IsDraw(state.Board):
		state.Status = StatusDraw
	default:
		state.Status = StatusRunning
	}

	state.ToMove = otherPlayer(player)
	state.LastMove = move
	state.HasLastMove = true
	return captures, true
}

func BenchmarkEvaluateBoardReplayMatrix(b *testing.B) {
	cases := []struct {
		scenario string
		moves    int
	}{
		{scenario: benchmarkScenarioEmpty, moves: 10},
		{scenario: benchmarkScenarioEmpty, moves: 100},
		{scenario: benchmarkScenarioEmpty, moves: 500},
		{scenario: benchmarkScenarioComplex, moves: 10},
		{scenario: benchmarkScenarioComplex, moves: 100},
		{scenario: benchmarkScenarioComplex, moves: 500},
		{scenario: benchmarkScenarioCaptures, moves: 10},
		{scenario: benchmarkScenarioCaptures, moves: 100},
		{scenario: benchmarkScenarioCaptures, moves: 500},
	}

	for _, tc := range cases {
		tc := tc
		b.Run(fmt.Sprintf("full/%s/%d", tc.scenario, tc.moves), func(b *testing.B) {
			evalCase := buildEvalReplayCase(b, tc.scenario, tc.moves)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				score := float64(EvaluateBoardWithContext(
					evalCase.initialBoard,
					PlayerRed,
					clampUint8(evalCase.initialCapturedBlue),
					clampUint8(evalCase.initialCapturedRed),
					evalCase.config,
				).Score)
				for _, step := range evalCase.steps {
					score = float64(EvaluateBoardWithContext(
						step.board,
						PlayerRed,
						clampUint8(step.capturedBlue),
						clampUint8(step.capturedRed),
						evalCase.config,
					).Score)
				}
				evalBenchmarkSink = score
			}
		})

		b.Run(fmt.Sprintf("incremental/%s/%d", tc.scenario, tc.moves), func(b *testing.B) {
			evalCase := buildEvalReplayCase(b, tc.scenario, tc.moves)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				evalState := BuildEvalStateFromBoard(
					evalCase.initialBoard,
					PlayerRed,
					clampUint8(evalCase.initialCapturedBlue),
					clampUint8(evalCase.initialCapturedRed),
					evalCase.config,
				)
				score := float64(evalState.ScoreOnly())
				for _, step := range evalCase.steps {
					boardAfter := step.board.Clone()
					score += applyReplayStepToEvalState(&evalState, &boardAfter, step)
				}
				evalBenchmarkSink = score
			}
		})
	}
}

func applyReplayStepToEvalState(evalState *EvalState, boardAfter *Board, step evalReplayStep) float64 {
	if evalState == nil || boardAfter == nil {
		return 0
	}
	delta := MoveDelta{
		Move:               step.move,
		Player:             step.player,
		CapturedCount:      uint8(len(step.captures)),
		CapturePairsGained: uint8(len(step.captures) / 2),
	}
	for i, captured := range step.captures {
		delta.CapturedCells[i] = CellIndex(captured.Y*boardAfter.Size() + captured.X)
	}
	undo := evalState.ApplyMove(boardAfter, delta)
	return float64(int64(evalState.ScoreOnly()) - int64(undo.PrevScore))
}
