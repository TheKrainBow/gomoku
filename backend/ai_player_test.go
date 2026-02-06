package main

import "testing"

func TestBestMoveFromScoresWhiteIgnoresUnscoredCells(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerWhite

	size := settings.BoardSize
	scores := make([]float64, size*size)
	for i := range scores {
		scores[i] = illegalScore
	}

	// Two legal scored moves; white must minimize Black-perspective score.
	scores[2*size+2] = 25.0
	scores[1*size+1] = 10.0

	move, ok := bestMoveFromScores(scores, state, rules, size)
	if !ok {
		t.Fatalf("expected a legal move")
	}
	if move.X != 1 || move.Y != 1 {
		t.Fatalf("expected minimizing move (1,1), got (%d,%d)", move.X, move.Y)
	}
}

func TestBestMoveFromScoresBlackMaximizesScoredMoves(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack

	size := settings.BoardSize
	scores := make([]float64, size*size)
	for i := range scores {
		scores[i] = illegalScore
	}

	scores[1*size+1] = 10.0
	scores[2*size+2] = 25.0

	move, ok := bestMoveFromScores(scores, state, rules, size)
	if !ok {
		t.Fatalf("expected a legal move")
	}
	if move.X != 2 || move.Y != 2 {
		t.Fatalf("expected maximizing move (2,2), got (%d,%d)", move.X, move.Y)
	}
}

func TestAIPlayersShareGlobalSearchCache(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 1
	cfg.AiMinDepth = 1
	cfg.AiMaxDepth = 1
	cfg.AiQuickWinExit = false
	cfg.AiEnableEvalCache = false
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	FlushGlobalCaches()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
	state.recomputeHashes()

	rules := NewRules(settings)
	black := &AIPlayer{}
	white := &AIPlayer{}

	move := black.ChooseMove(state, rules)
	if !move.IsValid(settings.BoardSize) {
		t.Fatalf("expected black AI to produce a legal move")
	}

	blackSize := black.CacheSize()
	whiteSize := white.CacheSize()
	if blackSize == 0 {
		t.Fatalf("expected shared TT to be populated after search")
	}
	if whiteSize != blackSize {
		t.Fatalf("expected both AIs to report the same TT size, black=%d white=%d", blackSize, whiteSize)
	}
}

func TestMaybeSelectLostModeMoveUsesFragilityTieBreaker(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack

	size := settings.BoardSize
	scores := make([]float64, size*size)
	for i := range scores {
		scores[i] = illegalScore
	}
	moveA := Move{X: 1, Y: 1}
	moveB := Move{X: 2, Y: 2}
	scores[moveA.Y*size+moveA.X] = -100.0
	scores[moveB.Y*size+moveB.X] = -120.0

	cfg := DefaultConfig()
	cfg.AiEnableLostMode = true
	cfg.AiLostModeThreshold = 10.0
	cfg.AiLostModeMaxMoves = 4
	cfg.AiLostModeReplyLimit = 4
	cfg.AiLostModeMinDepth = 2
	analysisSettings := AIScoreSettings{
		Depth:     2,
		BoardSize: size,
		Player:    state.ToMove,
		Config:    cfg,
	}

	oldFragility := lostModeFragilityFn
	defer func() { lostModeFragilityFn = oldFragility }()
	lostModeFragilityFn = func(_ GameState, _ Rules, _ AIScoreSettings, move Move) (float64, bool) {
		if move == moveA {
			return 1.0, true
		}
		if move == moveB {
			return 5.0, true
		}
		return 0.0, false
	}

	selected, changed := maybeSelectLostModeMove(scores, state, rules, analysisSettings, moveA)
	if !changed {
		t.Fatalf("expected lost mode to change move selection")
	}
	if selected != moveB {
		t.Fatalf("expected fragile move %v, got %v", moveB, selected)
	}
}

func TestMaybeSelectLostModeMoveSkipsWhenNotLosing(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack

	size := settings.BoardSize
	scores := make([]float64, size*size)
	for i := range scores {
		scores[i] = illegalScore
	}
	move := Move{X: 1, Y: 1}
	scores[move.Y*size+move.X] = -100.0

	cfg := DefaultConfig()
	cfg.AiEnableLostMode = true
	cfg.AiLostModeThreshold = 200.0
	cfg.AiLostModeMinDepth = 2
	analysisSettings := AIScoreSettings{
		Depth:     2,
		BoardSize: size,
		Player:    state.ToMove,
		Config:    cfg,
	}

	selected, changed := maybeSelectLostModeMove(scores, state, rules, analysisSettings, move)
	if changed {
		t.Fatalf("expected lost mode to be skipped, got %v", selected)
	}
}

func TestBestMoveFromScoresHandlesShortScoreSlice(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack

	move, ok := bestMoveFromScores([]float64{}, state, rules, settings.BoardSize)
	if !ok {
		t.Fatalf("expected fallback legal move on short score slice")
	}
	if !move.IsValid(settings.BoardSize) {
		t.Fatalf("expected valid move, got %+v", move)
	}
}

func TestMaybeSelectLostModeMoveHandlesShortScoreSlice(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack

	cfg := DefaultConfig()
	cfg.AiEnableLostMode = true
	cfg.AiLostModeThreshold = 10.0
	cfg.AiLostModeMinDepth = 2
	analysisSettings := AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}

	if _, changed := maybeSelectLostModeMove([]float64{}, state, rules, analysisSettings, Move{X: 1, Y: 1}); changed {
		t.Fatalf("expected lost mode to skip short score slice")
	}
}
