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
	ghostMutex    sync.Mutex
	moveMutex     sync.Mutex
	configMutex   sync.RWMutex
	workerDone    chan struct{}
	thinking      atomic.Bool
	moveReady     atomic.Bool
	ghostActive   atomic.Bool
	stopSignal    atomic.Bool
	readyMove     Move
	ghostBoard    Board
	ponderMu      sync.Mutex
	ponderCond    *sync.Cond
	ponderState   GameState
	ponderRules   Rules
	ponderVersion atomic.Uint64
	ponderKey     uint64
	ponderMove    Move
	ponderReady   atomic.Bool
	ponderStop    atomic.Bool
	heuristics    *HeuristicConfig
}

var moveRandomizer = rand.New(rand.NewSource(time.Now().UnixNano()))

func liveAIConfig(config Config) Config {
	if config.AiUseTtCache {
		return config
	}
	adjusted := config
	adjusted.AiTtSize = 0
	adjusted.AiTtMaxEntries = 0
	adjusted.AiEnableEvalCache = false
	adjusted.AiEnableRootTranspose = false
	return adjusted
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
	cache := SharedSearchCache()
	settings := AIScoreSettings{
		Depth:     config.AiDepth,
		TimeoutMs: config.AiTimeoutMs,
		BoardSize: state.Board.Size(),
		Player:    state.ToMove,
		Cache:     cache,
		Config:    config,
		Stats:     stats,
	}
	scores := ScoreBoard(state, rules, settings)
	bestMove, ok := a.selectBestMove(state, rules, settings, stats, scores)
	if config.AiLogSearchStats {
		logSearchStats("choose", stats, settings)
	}
	if ok {
		logMoveSelection(state.ToMove, bestMove, stats.CompletedDepths, settings.BoardSize)
		bestMove.Depth = stats.CompletedDepths
		return bestMove
	}
	return Move{}
}

func (a *AIPlayer) StartThinking(state GameState, rules Rules, ghostSink func(GameState), depthSink func(move Move, depth int, score float64)) {
	a.StartThinkingWithConfig(state, rules, ghostSink, depthSink, a.effectiveConfig())
}

func (a *AIPlayer) StartThinkingWithConfig(state GameState, rules Rules, ghostSink func(GameState), depthSink func(move Move, depth int, score float64), config Config) {
	config = liveAIConfig(config)
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
		cache := SharedSearchCache()
		settings := AIScoreSettings{
			Depth:      config.AiDepth,
			TimeoutMs:  config.AiTimeoutMs,
			BoardSize:  stateCopy.Board.Size(),
			Player:     stateCopy.ToMove,
			Cache:      cache,
			Config:     config,
			ShouldStop: func() bool { return a.stopSignal.Load() },
			Stats:      stats,
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
			logMoveSelection(stateCopy.ToMove, bestMove, stats.CompletedDepths, settings.BoardSize)
			bestMove.Depth = stats.CompletedDepths
			if depthSink != nil {
				score := scores[bestMove.Y*settings.BoardSize+bestMove.X]
				depthSink(bestMove, stats.CompletedDepths, score)
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
		a.ponderReady.Store(false)
		return move, true
	}
	return Move{}, false
}

func bestMoveFromScores(scores []float64, state GameState, rules Rules, size int) (Move, bool) {
	maximizing := state.ToMove == PlayerBlack
	bestScore := math.Inf(1)
	if maximizing {
		bestScore = math.Inf(-1)
	}
	bestMove := Move{}
	foundScored := false
	fallbackMove := Move{}
	foundFallback := false
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(state, move, state.ToMove); !ok {
				continue
			}
			if !foundFallback {
				fallbackMove = move
				foundFallback = true
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
	if !foundScored {
		if foundFallback {
			return fallbackMove, true
		}
		return Move{}, false
	}
	return bestMove, true
}

func (a *AIPlayer) selectBestMove(state GameState, rules Rules, settings AIScoreSettings, stats *SearchStats, scores []float64) (Move, bool) {
	candidates := collectCandidateMoves(state, state.ToMove, settings.BoardSize)
	candidateSet := buildCandidateSet(candidates)
	bestMove, ok := bestMoveFromScores(scores, state, rules, settings.BoardSize)
	if !ok {
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
			return a.ensureLegalOrFallback(state, rules, settings, false, Move{})
		}
	}
	fallbackUsed := false
	if candidateFallbackUsed {
		// Keep fallback candidate, avoid depth-1 fallback override.
		fallbackUsed = true
	} else {
		bestMove, fallbackUsed = a.maybeDepthOneBackup(state, rules, scores, bestMove, settings.BoardSize, stats.CompletedDepths)
	}
	if lostModeMove, changed := maybeSelectLostModeMove(scores, state, rules, settings, bestMove); changed {
		bestMove = lostModeMove
		fallbackUsed = false
		if _, ok := candidateSet[moveKey{X: bestMove.X, Y: bestMove.Y}]; !ok {
			log.Printf("[ai-player] lost-mode move %v outside candidate set, reverting to fallback candidate", bestMove)
			if fallback, found := firstLegalCandidate(state, rules, candidates, settings.BoardSize); found {
				bestMove = fallback
			} else {
				return a.ensureLegalOrFallback(state, rules, settings, false, Move{})
			}
		}
	}
	return a.ensureLegalOrFallback(state, rules, settings, fallbackUsed, bestMove)
}

func (a *AIPlayer) ensureLegalOrFallback(state GameState, rules Rules, settings AIScoreSettings, fallbackUsed bool, move Move) (Move, bool) {
	if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
		return move, true
	}
	if !fallbackUsed {
		if fallback, ok := a.depthOneBackupMove(state, rules); ok {
			log.Printf("[ai-player] using depth-1 fallback move %v", fallback)
			return fallback, true
		}
	}
	if fallback, ok := randomAdjacentMove(state, rules); ok {
		log.Printf("[ai-player] using random adjacent fallback move %v", fallback)
		return fallback, true
	}
	log.Printf("[ai-player] no fallback move available")
	return Move{}, false
}

type lostModeCandidate struct {
	move  Move
	score float64
}

var lostModeFragilityFn = opponentReplyFragilityGap

func maybeSelectLostModeMove(scores []float64, state GameState, rules Rules, settings AIScoreSettings, currentBest Move) (Move, bool) {
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
	maximizing := state.ToMove == PlayerBlack
	losing := (maximizing && bestScore <= -threshold) || (!maximizing && bestScore >= threshold)
	if !losing {
		return Move{}, false
	}

	candidates := collectLostModeCandidates(scores, state, rules, settings.BoardSize, maximizing)
	if len(candidates) == 0 {
		return Move{}, false
	}
	maxMoves := cfg.AiLostModeMaxMoves
	if maxMoves <= 0 {
		maxMoves = 6
	}
	if len(candidates) > maxMoves {
		candidates = candidates[:maxMoves]
	}

	chosen := currentBest
	chosenGap := -1.0
	chosenScore := bestScore
	for _, cand := range candidates {
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

func (a *AIPlayer) maybeDepthOneBackup(state GameState, rules Rules, scores []float64, best Move, boardSize, completedDepth int) (Move, bool) {
	config := a.effectiveConfig()
	if completedDepth < config.AiDepth {
		return best, false
	}
	bestScore := scoreForMove(scores, best, boardSize)
	if bestScore > -winScore/2 {
		return best, false
	}
	if fallback, ok := a.depthOneBackupMove(state, rules); ok {
		return fallback, true
	}
	return best, false
}

func (a *AIPlayer) depthOneBackupMove(state GameState, rules Rules) (Move, bool) {
	config := a.effectiveConfig()
	settings := AIScoreSettings{
		Depth:            1,
		TimeoutMs:        config.AiTimeoutMs,
		BoardSize:        state.Board.Size(),
		Player:           state.ToMove,
		Cache:            SharedSearchCache(),
		Config:           config,
		SkipQueueBacklog: true,
	}
	scores := ScoreBoard(state.Clone(), rules, settings)
	return bestMoveFromScores(scores, state, rules, settings.BoardSize)
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
	if player == PlayerWhite {
		playerID = 2
	}
	log.Printf("[ai-player] Player %d played [%d,%d] depth=%d", playerID, move.X, move.Y, depth)
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

func collectLostModeCandidates(scores []float64, state GameState, rules Rules, size int, maximizing bool) []lostModeCandidate {
	out := make([]lostModeCandidate, 0, size)
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
			out = append(out, lostModeCandidate{move: move, score: score})
		}
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
	oppMaximizing := opponent == PlayerBlack
	replyCandidates := collectCandidateMoves(next, opponent, settings.BoardSize)
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
	ttSize := 0
	ttSize = TranspositionSize(settings.Cache)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("[ai:%s] t=%dms depth=%d completed=%d nodes=%d nps=%.0f tt_size=%d tt_probe=%d tt_hit=%d tt_hit_rate=%.1f%% tt_hit_flag=(e:%d l:%d u:%d) tt_store=%d tt_replace=%d tt_replace_rate=%.1f%% cutoffs=%d tt_cutoff=%d ab_cutoff=%d tt_cutoff_rate=%.1f%% avg_branch=%.2f avg_root=%.2f avg_deep=%.2f eval_probe=%d eval_hit=%d eval_hit_rate=%.1f%% mem_alloc=%s mem_heap=%s mem_total=%s mem_sys=%s depth_times=[%s]\\n",
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
		stats.EvalCacheProbes,
		stats.EvalCacheHits,
		evalHitRate,
		formatBytes(mem.Alloc),
		formatBytes(mem.HeapAlloc),
		formatBytes(mem.TotalAlloc),
		formatBytes(mem.Sys),
		strings.Join(parts, ","),
	)
}
