package main

import "testing"

func TestSelectionCandidateMovesUsesThreatFirstPipeline(t *testing.T) {
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
	state.recomputeHashes()

	cfg := DefaultConfig()
	selection := selectionCandidateMoves(state, rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	})
	if len(selection) < 2 {
		t.Fatalf("expected at least two selection candidates, got %d", len(selection))
	}

	firstTwo := map[Move]struct{}{
		selection[0].move: {},
		selection[1].move: {},
	}
	if _, ok := firstTwo[Move{X: 1, Y: 4}]; !ok {
		t.Fatalf("expected left threat extension in selection candidates, got %#v", selection[:2])
	}
	if _, ok := firstTwo[Move{X: 5, Y: 4}]; !ok {
		t.Fatalf("expected right threat extension in selection candidates, got %#v", selection[:2])
	}
}

func TestSelectBestMoveFallsBackToCandidateListWhenNoScores(t *testing.T) {
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
	state.recomputeHashes()

	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}

	player := NewAIPlayer()
	move, ok := player.selectBestMove(state, rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    DefaultConfig(),
	}, &SearchStats{}, scores)
	if !ok {
		t.Fatalf("expected fallback move from candidate list")
	}
	if move.X != 1 && move.X != 5 || move.Y != 4 {
		t.Fatalf("expected threat-extension fallback, got (%d,%d)", move.X, move.Y)
	}
}

func TestSelectBestMovePrefersCandidateOrderOnEqualScores(t *testing.T) {
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
	state.recomputeHashes()

	cfg := DefaultConfig()
	selection := selectionCandidateMoves(state, rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	})
	if len(selection) == 0 {
		t.Fatalf("expected selection candidates")
	}

	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	preferred := selection[0].move
	scores[preferred.Y*settings.BoardSize+preferred.X] = 100.0
	scores[0] = 100.0

	player := NewAIPlayer()
	move, ok := player.selectBestMove(state, rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, &SearchStats{}, scores)
	if !ok {
		t.Fatalf("expected a move")
	}
	if move != preferred {
		t.Fatalf("expected candidate-ordered move %v, got %v", preferred, move)
	}
}

func TestBestMoveFromScoresRedIgnoresUnscoredCells(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerRed

	size := settings.BoardSize
	scores := make([]float64, size*size)
	for i := range scores {
		scores[i] = illegalScore
	}

	// Two legal scored moves; red must maximize Red-perspective score.
	scores[2*size+2] = 25.0
	scores[1*size+1] = 10.0

	move, ok := bestMoveFromScores(scores, state, rules, size)
	if !ok {
		t.Fatalf("expected a legal move")
	}
	if move.X != 2 || move.Y != 2 {
		t.Fatalf("expected maximizing move (2,2), got (%d,%d)", move.X, move.Y)
	}
}

func TestBestMoveFromScoresBlueMaximizesScoredMoves(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue

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
	if move.X != 1 || move.Y != 1 {
		t.Fatalf("expected minimizing move (1,1), got (%d,%d)", move.X, move.Y)
	}
}

func TestSelectBestMoveUsesOrderedFallbackWhenIncompleteScoresAreLosing(t *testing.T) {
	state, rules, settings := buildRedToPlaySixVsFourCapturesFixedState()
	cfg := liveAIConfig(DefaultConfig())
	cfg.AiDepth = 10
	cfg.AiMinDepth = 1
	cfg.AiMaxDepth = 10
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiUseTtCache = false
	cfg.AiEnableRootTranspose = false
	cfg.AiEnableLostMode = false

	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	scores[5*settings.BoardSize+8] = -winScore + 10

	player := NewAIPlayer()
	move, ok := player.selectBestMove(state, rules, AIScoreSettings{
		Depth:     cfg.AiDepth,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
		Cache:     newLiveSearchCache(),
	}, &SearchStats{CompletedDepths: 8}, scores)
	if !ok {
		t.Fatalf("expected move selection to succeed")
	}
	if !move.Equals(Move{X: 4, Y: 8}) {
		t.Fatalf("expected ordered fallback (4,8), got (%d,%d)", move.X, move.Y)
	}
}

func TestAIPlayersDoNotPopulateGlobalSearchCache(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 1
	cfg.AiMinDepth = 1
	cfg.AiMaxDepth = 1
	cfg.AiUseTtCache = false
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
	state.ToMove = PlayerBlue
	state.Board.Set(3, 3, CellBlue)
	state.Board.Set(2, 3, CellRed)
	state.recomputeHashes()

	rules := NewRules(settings)
	blue := &AIPlayer{}
	red := &AIPlayer{}

	move := blue.ChooseMove(state, rules)
	if !move.IsValid(settings.BoardSize) {
		t.Fatalf("expected blue AI to produce a legal move")
	}

	blueSize := blue.CacheSize()
	redSize := red.CacheSize()
	if blueSize != 0 {
		t.Fatalf("expected live search to avoid populating shared TT, got blue size=%d", blueSize)
	}
	if redSize != 0 {
		t.Fatalf("expected second AI to see empty shared TT too, got red size=%d", redSize)
	}
}

func TestLiveAIConfigKeepsBenchmarkSearchFeaturesEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableEvalCache = true
	cfg.AiEnableRootTranspose = true
	cfg.AiEnableAspiration = true

	live := liveAIConfig(cfg)
	if !live.AiEnableEvalCache {
		t.Fatalf("expected live config to keep eval cache enabled")
	}
	if !live.AiEnableRootTranspose {
		t.Fatalf("expected live config to keep root transpose enabled")
	}
	if !live.AiEnableAspiration {
		t.Fatalf("expected live config to keep aspiration enabled")
	}
}

func TestMaybeSelectLostModeMoveUsesFragilityTieBreaker(t *testing.T) {
	settings := DefaultGameSettings()
	settings.BoardSize = 5
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlue

	size := settings.BoardSize
	scores := make([]float64, size*size)
	for i := range scores {
		scores[i] = illegalScore
	}
	moveA := Move{X: 1, Y: 1}
	moveB := Move{X: 2, Y: 2}
	scores[moveA.Y*size+moveA.X] = 100.0
	scores[moveB.Y*size+moveB.X] = 120.0

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
	candidates := selectionCandidateMoves(state, rules, analysisSettings)

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

	selected, changed := maybeSelectLostModeMove(scores, state, rules, analysisSettings, moveA, candidates)
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
	state.ToMove = PlayerBlue

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
	candidates := selectionCandidateMoves(state, rules, analysisSettings)

	selected, changed := maybeSelectLostModeMove(scores, state, rules, analysisSettings, move, candidates)
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
	state.ToMove = PlayerBlue

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
	state.ToMove = PlayerBlue

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
	candidates := selectionCandidateMoves(state, rules, analysisSettings)

	if _, changed := maybeSelectLostModeMove([]float64{}, state, rules, analysisSettings, Move{X: 1, Y: 1}, candidates); changed {
		t.Fatalf("expected lost mode to skip short score slice")
	}
}

func TestSelectBestMoveKeepsLosingFallbackInsideCandidates(t *testing.T) {
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
	state.recomputeHashes()

	cfg := DefaultConfig()
	cfg.AiDepth = 2
	cfg.AiMinDepth = 2
	cfg.AiMaxDepth = 2
	cfg.AiEnableLostMode = false
	player := NewAIPlayer()
	selection := selectionCandidateMoves(state, rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	})
	if len(selection) == 0 {
		t.Fatalf("expected selection candidates")
	}
	candidateSet := buildCandidateSet(selection)
	if _, ok := candidateSet[moveKey{X: 0, Y: 0}]; ok {
		t.Fatalf("expected (0,0) to stay outside the candidate set in this position")
	}

	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	for _, cand := range selection {
		scores[cand.move.Y*settings.BoardSize+cand.move.X] = winScore
	}
	scores[0] = -winScore

	move, ok := player.selectBestMove(state, rules, AIScoreSettings{
		Depth:     2,
		BoardSize: settings.BoardSize,
		Player:    state.ToMove,
		Config:    cfg,
	}, &SearchStats{CompletedDepths: 2}, scores)
	if !ok {
		t.Fatalf("expected a move")
	}
	if move == (Move{X: 0, Y: 0}) {
		t.Fatalf("expected selector to stay inside candidate set, got %v", move)
	}
	if _, ok := candidateSet[moveKey{X: move.X, Y: move.Y}]; !ok {
		t.Fatalf("expected selected move %v to be in candidate set", move)
	}
}
