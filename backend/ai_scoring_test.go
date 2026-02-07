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

func TestCountCapturablePairsDetectsHangingPair(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	state := DefaultGameState(settings)
	state.Board.Set(1, 4, CellBlack)
	state.Board.Set(2, 4, CellBlack)
	state.Board.Set(3, 4, CellWhite)

	if got := countCapturablePairs(state.Board, PlayerBlack); got != 1 {
		t.Fatalf("expected one hanging black pair, got %d", got)
	}
	if got := countCapturablePairs(state.Board, PlayerWhite); got != 0 {
		t.Fatalf("expected no hanging white pair, got %d", got)
	}
}

func TestCaptureUrgencyHeuristicPenalizesOpponentCaptureWinThreat(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlack = false
	settings.ForbidDoubleThreeWhite = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlack
	state.Status = StatusRunning
	state.Board.Set(1, 4, CellBlack)
	state.Board.Set(2, 4, CellBlack)
	state.Board.Set(3, 4, CellWhite)
	state.CapturedWhite = 8

	score := captureUrgencyHeuristic(state, rules)
	if score > -winScore/4 {
		t.Fatalf("expected strong penalty for opponent capture-win threat, got %.2f", score)
	}
}

func TestHeuristicForMoveStronglyPenalizesCreatingCapturablePair(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlack = false
	settings.ForbidDoubleThreeWhite = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlack
	state.Status = StatusRunning
	// Risk pattern around y=4:
	// x=3 is empty, x=4 is Black, x=5 is candidate move, x=6 is White.
	// Playing at (5,4) creates B B with White on one side and empty on the other,
	// giving White an immediate free capture at (3,4).
	state.Board.Set(4, 4, CellBlack)
	state.Board.Set(6, 4, CellWhite)
	state.recomputeHashes()

	scoreSettings := AIScoreSettings{
		BoardSize: settings.BoardSize,
		Player:    PlayerBlack,
		Config:    DefaultConfig(),
	}
	risky := heuristicForMove(state, rules, scoreSettings, Move{X: 5, Y: 4})
	safer := heuristicForMove(state, rules, scoreSettings, Move{X: 4, Y: 5})

	if risky >= safer-1000.0 {
		t.Fatalf("expected risky move to be strongly penalized (risky=%.2f safer=%.2f)", risky, safer)
	}
}

func TestFindCaptureThreatResponsesBlocksDecisiveThreat(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlack = false
	settings.ForbidDoubleThreeWhite = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlack
	state.Status = StatusRunning
	state.Board.Set(1, 4, CellBlack)
	state.Board.Set(2, 4, CellBlack)
	state.Board.Set(3, 4, CellWhite)
	state.CapturedWhite = 8
	state.recomputeHashes()

	if !hasDecisiveCaptureThreat(state, rules, PlayerWhite) {
		t.Fatalf("expected white to have a decisive capture threat")
	}
	responses := findCaptureThreatResponses(state, rules, PlayerBlack, PlayerWhite, settings.BoardSize)
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
		if !applyMoveWithUndo(&next, rules, move, PlayerBlack, &undo) {
			t.Fatalf("response move should be legal: (%d,%d)", move.X, move.Y)
		}
		if hasDecisiveCaptureThreat(next, rules, PlayerWhite) {
			t.Fatalf("response move (%d,%d) still leaves decisive capture threat", move.X, move.Y)
		}
		undoMoveWithUndo(&next, undo)
	}
	if !hasBlock {
		t.Fatalf("expected direct blocking move (0,4) to be included in responses")
	}
}

func TestHasDecisiveCaptureThreatDetectsImmediateCaptureWinByCount(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 9
	settings.ForbidDoubleThreeBlack = false
	settings.ForbidDoubleThreeWhite = false
	rules := NewRules(settings)

	state := DefaultGameState(settings)
	state.ToMove = PlayerBlack
	state.Status = StatusRunning
	state.CapturedWhite = 8
	// White can capture the Black pair at x=[4,5], y=4 by playing at (3,4).
	state.Board.Set(4, 4, CellBlack)
	state.Board.Set(5, 4, CellBlack)
	state.Board.Set(6, 4, CellWhite)
	state.recomputeHashes()

	if !hasDecisiveCaptureThreat(state, rules, PlayerWhite) {
		t.Fatalf("expected immediate capture-win threat to be detected")
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
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
	state.recomputeHashes()

	cache := newAISearchCache()
	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	best := Move{X: 4, Y: 3}
	rootKey := ttKeyFor(state, settings.BoardSize)
	tt.Store(rootKey, 10, 1234, TTExact, best, TTMeta{})

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
	cfg.AiEnableRootTranspose = true
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
	base.ToMove = PlayerBlack
	base.Board.Set(6, 6, CellBlack)
	base.Board.Set(7, 6, CellWhite)
	base.Board.Set(6, 7, CellBlack)
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
	bestBase, ok := bestMoveFromScores(scoresBase, base, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected base search to produce move")
	}

	translated := DefaultGameState(settings)
	translated.Status = StatusRunning
	translated.ToMove = PlayerBlack
	dx, dy := 1, 1
	translated.Board.Set(6+dx, 6+dy, CellBlack)
	translated.Board.Set(7+dx, 6+dy, CellWhite)
	translated.Board.Set(6+dx, 7+dy, CellBlack)
	translated.recomputeHashes()

	if ttKeyFor(base, settings.BoardSize) == ttKeyFor(translated, settings.BoardSize) {
		t.Fatalf("expected translated board to have different absolute TT key")
	}

	statsTranslated := &SearchStats{}
	scoresTranslated := ScoreBoard(translated, rules, AIScoreSettings{
		Depth:     3,
		TimeoutMs: 0,
		BoardSize: settings.BoardSize,
		Player:    translated.ToMove,
		Cache:     &cache,
		Config:    cfg,
		Stats:     statsTranslated,
	})
	bestTranslated, ok := bestMoveFromScores(scoresTranslated, translated, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected translated search to produce move")
	}

	if statsTranslated.Nodes != 0 {
		t.Fatalf("expected translated board to use root transpose shortcut (no node search), got nodes=%d", statsTranslated.Nodes)
	}
	if bestTranslated.X != bestBase.X+dx || bestTranslated.Y != bestBase.Y+dy {
		t.Fatalf("expected translated best move (%d,%d), got (%d,%d)", bestBase.X+dx, bestBase.Y+dy, bestTranslated.X, bestTranslated.Y)
	}
}
