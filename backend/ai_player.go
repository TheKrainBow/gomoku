package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type AIPlayer struct {
	ghostMutex     sync.Mutex
	moveMutex      sync.Mutex
	configMutex    sync.RWMutex
	workerDone     chan struct{}
	thinking       atomic.Bool
	moveReady      atomic.Bool
	ghostActive    atomic.Bool
	stopSignal     atomic.Bool
	readyMove      Move
	ghostBoard     Board
	ponderMu       sync.Mutex
	ponderCond     *sync.Cond
	ponderState    GameState
	ponderRules    Rules
	ponderVersion  atomic.Uint64
	ponderKey      uint64
	ponderMove     Move
	ponderDecision AITurnDecisionLog
	ponderReady    atomic.Bool
	ponderStop     atomic.Bool
	heuristics     *HeuristicConfig
	readyDecision  AITurnDecisionLog
}

var moveRandomizer = rand.New(rand.NewSource(time.Now().UnixNano()))

type AIRootCandidateLog struct {
	Move       Move
	Score      float64
	ScoreKnown bool
	Status     string
}

type AIRootBandSummaryLog struct {
	Depth             int
	ForcedCount       int
	ForcedMoves       string
	PrincipalCount    int
	PrincipalMoves    string
	SpeculativeCount  int
	SpeculativeMoves  string
	VerificationCount int
	VerificationMoves string
}

type AIRootMoveDetailLog struct {
	Move           Move
	Priority       int
	SourceFlags    string
	ThreatFlags    string
	ThreatSeverity int
	ChildForcing   int
	ShallowScore   float64
	Forced         bool
	Status         string
}

type AITurnDecisionLog struct {
	Player         PlayerColor
	Move           Move
	BestDepth      int
	TimeToPlayMs   int64
	UsedCache      bool
	DecisionSource string
	RootCandidates []AIRootCandidateLog
	RootEvalLine   string
	RootPoolLine   string
	RootBands      []AIRootBandSummaryLog
	RootMoves      []AIRootMoveDetailLog
}

func liveAIConfig(config Config) Config {
	adjusted := config
	adjusted.AiEnableTtPersistence = false
	adjusted.AiPonderingEnabled = false
	adjusted.AiQueueEnabled = false
	adjusted.AiTimeBudgetMs = 0
	adjusted.AiMinDepth = 2
	adjusted.AiMaxDepth = adjusted.AiDepth
	if !adjusted.AiUseTtCache {
		adjusted.AiTtSize = 0
		adjusted.AiTtMaxEntries = 0
		adjusted.AiEnableRootTranspose = false
	}
	return adjusted
}

func newLiveSearchCache() *AISearchCache {
	cache := newAISearchCache()
	return &cache
}

func liveSearchCache(config Config) *AISearchCache {
	if config.AiUseTtCache {
		return SharedSearchCache()
	}
	return newLiveSearchCache()
}

func NewAIPlayer() *AIPlayer {
	player := &AIPlayer{}
	player.ponderCond = sync.NewCond(&player.ponderMu)
	player.startPonderWorker()
	return player
}

func (a *AIPlayer) IsHuman() bool {
	return false
}

func (a *AIPlayer) ChooseMove(state GameState, rules Rules) Move {
	config := a.effectiveConfig()
	stats := &SearchStats{Start: time.Now()}
	cache := liveSearchCache(config)
	settings := AIScoreSettings{
		Depth:           config.AiDepth,
		TimeoutMs:       config.AiTimeoutMs,
		BoardSize:       state.Board.Size(),
		Player:          state.ToMove,
		Cache:           cache,
		Config:          config,
		Stats:           stats,
		DirectDepthOnly: false,
	}
	scores := ScoreBoard(state, rules, settings)
	bestMove, ok := a.selectBestMove(state, rules, settings, stats, scores)
	if config.AiLogSearchStats {
		logSearchStats("choose", stats, settings)
	}
	if ok {
		reportedDepth := stats.CompletedDepths
		if stats.ReturnedDepth > 0 {
			reportedDepth = stats.ReturnedDepth
		}
		a.storeReadyDecision(buildAITurnDecisionLog(state, rules, settings, stats, scores, bestMove))
		logMoveSelection(state.ToMove, bestMove, reportedDepth, settings.BoardSize)
		bestMove.Depth = reportedDepth
		return bestMove
	}
	return Move{}
}

func (a *AIPlayer) StartThinking(state GameState, rules Rules, ghostSink func(GameState), depthSink func(move Move, depth int, score float64)) {
	a.StartThinkingWithConfig(state, rules, ghostSink, depthSink, a.effectiveConfig())
}

func (a *AIPlayer) StartThinkingWithConfig(state GameState, rules Rules, ghostSink func(GameState), depthSink func(move Move, depth int, score float64), config Config) {
	a.startThinking(state, rules, ghostSink, depthSink, liveAIConfig(config))
}

func (a *AIPlayer) StartThinkingWithRawConfig(state GameState, rules Rules, ghostSink func(GameState), depthSink func(move Move, depth int, score float64), config Config) {
	a.startThinking(state, rules, ghostSink, depthSink, config)
}

func (a *AIPlayer) startThinking(state GameState, rules Rules, ghostSink func(GameState), depthSink func(move Move, depth int, score float64), config Config) {
	if a.thinking.Load() {
		return
	}
	if a.workerDone != nil {
		<-a.workerDone
	}
	a.thinking.Store(true)
	a.moveReady.Store(false)
	a.ghostActive.Store(false)
	a.stopSignal.Store(false)

	stateCopy := state.Clone()
	rulesCopy := rules
	done := make(chan struct{})
	a.workerDone = done
	go func() {
		defer close(done)
		stats := &SearchStats{Start: time.Now()}
		cache := liveSearchCache(config)
		settings := AIScoreSettings{
			Depth:           config.AiDepth,
			TimeoutMs:       config.AiTimeoutMs,
			BoardSize:       stateCopy.Board.Size(),
			Player:          stateCopy.ToMove,
			Cache:           cache,
			Config:          config,
			ShouldStop:      func() bool { return a.stopSignal.Load() },
			Stats:           stats,
			DirectDepthOnly: false,
		}
		if config.GhostMode && ghostSink != nil {
			throttleMs := config.AiGhostThrottleMs
			var lastPublish time.Time
			settings.OnGhostUpdate = func(gs GameState) {
				if throttleMs > 0 {
					now := time.Now()
					if !lastPublish.IsZero() && now.Sub(lastPublish) < time.Duration(throttleMs)*time.Millisecond {
						return
					}
					lastPublish = now
				}
				a.ghostMutex.Lock()
				a.ghostBoard = gs.Board.Clone()
				a.ghostMutex.Unlock()
				a.ghostActive.Store(true)
				ghostSink(gs)
			}
		}
		if depthSink != nil {
			settings.OnDepthComplete = func(depth int, move Move, score float64) {
				if a.stopSignal.Load() {
					return
				}
				depthSink(move, depth, score)
			}
		}
		scores := ScoreBoard(stateCopy, rulesCopy, settings)
		if a.stopSignal.Load() {
			a.moveReady.Store(false)
			a.ghostActive.Store(false)
			a.thinking.Store(false)
			return
		}
		bestMove, ok := a.selectBestMove(stateCopy, rulesCopy, settings, stats, scores)
		if settings.Config.AiLogSearchStats {
			logSearchStats("think", settings.Stats, settings)
		}
		a.moveMutex.Lock()
		if ok {
			reportedDepth := stats.CompletedDepths
			if stats.ReturnedDepth > 0 {
				reportedDepth = stats.ReturnedDepth
			}
			a.readyDecision = buildAITurnDecisionLog(stateCopy, rulesCopy, settings, stats, scores, bestMove)
			logMoveSelection(stateCopy.ToMove, bestMove, reportedDepth, settings.BoardSize)
			bestMove.Depth = reportedDepth
			if depthSink != nil {
				score := scores[bestMove.Y*settings.BoardSize+bestMove.X]
				depthSink(bestMove, reportedDepth, score)
			}
			a.readyMove = bestMove
		} else {
			a.readyMove = Move{}
		}
		a.moveMutex.Unlock()
		a.moveReady.Store(true)
		a.ghostActive.Store(false)
		a.thinking.Store(false)
	}()
}

func (a *AIPlayer) StopThinking() {
	a.stopSignal.Store(true)
	if a.workerDone != nil {
		<-a.workerDone
	}
	a.moveReady.Store(false)
	a.ghostActive.Store(false)
	a.thinking.Store(false)
	a.stopSignal.Store(false)
}

func (a *AIPlayer) IsThinking() bool {
	return a.thinking.Load()
}

func (a *AIPlayer) HasMoveReady() bool {
	return a.moveReady.Load()
}

func (a *AIPlayer) TakeMove() Move {
	a.moveMutex.Lock()
	defer a.moveMutex.Unlock()
	a.moveReady.Store(false)
	return a.readyMove
}

func (a *AIPlayer) TakeMoveWithDecision() (Move, AITurnDecisionLog) {
	a.moveMutex.Lock()
	defer a.moveMutex.Unlock()
	a.moveReady.Store(false)
	move := a.readyMove
	decision := a.readyDecision
	a.readyDecision = AITurnDecisionLog{}
	return move, decision
}

func (a *AIPlayer) TakeLastDecision() AITurnDecisionLog {
	a.moveMutex.Lock()
	defer a.moveMutex.Unlock()
	decision := a.readyDecision
	a.readyDecision = AITurnDecisionLog{}
	return decision
}

func (a *AIPlayer) HasGhostBoard() bool {
	return a.ghostActive.Load()
}

func (a *AIPlayer) GhostBoardCopy() Board {
	a.ghostMutex.Lock()
	defer a.ghostMutex.Unlock()
	return a.ghostBoard.Clone()
}

func (a *AIPlayer) OnMoveApplied(state GameState, rules Rules) {
	ensureTT(SharedSearchCache(), GetConfig())
	a.updatePonderState(state, rules)
}

func (a *AIPlayer) CacheSize() int {
	return TranspositionSize(SharedSearchCache())
}

func (a *AIPlayer) ResetForConfigChange() {
	a.stopSignal.Store(true)
	a.ponderReady.Store(false)
	a.stopSignal.Store(false)
}

func (a *AIPlayer) startPonderWorker() {
	go func() {
		var lastVersion uint64
		for {
			a.ponderMu.Lock()
			for a.ponderVersion.Load() == lastVersion {
				a.ponderCond.Wait()
			}
			state := a.ponderState.Clone()
			rules := a.ponderRules
			version := a.ponderVersion.Load()
			lastVersion = version
			a.ponderMu.Unlock()

			config := a.effectiveConfig()
			if !config.AiPonderingEnabled {
				continue
			}
			if state.Hash == 0 {
				state.recomputeHashes()
			}
			stats := &SearchStats{Start: time.Now()}
			cache := SharedSearchCache()
			settings := AIScoreSettings{
				Depth:      config.AiDepth,
				TimeoutMs:  config.AiTimeoutMs,
				BoardSize:  state.Board.Size(),
				Player:     state.ToMove,
				Cache:      cache,
				Config:     config,
				ShouldStop: func() bool { return a.stopSignal.Load() || a.ponderVersion.Load() != version },
				Stats:      stats,
			}
			scores := ScoreBoard(state, rules, settings)
			if a.stopSignal.Load() || a.ponderVersion.Load() != version {
				continue
			}
			bestMove, ok := a.selectBestMove(state, rules, settings, stats, scores)
			if settings.Config.AiLogSearchStats {
				logSearchStats("ponder", stats, settings)
			}
			if ok {
				bestMove.Depth = stats.CompletedDepths
				key := ttKeyFor(state, settings.BoardSize)
				a.ponderMu.Lock()
				if a.ponderVersion.Load() == version {
					a.ponderKey = key
					a.ponderMove = bestMove
					a.ponderDecision = buildAITurnDecisionLog(state, rules, settings, stats, scores, bestMove)
					a.ponderReady.Store(true)
				}
				a.ponderMu.Unlock()
			}
		}
	}()
}

func (a *AIPlayer) updatePonderState(state GameState, rules Rules) {
	config := a.effectiveConfig()
	if !config.AiPonderingEnabled {
		return
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	a.ponderMu.Lock()
	a.ponderState = state.Clone()
	a.ponderRules = rules
	a.ponderVersion.Add(1)
	a.ponderReady.Store(false)
	a.ponderCond.Signal()
	a.ponderMu.Unlock()
}

func (a *AIPlayer) SetHeuristicsOverride(heuristics *HeuristicConfig) {
	a.configMutex.Lock()
	a.heuristics = cloneHeuristicConfigPtr(heuristics)
	a.configMutex.Unlock()
}

func (a *AIPlayer) effectiveConfig() Config {
	config := GetConfig()
	a.configMutex.RLock()
	override := cloneHeuristicConfigPtr(a.heuristics)
	a.configMutex.RUnlock()
	if override != nil {
		config.Heuristics = *override
	}
	return liveAIConfig(config)
}

func (a *AIPlayer) TakePonderedMove(state GameState, rules Rules) (Move, bool) {
	if !a.effectiveConfig().AiPonderingEnabled {
		a.ponderReady.Store(false)
		return Move{}, false
	}
	if !a.ponderReady.Load() {
		return Move{}, false
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	key := ttKeyFor(state, state.Board.Size())
	a.ponderMu.Lock()
	defer a.ponderMu.Unlock()
	if !a.ponderReady.Load() || a.ponderKey != key {
		return Move{}, false
	}
	move := a.ponderMove
	if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
		a.ponderDecision.Move = move
		a.ponderReady.Store(false)
		return move, true
	}
	return Move{}, false
}

func (a *AIPlayer) TakePonderedMoveWithDecision(state GameState, rules Rules) (Move, AITurnDecisionLog, bool) {
	if !a.effectiveConfig().AiPonderingEnabled {
		a.ponderReady.Store(false)
		return Move{}, AITurnDecisionLog{}, false
	}
	if !a.ponderReady.Load() {
		return Move{}, AITurnDecisionLog{}, false
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	key := ttKeyFor(state, state.Board.Size())
	a.ponderMu.Lock()
	defer a.ponderMu.Unlock()
	if !a.ponderReady.Load() || a.ponderKey != key {
		return Move{}, AITurnDecisionLog{}, false
	}
	move := a.ponderMove
	if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
		decision := a.ponderDecision
		decision.Move = move
		a.ponderDecision = AITurnDecisionLog{}
		a.ponderReady.Store(false)
		return move, decision, true
	}
	return Move{}, AITurnDecisionLog{}, false
}

func bestMoveFromScores(scores []float64, state GameState, rules Rules, size int) (Move, bool) {
	bestMove, ok := bestScoredMoveFromScores(scores, state, rules, size)
	if ok {
		return bestMove, true
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
				return move, true
			}
		}
	}
	return Move{}, false
}

func bestScoredMoveFromScores(scores []float64, state GameState, rules Rules, size int) (Move, bool) {
	maximizing := state.ToMove == PlayerRed
	bestScore := math.Inf(1)
	if maximizing {
		bestScore = math.Inf(-1)
	}
	bestMove := Move{}
	foundScored := false
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(state, move, state.ToMove); !ok {
				continue
			}
			idx := y*size + x
			if idx < 0 || idx >= len(scores) {
				continue
			}
			score := scores[idx]
			if score == illegalScore {
				continue
			}
			foundScored = true
			if maximizing && score > bestScore {
				bestScore = score
				bestMove = move
			}
			if !maximizing && score < bestScore {
				bestScore = score
				bestMove = move
			}
		}
	}
	return bestMove, foundScored
}

func bestScoredCandidateMove(scores []float64, candidates []candidateMove, state GameState, rules Rules, size int) (Move, bool) {
	maximizing := state.ToMove == PlayerRed
	bestScore := math.Inf(1)
	if maximizing {
		bestScore = math.Inf(-1)
	}
	bestMove := Move{}
	foundScored := false
	for _, candidate := range candidates {
		move := candidate.move
		if !move.IsValid(size) {
			continue
		}
		if ok, _ := rules.IsLegal(state, move, state.ToMove); !ok {
			continue
		}
		idx := move.Y*size + move.X
		if idx < 0 || idx >= len(scores) {
			continue
		}
		score := scores[idx]
		if score == illegalScore {
			continue
		}
		if !foundScored {
			bestScore = score
			bestMove = move
			foundScored = true
			continue
		}
		if maximizing {
			if score > bestScore {
				bestScore = score
				bestMove = move
			}
		} else if score < bestScore {
			bestScore = score
			bestMove = move
		}
	}
	return bestMove, foundScored
}

func (a *AIPlayer) selectBestMove(state GameState, rules Rules, settings AIScoreSettings, stats *SearchStats, scores []float64) (Move, bool) {
	candidates := selectionCandidateMoves(state, rules, settings)
	candidateSet := buildCandidateSet(candidates)
	bestMove, ok := bestScoredCandidateMove(scores, candidates, state, rules, settings.BoardSize)
	if !ok {
		if fallback, found := firstLegalCandidate(state, rules, candidates, settings.BoardSize); found {
			return a.ensureLegalOrFallback(state, rules, settings, candidates, true, fallback)
		}
		if fallback, found := a.heuristicFallbackMove(state, rules, settings, candidates); found {
			return a.ensureLegalOrFallback(state, rules, settings, candidates, true, fallback)
		}
		return Move{}, false
	}
	candidateFallbackUsed := false
	if _, ok := candidateSet[moveKey{X: bestMove.X, Y: bestMove.Y}]; !ok {
		log.Printf("[ai-player] best move %v outside candidate set, trying fallback candidate", bestMove)
		if fallback, found := firstLegalCandidate(state, rules, candidates, settings.BoardSize); found {
			log.Printf("[ai-player] fallback candidate %v", fallback)
			bestMove = fallback
			candidateFallbackUsed = true
		} else {
			log.Printf("[ai-player] no candidate fallback found")
			if fallback, found := a.heuristicFallbackMove(state, rules, settings, candidates); found {
				return a.ensureLegalOrFallback(state, rules, settings, candidates, true, fallback)
			}
			return Move{}, false
		}
	}
	fallbackUsed := false
	fullDepthReached := false
	if stats != nil {
		reportedDepth := stats.ReturnedDepth
		if reportedDepth <= 0 {
			reportedDepth = stats.CompletedDepths
		}
		fullDepthReached = reportedDepth >= settings.Depth && settings.Depth > 0
	}
	if !fullDepthReached && scoredMoveLooksLosing(scores, bestMove, state.ToMove, settings.BoardSize) && hasUnscoredCandidate(scores, candidates, settings.BoardSize) {
		if fallback, found := firstLegalCandidate(state, rules, candidates, settings.BoardSize); found && !fallback.Equals(bestMove) {
			log.Printf("[ai-player] incomplete root scores with losing best move %v, using ordered fallback %v", bestMove, fallback)
			bestMove = fallback
			fallbackUsed = true
		}
	}
	if candidateFallbackUsed {
		// Keep fallback candidate, avoid depth-1 fallback override.
		fallbackUsed = true
	} else if !fullDepthReached {
		bestMove, fallbackUsed = a.maybeDepthOneBackup(state, rules, scores, bestMove, settings.BoardSize, stats.CompletedDepths, candidates)
	}
	if !fullDepthReached {
		if lostModeMove, changed := maybeSelectLostModeMove(scores, state, rules, settings, bestMove, candidates); changed {
			bestMove = lostModeMove
			fallbackUsed = false
			if _, ok := candidateSet[moveKey{X: bestMove.X, Y: bestMove.Y}]; !ok {
				log.Printf("[ai-player] lost-mode move %v outside candidate set, reverting to fallback candidate", bestMove)
				if fallback, found := firstLegalCandidate(state, rules, candidates, settings.BoardSize); found {
					bestMove = fallback
				} else {
					if fallback, found := a.heuristicFallbackMove(state, rules, settings, candidates); found {
						return a.ensureLegalOrFallback(state, rules, settings, candidates, true, fallback)
					}
					return Move{}, false
				}
			}
		}
	}
	return a.ensureLegalOrFallback(state, rules, settings, candidates, fallbackUsed, bestMove)
}

func hasUnscoredCandidate(scores []float64, candidates []candidateMove, boardSize int) bool {
	for _, cand := range candidates {
		move := cand.move
		if !move.IsValid(boardSize) {
			continue
		}
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(scores) {
			continue
		}
		if scores[idx] == illegalScore {
			return true
		}
	}
	return false
}

func scoredMoveLooksLosing(scores []float64, move Move, player PlayerColor, boardSize int) bool {
	if !move.IsValid(boardSize) {
		return false
	}
	idx := move.Y*boardSize + move.X
	if idx < 0 || idx >= len(scores) {
		return false
	}
	score := scores[idx]
	if score == illegalScore {
		return false
	}
	threshold := winScore / 2
	if player == PlayerRed {
		return score <= -threshold
	}
	return score >= threshold
}

func selectionCandidateMoves(state GameState, rules Rules, settings AIScoreSettings) []candidateMove {
	ctx := newMinimaxContext(rules, settings, time.Now())
	attachEvalState(&ctx, state)
	rootPool := buildRootMovePool(state, ctx, settings.Player)
	if len(rootPool) == 0 {
		return collectCandidateMovesWithEval(state, rules, state.ToMove, settings.BoardSize, nil, settings.Stats)
	}
	depth := settings.Depth
	if depth <= 0 {
		depth = 1
	}
	ordered := sortRootMoveIndices(rootPool, settings.Player == PlayerRed, nil)
	bands := chooseRootSearchBands(ctx, rootPool, ordered, depth)
	indices := rootBandSearchOrder(bands)
	indices = append(indices, bands.verification...)
	candidates := candidateMovesFromRootIndices(rootPool, indices)
	if len(candidates) == 0 {
		return collectCandidateMovesWithEval(state, rules, state.ToMove, settings.BoardSize, nil, settings.Stats)
	}
	return candidates
}

func (a *AIPlayer) ensureLegalOrFallback(state GameState, rules Rules, settings AIScoreSettings, candidates []candidateMove, fallbackUsed bool, move Move) (Move, bool) {
	if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
		return move, true
	}
	if fallback, ok := firstLegalCandidate(state, rules, candidates, settings.BoardSize); ok {
		log.Printf("[ai-player] using candidate fallback move %v", fallback)
		return fallback, true
	}
	if !fallbackUsed {
		if fallback, ok := a.heuristicFallbackMove(state, rules, settings, candidates); ok {
			log.Printf("[ai-player] using heuristic fallback move %v", fallback)
			return fallback, true
		}
	}
	log.Printf("[ai-player] no fallback move available")
	return Move{}, false
}

type lostModeCandidate struct {
	move  Move
	score float64
}

var lostModeFragilityFn = opponentReplyFragilityGap

func maybeSelectLostModeMove(scores []float64, state GameState, rules Rules, settings AIScoreSettings, currentBest Move, candidates []candidateMove) (Move, bool) {
	cfg := settings.Config
	if !cfg.AiEnableLostMode {
		return Move{}, false
	}

	minDepth := cfg.AiLostModeMinDepth
	if minDepth < 2 {
		minDepth = 2
	}
	if settings.Depth < minDepth || settings.BoardSize <= 0 {
		return Move{}, false
	}
	if !currentBest.IsValid(settings.BoardSize) {
		return Move{}, false
	}
	scoreCount := settings.BoardSize * settings.BoardSize
	if scoreCount <= 0 || len(scores) < scoreCount {
		return Move{}, false
	}
	bestScore := scores[currentBest.Y*settings.BoardSize+currentBest.X]
	threshold := cfg.AiLostModeThreshold
	if threshold <= 0 {
		threshold = winScore / 2
	}
	maximizing := state.ToMove == PlayerRed
	losing := (maximizing && bestScore <= -threshold) || (!maximizing && bestScore >= threshold)
	if !losing {
		return Move{}, false
	}

	lostCandidates := collectLostModeCandidates(scores, state, rules, settings.BoardSize, maximizing, candidates)
	if len(lostCandidates) == 0 {
		return Move{}, false
	}
	maxMoves := cfg.AiLostModeMaxMoves
	if maxMoves <= 0 {
		maxMoves = 6
	}
	if len(lostCandidates) > maxMoves {
		lostCandidates = lostCandidates[:maxMoves]
	}

	chosen := currentBest
	chosenGap := -1.0
	chosenScore := bestScore
	for _, cand := range lostCandidates {
		gap, ok := lostModeFragilityFn(state, rules, settings, cand.move)
		if !ok {
			continue
		}
		if gap > chosenGap {
			chosen = cand.move
			chosenGap = gap
			chosenScore = cand.score
			continue
		}
		if gap == chosenGap {
			if maximizing {
				if cand.score > chosenScore {
					chosen = cand.move
					chosenScore = cand.score
				}
			} else {
				if cand.score < chosenScore {
					chosen = cand.move
					chosenScore = cand.score
				}
			}
		}
	}
	if chosen == currentBest {
		return Move{}, false
	}
	return chosen, true
}

func (a *AIPlayer) maybeDepthOneBackup(state GameState, rules Rules, scores []float64, best Move, boardSize, completedDepth int, candidates []candidateMove) (Move, bool) {
	config := a.effectiveConfig()
	if completedDepth < config.AiDepth {
		return best, false
	}
	bestScore := scoreForMove(scores, best, boardSize)
	threshold := winScore / 2
	losing := (state.ToMove == PlayerRed && bestScore <= -threshold) || (state.ToMove == PlayerBlue && bestScore >= threshold)
	if !losing {
		return best, false
	}
	if fallback, ok := a.heuristicFallbackMove(state, rules, AIScoreSettings{
		Depth:            1,
		TimeoutMs:        config.AiTimeoutMs,
		BoardSize:        state.Board.Size(),
		Player:           state.ToMove,
		Cache:            liveSearchCache(config),
		Config:           config,
		SkipQueueBacklog: true,
	}, candidates); ok {
		return fallback, true
	}
	return best, false
}

func scoreForMove(scores []float64, move Move, boardSize int) float64 {
	if !move.IsValid(boardSize) {
		return math.Inf(1)
	}
	idx := move.Y*boardSize + move.X
	if idx < 0 || idx >= len(scores) {
		return math.Inf(1)
	}
	return scores[idx]
}

func randomAdjacentMove(state GameState, rules Rules) (Move, bool) {
	size := state.Board.Size()
	if size <= 0 {
		return Move{}, false
	}
	visited := make([]bool, size*size)
	var moves []Move
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if state.Board.At(x, y) == CellEmpty {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					nx := x + dx
					ny := y + dy
					if !state.Board.InBounds(nx, ny) {
						continue
					}
					if state.Board.At(nx, ny) != CellEmpty {
						continue
					}
					idx := ny*size + nx
					if visited[idx] {
						continue
					}
					visited[idx] = true
					moves = append(moves, Move{X: nx, Y: ny})
				}
			}
		}
	}
	if len(moves) == 0 {
		return Move{}, false
	}
	moveRandomizer.Shuffle(len(moves), func(i, j int) {
		moves[i], moves[j] = moves[j], moves[i]
	})
	for _, move := range moves {
		if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
			return move, true
		}
	}
	return Move{}, false
}

type moveKey struct {
	X int
	Y int
}

func buildCandidateSet(candidates []candidateMove) map[moveKey]struct{} {
	set := make(map[moveKey]struct{}, len(candidates))
	for _, cand := range candidates {
		set[moveKey{X: cand.move.X, Y: cand.move.Y}] = struct{}{}
	}
	return set
}

func logMoveSelection(player PlayerColor, move Move, depth, boardSize int) {
	if boardSize <= 0 {
		return
	}
	if !move.IsValid(boardSize) {
		return
	}
	playerID := 1
	if player == PlayerRed {
		playerID = 2
	}
	log.Printf("[ai-player] Player %d played [%d,%d] depth=%d", playerID, move.X, move.Y, depth)
}

func buildAITurnDecisionLog(state GameState, rules Rules, settings AIScoreSettings, stats *SearchStats, scores []float64, move Move) AITurnDecisionLog {
	decision := AITurnDecisionLog{Move: move, Player: state.ToMove}
	if stats != nil {
		decision.BestDepth = stats.ReturnedDepth
		if decision.BestDepth <= 0 {
			decision.BestDepth = stats.CompletedDepths
		}
		decision.UsedCache = stats.UsedCache
		decision.DecisionSource = stats.DecisionSource
		if !stats.Start.IsZero() {
			decision.TimeToPlayMs = time.Since(stats.Start).Milliseconds()
		}
	}
	ctx := newMinimaxContext(rules, settings, time.Now())
	attachEvalState(&ctx, state)
	if ctx.evalState != nil {
		eval := ctx.evalState.Snapshot(&state.Board)
		decision.RootEvalLine = fmt.Sprintf(
			"fixed_root_eval score=%d structural=%d summary=%s own=%s opp=%s",
			eval.Score,
			eval.StructuralScore,
			formatTacticalSummaryForLog(eval.Summary),
			formatThreatsForLog(threatsForPlayerEvalResultForLog(eval, state.ToMove), 6),
			formatThreatsForLog(threatsForPlayerEvalResultForLog(eval, otherPlayer(state.ToMove)), 6),
		)
	}
	rootPool := []RootMove(nil)
	if stats != nil && len(stats.RootPoolSnapshot) > 0 {
		rootPool = cloneRootPool(stats.RootPoolSnapshot)
	}
	if len(rootPool) == 0 {
		rootPool = buildRootMovePool(state, ctx, settings.Player)
	}
	rootOrder := sortRootMoveIndices(rootPool, settings.Player == PlayerRed, nil)
	bandDepth := decision.BestDepth
	if bandDepth <= 0 {
		bandDepth = settings.Depth
	}
	if bandDepth <= 0 {
		bandDepth = 1
	}
	bandsDepth1 := chooseRootSearchBands(ctx, rootPool, rootOrder, 1)
	bandsBestDepth := chooseRootSearchBands(ctx, rootPool, rootOrder, bandDepth)
	decision.RootPoolLine = fmt.Sprintf(
		"fixed_root pool=%d forced=%d ordered=%s",
		len(rootPool),
		len(bandsDepth1.forced),
		formatRootBandMovesLimited(rootPool, rootOrder, 16),
	)
	decision.RootBands = []AIRootBandSummaryLog{
		buildRootBandSummaryLog(rootPool, bandsDepth1, 1),
		buildRootBandSummaryLog(rootPool, bandsBestDepth, bandDepth),
	}
	root := make([]AIRootCandidateLog, 0, len(rootOrder))
	rootMoves := make([]AIRootMoveDetailLog, 0, len(rootOrder))
	seen := make(map[moveKey]struct{}, len(rootOrder))
	for _, idx := range rootOrder {
		if idx < 0 || idx >= len(rootPool) {
			continue
		}
		candidate := rootPool[idx]
		if !candidate.Move.IsValid(settings.BoardSize) {
			continue
		}
		key := moveKey{X: candidate.Move.X, Y: candidate.Move.Y}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		scoreIdx := candidate.Move.Y*settings.BoardSize + candidate.Move.X
		entry := AIRootCandidateLog{Move: candidate.Move}
		if scoreIdx >= 0 && scoreIdx < len(scores) {
			score := scores[scoreIdx]
			if score == illegalScore {
				entry.Status = "ILLEGAL_SENTINEL"
			} else {
				entry.Score = score
				entry.ScoreKnown = true
			}
		}
		root = append(root, entry)
		rootMoves = append(rootMoves, AIRootMoveDetailLog{
			Move:           candidate.Move,
			Priority:       candidate.TacticalPriority,
			SourceFlags:    formatRootSourceFlagsForLog(candidate.SourceFlags),
			ThreatFlags:    formatRootThreatFlagsForLog(candidate.ThreatFlags),
			ThreatSeverity: candidate.ThreatSeverity,
			ChildForcing:   candidate.ChildForcingScore,
			ShallowScore:   candidate.ShallowScore,
			Forced:         candidate.IsForced,
			Status:         candidate.LastSearchStatus,
		})
	}
	decision.RootCandidates = root
	decision.RootMoves = rootMoves
	return decision
}

func buildRootBandSummaryLog(rootPool []RootMove, bands rootSearchBands, depth int) AIRootBandSummaryLog {
	return AIRootBandSummaryLog{
		Depth:             depth,
		ForcedCount:       len(bands.forced),
		ForcedMoves:       formatRootBandMovesLimited(rootPool, bands.forced, 8),
		PrincipalCount:    len(bands.principal),
		PrincipalMoves:    formatRootBandMovesLimited(rootPool, bands.principal, 12),
		SpeculativeCount:  len(bands.speculative),
		SpeculativeMoves:  formatRootBandMovesLimited(rootPool, bands.speculative, 8),
		VerificationCount: len(bands.verification),
		VerificationMoves: formatRootBandMovesLimited(rootPool, bands.verification, 8),
	}
}

func formatTacticalSummaryForLog(summary TacticalSummary) string {
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

func formatThreatsForLog(threats []Threat, limit int) string {
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
		fmt.Fprintf(&sb, "%s tier=%d ext=%s def=%s", patternNameForThreatForLog(threat.Type), threat.Tier, formatThreatPositionsForLog(threat.ExtensionSquares), formatThreatPositionsForLog(threat.DefenseSquares))
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatThreatPositionsForLog(positions []Pos) string {
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

func threatsForPlayerEvalResultForLog(result EvalResult, player PlayerColor) []Threat {
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

func patternNameForThreatForLog(typ ThreatType) string {
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

func formatRootSourceFlagsForLog(flags uint32) string {
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

func formatRootThreatFlagsForLog(flags uint32) string {
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

func (a *AIPlayer) storeReadyDecision(decision AITurnDecisionLog) {
	a.moveMutex.Lock()
	defer a.moveMutex.Unlock()
	a.readyDecision = decision
}

func firstLegalCandidate(state GameState, rules Rules, candidates []candidateMove, boardSize int) (Move, bool) {
	for _, cand := range candidates {
		move := cand.move
		if !move.IsValid(boardSize) {
			continue
		}
		if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
			return move, true
		}
	}
	return Move{}, false
}

func (a *AIPlayer) heuristicFallbackMove(state GameState, rules Rules, settings AIScoreSettings, candidates []candidateMove) (Move, bool) {
	bestMove := Move{}
	bestScore := math.Inf(1)
	if state.ToMove == PlayerRed {
		bestScore = math.Inf(-1)
	}
	found := false
	for _, cand := range candidates {
		move := cand.move
		if !move.IsValid(settings.BoardSize) {
			continue
		}
		if ok, _ := rules.IsLegal(state, move, state.ToMove); !ok {
			continue
		}
		score := heuristicForMove(state, rules, settings, move, nil)
		if score == illegalScore {
			continue
		}
		if !found {
			bestMove = move
			bestScore = score
			found = true
			continue
		}
		if state.ToMove == PlayerRed {
			if score > bestScore {
				bestMove = move
				bestScore = score
			}
		} else if score < bestScore {
			bestMove = move
			bestScore = score
		}
	}
	return bestMove, found
}

func collectLostModeCandidates(scores []float64, state GameState, rules Rules, size int, maximizing bool, candidates []candidateMove) []lostModeCandidate {
	out := make([]lostModeCandidate, 0, len(candidates))
	for _, cand := range candidates {
		move := cand.move
		if !move.IsValid(size) {
			continue
		}
		if ok, _ := rules.IsLegal(state, move, state.ToMove); !ok {
			continue
		}
		idx := move.Y*size + move.X
		if idx < 0 || idx >= len(scores) {
			continue
		}
		score := scores[idx]
		if score == illegalScore {
			continue
		}
		out = append(out, lostModeCandidate{move: move, score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if maximizing {
			return out[i].score > out[j].score
		}
		return out[i].score < out[j].score
	})
	return out
}

func opponentReplyFragilityGap(state GameState, rules Rules, settings AIScoreSettings, move Move) (float64, bool) {
	next := state.Clone()
	if !applyMove(&next, rules, move, state.ToMove) {
		return 0.0, false
	}
	opponent := next.ToMove
	oppMaximizing := opponent == PlayerRed
	replyCandidates := collectCandidateMovesWithEval(next, rules, opponent, settings.BoardSize, nil, settings.Stats)
	if len(replyCandidates) == 0 {
		return 0.0, false
	}
	replyLimit := settings.Config.AiLostModeReplyLimit
	if replyLimit <= 0 {
		replyLimit = 12
	}
	if replyLimit > len(replyCandidates) {
		replyLimit = len(replyCandidates)
	}
	ctx := minimaxContext{
		rules:    rules,
		settings: settings,
		start:    time.Now(),
	}
	replies := orderCandidateMoves(next, ctx, opponent, oppMaximizing, 1, replyCandidates, replyLimit, nil)
	if len(replies) == 0 {
		return 0.0, false
	}

	best := 0.0
	second := 0.0
	haveBest := false
	haveSecond := false
	for _, reply := range replies {
		replyState := next.Clone()
		if !applyMove(&replyState, rules, reply, opponent) {
			continue
		}
		score := evaluateStateHeuristic(replyState, rules, settings)
		if oppMaximizing {
			if !haveBest || score > best {
				second = best
				haveSecond = haveBest
				best = score
				haveBest = true
				continue
			}
			if !haveSecond || score > second {
				second = score
				haveSecond = true
			}
			continue
		}
		if !haveBest || score < best {
			second = best
			haveSecond = haveBest
			best = score
			haveBest = true
			continue
		}
		if !haveSecond || score < second {
			second = score
			haveSecond = true
		}
	}
	if !haveBest {
		return 0.0, false
	}
	if !haveSecond {
		return 0.0, true
	}
	if oppMaximizing {
		return best - second, true
	}
	return second - best, true
}

func logSearchStats(tag string, stats *SearchStats, settings AIScoreSettings) {
	if stats == nil {
		return
	}
	elapsed := time.Duration(0)
	if !stats.Start.IsZero() {
		elapsed = time.Since(stats.Start)
	} else {
		for _, d := range stats.DepthDurations {
			elapsed += d
		}
	}
	avgBranch := 0.0
	if stats.Nodes > 0 {
		avgBranch = float64(stats.CandidateCount) / float64(stats.Nodes)
	}
	avgRoot := 0.0
	if stats.RootSamples > 0 {
		avgRoot = float64(stats.RootCandidates) / float64(stats.RootSamples)
	}
	avgDeep := 0.0
	if stats.DeepSamples > 0 {
		avgDeep = float64(stats.DeepCandidates) / float64(stats.DeepSamples)
	}
	parts := make([]string, 0, len(stats.DepthDurations))
	for _, d := range stats.DepthDurations {
		parts = append(parts, fmt.Sprintf("%dms", d.Milliseconds()))
	}
	nps := 0.0
	if elapsed > 0 {
		nps = float64(stats.Nodes) / elapsed.Seconds()
	}
	ttHitRate := 0.0
	if stats.TTProbes > 0 {
		ttHitRate = float64(stats.TTHits) * 100.0 / float64(stats.TTProbes)
	}
	ttReplaceRate := 0.0
	if stats.TTStores > 0 {
		ttReplaceRate = float64(stats.TTReplacements) * 100.0 / float64(stats.TTStores)
	}
	ttCutoffRate := 0.0
	if stats.Cutoffs > 0 {
		ttCutoffRate = float64(stats.TTCutoffs) * 100.0 / float64(stats.Cutoffs)
	}
	evalHitRate := 0.0
	if stats.EvalCacheProbes > 0 {
		evalHitRate = float64(stats.EvalCacheHits) * 100.0 / float64(stats.EvalCacheProbes)
	}
	heuristicShare := 0.0
	boardGenShare := 0.0
	if elapsed > 0 {
		heuristicShare = float64(stats.HeuristicTime) * 100.0 / float64(elapsed)
		boardGenShare = float64(stats.BoardGenTime) * 100.0 / float64(elapsed)
	}
	rootFirstRate := percentRatio(stats.RootFirstMoveWins, stats.RootFirstMoveSamples)
	rootTop2Rate := percentRatio(stats.RootTop2Wins, stats.RootTop2Samples)
	rootTop3Rate := percentRatio(stats.RootTop3Wins, stats.RootTop3Samples)
	nodeFirstLeadRate := percentRatio(stats.NodeFirstLeadWins, stats.NodeFirstLeadSamples)
	nodeFirstExactRate := percentRatio(stats.NodeFirstExactWins, stats.NodeFirstExactSamples)
	nodeFirstCutoffRate := percentRatio(stats.NodeFirstCutoffWins, stats.NodeFirstCutoffSamples)
	pvsProxyRate := percentRatio(stats.PVSProxyWouldResearch, stats.PVSProxySamples)
	pvsProxyQuietRate := percentRatio(stats.PVSProxyQuietWouldResearch, stats.PVSProxyQuietSamples)
	pvsProxySoftRate := percentRatio(stats.PVSProxySoftWouldResearch, stats.PVSProxySoftSamples)
	pvsProxyHardRate := percentRatio(stats.PVSProxyHardWouldResearch, stats.PVSProxyHardSamples)
	rootFirstByDepth := formatRootFirstMoveByDepth(stats)
	pvsProxyByDepth := formatPVSProxyByDepth(stats)
	ttSize := 0
	ttSize = TranspositionSize(settings.Cache)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("[ai:%s] t=%dms depth=%d completed=%d nodes=%d nps=%.0f tt_size=%d tt_probe=%d tt_hit=%d tt_hit_rate=%.1f%% tt_hit_flag=(e:%d l:%d u:%d) tt_store=%d tt_replace=%d tt_replace_rate=%.1f%% cutoffs=%d tt_cutoff=%d ab_cutoff=%d tt_cutoff_rate=%.1f%% avg_branch=%.2f avg_root=%.2f avg_deep=%.2f root_first=%d/%d(%.1f%%) root_top2=%d/%d(%.1f%%) root_top3=%d/%d(%.1f%%) root_first_by_depth=[%s] node_first_lead=%d/%d(%.1f%%) node_first_exact=%d/%d(%.1f%%) node_first_cutoff=%d/%d(%.1f%%) pvs_proxy=%d/%d(%.1f%%) pvs_proxy_q=%d/%d(%.1f%%) pvs_proxy_s=%d/%d(%.1f%%) pvs_proxy_h=%d/%d(%.1f%%) pvs_proxy_by_depth=[%s] eval_probe=%d eval_hit=%d eval_hit_rate=%.1f%% heur_calls=%d heur_ms=%d heur_share=%.1f%% board_ops=%d board_ms=%d board_share=%.1f%% mem_alloc=%s mem_heap=%s mem_total=%s mem_sys=%s depth_times=[%s]\\n",
		tag,
		elapsed.Milliseconds(),
		settings.Depth,
		stats.CompletedDepths,
		stats.Nodes,
		nps,
		ttSize,
		stats.TTProbes,
		stats.TTHits,
		ttHitRate,
		stats.TTExactHits,
		stats.TTLowerHits,
		stats.TTUpperHits,
		stats.TTStores,
		stats.TTReplacements,
		ttReplaceRate,
		stats.Cutoffs,
		stats.TTCutoffs,
		stats.ABCutoffs,
		ttCutoffRate,
		avgBranch,
		avgRoot,
		avgDeep,
		stats.RootFirstMoveWins,
		stats.RootFirstMoveSamples,
		rootFirstRate,
		stats.RootTop2Wins,
		stats.RootTop2Samples,
		rootTop2Rate,
		stats.RootTop3Wins,
		stats.RootTop3Samples,
		rootTop3Rate,
		rootFirstByDepth,
		stats.NodeFirstLeadWins,
		stats.NodeFirstLeadSamples,
		nodeFirstLeadRate,
		stats.NodeFirstExactWins,
		stats.NodeFirstExactSamples,
		nodeFirstExactRate,
		stats.NodeFirstCutoffWins,
		stats.NodeFirstCutoffSamples,
		nodeFirstCutoffRate,
		stats.PVSProxyWouldResearch,
		stats.PVSProxySamples,
		pvsProxyRate,
		stats.PVSProxyQuietWouldResearch,
		stats.PVSProxyQuietSamples,
		pvsProxyQuietRate,
		stats.PVSProxySoftWouldResearch,
		stats.PVSProxySoftSamples,
		pvsProxySoftRate,
		stats.PVSProxyHardWouldResearch,
		stats.PVSProxyHardSamples,
		pvsProxyHardRate,
		pvsProxyByDepth,
		stats.EvalCacheProbes,
		stats.EvalCacheHits,
		evalHitRate,
		stats.HeuristicCalls,
		stats.HeuristicTime.Milliseconds(),
		heuristicShare,
		stats.BoardGenOps,
		stats.BoardGenTime.Milliseconds(),
		boardGenShare,
		formatBytes(mem.Alloc),
		formatBytes(mem.HeapAlloc),
		formatBytes(mem.TotalAlloc),
		formatBytes(mem.Sys),
		strings.Join(parts, ","),
	)
}

func percentRatio(wins, samples int64) float64 {
	if samples <= 0 {
		return 0.0
	}
	return float64(wins) * 100.0 / float64(samples)
}

func formatRootFirstMoveByDepth(stats *SearchStats) string {
	if stats == nil {
		return ""
	}
	parts := make([]string, 0, orderingStatsDepthBuckets)
	for depth := 1; depth < orderingStatsDepthBuckets; depth++ {
		samples := stats.RootFirstMoveDepthSamples[depth]
		if samples == 0 {
			continue
		}
		wins := stats.RootFirstMoveDepthWins[depth]
		parts = append(parts, fmt.Sprintf("d%d:%d/%d", depth, wins, samples))
	}
	return strings.Join(parts, " ")
}

func formatPVSProxyByDepth(stats *SearchStats) string {
	if stats == nil {
		return ""
	}
	parts := make([]string, 0, orderingStatsDepthBuckets)
	for depth := 1; depth < orderingStatsDepthBuckets; depth++ {
		samples := stats.PVSProxyDepthSamples[depth]
		if samples == 0 {
			continue
		}
		wouldResearch := stats.PVSProxyDepthWouldResearch[depth]
		parts = append(parts, fmt.Sprintf("d%d:%d/%d", depth, wouldResearch, samples))
	}
	return strings.Join(parts, " ")
}
