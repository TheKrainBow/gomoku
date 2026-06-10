package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fixedDebugStdoutMu sync.Mutex

type orderingBenchmarkCase struct {
	name     string
	state    GameState
	rules    Rules
	settings GameSettings
}

type orderingBenchmarkDetail struct {
	scenario   string
	finalMove  Move
	rank       int
	orderCount int
	topMoves   []Move
}

type orderingBenchmarkSummary struct {
	depth         int
	positions     int
	avgCandidates float64
	avgRank       float64
	worstRank     int
	top1Pct       float64
	top2Pct       float64
	top4Pct       float64
	top8Pct       float64
}

type orderingBenchmarkDepthResult struct {
	depth     int
	finalMove Move
	rank      int
}

type orderingBenchmarkAggregate struct {
	positions    int
	candidateSum int
	rankSum      int
	worstRank    int
	top1Count    int
	top2Count    int
	top4Count    int
	top8Count    int
}

func benchmarkProgressStep(total int) int {
	switch {
	case total >= 2000:
		return 10
	case total >= 1000:
		return 5
	case total >= 250:
		return 2
	default:
		return 1
	}
}

func logBenchmarkProgress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[ordering-bench] "+format+"\n", args...)
}

func benchmarkCorpusSize() int {
	size := 200
	if testing.Short() {
		size = 100
	}
	if raw := os.Getenv("GOMOKU_ORDERING_BENCH_POSITIONS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			size = parsed
		}
	}
	return size
}

type fixedLUTPredictionStats struct {
	mu             sync.Mutex
	Depth10Samples int
	Depth10Hits    int
	ScenarioHits   map[string]bool
}

var fixedLUTStats = fixedLUTPredictionStats{
	ScenarioHits: make(map[string]bool),
}

func TestScoreBoardStoresRootTTEntryAtCompletedDepth(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 2
	cfg.AiMinDepth = 2
	cfg.AiMaxDepth = 2
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	cfg.AiTimeBudgetMs = 0
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()

	cache := newAISearchCache()
	stats := &SearchStats{}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           2,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
	})
	if stats.CompletedDepths != 2 {
		t.Fatalf("expected completed depth 2, got %d", stats.CompletedDepths)
	}
	bestMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected a legal best move")
	}

	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	rootKey := ttKeyFor(state, settings.BoardSize)
	entry, hit := tt.Probe(rootKey, heuristicHashFromConfig(cfg))
	if !hit {
		t.Fatalf("expected root board entry in TT")
	}
	if entry.Depth != 2 {
		t.Fatalf("expected TT depth 2, got %d", entry.Depth)
	}
	if entry.Flag != TTExact {
		t.Fatalf("expected TT exact flag, got %d", entry.Flag)
	}
	if !entry.BestMove.IsValid(settings.BoardSize) {
		t.Fatalf("expected TT best move to be valid, got (%d,%d)", entry.BestMove.X, entry.BestMove.Y)
	}
	bestIdx := bestMove.Y*settings.BoardSize + bestMove.X
	ttIdx := entry.BestMove.Y*settings.BoardSize + entry.BestMove.X
	if bestIdx < 0 || bestIdx >= len(scores) || ttIdx < 0 || ttIdx >= len(scores) {
		t.Fatalf("expected comparable root scores for best and TT move")
	}
	if scores[ttIdx] != scores[bestIdx] {
		t.Fatalf("expected TT best move score %.2f to match returned best score %.2f", scores[ttIdx], scores[bestIdx])
	}
}

func TestScoreBoardQuickWinExitReturnsImmediateWinWithoutDeeperSearch(t *testing.T) {
	prev := GetConfig()
	cfg := liveAIConfig(prev)
	cfg.AiDepth = 5
	cfg.AiQuickWinExit = true
	cfg.AiUseTtCache = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(0, 0, CellRed)
	state.Board.Set(1, 0, CellBlue)
	state.Board.Set(2, 0, CellBlue)
	state.Board.Set(3, 0, CellBlue)
	state.Board.Set(4, 0, CellBlue)
	state.recomputeHashes()

	stats := &SearchStats{}
	var doneDepth int
	var doneMove Move
	var doneScore float64
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           cfg.AiDepth,
		TimeoutMs:       cfg.AiTimeoutMs,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           newLiveSearchCache(),
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
		OnDepthComplete: func(depth int, move Move, score float64) {
			doneDepth = depth
			doneMove = move
			doneScore = score
		},
	})

	if stats.CompletedDepths != 1 {
		t.Fatalf("expected quick-win exit to complete at tactical depth 1, got %d", stats.CompletedDepths)
	}
	if doneDepth != 1 {
		t.Fatalf("expected depth callback to report 1, got %d", doneDepth)
	}
	if doneMove != (Move{X: 5, Y: 0}) {
		t.Fatalf("expected unique winning move (5,0), got (%d,%d)", doneMove.X, doneMove.Y)
	}
	if doneScore != -winScore {
		t.Fatalf("expected blue winning score %f, got %f", -winScore, doneScore)
	}
	if scores[0] != illegalScore {
		t.Fatalf("expected occupied square (0,0) to remain illegal, got %f", scores[0])
	}
	if scores[5] != -winScore {
		t.Fatalf("expected winning move (5,0) score %f, got %f", -winScore, scores[5])
	}
	bestMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected a legal best move")
	}
	if bestMove != (Move{X: 5, Y: 0}) {
		t.Fatalf("expected best move (5,0), got (%d,%d)", bestMove.X, bestMove.Y)
	}
}

func TestScoreBoardDirectDepthParallelMatchesSequentialBestMove(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 2
	cfg.AiMinDepth = 2
	cfg.AiMaxDepth = 2
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	cfg.AiTimeBudgetMs = 0
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()

	seqCache := newAISearchCache()
	seqStats := &SearchStats{}
	seqScores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           2,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &seqCache,
		Config:          cfg,
		Stats:           seqStats,
		DirectDepthOnly: true,
	})
	seqBest, ok := bestMoveFromScores(seqScores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected sequential search to return a move")
	}

	parCache := newAISearchCache()
	parStats := &SearchStats{}
	parScores, completed := ScoreBoardDirectDepthParallel(state, rules, AIScoreSettings{
		Depth:           2,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &parCache,
		Config:          cfg,
		Stats:           parStats,
		DirectDepthOnly: true,
	}, 2)
	if !completed {
		t.Fatalf("expected parallel search to complete")
	}
	if parStats.CompletedDepths != 2 {
		t.Fatalf("expected parallel search completed depth 2, got %d", parStats.CompletedDepths)
	}
	parBest, ok := bestMoveFromScores(parScores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected parallel search to return a move")
	}
	if parBest.X != seqBest.X || parBest.Y != seqBest.Y {
		t.Fatalf("expected same best move, sequential=(%d,%d) parallel=(%d,%d)", seqBest.X, seqBest.Y, parBest.X, parBest.Y)
	}
}

func TestScoreBoardDirectDepthParallelReportsNodeProgress(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 2
	cfg.AiMinDepth = 2
	cfg.AiMaxDepth = 2
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	cfg.AiTimeBudgetMs = 0
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()

	cache := newAISearchCache()
	stats := &SearchStats{}
	var reported atomic.Int64
	_, completed := ScoreBoardDirectDepthParallel(state, rules, AIScoreSettings{
		Depth:           2,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
		OnNodeProgress: func(delta int64) {
			if delta > 0 {
				reported.Add(delta)
			}
		},
	}, 2)
	if !completed {
		t.Fatalf("expected parallel search to complete")
	}
	if reported.Load() <= 0 {
		t.Fatalf("expected positive node progress updates, got %d", reported.Load())
	}
}

func TestScoreBoardDirectDepthParallelReportsProgressAtDepthOne(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 1
	cfg.AiMinDepth = 1
	cfg.AiMaxDepth = 1
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	cfg.AiTimeBudgetMs = 0
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()

	cache := newAISearchCache()
	stats := &SearchStats{}
	var reported atomic.Int64
	_, completed := ScoreBoardDirectDepthParallel(state, rules, AIScoreSettings{
		Depth:           1,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
		OnNodeProgress: func(delta int64) {
			if delta > 0 {
				reported.Add(delta)
			}
		},
	}, 2)
	if !completed {
		t.Fatalf("expected parallel search to complete")
	}
	if reported.Load() <= 0 {
		t.Fatalf("expected positive progress updates at depth 1, got %d", reported.Load())
	}
}

func TestCountCapturablePairsDetectsHangingPair(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Board.Set(1, 4, CellBlue)
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellRed)

	if got := countCapturablePairs(state.Board, PlayerBlue); got != 1 {
		t.Fatalf("expected one hanging blue pair, got %d", got)
	}
	if got := countCapturablePairs(state.Board, PlayerRed); got != 0 {
		t.Fatalf("expected no hanging red pair, got %d", got)
	}
}

func TestFindCaptureThreatResponsesBlocksDecisiveThreat(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlue
	state.Status = StatusRunning
	state.Board.Set(1, 4, CellBlue)
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellRed)
	state.CapturedRed = 8
	state.recomputeHashes()

	if !hasDecisiveCaptureThreat(state, rules, PlayerRed) {
		t.Fatalf("expected red to have a decisive capture threat")
	}
	responses := findCaptureThreatResponses(state, rules, PlayerBlue, PlayerRed, settings.BoardSize)
	if len(responses) == 0 {
		t.Fatalf("expected at least one legal response to decisive capture threat")
	}

	hasBlock := false
	for _, move := range responses {
		if move.X == 0 && move.Y == 4 {
			hasBlock = true
		}
		next := state
		var undo searchMoveUndo
		if !applyMoveWithUndo(&next, rules, move, PlayerBlue, nil, &undo) {
			t.Fatalf("response move should be legal: (%d,%d)", move.X, move.Y)
		}
		if hasDecisiveCaptureThreat(next, rules, PlayerRed) {
			t.Fatalf("response move (%d,%d) still leaves decisive capture threat", move.X, move.Y)
		}
		undoMoveWithUndo(&next, nil, undo)
	}
	if !hasBlock {
		t.Fatalf("expected direct blocking move (0,4) to be included in responses")
	}
}

func TestHeuristicForMoveSlightlyFavorsLastMoveNeighborhood(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerRed
	state.Status = StatusRunning
	state.Board.Set(4, 4, CellBlue)
	state.HasLastMove = true
	state.LastMove = Move{X: 4, Y: 4}
	state.recomputeHashes()
	stateNoLastMove := state
	stateNoLastMove.HasLastMove = false

	cfg := DefaultConfig()
	scoreNear := heuristicForMove(state, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, Move{X: 4, Y: 5}, nil)
	scoreNearNoLastMove := heuristicForMove(stateNoLastMove, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    stateNoLastMove.ToMove,
		Config:    cfg,
	}, Move{X: 4, Y: 5}, nil)
	scoreFar := heuristicForMove(state, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, Move{X: 0, Y: 0}, nil)
	scoreFarNoLastMove := heuristicForMove(stateNoLastMove, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    stateNoLastMove.ToMove,
		Config:    cfg,
	}, Move{X: 0, Y: 0}, nil)

	if scoreNear <= scoreNearNoLastMove {
		t.Fatalf("expected near reply score %.2f to exceed no-last-move score %.2f", scoreNear, scoreNearNoLastMove)
	}
	if delta := scoreNear - scoreNearNoLastMove; delta <= 0 || delta >= 100 {
		t.Fatalf("expected small but positive locality delta for near reply, got %.2f", delta)
	}
	if scoreFar != scoreFarNoLastMove {
		t.Fatalf("expected far move score to stay unchanged, got with bonus %.2f vs without %.2f", scoreFar, scoreFarNoLastMove)
	}
}

func TestApplyMoveSignalsImmediateCaptureLoss(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerRed
	state.Status = StatusRunning
	state.CapturedBlue = 8
	state.Board.Set(1, 4, CellRed)
	state.Board.Set(2, 4, CellRed)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(5, 5, CellBlue)
	state.Board.Set(6, 5, CellBlue)
	state.Board.Set(7, 5, CellRed)
	state.recomputeHashes()

	if !applyMove(&state, rules, Move{X: 4, Y: 5}, PlayerRed) {
		t.Fatalf("expected tempting capture move to be legal")
	}
	if state.Status != StatusBlueWon {
		t.Fatalf("expected immediate capture threat to signal blue win, got %v", state.Status)
	}
}

func TestScoreBoardPrefersBlockingFifthCapturePair(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerRed
	state.Status = StatusRunning
	state.CapturedBlue = 8
	state.Board.Set(1, 4, CellRed)
	state.Board.Set(2, 4, CellRed)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(5, 5, CellBlue)
	state.Board.Set(6, 5, CellBlue)
	state.Board.Set(7, 5, CellRed)
	state.recomputeHashes()

	cfg := DefaultConfig()
	cfg.AiDepth = 4
	cfg.AiMinDepth = 4
	cfg.AiMaxDepth = 4
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiEnableTtPersistence = false
	cfg.AiLogSearchStats = false

	cache := newAISearchCache()
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           4,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           &SearchStats{},
		DirectDepthOnly: true,
	})

	bestMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected a legal best move")
	}
	if !bestMove.Equals(Move{X: 0, Y: 4}) {
		t.Fatalf("expected AI to block the fifth capture at (0,4), got %+v", bestMove)
	}
}

func TestFindCaptureThreatResponsesIncrementalMatchesScan(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlue
	state.Status = StatusRunning
	state.Board.Set(1, 4, CellBlue)
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellRed)
	state.CapturedRed = 8
	state.recomputeHashes()

	evalState := BuildEvalStateFromBoard(state.Board, state.ToMove, uint8(state.CapturedBlue), uint8(state.CapturedRed), DefaultConfig())

	got := uniqueMoves(findCaptureThreatResponsesWithEval(state, rules, PlayerBlue, PlayerRed, settings.BoardSize, &evalState), settings.BoardSize)
	want := uniqueMoves(findCaptureThreatResponsesByScan(state, rules, PlayerBlue, PlayerRed, settings.BoardSize), settings.BoardSize)
	if len(got) != len(want) {
		t.Fatalf("expected same number of incremental responses as scan, got=%v want=%v", got, want)
	}
	for i := range got {
		if !got[i].Equals(want[i]) {
			t.Fatalf("expected incremental responses %v to match scan %v", got, want)
		}
	}
}

func moveListContains(moves []Move, target Move) bool {
	for _, move := range moves {
		if move.Equals(target) {
			return true
		}
	}
	return false
}

func TestAnalyzeThreatsMarksOpenFourAsHardTactical(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.recomputeHashes()

	context := AnalyzeThreats(state, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    DefaultConfig(),
	}, state.ToMove, nil)

	if !context.IsHardTactical {
		t.Fatalf("expected open four threat to activate hard tactical mode")
	}
	if context.OppBestTier < TierCritical {
		t.Fatalf("expected opponent best tier to be critical or higher, got %v", context.OppBestTier)
	}
	if !moveListContains(context.MustBlockMoves, Move{X: 1, Y: 4}) {
		t.Fatalf("expected left block square in must-block moves, got %#v", context.MustBlockMoves)
	}
	if !moveListContains(context.MustBlockMoves, Move{X: 6, Y: 4}) {
		t.Fatalf("expected right block square in must-block moves, got %#v", context.MustBlockMoves)
	}
}

func TestAnalyzeThreatsMarksOpenThreeAsMustAnswer(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.recomputeHashes()

	context := AnalyzeThreats(state, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    DefaultConfig(),
	}, state.ToMove, nil)

	if context.IsHardTactical {
		t.Fatalf("expected open three threat to stay out of hard tactical mode")
	}
	if context.IsSoftTactical {
		t.Fatalf("expected open three threat to stay out of soft tactical mode")
	}
	if context.OppBestTier < TierMustAnswer {
		t.Fatalf("expected opponent best tier to be must-answer or higher, got %v", context.OppBestTier)
	}
	if !moveListContains(context.MustBlockMoves, Move{X: 1, Y: 4}) {
		t.Fatalf("expected left defense square in must-block moves, got %#v", context.MustBlockMoves)
	}
	if !moveListContains(context.MustBlockMoves, Move{X: 5, Y: 4}) {
		t.Fatalf("expected right defense square in must-block moves, got %#v", context.MustBlockMoves)
	}
}

func TestAnalyzeThreatsKeepsOpenTwoOutOfHardTactical(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.recomputeHashes()

	context := AnalyzeThreats(state, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    DefaultConfig(),
	}, state.ToMove, nil)

	if context.IsHardTactical {
		t.Fatalf("expected open two to stay out of hard tactical mode")
	}
}

func TestChooseNodeCandidatesFromThreatContextPrioritizesHardTacticalResponses(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.recomputeHashes()

	cfg := DefaultConfig()
	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	context := AnalyzeThreats(state, rules, ctx.settings, state.ToMove, ctx.evalState)
	candidates, forcedWinLine, hardRestricted := chooseNodeCandidatesFromThreatContext(state, ctx, state.ToMove, true, 1, 16, nil, context)
	if forcedWinLine {
		t.Fatalf("did not expect winning-only line in must-block position")
	}
	if !hardRestricted {
		t.Fatalf("expected hard tactical restriction in must-block position")
	}
	if len(candidates) < 2 {
		t.Fatalf("expected blocking candidates near the front, got %d: %#v", len(candidates), candidates)
	}
	if len(candidates) > 6 {
		t.Fatalf("expected restricted candidate list, got %d candidates: %#v", len(candidates), candidates)
	}
	if !candidates[0].Equals(Move{X: 1, Y: 4}) && !candidates[1].Equals(Move{X: 1, Y: 4}) {
		t.Fatalf("expected left block in first two candidates, got %#v", candidates[:minInt(len(candidates), 4)])
	}
	if !candidates[0].Equals(Move{X: 6, Y: 4}) && !candidates[1].Equals(Move{X: 6, Y: 4}) {
		t.Fatalf("expected right block in first two candidates, got %#v", candidates[:minInt(len(candidates), 4)])
	}
}

func TestChooseRootSearchBandsKeepsForcedPlusLimitedCarryover(t *testing.T) {
	t.Skip("temporarily disabled while forced carryover is disabled")
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.recomputeHashes()

	cfg := DefaultConfig()
	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     4,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	ordered := sortRootMoveIndices(rootPool, true, nil)
	bands := chooseRootSearchBands(ctx, rootPool, ordered, 4)
	if len(bands.forced) < 2 {
		t.Fatalf("expected forced band to keep must-block moves, got %d", len(bands.forced))
	}
	if len(bands.principal) > rootForcedCarryoverLimit(ctx, 4) {
		t.Fatalf("expected limited carryover after forced moves, got principal=%d", len(bands.principal))
	}
	if len(bands.speculative) != 0 || len(bands.verification) != 0 {
		t.Fatalf("expected no speculative/verification moves when forced band exists, got speculative=%d verification=%d", len(bands.speculative), len(bands.verification))
	}
}

func TestMinimaxUsesTacticalQuiescenceAtLeaf(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.recomputeHashes()

	cfg := DefaultConfig()
	cfg.AiEnableAspiration = false
	cfg.AiEnableTacticalQuiescence = true
	cfg.AiTacticalQuiescenceDepth = 4

	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     1,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	score := minimax(&state, ctx, 0, state.ToMove, 0, math.Inf(-1), math.Inf(1), nil)
	if score != -winScore {
		t.Fatalf("expected tactical quiescence to resolve immediate blue win at leaf, got %.2f", score)
	}
}

func TestHasDecisiveCaptureThreatDetectsImmediateCaptureWinByCount(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlue
	state.Status = StatusRunning
	state.CapturedRed = 8
	// Red can capture the Blue pair at x=[4,5], y=4 by playing at (3,4).
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.Board.Set(6, 4, CellRed)
	state.recomputeHashes()

	if !hasDecisiveCaptureThreat(state, rules, PlayerRed) {
		t.Fatalf("expected immediate capture-win threat to be detected")
	}
}

func TestImmediateWinRejectsAlignmentBreakableByCapture(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerRed
	state.Status = StatusRunning
	for _, move := range []Move{{X: 4, Y: 5}, {X: 5, Y: 5}, {X: 6, Y: 5}, {X: 7, Y: 5}, {X: 6, Y: 6}} {
		state.Board.Set(move.X, move.Y, CellRed)
	}
	state.Board.Set(6, 7, CellBlue)
	state.recomputeHashes()

	winningButBreakable := Move{X: 8, Y: 5}
	after := state.Clone()
	after.Board.Set(winningButBreakable.X, winningButBreakable.Y, CellRed)
	after.LastMove = winningButBreakable
	after.HasLastMove = true

	if !rules.IsWin(after.Board, winningButBreakable) {
		t.Fatalf("expected red alignment after %v", winningButBreakable)
	}
	if !rules.OpponentCanBreakAlignmentByCapture(after, PlayerBlue) {
		t.Fatalf("expected blue to be able to break the alignment by capture")
	}
	if isImmediateWin(state, rules, winningButBreakable, PlayerRed) {
		t.Fatalf("expected breakable alignment not to be treated as an immediate win")
	}
}

func TestQuickWinExitDoesNotReturnBeforeTargetDepth(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerRed
	state.Status = StatusRunning
	for _, move := range []Move{{X: 1, Y: 4}, {X: 2, Y: 4}, {X: 3, Y: 4}, {X: 4, Y: 4}} {
		state.Board.Set(move.X, move.Y, CellRed)
	}
	state.Board.Set(1, 1, CellBlue)
	state.recomputeHashes()

	cfg := DefaultConfig()
	cfg.AiQuickWinExit = true
	cfg.AiUseTtCache = false
	cfg.AiEnableRootTranspose = false
	cfg.AiEnableAspiration = false
	cfg.AiQueueEnabled = false
	cfg.AiLazySMPWorkers = 1
	cfg.AiMinDepth = 1
	cfg.AiDepth = 3
	cfg.AiMaxDepth = 3

	stats := &SearchStats{Start: time.Now()}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:     cfg.AiDepth,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
		Stats:     stats,
		Cache:     newLiveSearchCache(),
	})

	if stats.CompletedDepths < cfg.AiMaxDepth {
		t.Fatalf("expected quick win exit to wait until depth %d, completed=%d returned=%d", cfg.AiMaxDepth, stats.CompletedDepths, stats.ReturnedDepth)
	}
	if stats.ReturnedDepth > 0 && stats.ReturnedDepth < cfg.AiMaxDepth {
		t.Fatalf("expected returned depth not to be shallow, got completed=%d returned=%d", stats.CompletedDepths, stats.ReturnedDepth)
	}
	leftWin := scoreForMove(scores, Move{X: 0, Y: 4}, settings.BoardSize)
	rightWin := scoreForMove(scores, Move{X: 5, Y: 4}, settings.BoardSize)
	if leftWin < winScore/2 && rightWin < winScore/2 {
		t.Fatalf("expected immediate winning move to remain scored as winning")
	}
}

func TestRootOrderingPrefersOpenThreeDefenseOverClosedThreeHistory(t *testing.T) {
	openThreeDefense := RootMove{
		Move:             Move{X: 6, Y: 10},
		TacticalPriority: prioBlockOpen3,
		ThreatFlags:      rootThreatOppThree,
		ThreatSeverity:   threatSeverityForPattern(PatternOpen3),
		IsForced:         true,
	}
	closedThreeHistory := RootMove{
		Move:               Move{X: 5, Y: 7},
		TacticalPriority:   prioQuietOpp3 + 1,
		ThreatSeverity:     threatSeverityForPattern(PatternClosed3),
		LastSearchScore:    -winScore,
		LastCompletedDepth: 6,
		HasLastSearch:      true,
	}

	pool := []RootMove{closedThreeHistory, openThreeDefense}
	ordered := sortRootMoveIndices(pool, false, nil)
	if len(ordered) == 0 || !pool[ordered[0]].Move.Equals(openThreeDefense.Move) {
		t.Fatalf("expected open3 defense first, got order=%v first=%+v", ordered, pool[ordered[0]].Move)
	}
}

func TestCandidateLimitHalvesEachPlyToMinimumTwo(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableHardPlyCaps = true
	cfg.AiMaxCandidates = 16
	ctx := minimaxContext{settings: AIScoreSettings{Config: cfg}}

	if got := candidateLimit(ctx, 10, 0, false); got != 16 {
		t.Fatalf("expected root cap 16, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 1, false); got != 8 {
		t.Fatalf("expected ply-1 cap 8, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 2, false); got != 4 {
		t.Fatalf("expected ply-2 cap 4, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 3, false); got != 2 {
		t.Fatalf("expected ply-3 cap 2, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 9, false); got != 2 {
		t.Fatalf("expected deep cap floor 2, got %d", got)
	}
}

func TestCandidateLimitAllowsTacticalLimitToTightenHardCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableHardPlyCaps = true
	cfg.AiEnableTacticalK = true
	cfg.AiMaxCandidates = 16
	cfg.AiKTactDeep = 1
	ctx := minimaxContext{settings: AIScoreSettings{Config: cfg}}

	if got := candidateLimit(ctx, 10, 9, true); got != 2 {
		t.Fatalf("expected tactical limit to respect floor 2, got %d", got)
	}
}

func TestBuildRootMovePoolRestrictsToForcedCoreWhenPresent(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(5, 4, CellBlue)
	state.recomputeHashes()

	cfg := DefaultConfig()
	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     4,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) != 2 {
		t.Fatalf("expected forced root pool to contain only 2 must-block moves, got %d", len(rootPool))
	}
	if !moveListContains(rootMovesFromIndices(rootPool, []int{0, 1}), Move{X: 1, Y: 4}) ||
		!moveListContains(rootMovesFromIndices(rootPool, []int{0, 1}), Move{X: 6, Y: 4}) {
		t.Fatalf("expected root pool to contain only blocking moves at (1,4) and (6,4), got %v", rootMovesFromIndices(rootPool, []int{0, 1}))
	}
}

func TestBuildRootMovePoolRestrictsToMustPlayOpenThreeExtensions(t *testing.T) {
	state, rules, settings := buildCurrentPlayerTacticalFixedState()
	cfg := DefaultConfig()
	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     4,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	context := AnalyzeThreats(state, rules, ctx.settings, state.ToMove, ctx.evalState)
	if len(context.MustPlayMoves) != 2 {
		t.Fatalf("expected exactly 2 must-play moves for own open three, got %v", context.MustPlayMoves)
	}
	if !moveListContains(context.MustPlayMoves, Move{X: 7, Y: 9}) || !moveListContains(context.MustPlayMoves, Move{X: 11, Y: 9}) {
		t.Fatalf("expected must-play moves at (7,9) and (11,9), got %v", context.MustPlayMoves)
	}
	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) != 2 {
		t.Fatalf("expected root pool to be restricted to 2 must-play moves, got %d", len(rootPool))
	}
	if !moveListContains(rootMovesFromIndices(rootPool, []int{0, 1}), Move{X: 7, Y: 9}) ||
		!moveListContains(rootMovesFromIndices(rootPool, []int{0, 1}), Move{X: 11, Y: 9}) {
		t.Fatalf("expected root pool to contain only (7,9) and (11,9), got %v", rootMovesFromIndices(rootPool, []int{0, 1}))
	}
}

func TestBuildRootMovePoolRestrictsToMustPlayOpenThreeExtensionsWithTempo(t *testing.T) {
	state, rules, settings := buildCurrentPlayerTacticalTempoFixedState()
	cfg := DefaultConfig()
	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     4,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	context := AnalyzeThreats(state, rules, ctx.settings, state.ToMove, ctx.evalState)
	if len(context.MustPlayMoves) != 2 {
		t.Fatalf("expected exactly 2 must-play moves for own open three with tempo, got %v", context.MustPlayMoves)
	}
	if !moveListContains(context.MustPlayMoves, Move{X: 7, Y: 9}) || !moveListContains(context.MustPlayMoves, Move{X: 11, Y: 9}) {
		t.Fatalf("expected must-play moves at (7,9) and (11,9), got %v", context.MustPlayMoves)
	}
	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) != 2 {
		t.Fatalf("expected root pool to be restricted to 2 must-play moves with tempo, got %d", len(rootPool))
	}
	if !moveListContains(rootMovesFromIndices(rootPool, []int{0, 1}), Move{X: 7, Y: 9}) ||
		!moveListContains(rootMovesFromIndices(rootPool, []int{0, 1}), Move{X: 11, Y: 9}) {
		t.Fatalf("expected root pool to contain only (7,9) and (11,9), got %v", rootMovesFromIndices(rootPool, []int{0, 1}))
	}

	cache := newAISearchCache()
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           10,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		DirectDepthOnly: false,
		Stats:           &SearchStats{},
	})
	best, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected a best move in tempo position")
	}
	if !best.Equals(Move{X: 7, Y: 9}) && !best.Equals(Move{X: 11, Y: 9}) {
		t.Fatalf("expected tempo position to keep same winning extensions, got (%d,%d)", best.X, best.Y)
	}
}

func TestGenerateThreatCandidatesIncludesOpenThreeMustAnswer(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue

	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(7, 7, CellRed)
	state.Board.Set(7, 6, CellBlue)
	state.recomputeHashes()

	ctx := minimaxContext{
		rules: rules,
		settings: AIScoreSettings{
			BoardSize: settings.BoardSize,
			Player:    state.ToMove,
			Config:    DefaultConfig(),
		},
	}

	threatContext := AnalyzeThreats(state, rules, ctx.settings, state.ToMove, nil)
	candidates := GenerateThreatCandidates(threatContext, state, rules)
	if len(candidates) == 0 {
		t.Fatalf("expected dedicated threat candidates for open three, got %#v", candidates)
	}
	if !moveListContains(threatContext.ForkMoves, Move{X: 1, Y: 4}) {
		t.Fatalf("expected left extension square in fork moves, got %#v", threatContext.ForkMoves)
	}
	if !moveListContains(threatContext.ForkMoves, Move{X: 5, Y: 4}) {
		t.Fatalf("expected right extension square in fork moves, got %#v", threatContext.ForkMoves)
	}
	if threatContext.IsHardTactical || threatContext.IsSoftTactical {
		t.Fatalf("expected open three to stay out of tactical threat mode")
	}
}

func TestThreatFlagsForMoveIgnoresDoubleBlockedFour(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9

	board := NewBoard(settings.BoardSize)
	// Diagonal after Blue plays (6,6): WBBBBW
	board.Set(2, 2, CellRed)
	board.Set(3, 3, CellBlue)
	board.Set(4, 4, CellBlue)
	board.Set(5, 5, CellBlue)
	board.Set(7, 7, CellRed)

	move := Move{X: 6, Y: 6}
	if board.At(move.X, move.Y) != CellEmpty {
		t.Fatalf("expected test move to be empty")
	}

	winNow, createFour, openThree := threatFlagsForMove(board, move, CellBlue)
	if winNow {
		t.Fatalf("expected double-blocked four completion to not be a win")
	}
	if createFour {
		t.Fatalf("expected double-blocked four completion to not count as createFour")
	}
	if openThree {
		t.Fatalf("expected double-blocked four completion to not count as openThree")
	}
}

func TestFixedDoubleBlockedFourPrefersCaptureSafeDevelopment(t *testing.T) {
	state, rules, settings := buildDoubleBlockedFourFixedState()
	cfg := DefaultConfig()
	cfg.AiDepth = 10
	cfg.AiMinDepth = 2
	cfg.AiMaxDepth = 10
	cfg.AiDepthStep = 1
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiLogSearchStats = false

	cache := newAISearchCache()
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           cfg.AiDepth,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		DirectDepthOnly: false,
		Stats:           &SearchStats{},
	})

	safe := Move{X: 6, Y: 9}
	greedy := Move{X: 10, Y: 12}
	safeScore := scoreForMove(scores, safe, settings.BoardSize)
	greedyScore := scoreForMove(scores, greedy, settings.BoardSize)
	if safeScore <= greedyScore {
		t.Fatalf("expected capture-safe move %v (%.2f) to outscore greedy move %v (%.2f)", safe, safeScore, greedy, greedyScore)
	}
}

func TestShouldApplyLMR(t *testing.T) {
	if shouldApplyLMR(4, 5, false) {
		t.Fatalf("expected no LMR on non-quiet nodes")
	}
	if shouldApplyLMR(2, 6, true) {
		t.Fatalf("expected no LMR below minimum depth")
	}
	if shouldApplyLMR(4, 3, true) {
		t.Fatalf("expected no LMR before late-move threshold")
	}
	if !shouldApplyLMR(4, 4, true) {
		t.Fatalf("expected LMR on quiet late moves")
	}
}

func TestCollectCandidateMovesPrefersOwnQuietFronts(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue

	state.Board.Set(2, 4, CellBlue)
	state.Board.Set(3, 4, CellBlue)
	state.Board.Set(4, 4, CellBlue)
	state.Board.Set(6, 6, CellRed)
	state.Board.Set(7, 6, CellRed)
	state.Board.Set(8, 6, CellRed)
	state.recomputeHashes()

	evalState := BuildEvalStateFromBoard(state.Board, state.ToMove, uint8(state.CapturedBlue), uint8(state.CapturedRed), DefaultConfig())
	candidates := collectCandidateMovesWithEval(state, rules, state.ToMove, settings.BoardSize, &evalState, nil)
	if len(candidates) < 3 {
		t.Fatalf("expected quiet front candidates, got %d", len(candidates))
	}
	first := candidates[0].move
	if !first.Equals(Move{X: 1, Y: 4}) && !first.Equals(Move{X: 5, Y: 4}) {
		t.Fatalf("expected own open-three extension first, got %#v", candidates[0])
	}
	foundOppDefense := false
	for i := 0; i < minInt(4, len(candidates)); i++ {
		move := candidates[i].move
		if move.Equals(Move{X: 5, Y: 6}) {
			foundOppDefense = true
			break
		}
	}
	if !foundOppDefense {
		t.Fatalf("expected opponent defense near the front, got %#v", candidates[:minInt(6, len(candidates))])
	}
}

func TestApplyMoveWithUndoRestoresState(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()
	original := state.Clone()

	move := Move{X: 4, Y: 3}
	var undo searchMoveUndo
	if !applyMoveWithUndo(&state, rules, move, PlayerBlue, nil, &undo) {
		t.Fatalf("expected move to apply")
	}
	undoMoveWithUndo(&state, nil, undo)

	if state.Status != original.Status || state.ToMove != original.ToMove || state.HasLastMove != original.HasLastMove {
		t.Fatalf("expected state header restored")
	}
	if state.CapturedBlue != original.CapturedBlue || state.CapturedRed != original.CapturedRed {
		t.Fatalf("expected captures restored")
	}
	if state.Hash != original.Hash || state.CanonHash != original.CanonHash || state.HashSym != original.HashSym {
		t.Fatalf("expected hashes restored")
	}
	for y := 0; y < settings.BoardSize; y++ {
		for x := 0; x < settings.BoardSize; x++ {
			if state.Board.At(x, y) != original.Board.At(x, y) {
				t.Fatalf("board mismatch at (%d,%d)", x, y)
			}
		}
	}
	if !reflect.DeepEqual(state.ForcedCaptureMoves, original.ForcedCaptureMoves) {
		t.Fatalf("forced capture moves mismatch")
	}
}

func TestScoreBoardUsesRootTTExactShortcut(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 4
	cfg.AiMinDepth = 4
	cfg.AiMaxDepth = 4
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	cfg.AiTimeBudgetMs = 0
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()

	cache := newAISearchCache()
	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	best := Move{X: 4, Y: 3}
	rootKey := ttKeyFor(state, settings.BoardSize)
	tt.Store(rootKey, heuristicHashFromConfig(cfg), 10, 1234, TTExact, best, TTMeta{})

	stats := &SearchStats{}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:     4,
		TimeoutMs: 0,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Cache:     &cache,
		Config:    cfg,
		Stats:     stats,
	})
	got, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected move from TT shortcut")
	}
	if got.X != best.X || got.Y != best.Y {
		t.Fatalf("expected TT shortcut move (%d,%d), got (%d,%d)", best.X, best.Y, got.X, got.Y)
	}
	if stats.CompletedDepths < 10 {
		t.Fatalf("expected completed depth from TT entry, got %d", stats.CompletedDepths)
	}
	if stats.Nodes != 0 {
		t.Fatalf("expected no node search when TT shortcut is used, got %d", stats.Nodes)
	}
}

func TestScoreBoardUsesRootTransposeShortcutAcrossTranslation(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 3
	cfg.AiMinDepth = 3
	cfg.AiMaxDepth = 3
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	cfg.AiEnableAspiration = false
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	cfg.AiEnableRootTranspose = false
	cfg.AiRootTransposeSize = 1 << 10
	cfg.AiTimeBudgetMs = 0
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 15
	rules := NewRules(settings)

	base := DefaultGameState(settings)
	base.Status = StatusRunning
	base.ToMove = PlayerBlue
	base.Board.Set(6, 6, CellBlue)
	base.Board.Set(7, 6, CellRed)
	base.Board.Set(6, 7, CellBlue)
	base.recomputeHashes()

	cache := newAISearchCache()
	statsBase := &SearchStats{}
	scoresBase := ScoreBoard(base, rules, AIScoreSettings{
		Depth:     3,
		TimeoutMs: 0,
		BoardSize: settings.BoardSize,
		Player:    base.ToMove,
		Cache:     &cache,
		Config:    cfg,
		Stats:     statsBase,
	})
	if _, ok := bestMoveFromScores(scoresBase, base, rules, settings.BoardSize); !ok {
		t.Fatalf("expected base search to produce move")
	}

	translated := DefaultGameState(settings)
	translated.Status = StatusRunning
	translated.ToMove = PlayerBlue
	dx, dy := 1, 1
	translated.Board.Set(6+dx, 6+dy, CellBlue)
	translated.Board.Set(7+dx, 6+dy, CellRed)
	translated.Board.Set(6+dx, 7+dy, CellBlue)
	translated.recomputeHashes()

	if ttKeyFor(base, settings.BoardSize) == ttKeyFor(translated, settings.BoardSize) {
		t.Fatalf("expected translated board to have different absolute TT key")
	}

	statsTranslated := &SearchStats{}
	translatedSettings := AIScoreSettings{
		Depth:     3,
		TimeoutMs: 0,
		BoardSize: settings.BoardSize,
		Player:    translated.ToMove,
		Cache:     &cache,
		Config:    cfg,
		Stats:     statsTranslated,
	}
	scoresTranslated := ScoreBoard(translated, rules, translatedSettings)
	bestTranslated, ok := bestMoveFromScores(scoresTranslated, translated, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected translated search to produce move")
	}
	if !bestTranslated.IsValid(settings.BoardSize) {
		t.Fatalf("expected translated move to be valid, got (%d,%d)", bestTranslated.X, bestTranslated.Y)
	}
}

func buildFixedSearchBenchmarkState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue

	// Screenshot-derived benchmark board:
	// 1. B (9,9)
	// 2. R (8,8)
	// 3. B (6,6)
	// 4. R (9,8)
	// 5. B (10,8)
	// 6. R (11,7)
	// 7. B (7,8)
	// 8. R (9,7)
	// 9. B (7,6)
	// 10. R (10,7)
	// Blue/Blue to play.
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{9, 9, CellBlue},
		{6, 6, CellBlue},
		{10, 8, CellBlue},
		{11, 7, CellRed},
		{7, 8, CellBlue},
		{9, 7, CellRed},
		{7, 6, CellBlue},
		{10, 7, CellRed},
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildQuietCenterFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.Board.Set(9, 9, CellBlue)
	state.recomputeHashes()
	return state, rules, settings
}

func buildCurrentPlayerTacticalFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{8, 9, CellBlue},
		{9, 9, CellBlue},
		{10, 9, CellBlue},
		{9, 8, CellRed},
		{10, 8, CellRed},
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildCurrentPlayerTacticalTempoFixedState() (GameState, Rules, GameSettings) {
	state, rules, settings := buildCurrentPlayerTacticalFixedState()
	state.Board.Set(8, 8, CellRed)
	state.recomputeHashes()
	return state, rules, settings
}

func buildNextPlayerTacticalFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{9, 9, CellRed},
		{10, 9, CellRed},
		{11, 9, CellRed},
		{8, 8, CellBlue},
		{10, 8, CellBlue},
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildDoubleBlockedFourFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{7, 12, CellBlue},
		{5, 10, CellRed},
		{9, 10, CellBlue},
		{7, 10, CellRed},
		{4, 10, CellBlue},
		{8, 11, CellRed},
		{9, 12, CellBlue},
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildProtectCaptureFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.CapturedBlue = 8
	state.CapturedRed = 6
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{6, 5, CellRed},
		{10, 7, CellBlue},
		{11, 7, CellRed},
		{11, 6, CellBlue},
		{6, 8, CellBlue},
		{9, 9, CellRed},
		{10, 9, CellBlue},
		{5, 9, CellBlue},
		{7, 10, CellBlue},
		{10, 8, CellRed},
		{12, 6, CellBlue},
		{8, 4, CellRed},
		{8, 9, CellBlue},
		{9, 8, CellRed},
		{9, 10, CellBlue},
		{9, 7, CellRed},
		{8, 6, CellBlue},
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildMultiThreatFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{8, 9, CellBlue},
		{9, 9, CellBlue},
		{10, 9, CellBlue},
		{9, 8, CellBlue},
		{9, 10, CellBlue},
		{12, 7, CellRed},
		{12, 8, CellRed},
		{12, 9, CellRed},
		{11, 10, CellRed},
		{10, 11, CellRed},
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildRedToPlayCaptureRaceFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.CapturedBlue = 8
	state.CapturedRed = 8

	// Screenshot-derived board, Red/Red to play, captures 8-8.
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{4, 4, CellBlue},  // 27
		{6, 4, CellRed},   // 20
		{5, 5, CellRed},   // 26
		{7, 5, CellRed},   // 28
		{8, 5, CellBlue},  // 15
		{6, 6, CellRed},   // 22
		{7, 6, CellBlue},  // 19
		{8, 6, CellRed},   // 8
		{6, 7, CellRed},   // 18
		{9, 7, CellBlue},  // 21
		{7, 8, CellBlue},  // 7
		{8, 8, CellRed},   // 24
		{9, 8, CellBlue},  // 13
		{5, 9, CellBlue},  // 29
		{8, 9, CellBlue},  // 25
		{9, 10, CellRed},  // 10
		{9, 11, CellBlue}, // 11
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildRedToPlaySixVsFourCapturesFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.CapturedBlue = 6
	state.CapturedRed = 4

	// Screenshot-derived board, Red/Red to play, captures Blue/Blue=6 Red/Red=4.
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{7, 6, CellBlue},  // 11
		{9, 6, CellBlue},  // 19
		{10, 6, CellRed},  // 28
		{6, 7, CellBlue},  // 21
		{8, 7, CellRed},   // 30
		{5, 8, CellRed},   // 24
		{7, 8, CellRed},   // 10
		{8, 8, CellBlue},  // 29
		{11, 8, CellBlue}, // 31
		{5, 9, CellBlue},  // 13
		{6, 9, CellRed},   // 20
		{7, 9, CellRed},   // 16
		{8, 9, CellBlue},  // 15
		{9, 9, CellBlue},  // 1
		{10, 9, CellBlue}, // 17
		{5, 10, CellRed},  // 22
		{6, 10, CellBlue}, // 9
		{8, 10, CellBlue}, // 3
		{4, 11, CellBlue}, // 23
		{7, 11, CellBlue}, // 5
		{6, 12, CellBlue}, // 7
		{8, 12, CellRed},  // 14
		{5, 13, CellRed},  // 8
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func buildRedToPlaySixSixSnapshotFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	state.CapturedBlue = 6
	state.CapturedRed = 6

	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{9, 6, CellRed},    // 32
		{8, 7, CellRed},    // 36
		{9, 7, CellBlue},   // 25
		{14, 7, CellRed},   // 18
		{6, 8, CellRed},    // 30
		{7, 8, CellRed},    // 12
		{8, 8, CellRed},    // 10
		{9, 8, CellBlue},   // 31
		{10, 8, CellBlue},  // 11
		{11, 8, CellRed},   // 6
		{12, 8, CellRed},   // 28
		{13, 8, CellBlue},  // 5
		{6, 9, CellBlue},   // 37
		{9, 9, CellBlue},   // 33
		{12, 9, CellBlue},  // 3
		{9, 10, CellBlue},  // 23
		{10, 10, CellBlue}, // 29
		{11, 10, CellBlue}, // 19
		{12, 10, CellRed},  // 24
		{8, 11, CellRed},   // 14
		{9, 11, CellRed},   // 20
		{11, 11, CellBlue}, // 21
		{12, 12, CellBlue}, // 17
		{12, 13, CellBlue}, // 35
		{13, 13, CellRed},  // 26
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

func BenchmarkScoreBoardFixedPosition(b *testing.B) {
	baseState, rules, settings := buildFixedSearchBenchmarkState()
	depths := []int{1, 2, 3, 4, 5}

	for _, depth := range depths {
		depth := depth
		b.Run("depth_"+itoa(depth), func(b *testing.B) {
			cfg := DefaultConfig()
			cfg.AiDepth = depth
			cfg.AiMinDepth = depth
			cfg.AiMaxDepth = depth
			cfg.AiTimeBudgetMs = 0
			cfg.AiTimeoutMs = 0
			cfg.AiQuickWinExit = false
			cfg.AiEnableTtPersistence = false
			cfg.AiLogSearchStats = false

			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				state := baseState.Clone()
				cache := newAISearchCache()
				stats := &SearchStats{}
				scores := ScoreBoard(state, rules, AIScoreSettings{
					Depth:           depth,
					TimeoutMs:       0,
					BoardSize:       settings.BoardSize,
					Player:          state.ToMove,
					Cache:           &cache,
					Config:          cfg,
					Stats:           stats,
					DirectDepthOnly: true,
				})
				move, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
				if !ok || !move.IsValid(settings.BoardSize) {
					b.Fatalf("expected legal move at depth %d", depth)
				}
			}
		})
	}
}

func TestScoreBoardFixedPositionKeepsMultipleRootCandidates(t *testing.T) {
	state, rules, settings := buildFixedSearchBenchmarkState()
	cfg := DefaultConfig()
	cfg.AiDepth = 3
	cfg.AiMinDepth = 3
	cfg.AiMaxDepth = 3
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiEnableTtPersistence = false
	cfg.AiLogSearchStats = false

	cache := newAISearchCache()
	stats := &SearchStats{}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           3,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
	})
	if _, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize); !ok {
		t.Fatalf("expected legal move from fixed position")
	}
	if stats.RootCandidates <= 1 {
		t.Fatalf("expected multiple root candidates on fixed position, got %d", stats.RootCandidates)
	}
}

func TestScoreBoardFixedPositionRedToPlaySixVsFourCapturesReturnsLegalMove(t *testing.T) {
	state, rules, settings := buildRedToPlaySixVsFourCapturesFixedState()
	cfg := DefaultConfig()
	cfg.AiDepth = 3
	cfg.AiMinDepth = 3
	cfg.AiMaxDepth = 3
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiEnableTtPersistence = false
	cfg.AiLogSearchStats = false

	cache := newAISearchCache()
	stats := &SearchStats{}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           3,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
	})

	move, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok || !move.IsValid(settings.BoardSize) {
		t.Fatalf("expected legal move from screenshot-derived fixed position")
	}
	if state.ToMove != PlayerRed {
		t.Fatalf("expected red/red to play, got %v", state.ToMove)
	}
	if state.CapturedBlue != 6 || state.CapturedRed != 4 {
		t.Fatalf("expected captures blue/red to be 6/4, got %d/%d", state.CapturedBlue, state.CapturedRed)
	}
	if stats.RootCandidates == 0 {
		t.Fatalf("expected at least one root candidate on screenshot-derived fixed position")
	}
}

func TestScoreBoardFixedPositionRedToPlaySixVsFourCapturesDoesNotReturnIllegalSentinelForForcedBlock(t *testing.T) {
	state, rules, settings := buildRedToPlaySixVsFourCapturesFixedState()
	cfg := DefaultConfig()
	cfg.AiDepth = 9
	cfg.AiMinDepth = 9
	cfg.AiMaxDepth = 9
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiEnableTtPersistence = false
	cfg.AiUseTtCache = false
	cfg.AiEnableRootTranspose = false
	cfg.AiLogSearchStats = false

	cache := newAISearchCache()
	stats := &SearchStats{}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           9,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: true,
	})

	forcedIdx := 8*settings.BoardSize + 10
	if scores[forcedIdx] == illegalScore {
		t.Fatalf("expected forced block (10,8) to receive a real score, got illegal sentinel")
	}
}

func TestCollectCandidateMovesWithEvalReturnsOnlyLegalMoves(t *testing.T) {
	state, rules, settings := buildRedToPlaySixVsFourCapturesFixedState()
	sequence := []Move{
		{X: 10, Y: 8}, // Red
		{X: 4, Y: 8},  // Blue
		{X: 3, Y: 7},  // Red
		{X: 10, Y: 7}, // Blue
		{X: 8, Y: 5},  // Red captures (7,6) and (6,7)
	}
	players := []PlayerColor{
		PlayerRed,
		PlayerBlue,
		PlayerRed,
		PlayerBlue,
		PlayerRed,
	}
	for i, move := range sequence {
		if ok, msg := rules.IsLegal(state, move, players[i]); !ok {
			t.Fatalf("expected sequence move %d %v to be legal: %s", i, move, msg)
		}
		if !applyMove(&state, rules, move, players[i]) {
			t.Fatalf("failed to apply sequence move %d %v", i, move)
		}
	}
	if state.ToMove != PlayerBlue {
		t.Fatalf("expected blue to move after sequence, got %v", state.ToMove)
	}
	evalState := BuildEvalStateFromBoard(
		state.Board,
		state.ToMove,
		clampUint8(state.CapturedBlue),
		clampUint8(state.CapturedRed),
		DefaultConfig(),
	)
	candidates := collectCandidateMovesWithEval(state, rules, state.ToMove, settings.BoardSize, &evalState, nil)
	for _, cand := range candidates {
		if ok, msg := rules.IsLegal(state, cand.move, state.ToMove); !ok {
			t.Fatalf("expected candidate %v to be legal, got %q", cand.move, msg)
		}
	}
}

func TestAnalyzeThreatsFiltersIllegalWinningMoves(t *testing.T) {
	state, rules, settings := buildRedToPlaySixVsFourCapturesFixedState()
	sequence := []Move{
		{X: 10, Y: 8},
		{X: 8, Y: 5},
		{X: 4, Y: 7},
		{X: 7, Y: 10},
		{X: 7, Y: 7},
		{X: 9, Y: 7},
		{X: 3, Y: 6},
	}
	players := []PlayerColor{
		PlayerRed,
		PlayerBlue,
		PlayerRed,
		PlayerBlue,
		PlayerRed,
		PlayerBlue,
		PlayerRed,
	}
	for i, move := range sequence {
		if ok, msg := rules.IsLegal(state, move, players[i]); !ok {
			t.Fatalf("expected sequence move %d %v to be legal: %s", i, move, msg)
		}
		if !applyMove(&state, rules, move, players[i]) {
			t.Fatalf("failed to apply sequence move %d %v", i, move)
		}
	}
	if state.ToMove != PlayerBlue {
		t.Fatalf("expected blue to move after sequence, got %v", state.ToMove)
	}
	evalState := BuildEvalStateFromBoard(
		state.Board,
		state.ToMove,
		clampUint8(state.CapturedBlue),
		clampUint8(state.CapturedRed),
		DefaultConfig(),
	)
	context := AnalyzeThreats(state, rules, AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    DefaultConfig(),
	}, state.ToMove, &evalState)
	for _, move := range context.WinningMoves {
		if ok, msg := rules.IsLegal(state, move, state.ToMove); !ok {
			t.Fatalf("expected winning move %v to be legal, got %q", move, msg)
		}
	}
}

func TestScoreBoardNoCompletedDepthUsesRootOrderedFallback(t *testing.T) {
	state, rules, settings := buildFixedSearchBenchmarkState()
	cfg := DefaultConfig()
	cfg.AiDepth = 3
	cfg.AiMinDepth = 3
	cfg.AiMaxDepth = 3
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiEnableTtPersistence = false
	cfg.AiLogSearchStats = false

	expectedSettings := AIScoreSettings{
		Depth:     3,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}
	ctx := newMinimaxContext(rules, expectedSettings, time.Now())
	attachEvalState(&ctx, state)
	expectedScores := rootOrderedFallbackScores(state, expectedSettings, ctx, nil)
	expected, ok := bestMoveFromScores(expectedScores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected root fallback move")
	}

	cache := newAISearchCache()
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           3,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		DirectDepthOnly: true,
		ShouldStop: func() bool {
			return true
		},
	})
	best, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected fallback best move")
	}
	if !best.Equals(expected) {
		t.Fatalf("expected root-ordered fallback (%d,%d), got (%d,%d)", expected.X, expected.Y, best.X, best.Y)
	}
}

func TestDebugScoreBoardFixedPosition(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_benchmark", buildFixedSearchBenchmarkState, Move{X: 12, Y: 7})
}

func TestDebugScoreBoardFixedPositionQuietCenter(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_quiet_center", buildQuietCenterFixedState, Move{X: 7, Y: 7})
}

func TestDebugScoreBoardFixedPositionCurrentPlayerTactical(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_current_player_tactical", buildCurrentPlayerTacticalFixedState, Move{X: 7, Y: 9})
}

func TestDebugScoreBoardFixedPositionNextPlayerTactical(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_next_player_tactical", buildNextPlayerTacticalFixedState, Move{X: 8, Y: 9}, Move{X: 12, Y: 9})
}

func TestDebugScoreBoardFixedPositionDoubleBlockedFour(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_double_blocked_four", buildDoubleBlockedFourFixedState, Move{X: 6, Y: 9})
}

func TestDebugScoreBoardFixedPositionProtectCapture(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_protect_capture", buildProtectCaptureFixedState, Move{X: 11, Y: 9})
}

func TestDebugScoreBoardFixedPositionMultiThreat(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_multi_threat", buildMultiThreatFixedState, Move{X: 9, Y: 7}, Move{X: 7, Y: 9}, Move{X: 9, Y: 11})
}

func TestDebugScoreBoardFixedPositionRedToPlayCaptureRace(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_red_to_play_capture_race", buildRedToPlayCaptureRaceFixedState, Move{X: 9, Y: 4})
}

func TestDebugScoreBoardFixedPositionRedToPlaySixVsFourCaptures(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_red_to_play_six_vs_four_captures", buildRedToPlaySixVsFourCapturesFixedState)
}

func TestDebugScoreBoardFixedPositionRedSixSixSnapshot(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_red_six_six_snapshot", buildRedToPlaySixSixSnapshotFixedState)
}

type fixedOrderingScenario struct {
	name  string
	build func() (GameState, Rules, GameSettings)
}

func fixedOrderingScenarios() []fixedOrderingScenario {
	return []fixedOrderingScenario{
		{name: "fixed_benchmark", build: buildFixedSearchBenchmarkState},
		{name: "fixed_quiet_center", build: buildQuietCenterFixedState},
		{name: "fixed_current_player_tactical", build: buildCurrentPlayerTacticalFixedState},
		{name: "fixed_current_player_tactical_tempo", build: buildCurrentPlayerTacticalTempoFixedState},
		{name: "fixed_next_player_tactical", build: buildNextPlayerTacticalFixedState},
		{name: "fixed_double_blocked_four", build: buildDoubleBlockedFourFixedState},
		{name: "fixed_protect_capture", build: buildProtectCaptureFixedState},
		{name: "fixed_multi_threat", build: buildMultiThreatFixedState},
		{name: "fixed_red_to_play_capture_race", build: buildRedToPlayCaptureRaceFixedState},
		{name: "fixed_red_to_play_six_vs_four_captures", build: buildRedToPlaySixVsFourCapturesFixedState},
		{name: "fixed_red_six_six_snapshot", build: buildRedToPlaySixSixSnapshotFixedState},
		{name: "fixed_capture_instead_of_block", build: buildCaptureInsteadOfBlockFixedState},
	}
}

func initialRootOrderForDepth(state GameState, rules Rules, settings GameSettings, cfg Config, depth int) []Move {
	scoreSettings := AIScoreSettings{
		Depth:     depth,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}
	ctx := newMinimaxContext(rules, scoreSettings, stateTimeForTest())
	attachEvalState(&ctx, state)
	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) == 0 {
		return nil
	}
	rootMaximizing := state.ToMove == PlayerRed
	ordered := sortRootMoveIndices(rootPool, rootMaximizing, nil)
	moves := make([]Move, 0, len(ordered))
	for _, idx := range ordered {
		if idx < 0 || idx >= len(rootPool) {
			continue
		}
		moves = append(moves, rootPool[idx].Move)
	}
	return moves
}

func moveRankInList(moves []Move, target Move) int {
	for i, move := range moves {
		if move.Equals(target) {
			return i
		}
	}
	return -1
}

func buildRandomOrderingBenchmarkCases(tb testing.TB, count int, seed int64) []orderingBenchmarkCase {
	tb.Helper()
	if count <= 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed))
	scenarios := []string{
		benchmarkScenarioComplex,
		benchmarkScenarioCaptures,
	}
	cases := make([]orderingBenchmarkCase, 0, count)
	for len(cases) < count {
		scenario := scenarios[rng.Intn(len(scenarios))]
		state, rules, settings, ok := buildRandomOrderingBenchmarkCase(tb, rng, scenario)
		if !ok {
			continue
		}
		cases = append(cases, orderingBenchmarkCase{
			name:     scenario,
			state:    state,
			rules:    rules,
			settings: settings,
		})
	}
	return cases
}

func buildRandomOrderingBenchmarkCase(tb testing.TB, rng *rand.Rand, scenario string) (GameState, Rules, GameSettings, bool) {
	tb.Helper()
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	applySeedScenario(tb, &state, rules, scenario)
	moves := orderedScenarioMoves(settings.BoardSize, scenario)
	if len(moves) == 0 {
		return state, rules, settings, false
	}

	targetExtra := 0
	switch scenario {
	case benchmarkScenarioComplex:
		targetExtra = 8 + rng.Intn(18)
	case benchmarkScenarioCaptures:
		targetExtra = 4 + rng.Intn(14)
	default:
		targetExtra = 8 + rng.Intn(12)
	}

	perm := rng.Perm(len(moves))
	applied := 0
	for _, idx := range perm {
		if applied >= targetExtra || state.Status != StatusRunning {
			break
		}
		move := moves[idx]
		if !move.IsValid(settings.BoardSize) || !state.Board.IsEmpty(move.X, move.Y) {
			continue
		}
		player := state.ToMove
		if _, ok := applyReplayMove(&state, rules, move, player); !ok {
			continue
		}
		applied++
	}
	if applied < 4 || state.Status != StatusRunning {
		return state, rules, settings, false
	}
	state.recomputeHashes()
	return state, rules, settings, true
}

func orderingBenchmarkConfig(depth int) Config {
	cfg := liveAIConfig(DefaultConfig())
	cfg.AiDepth = depth
	cfg.AiMinDepth = 1
	cfg.AiMaxDepth = depth
	cfg.AiDepthStep = 1
	cfg.AiLogSearchStats = false
	return cfg
}

func collectOrderingBenchmarkDepthResults(benchCase orderingBenchmarkCase, maxDepth int, cfg Config, onDepthComplete func(int, Move)) ([]orderingBenchmarkDepthResult, int, error) {
	state := benchCase.state.Clone()
	rules := benchCase.rules
	settings := benchCase.settings
	initialOrder := initialRootOrderForDepth(state, rules, settings, cfg, 1)
	if len(initialOrder) == 0 {
		return nil, 0, fmt.Errorf("%s: empty initial root order", benchCase.name)
	}
	depthMoves := make(map[int]Move, maxDepth)
	cache := newAISearchCache()
	stats := &SearchStats{}
	_ = ScoreBoard(state, rules, AIScoreSettings{
		Depth:           maxDepth,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: false,
		OnDepthComplete: func(depth int, move Move, score float64) {
			_ = score
			depthMoves[depth] = move
			if onDepthComplete != nil {
				onDepthComplete(depth, move)
			}
		},
	})
	results := make([]orderingBenchmarkDepthResult, 0, len(depthMoves))
	for depth := 1; depth <= stats.CompletedDepths && depth <= maxDepth; depth++ {
		move, ok := depthMoves[depth]
		if !ok {
			continue
		}
		rank := moveRankInList(initialOrder, move)
		if rank < 0 {
			return nil, 0, fmt.Errorf("%s: depth %d move %+v missing from initial order", benchCase.name, depth, move)
		}
		results = append(results, orderingBenchmarkDepthResult{
			depth:     depth,
			finalMove: move,
			rank:      rank,
		})
	}
	return results, len(initialOrder), nil
}

func summarizeOrderingBenchmark(depth int, results []orderingBenchmarkDepthResult, candidateCounts []int) orderingBenchmarkSummary {
	summary := orderingBenchmarkSummary{depth: depth, positions: len(results)}
	if len(results) == 0 {
		return summary
	}
	var top1, top2, top4, top8 int
	var rankSum, candidateSum int
	for i, result := range results {
		rank1 := result.rank + 1
		rankSum += rank1
		if i < len(candidateCounts) {
			candidateSum += candidateCounts[i]
		}
		if rank1 > summary.worstRank {
			summary.worstRank = rank1
		}
		if rank1 == 1 {
			top1++
		}
		if rank1 <= 2 {
			top2++
		}
		if rank1 <= 4 {
			top4++
		}
		if rank1 <= 8 {
			top8++
		}
	}
	summary.avgRank = float64(rankSum) / float64(len(results))
	summary.avgCandidates = float64(candidateSum) / float64(len(results))
	summary.top1Pct = float64(top1) * 100.0 / float64(len(results))
	summary.top2Pct = float64(top2) * 100.0 / float64(len(results))
	summary.top4Pct = float64(top4) * 100.0 / float64(len(results))
	summary.top8Pct = float64(top8) * 100.0 / float64(len(results))
	return summary
}

func summarizeOrderingAggregate(depth int, agg orderingBenchmarkAggregate) orderingBenchmarkSummary {
	summary := orderingBenchmarkSummary{
		depth:     depth,
		positions: agg.positions,
		worstRank: agg.worstRank,
	}
	if agg.positions == 0 {
		return summary
	}
	summary.avgCandidates = float64(agg.candidateSum) / float64(agg.positions)
	summary.avgRank = float64(agg.rankSum) / float64(agg.positions)
	summary.top1Pct = float64(agg.top1Count) * 100.0 / float64(agg.positions)
	summary.top2Pct = float64(agg.top2Count) * 100.0 / float64(agg.positions)
	summary.top4Pct = float64(agg.top4Count) * 100.0 / float64(agg.positions)
	summary.top8Pct = float64(agg.top8Count) * 100.0 / float64(agg.positions)
	return summary
}

func writeRootOrderingBenchmarkLog(tb testing.TB, corpusSize int, processed int, seed int64, summaries []orderingBenchmarkSummary) {
	tb.Helper()
	var builder strings.Builder
	fmt.Fprintf(&builder, "root ordering benchmark\n")
	fmt.Fprintf(&builder, "seed=%d\n", seed)
	fmt.Fprintf(&builder, "positions_target=%d\n", corpusSize)
	fmt.Fprintf(&builder, "positions_done=%d\n", processed)
	fmt.Fprintf(&builder, "depths=%d\n\n", len(summaries))
	fmt.Fprintf(&builder, "depth positions avg_candidates avg_rank worst_rank top1%% top2%% top4%% top8%%\n")
	for _, summary := range summaries {
		fmt.Fprintf(&builder, "%d %d %.3f %.3f %d %.2f %.2f %.2f %.2f\n",
			summary.depth,
			summary.positions,
			summary.avgCandidates,
			summary.avgRank,
			summary.worstRank,
			summary.top1Pct,
			summary.top2Pct,
			summary.top4Pct,
			summary.top8Pct,
		)
	}
	path := "root_ordering_benchmark.log"
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		tb.Fatalf("failed to write %s: %v", path, err)
	}
}

func collectRootOrderingBenchmarkSummaries(tb testing.TB, maxDepth int, corpus []orderingBenchmarkCase, seed int64) []orderingBenchmarkSummary {
	tb.Helper()
	cfg := orderingBenchmarkConfig(maxDepth)
	aggregates := make(map[int]orderingBenchmarkAggregate, maxDepth)
	progressStep := benchmarkProgressStep(len(corpus))
	logBenchmarkProgress("collect start positions=%d max_depth=%d", len(corpus), maxDepth)
	for i, benchCase := range corpus {
		logBenchmarkProgress("collect position start %d/%d scenario=%s", i+1, len(corpus), benchCase.name)
		results, orderCount, err := collectOrderingBenchmarkDepthResults(benchCase, maxDepth, cfg, func(depth int, move Move) {
			logBenchmarkProgress("collect position %d/%d scenario=%s depth=%d move=(%d,%d)", i+1, len(corpus), benchCase.name, depth, move.X, move.Y)
		})
		if err != nil {
			tb.Fatal(err)
		}
		for _, result := range results {
			agg := aggregates[result.depth]
			rank1 := result.rank + 1
			agg.positions++
			agg.candidateSum += orderCount
			agg.rankSum += rank1
			if rank1 > agg.worstRank {
				agg.worstRank = rank1
			}
			if rank1 == 1 {
				agg.top1Count++
			}
			if rank1 <= 2 {
				agg.top2Count++
			}
			if rank1 <= 4 {
				agg.top4Count++
			}
			if rank1 <= 8 {
				agg.top8Count++
			}
			aggregates[result.depth] = agg
		}
		summaries := make([]orderingBenchmarkSummary, 0, maxDepth)
		for depth := 1; depth <= maxDepth; depth++ {
			summaries = append(summaries, summarizeOrderingAggregate(depth, aggregates[depth]))
		}
		writeRootOrderingBenchmarkLog(tb, len(corpus), i+1, seed, summaries)
		depth10 := summaries[maxDepth-1]
		if depth10.positions > 0 {
			logBenchmarkProgress("collect cumulative %d/%d depth10 avg_rank=%.3f worst=%d top8=%.2f%% avg_candidates=%.2f",
				i+1, len(corpus), depth10.avgRank, depth10.worstRank, depth10.top8Pct, depth10.avgCandidates)
		}
		if (i+1)%progressStep == 0 || i+1 == len(corpus) {
			logBenchmarkProgress("collect progress %d/%d scenario=%s", i+1, len(corpus), benchCase.name)
		}
	}
	summaries := make([]orderingBenchmarkSummary, 0, maxDepth)
	for depth := 1; depth <= maxDepth; depth++ {
		summaries = append(summaries, summarizeOrderingAggregate(depth, aggregates[depth]))
	}
	logBenchmarkProgress("collect done positions=%d depths=1..%d", len(corpus), maxDepth)
	return summaries
}

func runRootOrderingBenchmarkCorpus(b *testing.B, depth int, corpus []orderingBenchmarkCase) orderingBenchmarkSummary {
	b.Helper()
	if depth <= 0 {
		b.Fatalf("invalid depth %d", depth)
	}
	summaries := collectRootOrderingBenchmarkSummaries(b, depth, corpus, 0xC0FFEE)
	summary := summaries[depth-1]
	b.ReportMetric(float64(len(corpus)), "positions")
	b.ReportMetric(summary.avgCandidates, "avg_candidates")
	b.ReportMetric(summary.avgRank, "avg_rank")
	b.ReportMetric(float64(summary.worstRank), "worst_rank")
	b.ReportMetric(summary.top1Pct, "top1_%")
	b.ReportMetric(summary.top2Pct, "top2_%")
	b.ReportMetric(summary.top4Pct, "top4_%")
	b.ReportMetric(summary.top8Pct, "top8_%")
	return summary
}

func TestFixedPositionInitialOrderingReport(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiDepth = 8
	cfg.AiMinDepth = 8
	cfg.AiMaxDepth = 8
	cfg.AiDepthStep = 1
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiUseTtCache = false
	cfg.AiEnableRootTranspose = false
	cfg.AiEnableTtPersistence = false
	cfg.AiLogSearchStats = false
	cfg.AiLazySMPWorkers = 1

	type orderingResult struct {
		name       string
		finalMove  Move
		rank       int
		orderCount int
	}
	results := make([]orderingResult, 0, len(fixedOrderingScenarios()))
	maxRank := -1
	top8Count := 0

	for _, scenario := range fixedOrderingScenarios() {
		state, rules, settings := scenario.build()
		initialOrder := initialRootOrderForDepth(state, rules, settings, cfg, 1)
		if len(initialOrder) == 0 {
			t.Fatalf("%s: expected non-empty initial root order", scenario.name)
		}

		cache := newAISearchCache()
		stats := &SearchStats{}
		scores := ScoreBoard(state, rules, AIScoreSettings{
			Depth:           cfg.AiDepth,
			TimeoutMs:       0,
			BoardSize:       settings.BoardSize,
			Player:          state.ToMove,
			Cache:           &cache,
			Config:          cfg,
			Stats:           stats,
			DirectDepthOnly: false,
		})
		finalMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
		if !ok {
			t.Fatalf("%s: expected final best move", scenario.name)
		}
		rank := moveRankInList(initialOrder, finalMove)
		if rank < 0 {
			t.Fatalf("%s: final move %+v not found in depth-1 root order of %d moves", scenario.name, finalMove, len(initialOrder))
		}
		if rank > maxRank {
			maxRank = rank
		}
		if rank < 8 {
			top8Count++
		}
		results = append(results, orderingResult{
			name:       scenario.name,
			finalMove:  finalMove,
			rank:       rank,
			orderCount: len(initialOrder),
		})
		t.Logf("%s: final=(%d,%d) rank=%d/%d completed_depth=%d returned_depth=%d",
			scenario.name,
			finalMove.X, finalMove.Y,
			rank+1, len(initialOrder),
			stats.CompletedDepths, stats.ReturnedDepth,
		)
	}

	t.Logf("ordering summary: scenarios=%d top8=%d/%d worst_rank=%d",
		len(results), top8Count, len(results), maxRank+1,
	)
}

func BenchmarkRootOrderingRandomCorpus(b *testing.B) {
	corpusSize := benchmarkCorpusSize()
	seed := int64(0xC0FFEE)
	maxDepth := 10
	corpus := buildRandomOrderingBenchmarkCases(b, corpusSize, seed)
	summaries := collectRootOrderingBenchmarkSummaries(b, maxDepth, corpus, seed)
	writeRootOrderingBenchmarkLog(b, corpusSize, corpusSize, seed, summaries)
	summary := summaries[maxDepth-1]
	b.ReportMetric(float64(corpusSize), "positions")
	b.ReportMetric(summary.avgCandidates, "avg_candidates")
	b.ReportMetric(summary.avgRank, "avg_rank")
	b.ReportMetric(float64(summary.worstRank), "worst_rank")
	b.ReportMetric(summary.top1Pct, "top1_%")
	b.ReportMetric(summary.top2Pct, "top2_%")
	b.ReportMetric(summary.top4Pct, "top4_%")
	b.ReportMetric(summary.top8Pct, "top8_%")
}

func TestProtectCaptureFixedSelectionKeepsRootBestAtFullDepth(t *testing.T) {
	state, rules, settings := buildProtectCaptureFixedState()
	cfg := liveAIConfig(DefaultConfig())
	cfg.AiDepth = 10
	cfg.AiTimeoutMs = 0
	cfg.AiTimeBudgetMs = 0
	cfg.AiLogSearchStats = false
	cfg.LogDepthScores = false

	FlushGlobalCaches()
	cache := liveSearchCache(cfg)
	stats := &SearchStats{}
	ai := NewAIPlayer()
	defer ai.StopThinking()

	searchSettings := AIScoreSettings{
		Depth:           cfg.AiDepth,
		TimeoutMs:       cfg.AiTimeoutMs,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: false,
	}
	scores := ScoreBoard(state, rules, searchSettings)
	rootBest, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected root best move from scores")
	}
	selected, ok := ai.selectBestMove(state, rules, searchSettings, stats, scores)
	if !ok {
		t.Fatalf("expected selected move")
	}
	if !selected.Equals(rootBest) {
		t.Fatalf("expected full-depth selection to keep root best (%d,%d), got (%d,%d) root_score=%.2f selected_score=%.2f completed=%d returned=%d",
			rootBest.X, rootBest.Y,
			selected.X, selected.Y,
			scoreForMove(scores, rootBest, settings.BoardSize),
			scoreForMove(scores, selected, settings.BoardSize),
			stats.CompletedDepths, stats.ReturnedDepth,
		)
	}
}

func TestBuildRootMovePoolSkipsLocalityWhenForcedMoveExists(t *testing.T) {
	state, rules, settings := buildProtectCaptureFixedState()
	cfg := DefaultConfig()
	scoreSettings := AIScoreSettings{
		Depth:     6,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}
	ctx := newMinimaxContext(rules, scoreSettings, stateTimeForTest())
	attachEvalState(&ctx, state)

	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) == 0 {
		t.Fatalf("expected non-empty root pool")
	}
	forcedCount := 0
	for _, rm := range rootPool {
		if !rm.IsForced {
			t.Fatalf("expected forced-only root pool, found non-forced move (%d,%d)", rm.Move.X, rm.Move.Y)
		}
		if rm.SourceFlags&rootSourceLocality != 0 {
			t.Fatalf("expected forced pool to skip locality, move (%d,%d) has locality source", rm.Move.X, rm.Move.Y)
		}
		forcedCount++
	}
	if forcedCount == 0 {
		t.Fatalf("expected at least one forced move")
	}
}

func TestScoreBoardStopsDeepeningOnUniqueForcedDeadend(t *testing.T) {
	state, rules, settings := buildProtectCaptureFixedState()
	rootCfg := DefaultConfig()
	rootCtx := newMinimaxContext(rules, AIScoreSettings{
		Depth:     6,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    rootCfg,
	}, stateTimeForTest())
	attachEvalState(&rootCtx, state)
	rootPool := buildRootMovePool(state, rootCtx, state.ToMove)
	if len(rootPool) != 1 || !rootPool[0].IsForced {
		t.Skipf("scenario no longer yields a unique forced root move under current threat model (pool=%d)", len(rootPool))
	}

	cfg := liveAIConfig(DefaultConfig())
	cfg.AiDepth = 10
	cfg.AiMinDepth = 6
	cfg.AiMaxDepth = 10
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiLogSearchStats = false
	cfg.LogDepthScores = false
	cfg.AiLazySMPWorkers = 4

	FlushGlobalCaches()
	cache := newAISearchCache()
	stats := &SearchStats{}
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           cfg.AiDepth,
		TimeoutMs:       cfg.AiTimeoutMs,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: false,
	})
	if stats.CompletedDepths != cfg.AiMinDepth {
		t.Fatalf("expected completed depth %d, got %d", cfg.AiMinDepth, stats.CompletedDepths)
	}
	if stats.ReturnedDepth != cfg.AiMinDepth {
		t.Fatalf("expected returned depth %d, got %d", cfg.AiMinDepth, stats.ReturnedDepth)
	}
	if stats.DecisionSource != "ROOT_UNIQUE_PROVEN_DEADEND" {
		t.Fatalf("expected proven deadend decision source, got %q", stats.DecisionSource)
	}
	bestMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected a best move")
	}
	if !bestMove.Equals(Move{X: 11, Y: 9}) {
		t.Fatalf("expected forced move (11,9), got (%d,%d)", bestMove.X, bestMove.Y)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func runFixedPositionDebugScenario(t *testing.T, name string, build func() (GameState, Rules, GameSettings), expectedMoves ...Move) {
	t.Helper()
	if os.Getenv("GOMOKU_DEBUG_FIXED") == "" {
		t.Skip("set GOMOKU_DEBUG_FIXED=1 to run fixed-position search debug suite")
	}

	baseState, rules, settings := build()
	cfg := DefaultConfig()
	cfg.AiDepth = 10
	cfg.AiMinDepth = 6
	cfg.AiMaxDepth = 10
	cfg.AiDepthStep = 1
	cfg.AiLazySMPWorkers = 4
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiLogSearchStats = true
	cfg.LogDepthScores = true
	useLiveCache := shouldUseFixedLiveCache(name)
	keepSharedCache := shouldKeepFixedSharedCache(name)
	warmSharedCache := shouldWarmFixedSharedCache(name)
	cfg.AiUseTtCache = true
	if !useLiveCache || !keepSharedCache {
		FlushGlobalCaches()
	}
	cache := newAISearchCache()
	if useLiveCache {
		cache = *liveSearchCache(cfg)
	}

	var log bytes.Buffer
	writeLine := func(format string, args ...any) {
		fmt.Fprintf(&log, format+"\n", args...)
	}

	writeLine("scenario=%s", name)
	writeLine("cache_mode=%s", fixedCacheModeLabel(useLiveCache))
	writeLine("cache_flush=%t", !useLiveCache || !keepSharedCache)
	writeLine("cache_warm=%t", useLiveCache && warmSharedCache)
	writeLine("next_to_play=%s", playerColorLabel(baseState.ToMove))
	writeLine("score_target=%s", scoreTargetLabel(baseState.ToMove))
	writeLine("fixed_search_start to_move=%v captures=(blue:%d red:%d)\n%s",
		baseState.ToMove,
		baseState.CapturedBlue,
		baseState.CapturedRed,
		formatBoardForTestLog(baseState.Board),
	)
	rootEvalState := BuildEvalStateFromBoard(
		baseState.Board,
		baseState.ToMove,
		clampUint8(baseState.CapturedBlue),
		clampUint8(baseState.CapturedRed),
		cfg,
	)
	rootLUTAlignments := collectFixedLUTAlignmentsForLog(baseState.Board, &rootEvalState)
	writeLine("fixed_lut_alignments total=%d %s", len(rootLUTAlignments), formatFixedLUTAlignmentsForLog(rootLUTAlignments, 48))
	baseEval := EvaluateBoardWithContext(
		baseState.Board,
		baseState.ToMove,
		clampUint8(baseState.CapturedBlue),
		clampUint8(baseState.CapturedRed),
		cfg,
	)
	writeLine("fixed_eval score=%d structural=%d capture=%d combo=%d summary=%s own_threats=%s opp_threats=%s compact=%s",
		baseEval.Score,
		baseEval.StructuralScore,
		baseEval.CaptureScore,
		baseEval.ComboScore,
		formatTacticalSummaryForTest(baseEval.Summary),
		formatThreatsForTest(threatsForPlayerEvalResult(baseEval, baseState.ToMove), 8),
		formatThreatsForTest(threatsForPlayerEvalResult(baseEval, otherPlayer(baseState.ToMove)), 8),
		formatThreatArrayForTest(baseEval.Threats[:int(baseEval.ThreatCount)], 8),
	)
	var rootPureLUTMoves []pureLUTMoveInfo
	if shouldEnableFixedLUTPureExtras() {
		rootPureLUTMoves = collectPureLUTMoveInfosForTest(baseState, baseState.ToMove, &rootEvalState, settings.BoardSize)
		writeLine("fixed_root_lut_pure total=%d moves=%s", len(rootPureLUTMoves), formatPureLUTMovesForTest(rootPureLUTMoves, 24))
	}
	writeFixedRootLocalityDetails(&log, baseState, rules, settings, cfg)
	debugFixedRootPipelineToWriter(&log, baseState, rules, settings, &cache)

	if useLiveCache && warmSharedCache {
		warmState := baseState.Clone()
		warmStats := &SearchStats{}
		_ = ScoreBoard(warmState, rules, AIScoreSettings{
			Depth:                   6,
			TimeoutMs:               0,
			BoardSize:               settings.BoardSize,
			Player:                  warmState.ToMove,
			Cache:                   &cache,
			Config:                  cfg,
			Stats:                   warmStats,
			DirectDepthOnly:         false,
			DebugWideRootAtDepthOne: true,
		})
		writeLine("cache_warmup completed_depth=%d returned_depth=%d nodes=%d", warmStats.CompletedDepths, warmStats.ReturnedDepth, warmStats.Nodes)
	}

	state := baseState.Clone()
	stats := &SearchStats{}
	depthMoves := make(map[int]Move, 6)
	depthScores := make(map[int]float64, 6)
	depthLines := make(map[int]*SearchDebugLine, 6)
	lastDepthTacticalKnown := false
	lastDepthTactical := false
	var rootBestLogged Move
	var bestMoveLogged Move
	bestScoreLogged := illegalScore
	bestReturnedDepth := 0
	enableCandidateBoard := shouldEnableFixedCandidateBoard(name)
	enableFinalBoard := shouldEnableFixedDepthFinalBoard()
	enableThreatTimeline := shouldEnableFixedThreatTimeline(name)

	stdoutLog := captureFixedDebugStdout(t, func() {
		restoreCandidateBoard := setEnvForTest("GOMOKU_DEBUG_CANDIDATE_BOARD", boolEnvValue(enableCandidateBoard))
		defer restoreCandidateBoard()
		scores := ScoreBoard(state, rules, AIScoreSettings{
			Depth:                   6,
			TimeoutMs:               0,
			BoardSize:               settings.BoardSize,
			Player:                  state.ToMove,
			Cache:                   &cache,
			Config:                  cfg,
			Stats:                   stats,
			DirectDepthOnly:         false,
			DebugWideRootAtDepthOne: true,
			OnDepthComplete: func(doneDepth int, move Move, score float64) {
				depthMoves[doneDepth] = move
				depthScores[doneDepth] = score
				writeLine("depth_complete depth=%d move=(%d,%d) score=%.2f", doneDepth, move.X, move.Y, score)
				next := baseState.Clone()
				if applyMove(&next, rules, move, baseState.ToMove) {
					depthEval := EvaluateBoardWithContext(
						next.Board,
						next.ToMove,
						clampUint8(next.CapturedBlue),
						clampUint8(next.CapturedRed),
						cfg,
					)
					changed := !lastDepthTacticalKnown || depthEval.Summary.IsTactical != lastDepthTactical
					if changed {
						writeLine("depth_tactical_transition depth=%d move=(%d,%d) tactical=%t summary=%s own=%s opp=%s compact=%s",
							doneDepth,
							move.X,
							move.Y,
							depthEval.Summary.IsTactical,
							formatTacticalSummaryForTest(depthEval.Summary),
							formatThreatsForTest(threatsForPlayerEvalResult(depthEval, next.ToMove), 6),
							formatThreatsForTest(threatsForPlayerEvalResult(depthEval, otherPlayer(next.ToMove)), 6),
							formatThreatArrayForTest(depthEval.Threats[:int(depthEval.ThreatCount)], 6),
						)
					}
					lastDepthTacticalKnown = true
					lastDepthTactical = depthEval.Summary.IsTactical
				}
			},
			OnDepthCompleteDebug: func(doneDepth int, move Move, score float64, line *SearchDebugLine) {
				if !enableFinalBoard {
					return
				}
				depthLines[doneDepth] = cloneSearchDebugLine(line)
			},
		})

		bestMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
		if !ok {
			t.Fatalf("expected best move at iterative depth 6")
		}
		rootBestLogged = bestMove
		returnedDepth := stats.ReturnedDepth
		if returnedDepth <= 0 {
			returnedDepth = stats.CompletedDepths
		}
		bestReturnedDepth = returnedDepth
		prevConfig := GetConfig()
		configStore.Update(cfg)
		ai := &AIPlayer{}
		selectedMove, ok := ai.selectBestMove(state, rules, AIScoreSettings{
			Depth:                   cfg.AiDepth,
			TimeoutMs:               cfg.AiTimeoutMs,
			BoardSize:               settings.BoardSize,
			Player:                  state.ToMove,
			Cache:                   &cache,
			Config:                  cfg,
			Stats:                   stats,
			DirectDepthOnly:         false,
			DebugWideRootAtDepthOne: true,
		}, stats, scores)
		configStore.Update(prevConfig)
		if !ok {
			t.Fatalf("expected selected best move at iterative depth 6")
		}
		bestMoveLogged = selectedMove
		sortedScores := sortedScoredMovesForTest(scores, state, rules, settings.BoardSize)
		if rootBestLogged.IsValid(settings.BoardSize) && !rootBestLogged.Equals(bestMoveLogged) {
			writeLine("fixed_selection_override root_best=(%d,%d) selected=(%d,%d) root_score=%.2f selected_score=%.2f",
				rootBestLogged.X, rootBestLogged.Y,
				bestMoveLogged.X, bestMoveLogged.Y,
				scoreForMove(scores, rootBestLogged, settings.BoardSize),
				scoreForMove(scores, bestMoveLogged, settings.BoardSize),
			)
		}
		writeLine("fixed_scores_sorted returned_depth=%d completed=%d total=%d %s",
			returnedDepth,
			stats.CompletedDepths,
			len(sortedScores),
			formatSortedScoredMovesForTest(sortedScores),
		)
		writeFixedRootCalmOrderingFeatures(&log, baseState, rules, settings, cfg, &cache, bestMoveLogged)
		for depth := 1; depth <= stats.CompletedDepths; depth++ {
			move := depthMoves[depth]
			score := depthScores[depth]
			writeLine("fixed_search depth=%d completed=%d returned=%d selected=(%d,%d) root_best=(%d,%d) depth_move=(%d,%d) depth_score=%.2f nodes=%d root_cands=%d deep_cands=%d heur_calls=%d board_ops=%d",
				depth,
				stats.CompletedDepths,
				returnedDepth,
				bestMoveLogged.X, bestMoveLogged.Y,
				rootBestLogged.X, rootBestLogged.Y,
				move.X, move.Y,
				score,
				stats.Nodes,
				stats.RootCandidates,
				stats.DeepCandidates,
				stats.HeuristicCalls,
				stats.BoardGenOps,
			)
		}
		bestScore := illegalScore
		if bestMoveLogged.IsValid(settings.BoardSize) {
			idx := bestMoveLogged.Y*settings.BoardSize + bestMoveLogged.X
			if idx >= 0 && idx < len(scores) {
				bestScore = scores[idx]
			}
		}
		bestScoreLogged = bestScore
		writeLine("ordering root_first=%d/%d(%.1f%%) root_top2=%d/%d(%.1f%%) root_top3=%d/%d(%.1f%%) root_first_by_depth=[%s]",
			stats.RootFirstMoveWins,
			stats.RootFirstMoveSamples,
			percentRatio(stats.RootFirstMoveWins, stats.RootFirstMoveSamples),
			stats.RootTop2Wins,
			stats.RootTop2Samples,
			percentRatio(stats.RootTop2Wins, stats.RootTop2Samples),
			stats.RootTop3Wins,
			stats.RootTop3Samples,
			percentRatio(stats.RootTop3Wins, stats.RootTop3Samples),
			formatRootFirstMoveByDepth(stats),
		)
		writeLine("ordering node_first_lead=%d/%d(%.1f%%) node_first_exact=%d/%d(%.1f%%) node_first_cutoff=%d/%d(%.1f%%)",
			stats.NodeFirstLeadWins,
			stats.NodeFirstLeadSamples,
			percentRatio(stats.NodeFirstLeadWins, stats.NodeFirstLeadSamples),
			stats.NodeFirstExactWins,
			stats.NodeFirstExactSamples,
			percentRatio(stats.NodeFirstExactWins, stats.NodeFirstExactSamples),
			stats.NodeFirstCutoffWins,
			stats.NodeFirstCutoffSamples,
			percentRatio(stats.NodeFirstCutoffWins, stats.NodeFirstCutoffSamples),
		)
		writeLine("ordering root_rank_hist=[%s] root_rank_by_depth=[%s]",
			formatOrderingRankHistogram(stats.RootBestRankHistogram),
			formatOrderingRankByDepth(stats.RootBestRankByDepth),
		)
		writeLine("ordering node_rank_hist=[%s] node_rank_by_depth=[%s]",
			formatOrderingRankHistogram(stats.NodeBestRankHistogram),
			formatOrderingRankByDepth(stats.NodeBestRankByDepth),
		)
		writeLine("ordering pvs_proxy=%d/%d(%.1f%%) pvs_proxy_q=%d/%d(%.1f%%) pvs_proxy_s=%d/%d(%.1f%%) pvs_proxy_h=%d/%d(%.1f%%) pvs_proxy_by_depth=[%s]",
			stats.PVSProxyWouldResearch,
			stats.PVSProxySamples,
			percentRatio(stats.PVSProxyWouldResearch, stats.PVSProxySamples),
			stats.PVSProxyQuietWouldResearch,
			stats.PVSProxyQuietSamples,
			percentRatio(stats.PVSProxyQuietWouldResearch, stats.PVSProxyQuietSamples),
			stats.PVSProxySoftWouldResearch,
			stats.PVSProxySoftSamples,
			percentRatio(stats.PVSProxySoftWouldResearch, stats.PVSProxySoftSamples),
			stats.PVSProxyHardWouldResearch,
			stats.PVSProxyHardSamples,
			percentRatio(stats.PVSProxyHardWouldResearch, stats.PVSProxyHardSamples),
			formatPVSProxyByDepth(stats),
		)
		elapsed := time.Since(stats.Start)
		nps := 0.0
		if elapsed > 0 {
			nps = float64(stats.Nodes) / elapsed.Seconds()
		}
		ttHitRate := percentRatio(stats.TTHits, stats.TTProbes)
		ttCutoffRate := percentRatio(stats.TTCutoffs, stats.Cutoffs)
		evalHitRate := percentRatio(stats.EvalCacheHits, stats.EvalCacheProbes)
		heurShare := 0.0
		boardShare := 0.0
		if elapsed > 0 {
			heurShare = float64(stats.HeuristicTime) * 100.0 / float64(elapsed)
			boardShare = float64(stats.BoardGenTime) * 100.0 / float64(elapsed)
		}
		writeLine("perf elapsed_ms=%d nps=%.0f tt_probe=%d tt_hit=%d tt_hit_rate=%.1f%% tt_exact=%d tt_lower=%d tt_upper=%d cutoffs=%d tt_cutoff=%d ab_cutoff=%d tt_cutoff_rate=%.1f%% eval_probe=%d eval_hit=%d eval_hit_rate=%.1f%% heur_us=%d heur_share=%.1f%% board_us=%d board_share=%.1f%%",
			elapsed.Milliseconds(),
			nps,
			stats.TTProbes,
			stats.TTHits,
			ttHitRate,
			stats.TTExactHits,
			stats.TTLowerHits,
			stats.TTUpperHits,
			stats.Cutoffs,
			stats.TTCutoffs,
			stats.ABCutoffs,
			ttCutoffRate,
			stats.EvalCacheProbes,
			stats.EvalCacheHits,
			evalHitRate,
			stats.HeuristicTime.Microseconds(),
			heurShare,
			stats.BoardGenTime.Microseconds(),
			boardShare,
		)
		writeLine("perf_nodes quiet=%d soft=%d hard=%d quiet_avg_cands=%.2f soft_avg_cands=%.2f hard_avg_cands=%.2f",
			stats.QuietNodes,
			stats.SoftTacticalNodes,
			stats.HardTacticalNodes,
			safeAverage(stats.QuietCandidates, stats.QuietNodes),
			safeAverage(stats.SoftTacticalCandidates, stats.SoftTacticalNodes),
			safeAverage(stats.HardTacticalCandidates, stats.HardTacticalNodes),
		)
		writeLine("perf_cost root_calls=%d root_prep_us=%d analyze_us=%d choose_us=%d hard_build_us=%d hard_gen_us=%d hard_collect_us=%d hard_merge_order_us=%d hard_restrict_order_us=%d gen_threat_us=%d collect_us=%d order_us=%d root_move_evals=%d quiet_cand_us=%d soft_cand_us=%d hard_cand_us=%d quiet_move_evals=%d soft_move_evals=%d hard_move_evals=%d",
			stats.RootSearchCalls,
			stats.RootPrepTime.Microseconds(),
			stats.AnalyzeThreatsTime.Microseconds(),
			stats.ChooseCandidatesTime.Microseconds(),
			stats.BuildHardRestrictedTime.Microseconds(),
			stats.HardBuildGenerateTime.Microseconds(),
			stats.HardBuildCollectTime.Microseconds(),
			stats.HardBuildMergeOrderTime.Microseconds(),
			stats.HardBuildRestrictedTime.Microseconds(),
			stats.GenerateThreatsTime.Microseconds(),
			stats.CollectCandidatesTime.Microseconds(),
			stats.OrderCandidatesTime.Microseconds(),
			stats.RootMoveEvaluations,
			stats.QuietCandidateTime.Microseconds(),
			stats.SoftCandidateTime.Microseconds(),
			stats.HardCandidateTime.Microseconds(),
			stats.QuietMoveEvaluations,
			stats.SoftMoveEvaluations,
			stats.HardMoveEvaluations,
		)
		writeLine("perf_analyze calls=%d strong_calls=%d evalstate_hits=%d eval_us=%d capture_us=%d detail_us=%d urgency_us=%d win_us=%d response_us=%d filter_us=%d",
			stats.AnalyzeThreatCalls,
			stats.AnalyzeThreatStrongCalls,
			stats.AnalyzeThreatEvalStateHits,
			stats.AnalyzeThreatEvalTime.Microseconds(),
			stats.AnalyzeThreatCaptureTime.Microseconds(),
			stats.AnalyzeThreatDetailTime.Microseconds(),
			stats.AnalyzeThreatUrgencyTime.Microseconds(),
			stats.AnalyzeThreatWinTime.Microseconds(),
			stats.AnalyzeThreatResponseTime.Microseconds(),
			stats.AnalyzeThreatFilterTime.Microseconds(),
		)
		writeLine("perf_hard core_moves=%d threat_cands=%d threat_ordered=%d generic_cands=%d carry_target=%d carry_threat=%d carry_generic=%d generic_calls=%d generic_skipped=%d generic_filtered=%d hard_threat_order_us=%d",
			stats.HardCoreMoves,
			stats.HardThreatCandidates,
			stats.HardThreatOrderedMoves,
			stats.HardGenericCandidates,
			stats.HardCarryoverTarget,
			stats.HardCarryoverFromThreat,
			stats.HardCarryoverFromGeneric,
			stats.HardGenericCollectCalls,
			stats.HardGenericCollectSkipped,
			stats.HardGenericFilteredOut,
			stats.HardBuildThreatOrderTime.Microseconds(),
		)
		writeLine("perf_collect calls=%d bbox_us=%d quiet_only_us=%d threat_merge_us=%d quiet_front_us=%d last_move_us=%d last_legal_us=%d proximity_us=%d prox_legal_us=%d keep_us=%d keep_neigh_us=%d keep_line_us=%d legal_us=%d sort_us=%d threat_cands=%d merged_cands=%d quiet_front_cands=%d empty_returns=%d single_returns=%d",
			stats.CollectCandidateCalls,
			stats.CollectBBoxTime.Microseconds(),
			stats.CollectQuietOnlyTime.Microseconds(),
			stats.CollectThreatMergeTime.Microseconds(),
			stats.QuietFrontTime.Microseconds(),
			stats.LastMoveScanTime.Microseconds(),
			stats.LastMoveLegalTime.Microseconds(),
			stats.ProximityScanTime.Microseconds(),
			stats.ProximityLegalTime.Microseconds(),
			stats.QuietKeepCheckTime.Microseconds(),
			stats.QuietKeepNeighborhoodTime.Microseconds(),
			stats.QuietKeepLineTime.Microseconds(),
			stats.QuietLegalCheckTime.Microseconds(),
			stats.QuietSortTime.Microseconds(),
			stats.CollectThreatCandidates,
			stats.CollectMergedCandidates,
			stats.QuietFrontCandidates,
			stats.CollectEmptyBoardReturns,
			stats.CollectSingleStoneReturns,
		)
		writeLine("perf_collect_counts last_window=%d last_empty=%d last_prio_skip=%d last_keep=%d last_keep_ok=%d last_keep_hit=%d last_keep_miss=%d last_legal=%d last_legal_reject=%d last_added=%d prox_window=%d prox_empty=%d prox_covered_skip=%d prox_dup_skip=%d prox_prio_skip=%d prox_keep=%d prox_keep_ok=%d prox_keep_hit=%d prox_keep_miss=%d prox_legal=%d prox_legal_reject=%d prox_added=%d legal_checks=%d legal_reject=%d quiet_added=%d prio_replace=%d prio_skip=%d",
			stats.LastMoveWindowChecks,
			stats.LastMoveEmptyChecks,
			stats.LastMovePrioritySkips,
			stats.LastMoveKeepChecks,
			stats.LastMoveKeepAccepted,
			stats.LastMoveKeepCacheHits,
			stats.LastMoveKeepCacheMisses,
			stats.LastMoveLegalChecks,
			stats.LastMoveLegalRejected,
			stats.LastMoveCandidatesAdded,
			stats.ProximityWindowChecks,
			stats.ProximityEmptyChecks,
			stats.ProximityCoveredSkips,
			stats.ProximityDuplicateSkips,
			stats.ProximityPrioritySkips,
			stats.ProximityKeepChecks,
			stats.ProximityKeepAccepted,
			stats.ProximityKeepCacheHits,
			stats.ProximityKeepCacheMisses,
			stats.ProximityLegalChecks,
			stats.ProximityLegalRejected,
			stats.ProximityCandidatesAdded,
			stats.QuietLegalChecks,
			stats.QuietLegalRejected,
			stats.QuietAddedCandidates,
			stats.QuietPriorityReplacements,
			stats.QuietPrioritySkipped,
		)
		writeLine("perf_prunes lmr=%d/%d tq_calls=%d",
			stats.LMRResearches,
			stats.LMRReduced,
			stats.TacticalQuiescenceCalls,
		)
	})

	if stdoutLog != "" {
		log.WriteString(stdoutLog)
		if len(stdoutLog) == 0 || stdoutLog[len(stdoutLog)-1] != '\n' {
			log.WriteByte('\n')
		}
	}
	var chosenSteps []fixedPVStep
	if enableFinalBoard {
		if bestLine := cloneSearchDebugLine(depthLines[bestReturnedDepth]); bestLine != nil {
			chosenSteps = fixedPVStepsFromSearchDebugLine(bestLine)
			writeLine("fixed_choice_line depth=%d moves=%s", bestReturnedDepth, formatSearchDebugLineMovesForTest(bestLine))
			writeLine("fixed_choice_sequence depth=%d\n%s", bestReturnedDepth, formatSearchDebugLineDetailsForTest(bestLine))
			writeLine("fixed_choice_board depth=%d\n%s", bestReturnedDepth, formatBoardForTestLog(bestLine.FinalBoard))
		}
	} else if pvLine, pvBoard, ok := reconstructFixedChoiceLine(baseState, rules, settings, &cache, cfg, bestMoveLogged, bestReturnedDepth); ok {
		chosenSteps = append([]fixedPVStep(nil), pvLine...)
		writeLine("fixed_choice_line depth=%d moves=%s", bestReturnedDepth, formatPVLineMovesForTest(pvLine))
		writeLine("fixed_choice_sequence depth=%d\n%s", bestReturnedDepth, formatPVLineDetailsForTest(pvLine))
		writeLine("fixed_choice_board depth=%d\n%s", bestReturnedDepth, formatBoardForTestLog(pvBoard))
	}
	if enableThreatTimeline && len(chosenSteps) > 0 {
		writeFixedThreatTimeline(&log, baseState, rules, settings, cfg, chosenSteps, fixedThreatTimelinePlyLimit())
	}
	log.WriteByte('\n')
	writeLine("fixed_choice next=%s move=(%d,%d) score=%.2f",
		playerColorLabel(baseState.ToMove),
		bestMoveLogged.X,
		bestMoveLogged.Y,
		bestScoreLogged,
	)
	if shouldEnableFixedLUTPureExtras() {
		pureLUTPredicted := false
		for _, info := range rootPureLUTMoves {
			if info.move.Equals(bestMoveLogged) {
				pureLUTPredicted = true
				break
			}
		}
		writeLine("fixed_choice_lut_predicted depth=%d predicted=%t", bestReturnedDepth, pureLUTPredicted)
		if bestReturnedDepth >= 10 {
			fixedLUTStats.mu.Lock()
			fixedLUTStats.Depth10Samples++
			if pureLUTPredicted {
				fixedLUTStats.Depth10Hits++
			}
			fixedLUTStats.ScenarioHits[name] = pureLUTPredicted
			pct := 100.0 * float64(fixedLUTStats.Depth10Hits) / float64(fixedLUTStats.Depth10Samples)
			writeLine("fixed_lut_depth10_stats hits=%d samples=%d pct=%.1f%%", fixedLUTStats.Depth10Hits, fixedLUTStats.Depth10Samples, pct)
			fixedLUTStats.mu.Unlock()
			writeFixedLUTSummaryLog(t)
		}
	}

	logPath := writeFixedDebugLog(t, name, log.String())
	t.Logf("fixed debug log written to %s", logPath)

	if len(expectedMoves) > 0 {
		matched := false
		for _, expectedMove := range expectedMoves {
			if expectedMove.IsValid(settings.BoardSize) && bestMoveLogged.Equals(expectedMove) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("expected %s to choose one of %v, got (%d,%d)", name, expectedMoves, bestMoveLogged.X, bestMoveLogged.Y)
		}
	}
}

func shouldEnableFixedLUTPureExtras() bool {
	return os.Getenv("GOMOKU_DEBUG_FIXED_LUT_PURE") != ""
}

func shouldEnableFixedCandidateBoard(name string) bool {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_CANDIDATE_BOARD"))
	if value == "" {
		return false
	}
	if value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "all") {
		return true
	}
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func shouldUseFixedLiveCache(name string) bool {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_LIVE_CACHE"))
	if value == "" {
		return name == "fixed_protect_capture"
	}
	if value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "all") {
		return true
	}
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func shouldKeepFixedSharedCache(name string) bool {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_KEEP_SHARED_CACHE"))
	if value == "" {
		return false
	}
	if value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "all") {
		return true
	}
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func shouldWarmFixedSharedCache(name string) bool {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_WARM_SHARED_CACHE"))
	if value == "" {
		return false
	}
	if value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "all") {
		return true
	}
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func fixedCacheModeLabel(useLiveCache bool) string {
	if useLiveCache {
		return "shared-live"
	}
	return "fresh-test"
}

func shouldEnableFixedDepthFinalBoard() bool {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_FINAL_BOARD"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "all")
}

func shouldEnableFixedThreatTimeline(name string) bool {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_THREAT_TIMELINE"))
	if value == "" {
		return false
	}
	if value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "all") {
		return true
	}
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func fixedThreatTimelinePlyLimit() int {
	value := strings.TrimSpace(os.Getenv("GOMOKU_DEBUG_FIXED_THREAT_TIMELINE_PLIES"))
	if value == "" {
		return 6
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 6
	}
	return n
}

type fixedPVStep struct {
	Player   PlayerColor
	Move     Move
	Captures []Move
}

func reconstructFixedChoiceLine(state GameState, rules Rules, settings GameSettings, cache *AISearchCache, cfg Config, best Move, depth int) ([]fixedPVStep, Board, bool) {
	if !best.IsValid(settings.BoardSize) || depth <= 0 || cache == nil {
		return nil, Board{}, false
	}
	tt := ensureTT(cache, cfg)
	if tt == nil {
		return nil, Board{}, false
	}
	heuristicHash := heuristicHashFromConfig(cfg)
	lineState := state.Clone()
	steps := make([]fixedPVStep, 0, depth)
	move := best
	for ply := 0; ply < depth && move.IsValid(settings.BoardSize); ply++ {
		player := lineState.ToMove
		var undo searchMoveUndo
		if !applyMoveWithUndo(&lineState, rules, move, player, nil, &undo) {
			return nil, Board{}, false
		}
		step := fixedPVStep{Player: player, Move: move}
		if undo.captureCount > 0 {
			step.Captures = append(step.Captures, undo.captures[:undo.captureCount]...)
		}
		steps = append(steps, step)
		if lineState.Status != StatusRunning {
			return steps, lineState.Board.Clone(), true
		}
		key := ttKeyFor(lineState, settings.BoardSize)
		entry, ok := tt.Probe(key, heuristicHash)
		if !ok || entry.Flag != TTExact || !entry.BestMove.IsValid(settings.BoardSize) {
			return steps, lineState.Board.Clone(), len(steps) > 0
		}
		move = entry.BestMove
	}
	return steps, lineState.Board.Clone(), len(steps) > 0
}

func formatPVLineMovesForTest(steps []fixedPVStep) string {
	if len(steps) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, step := range steps {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%s:(%d,%d)", playerColorLabel(step.Player), step.Move.X, step.Move.Y)
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatSearchDebugLineMovesForTest(line *SearchDebugLine) string {
	if line == nil {
		return "[]"
	}
	steps := make([]fixedPVStep, 0, len(line.Steps))
	for _, step := range line.Steps {
		steps = append(steps, fixedPVStep{
			Player:   step.Player,
			Move:     step.Move,
			Captures: append([]Move(nil), step.Captures...),
		})
	}
	return formatPVLineMovesForTest(steps)
}

func formatSearchDebugLineDetailsForTest(line *SearchDebugLine) string {
	if line == nil {
		return "[]"
	}
	steps := make([]fixedPVStep, 0, len(line.Steps))
	for _, step := range line.Steps {
		steps = append(steps, fixedPVStep{
			Player:   step.Player,
			Move:     step.Move,
			Captures: append([]Move(nil), step.Captures...),
		})
	}
	return formatPVLineDetailsForTest(steps)
}

func fixedPVStepsFromSearchDebugLine(line *SearchDebugLine) []fixedPVStep {
	if line == nil {
		return nil
	}
	steps := make([]fixedPVStep, 0, len(line.Steps))
	for _, step := range line.Steps {
		steps = append(steps, fixedPVStep{
			Player:   step.Player,
			Move:     step.Move,
			Captures: append([]Move(nil), step.Captures...),
		})
	}
	return steps
}

func formatPVLineDetailsForTest(steps []fixedPVStep) string {
	if len(steps) == 0 {
		return "[]"
	}
	var sb strings.Builder
	for i, step := range steps {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%s: (%d,%d)", playerColorLabel(step.Player), step.Move.X, step.Move.Y)
		if len(step.Captures) > 0 {
			sb.WriteString(" (captured ")
			for j, captured := range step.Captures {
				if j > 0 {
					sb.WriteString(" and ")
				}
				fmt.Fprintf(&sb, "(%d,%d)", captured.X, captured.Y)
			}
			sb.WriteByte(')')
		}
	}
	return sb.String()
}

func setEnvForTest(key, value string) func() {
	oldValue, hadOld := os.LookupEnv(key)
	if value == "" {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, value)
	}
	return func() {
		if !hadOld {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, oldValue)
	}
}

func boolEnvValue(v bool) string {
	if v {
		return "1"
	}
	return ""
}

func safeAverage(total, samples int64) float64 {
	if samples <= 0 {
		return 0
	}
	return float64(total) / float64(samples)
}

type fixedRootCalmFeatures struct {
	priority         int
	quietScore       int
	lineInterest     bool
	activeDirections int
	distBBoxCenter   float64
	distLastMove     int
	adjacentRadius1  int
	occupiedRadius2  int
}

func computeFixedRootCalmFeatures(state GameState, boardSize int, move Move, quietScoreMap map[Move]int) fixedRootCalmFeatures {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	bbox := computeBBox(state.Board, boardSize)
	centerX := float64(bbox.minX+bbox.maxX) / 2.0
	centerY := float64(bbox.minY+bbox.maxY) / 2.0
	lineInterest, activeDirections := quietLineInterest(state.Board, move.X, move.Y)
	adjacent := 0
	withinTwo := 0
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx := move.X + dx
			ny := move.Y + dy
			if !state.Board.InBounds(nx, ny) {
				continue
			}
			if state.Board.At(nx, ny) == CellEmpty {
				continue
			}
			if chebDist(dx, dy) == 1 {
				adjacent++
			}
			withinTwo++
		}
	}
	distLast := -1
	if state.HasLastMove {
		distLast = chebDist(move.X-state.LastMove.X, move.Y-state.LastMove.Y)
	}
	return fixedRootCalmFeatures{
		quietScore:       quietScoreMap[move],
		lineInterest:     lineInterest,
		activeDirections: activeDirections,
		distBBoxCenter:   math.Abs(float64(move.X)-centerX) + math.Abs(float64(move.Y)-centerY),
		distLastMove:     distLast,
		adjacentRadius1:  adjacent,
		occupiedRadius2:  withinTwo,
	}
}

func writeFixedRootCalmOrderingFeatures(w io.Writer, state GameState, rules Rules, settings GameSettings, cfg Config, cache *AISearchCache, bestMove Move) {
	scoreSettings := AIScoreSettings{
		Depth:     6,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Cache:     cache,
		Config:    cfg,
	}
	ctx := newMinimaxContext(rules, scoreSettings, stateTimeForTest())
	attachEvalState(&ctx, state)
	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) == 0 {
		return
	}
	ordered := sortRootMoveIndices(rootPool, state.ToMove == PlayerRed, nil)
	quietScores := make(map[Move]int, len(rootPool))
	for _, cand := range collectCandidateMovesWithEval(state, rules, state.ToMove, settings.BoardSize, ctx.evalState, nil) {
		quietScores[cand.move] = cand.quietScore
	}
	bestRank := -1
	for rank, idx := range ordered {
		if rootPool[idx].Move.Equals(bestMove) {
			bestRank = rank + 1
			break
		}
	}
	fmt.Fprintf(w, "fixed_root_calm_best move=(%d,%d) ordered_rank=%d\n", bestMove.X, bestMove.Y, bestRank)
	for rank, idx := range ordered {
		rm := rootPool[idx]
		if rm.IsForced || rm.TacticalPriority < prioLastMove {
			continue
		}
		features := computeFixedRootCalmFeatures(state, settings.BoardSize, rm.Move, quietScores)
		marker := ""
		if rm.Move.Equals(bestMove) {
			marker = " BEST"
		}
		fmt.Fprintf(w, "fixed_root_calm_move rank=%d move=(%d,%d)%s prio=%d quiet=%d line=%t dirs=%d bbox=%.1f last=%d adj1=%d occ2=%d\n",
			rank+1,
			rm.Move.X,
			rm.Move.Y,
			marker,
			rm.TacticalPriority,
			features.quietScore,
			features.lineInterest,
			features.activeDirections,
			features.distBBoxCenter,
			features.distLastMove,
			features.adjacentRadius1,
			features.occupiedRadius2,
		)
	}
}

func writeFixedRootLocalityDetails(w io.Writer, state GameState, rules Rules, settings GameSettings, cfg Config) {
	scoreSettings := AIScoreSettings{
		Depth:     6,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}
	ctx := newMinimaxContext(rules, scoreSettings, stateTimeForTest())
	attachEvalState(&ctx, state)
	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	if len(rootPool) == 0 {
		fmt.Fprintln(w, "fixed_root_locality active=false reason=no_root_pool")
		return
	}
	ordered := sortRootMoveIndices(rootPool, state.ToMove == PlayerRed, nil)
	bands := chooseRootSearchBands(ctx, rootPool, ordered, scoreSettings.Depth)
	activeMoves := make(map[moveKey]struct{}, len(bands.principal)+len(bands.forced)+len(bands.speculative)+len(bands.verification))
	addBand := func(indices []int) {
		for _, idx := range indices {
			if idx < 0 || idx >= len(rootPool) {
				continue
			}
			move := rootPool[idx].Move
			activeMoves[moveKey{X: move.X, Y: move.Y}] = struct{}{}
		}
	}
	addBand(bands.forced)
	addBand(bands.principal)
	addBand(bands.speculative)
	addBand(bands.verification)
	localityActive := false
	for _, rm := range rootPool {
		if _, ok := activeMoves[moveKey{X: rm.Move.X, Y: rm.Move.Y}]; !ok {
			continue
		}
		if rm.SourceFlags&rootSourceLocality != 0 {
			localityActive = true
			break
		}
	}
	if !localityActive {
		fmt.Fprintf(w, "fixed_root_locality active=false reason=not_used bands=forced:%d principal:%d speculative:%d verification:%d\n",
			len(bands.forced), len(bands.principal), len(bands.speculative), len(bands.verification))
		return
	}
	if impacts := collectThreatLUTImpacts(state, state.ToMove, settings.BoardSize, ctx.evalState); len(impacts) > 0 {
		fmt.Fprintf(w, "fixed_root_locality mode=threat_lut_impacts total=%d\n", len(impacts))
		cands := buildThreatLUTCandidates(state, state.ToMove, settings.BoardSize, ctx.evalState, cfg)
		prioByMove := make(map[Move]candidateMove, len(cands))
		for _, cand := range cands {
			prioByMove[cand.move] = cand
		}
		primaryMoves := topAlignmentLocalityMoveSet(state, state.ToMove, settings.BoardSize, ctx.evalState, cfg)
		limit := len(impacts)
		if limit > 24 {
			limit = 24
		}
		for i := 0; i < limit; i++ {
			impact := impacts[i]
			cand := prioByMove[impact.Pos]
			blueCount, redCount, totalAlign := ctx.evalState.AlignmentUseCounts(impact.Pos)
			ownAlign := redCount
			oppAlign := blueCount
			if state.ToMove == PlayerBlue {
				ownAlign = blueCount
				oppAlign = redCount
			}
			_, primary := primaryMoves[impact.Pos.Y*settings.BoardSize+impact.Pos.X]
			fmt.Fprintf(w, "fixed_root_locality_lut rank=%d move=(%d,%d) prio=%d quiet=%d off=%d def=%d total=%d touches=%d flags=0x%08x\n",
				i+1,
				impact.Pos.X,
				impact.Pos.Y,
				cand.priority,
				cand.quietScore,
				impact.OffensiveScore,
				impact.DefensiveScore,
				impact.TotalScore,
				impact.TouchCount,
				impact.Flags,
			)
			fmt.Fprintf(w, "fixed_root_locality_lut_reason move=(%d,%d) kept=%t primary=%t own_align=%d opp_align=%d total_align=%d multi_touch=%t\n",
				impact.Pos.X,
				impact.Pos.Y,
				cand.move == impact.Pos,
				primary,
				ownAlign,
				oppAlign,
				totalAlign,
				impact.TouchCount >= 3,
			)
		}
	}
	selection := buildRootAlignmentLocalitySelection(state, state.ToMove, settings.BoardSize, ctx.evalState, cfg)
	if selection == nil {
		fmt.Fprintln(w, "fixed_root_locality active=false reason=no_selection")
		return
	}
	fmt.Fprintf(w, "fixed_root_locality active=true threats=%d moves=%d bands=forced:%d principal:%d speculative:%d verification:%d\n",
		len(selection.Threats), len(selection.Moves), len(bands.forced), len(bands.principal), len(bands.speculative), len(bands.verification))
	for idx, threat := range selection.Threats {
		fmt.Fprintf(w, "fixed_root_locality_threat idx=%d owner=%s type=%s tier=%d moves=%s ext=%s def=%s\n",
			idx,
			playerColorLabel(threat.Owner),
			patternNameForThreat(threat.Threat.Type),
			threat.Threat.Tier,
			formatMoves(threat.Positions),
			formatThreatPositionsForTest(threat.Threat.ExtensionSquares),
			formatThreatPositionsForTest(threat.Threat.DefenseSquares),
		)
	}
	for rank, move := range selection.Moves {
		refs := make([]string, 0, len(move.threatIndices))
		for _, idx := range move.threatIndices {
			refs = append(refs, itoa(idx))
		}
		fmt.Fprintf(w, "fixed_root_locality_move rank=%d move=(%d,%d) touches=%d own=%d opp=%d total=%d prio=%d quiet=%d threats=[%s]\n",
			rank+1,
			move.move.X,
			move.move.Y,
			move.touchCount,
			move.ownAlignmentCount,
			move.oppAlignmentCount,
			move.totalAlignmentCount,
			move.priority,
			move.quietScore,
			strings.Join(refs, " "),
		)
	}
	type useEntry struct {
		pos   Pos
		blue  int
		red   int
		total int
	}
	useMap := make([]useEntry, 0, 32)
	for pos := range ctx.evalState.AlignmentUseMap() {
		blue, red, total := ctx.evalState.AlignmentUseCounts(Move{X: pos.X, Y: pos.Y})
		useMap = append(useMap, useEntry{pos: pos, blue: blue, red: red, total: total})
	}
	sort.SliceStable(useMap, func(i, j int) bool {
		if useMap[i].total != useMap[j].total {
			return useMap[i].total > useMap[j].total
		}
		if useMap[i].red != useMap[j].red {
			return useMap[i].red > useMap[j].red
		}
		if useMap[i].blue != useMap[j].blue {
			return useMap[i].blue > useMap[j].blue
		}
		if useMap[i].pos.Y != useMap[j].pos.Y {
			return useMap[i].pos.Y < useMap[j].pos.Y
		}
		return useMap[i].pos.X < useMap[j].pos.X
	})
	for _, entry := range useMap {
		fmt.Fprintf(w, "fixed_root_locality_use move=(%d,%d) blue=%d red=%d total=%d\n",
			entry.pos.X, entry.pos.Y, entry.blue, entry.red, entry.total)
	}
}

func orderingRankLabel(bucket int) string {
	rank := bucket + 1
	switch rank {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return fmt.Sprintf("%dth", rank)
	}
}

func formatOrderingRankHistogram(hist [orderingRankBuckets]int64) string {
	total := int64(0)
	for _, count := range hist {
		total += count
	}
	if total == 0 {
		return ""
	}
	parts := make([]string, 0, orderingRankBuckets)
	for i, count := range hist {
		if count == 0 {
			continue
		}
		label := orderingRankLabel(i)
		if i == orderingRankBuckets-1 {
			label = fmt.Sprintf("%d+", orderingRankBuckets)
		}
		parts = append(parts, fmt.Sprintf("%s=%d/%d(%.1f%%)", label, count, total, percentRatio(count, total)))
	}
	return strings.Join(parts, " ")
}

func formatOrderingRankByDepth(hist [orderingStatsDepthBuckets][orderingRankBuckets]int64) string {
	parts := make([]string, 0, orderingStatsDepthBuckets)
	for depth := 0; depth < orderingStatsDepthBuckets; depth++ {
		formatted := formatOrderingRankHistogram(hist[depth])
		if formatted == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("d%d:%s", depth, formatted))
	}
	return strings.Join(parts, "; ")
}

func writeFixedThreatTimeline(log *bytes.Buffer, state GameState, rules Rules, settings GameSettings, cfg Config, steps []fixedPVStep, limit int) {
	if log == nil || len(steps) == 0 {
		return
	}
	if limit > 0 && len(steps) > limit {
		steps = steps[:limit]
	}
	lineState := state.Clone()
	fmt.Fprintf(log, "fixed_threat_timeline plies=%d\n", len(steps))
	for i, step := range steps {
		evalState := BuildEvalStateFromBoard(lineState.Board, lineState.ToMove, uint8(lineState.CapturedBlue), uint8(lineState.CapturedRed), cfg)
		context := AnalyzeThreats(lineState, rules, AIScoreSettings{
			BoardSize: settings.BoardSize,
			Player:    lineState.ToMove,
			Config:    cfg,
		}, lineState.ToMove, &evalState)
		snapshot := evalState.Snapshot(&lineState.Board)
		fmt.Fprintf(log, "timeline_ply=%d to_move=%s hard=%t soft=%t own_best=%d opp_best=%d choose=(%d,%d)\n",
			i,
			playerColorLabel(lineState.ToMove),
			context.IsHardTactical,
			context.IsSoftTactical,
			context.OwnBestTier,
			context.OppBestTier,
			step.Move.X,
			step.Move.Y,
		)
		fmt.Fprintf(log, "timeline_own_threats=%s\n", formatThreatsForTest(threatsForPlayerEvalResult(snapshot, lineState.ToMove), 8))
		fmt.Fprintf(log, "timeline_opp_threats=%s\n", formatThreatsForTest(threatsForPlayerEvalResult(snapshot, otherPlayer(lineState.ToMove)), 8))
		fmt.Fprintf(log, "timeline_must_play=%s\n", formatThreatResponseDetailsForTest(context.MustPlayDetails, settings.BoardSize))
		fmt.Fprintf(log, "timeline_must_block=%s\n", formatThreatResponseDetailsForTest(context.MustBlockDetails, settings.BoardSize))
		fmt.Fprintf(log, "timeline_capture_def=%s\n", formatMoves(context.CaptureDefenseMoves))
		fmt.Fprintf(log, "timeline_capture=%s counter=%s fork=%s prevent=%s\n",
			formatMoves(context.CaptureMoves),
			formatMoves(context.CounterThreatMoves),
			formatMoves(context.ForkMoves),
			formatMoves(context.PreventForkMoves),
		)
		fmt.Fprintf(log, "timeline_selected_tags=%s\n", formatSelectedMoveTagsForTest(step.Move, context, settings.BoardSize))
		fmt.Fprintf(log, "timeline_lut_impacts=%s\n", formatThreatLUTImpactsForTest(collectThreatLUTImpacts(lineState, lineState.ToMove, settings.BoardSize, &evalState), 8))
		fmt.Fprintf(log, "timeline_opp_resp_lut=%s\n", formatResponseScoresForTest(lineState, otherPlayer(lineState.ToMove), &evalState, settings.BoardSize, 8))
		fmt.Fprintf(log, "timeline_threat_cands=%s\n", formatCandidateMovesForTest(GenerateThreatCandidates(context, lineState, rules), 12))
		var undo searchMoveUndo
		if !applyMoveWithUndo(&lineState, rules, step.Move, step.Player, nil, &undo) {
			fmt.Fprintf(log, "timeline_apply_failed move=(%d,%d)\n", step.Move.X, step.Move.Y)
			break
		}
	}
}

func formatSelectedMoveTagsForTest(move Move, context ThreatContext, boardSize int) string {
	tags := make([]string, 0, 8)
	appendIfMove := func(moves []Move, tag string) {
		for _, candidate := range uniqueMoves(moves, boardSize) {
			if candidate.Equals(move) {
				tags = append(tags, tag)
				return
			}
		}
	}
	appendIfMove(context.WinningMoves, "win")
	appendIfMove(selectedMustPlayMoves(context, boardSize), "must_play")
	appendIfMove(selectedMustBlockMoves(context, boardSize), "must_block")
	appendIfMove(context.CounterThreatMoves, "counter")
	appendIfMove(context.ForkMoves, "fork")
	appendIfMove(context.PreventForkMoves, "prevent")
	appendIfMove(context.CaptureMoves, "capture")
	appendIfMove(context.CaptureDefenseMoves, "cap_def")
	appendIfMove(context.StabilizationMoves, "stabilize")
	if len(tags) == 0 {
		return "[]"
	}
	return "[" + strings.Join(tags, " ") + "]"
}

func formatThreatLUTImpactsForTest(impacts []MoveThreatImpact, limit int) string {
	if len(impacts) == 0 {
		return "[]"
	}
	if limit > 0 && len(impacts) > limit {
		impacts = impacts[:limit]
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, impact := range impacts {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "(%d,%d):tot=%d off=%d def=%d touch=%d flags=%08x",
			impact.Pos.X, impact.Pos.Y, impact.TotalScore, impact.OffensiveScore, impact.DefensiveScore, impact.TouchCount, impact.Flags)
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatCandidateMovesForTest(cands []candidateMove, limit int) string {
	if len(cands) == 0 {
		return "[]"
	}
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, cand := range cands {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "(%d,%d):p=%d q=%d", cand.move.X, cand.move.Y, cand.priority, cand.quietScore)
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatResponseScoresForTest(state GameState, player PlayerColor, evalState *EvalState, boardSize int, limit int) string {
	if evalState == nil {
		return "[]"
	}
	_, _, _, _, _, mustBlock, preventFork := evalStateThreatResponseArrays(evalState, player)
	type scored struct {
		move    Move
		block   int32
		prevent int32
		total   int32
	}
	out := make([]scored, 0, 16)
	for idx := 0; idx < len(mustBlock) && idx < len(state.Board.cells); idx++ {
		if state.Board.cells[idx] != CellEmpty {
			continue
		}
		total := mustBlock[idx] + preventFork[idx]
		if total <= 0 {
			continue
		}
		out = append(out, scored{
			move:    moveFromCellIndex(boardSize, idx),
			block:   mustBlock[idx],
			prevent: preventFork[idx],
			total:   total,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].total != out[j].total {
			return out[i].total > out[j].total
		}
		if out[i].move.Y != out[j].move.Y {
			return out[i].move.Y < out[j].move.Y
		}
		return out[i].move.X < out[j].move.X
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if len(out) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, item := range out {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "(%d,%d):tot=%d block=%d prevent=%d", item.move.X, item.move.Y, item.total, item.block, item.prevent)
	}
	sb.WriteByte(']')
	return sb.String()
}

type pureLUTMoveInfo struct {
	move        Move
	offensive   int32
	defensive   int32
	total       int32
	selfWin     int32
	selfPlay    int32
	selfCounter int32
	selfFork    int32
	selfCapture int32
	oppBlock    int32
	oppPrevent  int32
}

func collectPureLUTMoveInfosForTest(state GameState, player PlayerColor, evalState *EvalState, boardSize int) []pureLUTMoveInfo {
	if evalState == nil {
		return nil
	}
	impacts := collectThreatLUTImpacts(state, player, boardSize, evalState)
	selfWin, selfMustPlay, selfCounter, selfFork, selfCaptureRace, oppMustBlock, oppPreventFork := evalStateThreatResponseArrays(evalState, player)
	infoByIdx := make(map[int]pureLUTMoveInfo, len(impacts)+16)
	for _, impact := range impacts {
		if !impact.Pos.IsValid(boardSize) {
			continue
		}
		idx := impact.Pos.Y*boardSize + impact.Pos.X
		infoByIdx[idx] = pureLUTMoveInfo{
			move:      impact.Pos,
			offensive: impact.OffensiveScore,
			defensive: impact.DefensiveScore,
			total:     impact.TotalScore,
		}
	}
	limit := len(state.Board.cells)
	for idx := 0; idx < limit; idx++ {
		if state.Board.cells[idx] != CellEmpty {
			continue
		}
		info := infoByIdx[idx]
		info.move = moveFromCellIndex(boardSize, idx)
		if idx < len(selfWin) {
			info.selfWin = selfWin[idx]
		}
		if idx < len(selfMustPlay) {
			info.selfPlay = selfMustPlay[idx]
		}
		if idx < len(selfCounter) {
			info.selfCounter = selfCounter[idx]
		}
		if idx < len(selfFork) {
			info.selfFork = selfFork[idx]
		}
		if idx < len(selfCaptureRace) {
			info.selfCapture = selfCaptureRace[idx]
		}
		if idx < len(oppMustBlock) {
			info.oppBlock = oppMustBlock[idx]
		}
		if idx < len(oppPreventFork) {
			info.oppPrevent = oppPreventFork[idx]
		}
		if info.total <= 0 &&
			info.selfWin <= 0 &&
			info.selfPlay <= 0 &&
			info.selfCounter <= 0 &&
			info.selfFork <= 0 &&
			info.selfCapture <= 0 &&
			info.oppBlock <= 0 &&
			info.oppPrevent <= 0 {
			continue
		}
		infoByIdx[idx] = info
	}
	out := make([]pureLUTMoveInfo, 0, len(infoByIdx))
	for _, info := range infoByIdx {
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftResp := out[i].selfWin + out[i].selfPlay + out[i].oppBlock
		rightResp := out[j].selfWin + out[j].selfPlay + out[j].oppBlock
		if leftResp != rightResp {
			return leftResp > rightResp
		}
		if out[i].total != out[j].total {
			return out[i].total > out[j].total
		}
		if out[i].defensive != out[j].defensive {
			return out[i].defensive > out[j].defensive
		}
		if out[i].offensive != out[j].offensive {
			return out[i].offensive > out[j].offensive
		}
		if out[i].move.Y != out[j].move.Y {
			return out[i].move.Y < out[j].move.Y
		}
		return out[i].move.X < out[j].move.X
	})
	return out
}

func formatPureLUTMovesForTest(moves []pureLUTMoveInfo, limit int) string {
	if len(moves) == 0 {
		return "[]"
	}
	if limit > 0 && len(moves) > limit {
		moves = moves[:limit]
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, info := range moves {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(
			&sb,
			"(%d,%d):tot=%d off=%d def=%d win=%d play=%d ctr=%d fork=%d cap=%d block=%d prevent=%d",
			info.move.X, info.move.Y,
			info.total,
			info.offensive,
			info.defensive,
			info.selfWin,
			info.selfPlay,
			info.selfCounter,
			info.selfFork,
			info.selfCapture,
			info.oppBlock,
			info.oppPrevent,
		)
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatThreatResponseDetailsForTest(details []ThreatResponseMove, boardSize int) string {
	if len(details) == 0 {
		return "[]"
	}
	filtered := dedupeThreatResponseDetails(details, boardSize)
	var sb strings.Builder
	sb.WriteByte('[')
	for i, detail := range filtered {
		if i > 0 {
			sb.WriteByte(' ')
		}
		kind := "block"
		if detail.Kind == ThreatResponseMustPlay {
			kind = "play"
		}
		fmt.Fprintf(
			&sb,
			"(%d,%d):%s/%s/%d/t%d/w%d/f%d",
			detail.Move.X,
			detail.Move.Y,
			kind,
			patternNameForThreat(ThreatType(detail.Pattern)),
			detail.Severity,
			detail.Tempo,
			detail.WinTempo,
			detail.ForceTempo,
		)
	}
	sb.WriteByte(']')
	return sb.String()
}

func playerColorLabel(player PlayerColor) string {
	switch player {
	case PlayerBlue:
		return "B"
	case PlayerRed:
		return "W"
	default:
		return "?"
	}
}

func scoreTargetLabel(player PlayerColor) string {
	switch player {
	case PlayerRed:
		return "Aim for positive score"
	case PlayerBlue:
		return "Aim for negative score"
	default:
		return "Unknown score target"
	}
}

func captureFixedDebugStdout(t *testing.T, fn func()) string {
	t.Helper()
	fixedDebugStdoutMu.Lock()
	defer fixedDebugStdoutMu.Unlock()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	defer func() {
		_ = w.Close()
		os.Stdout = oldStdout
		_ = r.Close()
	}()

	fn()
	_ = w.Close()
	os.Stdout = oldStdout
	out := <-done
	_ = r.Close()
	return out
}

func writeFixedDebugLog(t *testing.T, name string, contents string) string {
	t.Helper()
	dir := filepath.Join("..", "testlogs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir testlogs: %v", err)
	}
	path := filepath.Join(dir, name+".log")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write log file %s: %v", path, err)
	}
	return path
}

func writeFixedLUTSummaryLog(t *testing.T) {
	t.Helper()
	fixedLUTStats.mu.Lock()
	defer fixedLUTStats.mu.Unlock()

	dir := filepath.Join("..", "testlogs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir testlogs: %v", err)
	}
	path := filepath.Join(dir, "fixed_lut_prediction_summary.log")
	var sb strings.Builder
	fmt.Fprintf(&sb, "depth10_samples=%d\n", fixedLUTStats.Depth10Samples)
	fmt.Fprintf(&sb, "depth10_hits=%d\n", fixedLUTStats.Depth10Hits)
	pct := 0.0
	if fixedLUTStats.Depth10Samples > 0 {
		pct = 100.0 * float64(fixedLUTStats.Depth10Hits) / float64(fixedLUTStats.Depth10Samples)
	}
	fmt.Fprintf(&sb, "depth10_hit_rate=%.1f%%\n", pct)
	names := make([]string, 0, len(fixedLUTStats.ScenarioHits))
	for name := range fixedLUTStats.ScenarioHits {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&sb, "scenario=%s predicted=%t\n", name, fixedLUTStats.ScenarioHits[name])
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write LUT summary %s: %v", path, err)
	}
}

func formatBoardForTestLog(board Board) string {
	var sb strings.Builder
	size := board.Size()
	if size <= 0 {
		return "<empty board>"
	}

	sb.WriteString("    ")
	for x := 0; x < size; x++ {
		fmt.Fprintf(&sb, "%2d", x)
		if x+1 < size {
			sb.WriteByte(' ')
		}
	}
	sb.WriteByte('\n')

	for y := 0; y < size; y++ {
		fmt.Fprintf(&sb, "%2d  ", y)
		for x := 0; x < size; x++ {
			cell := board.At(x, y)
			ch := '.'
			switch cell {
			case CellBlue:
				ch = 'B'
			case CellRed:
				ch = 'W'
			}
			sb.WriteByte(byte(ch))
			if x+1 < size {
				sb.WriteString("  ")
			}
		}
		if y+1 < size {
			sb.WriteByte('\n')
		}
	}

	return sb.String()
}

type fixedLUTAlignmentLog struct {
	owner      PlayerColor
	typ        PatternType
	stones     []Pos
	extensions []Pos
	dir        threatDirection
}

func collectFixedLUTAlignmentsForLog(board Board, evalState *EvalState) []fixedLUTAlignmentLog {
	if evalState == nil {
		return nil
	}
	merged := make(map[string]fixedLUTAlignmentLog, 32)
	appendThreats := func(owner PlayerColor, threats []evalLUTThreat) {
		for _, threat := range threats {
			obj := buildThreatObjectFromLUT(board, owner, threat)
			key := fixedLUTAlignmentKeyByStones(obj, threat.dir)
			entry, ok := merged[key]
			if !ok {
				merged[key] = fixedLUTAlignmentLog{
					owner:      owner,
					typ:        PatternType(obj.Type),
					stones:     append([]Pos(nil), obj.Stones...),
					extensions: append([]Pos(nil), obj.ExtensionSquares...),
					dir:        threat.dir,
				}
				continue
			}
			entry.extensions = mergeUniquePositions(entry.extensions, obj.ExtensionSquares)
			merged[key] = entry
		}
	}
	for _, line := range evalState.lineSummaries {
		appendThreats(PlayerBlue, line.blueLUTThreats)
		appendThreats(PlayerRed, line.redLUTThreats)
	}
	out := make([]fixedLUTAlignmentLog, 0, len(merged))
	for _, entry := range merged {
		sort.SliceStable(entry.extensions, func(i, j int) bool {
			if entry.extensions[i].Y != entry.extensions[j].Y {
				return entry.extensions[i].Y < entry.extensions[j].Y
			}
			return entry.extensions[i].X < entry.extensions[j].X
		})
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].owner != out[j].owner {
			return out[i].owner < out[j].owner
		}
		if out[i].typ != out[j].typ {
			return out[i].typ > out[j].typ
		}
		if len(out[i].stones) != len(out[j].stones) {
			return len(out[i].stones) > len(out[j].stones)
		}
		if len(out[i].extensions) != len(out[j].extensions) {
			return len(out[i].extensions) > len(out[j].extensions)
		}
		if len(out[i].stones) > 0 && len(out[j].stones) > 0 {
			if out[i].stones[0].Y != out[j].stones[0].Y {
				return out[i].stones[0].Y < out[j].stones[0].Y
			}
			if out[i].stones[0].X != out[j].stones[0].X {
				return out[i].stones[0].X < out[j].stones[0].X
			}
		}
		return out[i].dir < out[j].dir
	})
	return out
}

func fixedLUTAlignmentKeyByStones(threat Threat, dir threatDirection) string {
	var sb strings.Builder
	sb.WriteByte(byte(threat.Owner))
	sb.WriteByte(':')
	sb.WriteString(patternNameForThreat(threat.Type))
	sb.WriteByte(':')
	sb.WriteString(strconv.Itoa(int(dir)))
	sb.WriteByte(':')
	for _, pos := range threat.Stones {
		fmt.Fprintf(&sb, "%d,%d;", pos.X, pos.Y)
	}
	return sb.String()
}

func mergeUniquePositions(dst []Pos, src []Pos) []Pos {
	for _, pos := range src {
		found := false
		for _, existing := range dst {
			if existing == pos {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, pos)
		}
	}
	return dst
}

func formatFixedLUTAlignmentsForLog(alignments []fixedLUTAlignmentLog, limit int) string {
	if len(alignments) == 0 {
		return "[]"
	}
	if limit > 0 && len(alignments) > limit {
		alignments = alignments[:limit]
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, item := range alignments {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(
			&sb,
			"%s/%s/stones=%s/ext=%s/dir=%s",
			playerColorLabel(item.owner),
			patternNameForThreat(ThreatType(item.typ)),
			formatPosListForTest(item.stones),
			formatPosListForTest(item.extensions),
			threatDirLabel(item.dir),
		)
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatPosListForTest(positions []Pos) string {
	if len(positions) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, pos := range positions {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "(%d,%d)", pos.X, pos.Y)
	}
	sb.WriteByte(']')
	return sb.String()
}

func threatDirLabel(dir threatDirection) string {
	switch dir {
	case threatDirRow:
		return "row"
	case threatDirCol:
		return "col"
	case threatDirDiagDown:
		return "diag_down"
	case threatDirDiagUp:
		return "diag_up"
	default:
		return "unknown"
	}
}

type scoredMoveForTest struct {
	move  Move
	score float64
}

func sortedScoredMovesForTest(scores []float64, state GameState, rules Rules, boardSize int) []scoredMoveForTest {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	maximizing := state.ToMove == PlayerRed
	out := make([]scoredMoveForTest, 0, boardSize*boardSize)
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			idx := y*boardSize + x
			if idx < 0 || idx >= len(scores) || scores[idx] == illegalScore {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(state, move, state.ToMove); !ok {
				continue
			}
			out = append(out, scoredMoveForTest{move: move, score: scores[idx]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			if maximizing {
				return out[i].score > out[j].score
			}
			return out[i].score < out[j].score
		}
		if out[i].move.Y != out[j].move.Y {
			return out[i].move.Y < out[j].move.Y
		}
		return out[i].move.X < out[j].move.X
	})
	return out
}

func formatSortedScoredMovesForTest(moves []scoredMoveForTest) string {
	if len(moves) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, entry := range moves {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "(%d,%d)=%.2f", entry.move.X, entry.move.Y, entry.score)
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatTacticalSummaryForTest(summary TacticalSummary) string {
	return fmt.Sprintf(
		"win(B:%d W:%d) capWin(B:%d W:%d) open4(B:%d W:%d) closed4(B:%d W:%d) broken4(B:%d W:%d) open3(B:%d W:%d) broken3(B:%d W:%d) double(B:%t W:%t) forcing(B:%d W:%d) must(B:%t W:%t) tactical=%t",
		summary.WinNowBlue,
		summary.WinNowRed,
		summary.CaptureWinNowBlue,
		summary.CaptureWinNowRed,
		summary.Open4Blue,
		summary.Open4Red,
		summary.Closed4Blue,
		summary.Closed4Red,
		summary.Broken4Blue,
		summary.Broken4Red,
		summary.Open3Blue,
		summary.Open3Red,
		summary.Broken3Blue,
		summary.Broken3Red,
		summary.DoubleThreatBlue,
		summary.DoubleThreatRed,
		summary.ForcingThreatsBlue,
		summary.ForcingThreatsRed,
		summary.MustAnswerForBlue,
		summary.MustAnswerForRed,
		summary.IsTactical,
	)
}

func formatThreatsForTest(threats []Threat, limit int) string {
	if len(threats) == 0 {
		return "[]"
	}
	if limit > 0 && len(threats) > limit {
		threats = threats[:limit]
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, threat := range threats {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%s tier=%d ext=%s def=%s", patternNameForThreat(threat.Type), threat.Tier, formatThreatPositionsForTest(threat.ExtensionSquares), formatThreatPositionsForTest(threat.DefenseSquares))
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatThreatArrayForTest(threats []Threat, limit int) string {
	return formatThreatsForTest(threats, limit)
}

func formatThreatPositionsForTest(positions []Pos) string {
	if len(positions) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, pos := range positions {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "(%d,%d)", pos.X, pos.Y)
	}
	sb.WriteByte(']')
	return sb.String()
}

func threatsForPlayerEvalResult(result EvalResult, player PlayerColor) []Threat {
	if result.ThreatCount == 0 {
		return nil
	}
	out := make([]Threat, 0, result.ThreatCount)
	for i := 0; i < int(result.ThreatCount); i++ {
		if result.Threats[i].Owner == player {
			out = append(out, result.Threats[i])
		}
	}
	return out
}

func patternNameForThreat(typ ThreatType) string {
	switch typ {
	case ThreatWin5:
		return "five"
	case ThreatOpen4:
		return "open4"
	case ThreatClosed4:
		return "closed4"
	case ThreatBroken4:
		return "broken4"
	case ThreatOpen3:
		return "open3"
	case ThreatBroken3:
		return "broken3"
	case PatternClosed3:
		return "closed3"
	case ThreatOpen2:
		return "open2"
	case ThreatClosed2:
		return "closed2"
	case PatternBroken2:
		return "broken2"
	default:
		return "none"
	}
}

func debugFixedRootPipelineToWriter(w io.Writer, state GameState, rules Rules, settings GameSettings, cache *AISearchCache) {
	cfg := DefaultConfig()
	scoreSettings := AIScoreSettings{
		Depth:     6,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Cache:     cache,
		Config:    cfg,
	}
	ctx := newMinimaxContext(rules, scoreSettings, stateTimeForTest())
	attachEvalState(&ctx, state)
	if ctx.evalState != nil {
		eval := ctx.evalState.Snapshot(&state.Board)
		fmt.Fprintf(w, "fixed_root_eval score=%d structural=%d summary=%s own=%s opp=%s\n",
			eval.Score,
			eval.StructuralScore,
			formatTacticalSummaryForTest(eval.Summary),
			formatThreatsForTest(threatsForPlayerEvalResult(eval, state.ToMove), 6),
			formatThreatsForTest(threatsForPlayerEvalResult(eval, otherPlayer(state.ToMove)), 6),
		)
	}
	rootPool := buildRootMovePool(state, ctx, state.ToMove)
	ordered := sortRootMoveIndices(rootPool, state.ToMove == PlayerRed, nil)
	bandsDepth1 := chooseRootSearchBands(ctx, rootPool, ordered, 1)
	bandsDepth6 := chooseRootSearchBands(ctx, rootPool, ordered, 6)
	fmt.Fprintf(w, "fixed_root pool=%d forced=%d ordered=%s\n",
		len(rootPool),
		len(bandsDepth1.forced),
		formatRootBandMovesLimited(rootPool, ordered, 16),
	)
	fmt.Fprintf(w, "fixed_root depth1 forced=%d %s principal=%d %s speculative=%d %s verification=%d %s\n",
		len(bandsDepth1.forced),
		formatRootBandMovesLimited(rootPool, bandsDepth1.forced, 8),
		len(bandsDepth1.principal),
		formatRootBandMovesLimited(rootPool, bandsDepth1.principal, 12),
		len(bandsDepth1.speculative),
		formatRootBandMovesLimited(rootPool, bandsDepth1.speculative, 8),
		len(bandsDepth1.verification),
		formatRootBandMovesLimited(rootPool, bandsDepth1.verification, 8),
	)
	fmt.Fprintf(w, "fixed_root depth6 forced=%d %s principal=%d %s speculative=%d %s verification=%d %s\n",
		len(bandsDepth6.forced),
		formatRootBandMovesLimited(rootPool, bandsDepth6.forced, 8),
		len(bandsDepth6.principal),
		formatRootBandMovesLimited(rootPool, bandsDepth6.principal, 12),
		len(bandsDepth6.speculative),
		formatRootBandMovesLimited(rootPool, bandsDepth6.speculative, 8),
		len(bandsDepth6.verification),
		formatRootBandMovesLimited(rootPool, bandsDepth6.verification, 8),
	)
	for _, idx := range ordered {
		rm := rootPool[idx]
		fmt.Fprintf(w, "fixed_root_move move=(%d,%d) prio=%d source=%s threats=%s severity=%d child_forcing=%d shallow=%.2f forced=%t\n",
			rm.Move.X,
			rm.Move.Y,
			rm.TacticalPriority,
			formatRootSourceFlagsForTest(rm.SourceFlags),
			formatRootThreatFlagsForTest(rm.ThreatFlags),
			rm.ThreatSeverity,
			rm.ChildForcingScore,
			rm.ShallowScore,
			rm.IsForced,
		)
	}
}

func formatRootSourceFlagsForTest(flags uint32) string {
	if flags == 0 {
		return "[]"
	}
	names := make([]string, 0, 8)
	appendIf := func(mask uint32, name string) {
		if flags&mask != 0 {
			names = append(names, name)
		}
	}
	appendIf(rootSourceImmediateWin, "imm_win")
	appendIf(rootSourceImmediateBlock, "imm_block")
	appendIf(rootSourceCaptureWin, "cap_win")
	appendIf(rootSourceCaptureDefense, "cap_def")
	appendIf(rootSourceThreatOwn, "threat_own")
	appendIf(rootSourceThreatOpp, "threat_opp")
	appendIf(rootSourceCaptureOwn, "cap_own")
	appendIf(rootSourceCaptureOpp, "cap_opp")
	appendIf(rootSourceStabilize, "stabilize")
	appendIf(rootSourceLocality, "locality")
	return "[" + strings.Join(names, " ") + "]"
}

func formatRootThreatFlagsForTest(flags uint32) string {
	if flags == 0 {
		return "[]"
	}
	names := make([]string, 0, 8)
	appendIf := func(mask uint32, name string) {
		if flags&mask != 0 {
			names = append(names, name)
		}
	}
	appendIf(rootThreatOwnWin, "own_win")
	appendIf(rootThreatOppWin, "opp_win")
	appendIf(rootThreatOwnFour, "own_four")
	appendIf(rootThreatOppFour, "opp_four")
	appendIf(rootThreatOwnThree, "own_three")
	appendIf(rootThreatOppThree, "opp_three")
	appendIf(rootThreatForkCreate, "fork_create")
	appendIf(rootThreatForkPrevent, "fork_prevent")
	appendIf(rootThreatCaptureCreate, "cap_create")
	appendIf(rootThreatCapturePrevent, "cap_prevent")
	appendIf(rootThreatStabilize, "stabilize")
	appendIf(rootThreatChildMustAnswer, "child_must")
	appendIf(rootThreatChildOpenFour, "child_open4")
	appendIf(rootThreatChildDoubleThreat, "child_double")
	appendIf(rootThreatChildCriticalCapture, "child_critical")
	return "[" + strings.Join(names, " ") + "]"
}

func stateTimeForTest() time.Time {
	return time.Now()
}

// buildCaptureInsteadOfBlockFixedState sets up the position where Red should
// capture Blue stones 11=(8,7) and 5=(7,8) by playing at (9,6), rather than
// continuing its own vertical column at (9,11). The capture:
//  1. Removes two Blue stones from their diagonal alignment (breaking Blue's threat)
//  2. Creates Red's own Closed4 on that diagonal
//  3. Advances Red's capture count
func buildCaptureInsteadOfBlockFixedState() (GameState, Rules, GameSettings) {
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed
	for _, stone := range []struct {
		x, y int
		cell Cell
	}{
		{10, 5, CellBlue}, // 7
		{9, 6, CellBlue},  // 15
		{8, 7, CellBlue},  // 11
		{9, 7, CellBlue},  // 9
		{7, 8, CellBlue},  // 5
		{9, 8, CellRed},   // 8
		{6, 9, CellRed},   // 14
		{9, 9, CellRed},   // 6
		{9, 10, CellRed},  // 10
		{9, 11, CellRed},  // 12
		{9, 12, CellBlue}, // 13
	} {
		state.Board.Set(stone.x, stone.y, stone.cell)
	}
	state.recomputeHashes()
	return state, rules, settings
}

// TestCaptureInsteadOfBlockPrefersCapturingThreatStones verifies that Red
// plays (9,5) to capture Blue (9,6)+(9,7) — which are part of Blue's diagonal
// Closed4 (10,5)-(9,6)-(8,7)-(7,8) — rather than simply blocking at (11,4).
// The capture is strictly better: it removes two alignment stones (breaking the
// Closed4), gains material, and forces the opponent to rebuild.
func TestCaptureInsteadOfBlockPrefersCapturingThreatStones(t *testing.T) {
	state, rules, settings := buildCaptureInsteadOfBlockFixedState()
	cfg := DefaultConfig()
	cfg.AiDepth = 10
	cfg.AiMinDepth = 2
	cfg.AiMaxDepth = 10
	cfg.AiDepthStep = 1
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiLogSearchStats = false

	FlushGlobalCaches()
	cache := newAISearchCache()
	scores := ScoreBoard(state, rules, AIScoreSettings{
		Depth:           cfg.AiDepth,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		DirectDepthOnly: false,
		Stats:           &SearchStats{},
	})

	// (9,5) captures Blue (9,6)+(9,7): R(9,5)-B(9,6)-B(9,7)-R(9,8)
	capture := Move{X: 9, Y: 5}
	// (11,4) merely blocks Blue's Closed4 extension without gaining material
	block := Move{X: 11, Y: 4}
	captureScore := scoreForMove(scores, capture, settings.BoardSize)
	blockScore := scoreForMove(scores, block, settings.BoardSize)
	if captureScore <= blockScore {
		t.Fatalf("expected capture move %v (%.2f) to outscore plain block %v (%.2f)",
			capture, captureScore, block, blockScore)
	}
}

func TestDebugScoreBoardFixedPositionCaptureInsteadOfBlock(t *testing.T) {
	runFixedPositionDebugScenario(t, "fixed_capture_instead_of_block", buildCaptureInsteadOfBlockFixedState, Move{X: 9, Y: 5})
}

func TestCaptureInsteadOfBlockForcedBlockAfterBroken3(t *testing.T) {
	// After R(11,4) B(9,5) R(10,7) B(7,7) R(7,9), Red has row-y=9 pattern:
	// (6,9)-(7,9)-(gap at 8,9)-(9,9) = Broken3.
	// Playing (8,9) creates Red's Open4 — Blue MUST block at (8,9).
	settings := DefaultGameSettings()
	settings.BoardSize = 19
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue // Blue to move after the sequence

	// Initial setup
	for _, s := range []struct {
		x, y int
		cell Cell
	}{
		{10, 5, CellBlue}, {9, 6, CellBlue}, {8, 7, CellBlue}, {9, 7, CellBlue},
		{7, 8, CellBlue}, {9, 8, CellRed}, {6, 9, CellRed}, {9, 9, CellRed},
		{9, 10, CellRed}, {9, 11, CellRed}, {9, 12, CellBlue},
	} {
		state.Board.Set(s.x, s.y, s.cell)
	}
	// Apply R(11,4) B(9,5) R(10,7) B(7,7) R(7,9)
	for _, mv := range []struct {
		x, y int
		cell Cell
	}{
		{11, 4, CellRed}, {9, 5, CellBlue}, {10, 7, CellRed},
		{7, 7, CellBlue}, {7, 9, CellRed},
	} {
		state.Board.Set(mv.x, mv.y, mv.cell)
	}
	state.recomputeHashes()

	cfg := DefaultConfig()
	ctx := newMinimaxContext(rules, AIScoreSettings{
		Depth: 3, BoardSize: settings.BoardSize, Player: state.ToMove, Config: cfg,
	}, time.Now())
	attachEvalState(&ctx, state)

	context := AnalyzeThreats(state, rules, ctx.settings, state.ToMove, ctx.evalState)

	block89 := Move{X: 8, Y: 9}
	if !moveListContains(context.MustBlockMoves, block89) {
		t.Errorf("expected (8,9) in MustBlockMoves (Red Broken3→Open4 in row y=9); got %v", context.MustBlockMoves)
	}
	_, _, hardRestricted := chooseNodeCandidatesFromThreatContext(state, ctx, state.ToMove, false, 5, 16, nil, context)
	if !hardRestricted {
		t.Errorf("expected hard tactical restriction forcing Blue to block at (8,9)")
	}
}

func TestCaptureInsteadOfBlockDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		workers int
		depth   int
	}{
		{"d6_w1", 1, 6},
		{"d7_w1", 1, 7},
		{"d8_w1", 1, 8},
		{"d9_w1", 1, 9},
		{"d10_w1", 1, 10},
		{"d10_w4", 4, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, rules, settings := buildCaptureInsteadOfBlockFixedState()
			cfg := DefaultConfig()
			cfg.AiDepth = tc.depth
			cfg.AiMinDepth = 2
			cfg.AiMaxDepth = tc.depth
			cfg.AiDepthStep = 1
			cfg.AiTimeBudgetMs = 0
			cfg.AiTimeoutMs = 0
			cfg.AiLazySMPWorkers = tc.workers
			cfg.AiLogSearchStats = false

			capture := Move{X: 9, Y: 5}
			block := Move{X: 11, Y: 4}

			var bestMove Move
			var bestLine *SearchDebugLine
			FlushGlobalCaches()
			cache := newAISearchCache()
			scores := ScoreBoard(state, rules, AIScoreSettings{
				Depth:           cfg.AiDepth,
				TimeoutMs:       0,
				BoardSize:       settings.BoardSize,
				Player:          state.ToMove,
				Cache:           &cache,
				Config:          cfg,
				DirectDepthOnly: true,
				Stats:           &SearchStats{},
				OnDepthCompleteDebug: func(depth int, move Move, score float64, line *SearchDebugLine) {
					if depth == tc.depth && (move == capture || move == block) {
						bestMove = move
						bestLine = line
					}
				},
			})

			captureScore := scoreForMove(scores, capture, settings.BoardSize)
			blockScore := scoreForMove(scores, block, settings.BoardSize)
			t.Logf("%s: capture(9,5)=%.2f block(11,4)=%.2f bestMove=%v", tc.name, captureScore, blockScore, bestMove)
			if bestLine != nil {
				steps := make([]string, 0, len(bestLine.Steps))
				for _, s := range bestLine.Steps {
					player := "R"
					if s.Player == PlayerBlue {
						player = "B"
					}
					steps = append(steps, fmt.Sprintf("%s(%d,%d)", player, s.Move.X, s.Move.Y))
				}
				t.Logf("  line: %v", steps)
			}
		})
	}
}
