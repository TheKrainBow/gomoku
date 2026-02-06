package main

import (
	"reflect"
	"sync/atomic"
	"testing"
)

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
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
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
	entry, hit := tt.Probe(rootKey)
	if !hit {
		t.Fatalf("expected root board entry in TT")
	}
	if entry.Depth != 2 {
		t.Fatalf("expected TT depth 2, got %d", entry.Depth)
	}
	if entry.Flag != TTExact {
		t.Fatalf("expected TT exact flag, got %d", entry.Flag)
	}
	if entry.BestMove.X != bestMove.X || entry.BestMove.Y != bestMove.Y {
		t.Fatalf("expected TT best move (%d,%d), got (%d,%d)", bestMove.X, bestMove.Y, entry.BestMove.X, entry.BestMove.Y)
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
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
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
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
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
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
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

func TestCandidateLimitAppliesDeepPlyCaps(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableHardPlyCaps = true
	cfg.AiMaxCandidatesRoot = 24
	cfg.AiMaxCandidatesPly7 = 16
	cfg.AiMaxCandidatesPly8 = 12
	cfg.AiMaxCandidatesPly9 = 8
	ctx := minimaxContext{settings: AIScoreSettings{Config: cfg}}

	if got := candidateLimit(ctx, 10, 6, false); got != 24 {
		t.Fatalf("expected hard cap for ply <= 6, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 7, false); got != 16 {
		t.Fatalf("expected ply-7 cap to apply, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 8, false); got != 12 {
		t.Fatalf("expected ply-8 cap to apply, got %d", got)
	}
	if got := candidateLimit(ctx, 10, 9, false); got != 8 {
		t.Fatalf("expected ply-9 cap to apply, got %d", got)
	}
}

func TestCandidateLimitAllowsTacticalLimitToTightenHardCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableHardPlyCaps = true
	cfg.AiEnableTacticalK = true
	cfg.AiMaxCandidatesPly9 = 8
	cfg.AiKTactDeep = 6
	ctx := minimaxContext{settings: AIScoreSettings{Config: cfg}}

	if got := candidateLimit(ctx, 10, 9, true); got != 6 {
		t.Fatalf("expected tactical limit to tighten hard cap, got %d", got)
	}
}

func TestCandidateLimitKeepsLegacyPathWhenHardCapsDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableHardPlyCaps = false
	cfg.AiEnableDynamicTopK = false
	cfg.AiTopCandidates = 6
	ctx := minimaxContext{settings: AIScoreSettings{Config: cfg}}

	if got := candidateLimit(ctx, 10, 9, false); got != 6 {
		t.Fatalf("expected legacy candidate limit when hard caps disabled, got %d", got)
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

func TestApplyMoveWithUndoRestoresState(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
	state.recomputeHashes()
	original := state.Clone()

	move := Move{X: 4, Y: 3}
	var undo searchMoveUndo
	if !applyMoveWithUndo(&state, rules, move, PlayerBlack, &undo) {
		t.Fatalf("expected move to apply")
	}
	undoMoveWithUndo(&state, undo)

	if state.Status != original.Status || state.ToMove != original.ToMove || state.HasLastMove != original.HasLastMove {
		t.Fatalf("expected state header restored")
	}
	if state.CapturedBlack != original.CapturedBlack || state.CapturedWhite != original.CapturedWhite {
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
