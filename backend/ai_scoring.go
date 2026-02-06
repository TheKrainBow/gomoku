package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	illegalScore = -1e9
	winScore     = 2000000000.0
	// Keep node-loop instrumentation cheap: sample high-cost timers and emit progress in chunks.
	searchTimingSampleMask  int64 = 0x3ff // 1/1024
	searchProgressChunkMask int64 = 0x3f  // 64
	lmrLateMoveStart              = 4
	lmrMinDepth                   = 4
	lmrReduction                  = 1
	maxSearchBoardCells           = 19 * 19
)

type AISearchCache struct {
	mu            sync.Mutex
	TT            *TranspositionTable
	TTSize        int
	TTBuckets     int
	EvalCache     *EvalCache
	EvalCacheSize int
}

type AIScoreSettings struct {
	Depth            int
	TimeoutMs        int
	BoardSize        int
	Player           PlayerColor
	OnGhostUpdate    func(GameState)
	OnNodeProgress   func(delta int64)
	OnSearchProgress func(delta SearchProgressDelta)
	Cache            *AISearchCache
	Config           Config
	ShouldStop       func() bool
	Stats            *SearchStats
	DirectDepthOnly  bool
	SkipQueueBacklog bool
}

type minimaxContext struct {
	rules       Rules
	settings    AIScoreSettings
	start       time.Time
	killers     [][]Move
	history     []int
	deadline    time.Time
	hasDeadline bool
	logIndent   int
}

func maxScore(scores []float64) float64 {
	if len(scores) == 0 {
		return math.Inf(-1)
	}
	best := math.Inf(-1)
	for _, v := range scores {
		if v > best {
			best = v
		}
	}
	return best
}

type SearchStats struct {
	Nodes           int64
	TTProbes        int64
	TTHits          int64
	TTExactHits     int64
	TTLowerHits     int64
	TTUpperHits     int64
	TTStores        int64
	TTOverwrites    int64
	TTReplacements  int64
	Cutoffs         int64
	TTCutoffs       int64
	ABCutoffs       int64
	CandidateCount  int64
	RootCandidates  int64
	DeepCandidates  int64
	RootSamples     int64
	DeepSamples     int64
	EvalCacheProbes int64
	EvalCacheHits   int64
	Start           time.Time
	DepthDurations  []time.Duration
	CompletedDepths int
	HeuristicCalls  int64
	HeuristicTime   time.Duration
	BoardGenOps     int64
	BoardGenTime    time.Duration

	progressReportedNodes    int64
	progressReportedBoardGen int64
	progressMetricNodes      int64
	progressMetricCandidates int64
	progressMetricTTProbes   int64
	progressMetricTTHits     int64
	progressMetricTTCutoffs  int64
	progressMetricABCutoffs  int64
}

type SearchProgressDelta struct {
	Nodes          int64
	CandidateCount int64
	TTProbes       int64
	TTHits         int64
	TTCutoffs      int64
	ABCutoffs      int64
}

func newAISearchCache() AISearchCache {
	return AISearchCache{}
}

type EvalCacheEntry struct {
	Key         uint64
	Value       float64
	GenWritten  uint32
	GenLastUsed uint32
	Valid       bool
}

type EvalCache struct {
	mu      sync.Mutex
	mask    uint64
	buckets int
	entries []EvalCacheEntry
	gen     uint32
}

func NewEvalCache(size uint64, buckets int) *EvalCache {
	if buckets <= 0 {
		buckets = 2
	}
	if size < 1 {
		size = 1
	}
	if (size & (size - 1)) != 0 {
		size = nextPowerOfTwo(size)
	}
	return &EvalCache{
		mask:    size - 1,
		buckets: buckets,
		entries: make([]EvalCacheEntry, int(size)*buckets),
		gen:     1,
	}
}

func (ec *EvalCache) NextGeneration() {
	if ec == nil {
		return
	}
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.gen++
	if ec.gen == 0 {
		ec.gen = 1
	}
}

func (ec *EvalCache) bucketIndex(key uint64) int {
	return int(key&ec.mask) * ec.buckets
}

func (ec *EvalCache) Get(key uint64) (float64, bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	start := ec.bucketIndex(key)
	for i := 0; i < ec.buckets; i++ {
		idx := start + i
		entry := ec.entries[idx]
		if entry.Valid && entry.Key == key {
			entry.GenLastUsed = ec.gen
			ec.entries[idx] = entry
			return entry.Value, true
		}
	}
	return 0.0, false
}

func (ec *EvalCache) Put(key uint64, value float64) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	start := ec.bucketIndex(key)
	victim := -1
	oldestAge := uint32(0)
	for i := 0; i < ec.buckets; i++ {
		idx := start + i
		entry := ec.entries[idx]
		if entry.Valid && entry.Key == key {
			ec.entries[idx] = EvalCacheEntry{
				Key:         key,
				Value:       value,
				GenWritten:  ec.gen,
				GenLastUsed: ec.gen,
				Valid:       true,
			}
			return
		}
		if !entry.Valid {
			victim = idx
			break
		}
		age := ec.gen - entry.GenLastUsed
		if victim == -1 || age > oldestAge {
			victim = idx
			oldestAge = age
		}
	}
	if victim >= 0 {
		ec.entries[victim] = EvalCacheEntry{
			Key:         key,
			Value:       value,
			GenWritten:  ec.gen,
			GenLastUsed: ec.gen,
			Valid:       true,
		}
	}
}

func (ec *EvalCache) Clear() {
	if ec == nil {
		return
	}
	ec.mu.Lock()
	defer ec.mu.Unlock()
	for i := range ec.entries {
		ec.entries[i] = EvalCacheEntry{}
	}
	ec.gen = 1
}

func selectCache(ctx minimaxContext) *AISearchCache {
	if ctx.settings.Cache != nil {
		return ctx.settings.Cache
	}
	return SharedSearchCache()
}

var (
	defaultCache      = newAISearchCache()
	defaultCacheMutex sync.Mutex
)

func SharedSearchCache() *AISearchCache {
	return &defaultCache
}

func lockDefaultCache() func() {
	defaultCacheMutex.Lock()
	return defaultCacheMutex.Unlock
}

func FlushGlobalCaches() {
	unlock := lockDefaultCache()
	defer unlock()
	defaultCache.mu.Lock()
	tt := defaultCache.TT
	evalCache := defaultCache.EvalCache
	defaultCache.EvalCacheSize = 0
	defaultCache.mu.Unlock()
	if tt != nil {
		tt.Clear()
	}
	if evalCache != nil {
		evalCache.Clear()
	}
}

func ensureTT(cache *AISearchCache, config Config) *TranspositionTable {
	if cache == nil {
		return nil
	}
	if config.AiTtSize <= 0 {
		config.AiTtSize = int(config.AiTtMaxEntries)
	}
	buckets := config.AiTtBuckets
	if !config.AiTtUseSetAssoc {
		buckets = 1
	}
	if buckets <= 0 {
		buckets = 2
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.TT == nil || cache.TTSize != config.AiTtSize || cache.TTBuckets != buckets {
		cache.TT = NewTranspositionTable(uint64(config.AiTtSize), buckets)
		cache.TTSize = config.AiTtSize
		cache.TTBuckets = buckets
	}
	return cache.TT
}

func ensureEvalCache(cache *AISearchCache, config Config) *EvalCache {
	if cache == nil {
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !config.AiEnableEvalCache {
		cache.EvalCache = nil
		cache.EvalCacheSize = 0
		return nil
	}
	size := config.AiEvalCacheSize
	if size <= 0 {
		size = 1 << 18
	}
	if cache.EvalCache == nil || cache.EvalCacheSize != size {
		cache.EvalCache = NewEvalCache(uint64(size), 2)
		cache.EvalCacheSize = size
	}
	return cache.EvalCache
}

func playerCell(player PlayerColor) Cell {
	return CellFromPlayer(player)
}

type candidateMove struct {
	move     Move
	priority int
}

const (
	prioWin          = 0
	prioBlockWin     = 1
	prioCreateFour   = 2
	prioBlockFour    = 3
	prioCreateOpen3  = 4
	prioBlockOpen3   = 5
	prioLastMove     = 10
	prioProximity    = 20
	prioDefault      = 50
	maxCandidatePrio = 100
	proximityRadius  = 2
	lastMoveRadius   = 3
)

type boardBBox struct {
	minX, maxX int
	minY, maxY int
	width      int
	height     int
	spread     int
	stones     int
}

func computeBBox(board Board, boardSize int) boardBBox {
	bbox := boardBBox{
		minX:   boardSize,
		maxX:   -1,
		minY:   boardSize,
		maxY:   -1,
		width:  0,
		height: 0,
		spread: 0,
		stones: 0,
	}
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			bbox.stones++
			if x < bbox.minX {
				bbox.minX = x
			}
			if x > bbox.maxX {
				bbox.maxX = x
			}
			if y < bbox.minY {
				bbox.minY = y
			}
			if y > bbox.maxY {
				bbox.maxY = y
			}
		}
	}
	if bbox.stones == 0 {
		return bbox
	}
	bbox.width = bbox.maxX - bbox.minX + 1
	bbox.height = bbox.maxY - bbox.minY + 1
	if bbox.width > bbox.height {
		bbox.spread = bbox.width
	} else {
		bbox.spread = bbox.height
	}
	return bbox
}

func computeDensity(stones, width, height int) float64 {
	if stones <= 0 || width <= 0 || height <= 0 {
		return 0.0
	}
	return float64(stones) / float64(width*height)
}

func logAITask(ctx minimaxContext, indent int, format string, args ...interface{}) {
	if !ctx.settings.Config.AiLogSearchStats {
		return
	}
	prefix := strings.Repeat("  ", indent)
	fmt.Printf("[ai:trace] %s%s\n", prefix, fmt.Sprintf(format, args...))
}

func logPrune(ctx minimaxContext, depth int, move Move, best, alpha, beta float64) {
	if !ctx.settings.Config.AiLogSearchStats {
		return
	}
	prefix := strings.Repeat("  ", ctx.logIndent+1)
	fmt.Printf("[ai:prune] %sdepth=%d move=(%d,%d) best=%.2f alpha=%.2f beta=%.2f\n", prefix, depth, move.X, move.Y, best, alpha, beta)
}

func computeAvgDistToCenter(board Board, boardSize int) float64 {
	bbox := computeBBox(board, boardSize)
	if bbox.stones == 0 {
		return 0.0
	}
	center := boardSize / 2
	total := 0
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			dx := x - center
			if dx < 0 {
				dx = -dx
			}
			dy := y - center
			if dy < 0 {
				dy = -dy
			}
			if dx > dy {
				total += dx
			} else {
				total += dy
			}
		}
	}
	return float64(total) / float64(bbox.stones)
}

func countContiguous(board Board, x, y, dx, dy int, target Cell) int {
	count := 0
	nx := x + dx
	ny := y + dy
	for board.InBounds(nx, ny) && board.At(nx, ny) == target {
		count++
		nx += dx
		ny += dy
	}
	return count
}

func chebDist(dx, dy int) int {
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dy > dx {
		return dy
	}
	return dx
}

func threatFlagsForMove(board Board, move Move, target Cell) (winNow bool, createFour bool, openThree bool) {
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for _, dir := range directions {
		dx := dir[0]
		dy := dir[1]
		left := countContiguous(board, move.X, move.Y, -dx, -dy, target)
		right := countContiguous(board, move.X, move.Y, dx, dy, target)
		total := left + right + 1
		if total >= 5 {
			winNow = true
			continue
		}
		if total == 4 {
			createFour = true
			continue
		}
		if total == 3 {
			leftX := move.X - (left+1)*dx
			leftY := move.Y - (left+1)*dy
			rightX := move.X + (right+1)*dx
			rightY := move.Y + (right+1)*dy
			openLeft := board.InBounds(leftX, leftY) && board.At(leftX, leftY) == CellEmpty
			openRight := board.InBounds(rightX, rightY) && board.At(rightX, rightY) == CellEmpty
			if openLeft && openRight {
				openThree = true
			}
		}
	}
	return winNow, createFour, openThree
}

func generateThreatMoves(board Board, boardSize int, toPlay PlayerColor) ([]candidateMove, bool) {
	threats := make([]candidateMove, 0, 32)
	cellCount := boardSize * boardSize
	var seenPriorityStack [maxSearchBoardCells]int
	seenPriority := seenPriorityStack[:0]
	if cellCount <= len(seenPriorityStack) {
		seenPriority = seenPriorityStack[:cellCount]
	} else {
		seenPriority = make([]int, cellCount)
	}
	for i := range seenPriority {
		seenPriority[i] = maxCandidatePrio
	}
	toPlayCell := CellFromPlayer(toPlay)
	oppCell := CellFromPlayer(otherPlayer(toPlay))
	urgent := false
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) != CellEmpty {
				continue
			}
			move := Move{X: x, Y: y}
			bestPrio := maxCandidatePrio

			winNow, createFour, openThree := threatFlagsForMove(board, move, toPlayCell)
			if winNow {
				bestPrio = prioWin
				urgent = true
			} else if createFour {
				if prioCreateFour < bestPrio {
					bestPrio = prioCreateFour
				}
				urgent = true
			} else if openThree {
				if prioCreateOpen3 < bestPrio {
					bestPrio = prioCreateOpen3
				}
			}

			winNow, createFour, openThree = threatFlagsForMove(board, move, oppCell)
			if winNow {
				if prioBlockWin < bestPrio {
					bestPrio = prioBlockWin
				}
				urgent = true
			} else if createFour {
				if prioBlockFour < bestPrio {
					bestPrio = prioBlockFour
				}
				urgent = true
			} else if openThree {
				if prioBlockOpen3 < bestPrio {
					bestPrio = prioBlockOpen3
				}
			}

			if bestPrio == maxCandidatePrio {
				continue
			}
			idx := y*boardSize + x
			if bestPrio < seenPriority[idx] {
				seenPriority[idx] = bestPrio
				threats = append(threats, candidateMove{move: move, priority: bestPrio})
			}
		}
	}
	return threats, urgent
}

func hasUrgentThreat(board Board, boardSize int, toPlay PlayerColor) bool {
	_, urgent := generateThreatMoves(board, boardSize, toPlay)
	return urgent
}

func collectCandidateMoves(state GameState, currentPlayer PlayerColor, boardSize int) []candidateMove {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	board := state.Board
	bbox := computeBBox(board, boardSize)
	if bbox.stones == 0 {
		center := boardSize / 2
		return []candidateMove{{move: Move{X: center, Y: center}, priority: prioDefault}}
	}
	if bbox.stones == 1 {
		moves := []candidateMove{}
		cellCount := boardSize * boardSize
		var seenStack [maxSearchBoardCells]bool
		seen := seenStack[:0]
		if cellCount <= len(seenStack) {
			seen = seenStack[:cellCount]
		} else {
			seen = make([]bool, cellCount)
		}
		for y := 0; y < boardSize; y++ {
			for x := 0; x < boardSize; x++ {
				if board.At(x, y) == CellEmpty {
					continue
				}
				for dy := -proximityRadius; dy <= proximityRadius; dy++ {
					for dx := -proximityRadius; dx <= proximityRadius; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						if chebDist(dx, dy) > proximityRadius {
							continue
						}
						nx := x + dx
						ny := y + dy
						if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
							continue
						}
						idx := ny*boardSize + nx
						if !seen[idx] {
							seen[idx] = true
							moves = append(moves, candidateMove{move: Move{X: nx, Y: ny}, priority: prioProximity})
						}
					}
				}
				return moves
			}
		}
	}

	threatMoves, urgent := generateThreatMoves(board, boardSize, currentPlayer)
	density := computeDensity(bbox.stones, bbox.width, bbox.height)
	margin := 2
	if density < 0.15 {
		margin++
	}
	if urgent {
		margin++
	}
	if margin > 4 {
		margin = 4
	}
	x0 := bbox.minX - margin
	y0 := bbox.minY - margin
	x1 := bbox.maxX + margin
	y1 := bbox.maxY + margin
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= boardSize {
		x1 = boardSize - 1
	}
	if y1 >= boardSize {
		y1 = boardSize - 1
	}

	cellCount := boardSize * boardSize
	var seenPriorityStack [maxSearchBoardCells]int
	seenPriority := seenPriorityStack[:0]
	if cellCount <= len(seenPriorityStack) {
		seenPriority = seenPriorityStack[:cellCount]
	} else {
		seenPriority = make([]int, cellCount)
	}
	for i := range seenPriority {
		seenPriority[i] = maxCandidatePrio
	}
	candidates := make([]candidateMove, 0, 64)
	addCandidate := func(move Move, priority int) {
		idx := move.Y*boardSize + move.X
		if priority < seenPriority[idx] {
			seenPriority[idx] = priority
			candidates = append(candidates, candidateMove{move: move, priority: priority})
		}
	}

	for _, threat := range threatMoves {
		addCandidate(threat.move, threat.priority)
	}

	if state.HasLastMove {
		lm := state.LastMove
		for dy := -lastMoveRadius; dy <= lastMoveRadius; dy++ {
			for dx := -lastMoveRadius; dx <= lastMoveRadius; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				if chebDist(dx, dy) > lastMoveRadius {
					continue
				}
				nx := lm.X + dx
				ny := lm.Y + dy
				if nx < x0 || nx > x1 || ny < y0 || ny > y1 {
					continue
				}
				if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
					continue
				}
				addCandidate(Move{X: nx, Y: ny}, prioLastMove)
			}
		}
	}

	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			for dy := -proximityRadius; dy <= proximityRadius; dy++ {
				for dx := -proximityRadius; dx <= proximityRadius; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					if chebDist(dx, dy) > proximityRadius {
						continue
					}
					nx := x + dx
					ny := y + dy
					if nx < x0 || nx > x1 || ny < y0 || ny > y1 {
						continue
					}
					if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
						continue
					}
					addCandidate(Move{X: nx, Y: ny}, prioProximity)
				}
			}
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		if candidates[i].move.Y != candidates[j].move.Y {
			return candidates[i].move.Y < candidates[j].move.Y
		}
		return candidates[i].move.X < candidates[j].move.X
	})
	return candidates
}

func hardPlyCandidateCap(config Config, depthFromRoot int) int {
	switch {
	case depthFromRoot >= 9:
		if config.AiMaxCandidatesPly9 > 0 {
			return config.AiMaxCandidatesPly9
		}
		return 8
	case depthFromRoot == 8:
		if config.AiMaxCandidatesPly8 > 0 {
			return config.AiMaxCandidatesPly8
		}
		return 12
	case depthFromRoot == 7:
		if config.AiMaxCandidatesPly7 > 0 {
			return config.AiMaxCandidatesPly7
		}
		return 16
	default:
		if config.AiMaxCandidatesRoot > 0 {
			return config.AiMaxCandidatesRoot
		}
		return 24
	}
}

func tacticalKLimit(config Config, depthFromRoot int) int {
	if depthFromRoot == 0 && config.AiKTactRoot > 0 {
		return config.AiKTactRoot
	}
	if depthFromRoot <= 2 && config.AiKTactMid > 0 {
		return config.AiKTactMid
	}
	if config.AiKTactDeep > 0 {
		return config.AiKTactDeep
	}
	return 0
}

func candidateLimit(ctx minimaxContext, depthLeft, depthFromRoot int, tactical bool) int {
	config := ctx.settings.Config
	if config.AiEnableHardPlyCaps {
		limit := hardPlyCandidateCap(config, depthFromRoot)
		if config.AiEnableTacticalK && tactical {
			if tacticalLimit := tacticalKLimit(config, depthFromRoot); tacticalLimit > 0 && tacticalLimit < limit {
				limit = tacticalLimit
			}
		}
		return limit
	}

	limit := 0
	if config.AiEnableTacticalK && tactical {
		limit = tacticalKLimit(config, depthFromRoot)
	} else if !config.AiEnableDynamicTopK {
		limit = config.AiTopCandidates
	} else if config.AiEnableTacticalK {
		if depthFromRoot == 0 && config.AiKQuietRoot > 0 {
			limit = config.AiKQuietRoot
		} else if depthFromRoot <= 2 && config.AiKQuietMid > 0 {
			limit = config.AiKQuietMid
		} else if config.AiKQuietDeep > 0 {
			limit = config.AiKQuietDeep
		}
	} else if depthFromRoot == 0 && config.AiMaxCandidatesRoot > 0 {
		limit = config.AiMaxCandidatesRoot
	} else if depthLeft >= 3 && config.AiMaxCandidatesDeep > 0 {
		limit = config.AiMaxCandidatesDeep
	} else if config.AiMaxCandidatesMid > 0 {
		limit = config.AiMaxCandidatesMid
	} else if config.AiTopCandidates > 0 {
		limit = config.AiTopCandidates
	}

	if depthFromRoot >= 9 && config.AiMaxCandidatesPly9 > 0 {
		if limit <= 0 || config.AiMaxCandidatesPly9 < limit {
			limit = config.AiMaxCandidatesPly9
		}
	} else if depthFromRoot >= 8 && config.AiMaxCandidatesPly8 > 0 {
		if limit <= 0 || config.AiMaxCandidatesPly8 < limit {
			limit = config.AiMaxCandidatesPly8
		}
	} else if depthFromRoot >= 7 && config.AiMaxCandidatesPly7 > 0 {
		if limit <= 0 || config.AiMaxCandidatesPly7 < limit {
			limit = config.AiMaxCandidatesPly7
		}
	}
	return limit
}

func defensiveTacticalCandidates(candidates []candidateMove) []candidateMove {
	filtered := make([]candidateMove, 0, len(candidates))
	for _, cand := range candidates {
		switch cand.priority {
		case prioWin, prioBlockWin, prioBlockFour:
			filtered = append(filtered, cand)
		}
	}
	return filtered
}

func applyCandidateCap(candidates []Move, limit int) []Move {
	if limit <= 0 || len(candidates) <= limit {
		return candidates
	}
	return candidates[:limit]
}

func isKillerMove(ctx minimaxContext, depthFromRoot int, move Move) bool {
	if depthFromRoot < 0 || depthFromRoot >= len(ctx.killers) {
		return false
	}
	for _, km := range ctx.killers[depthFromRoot] {
		if km.Equals(move) {
			return true
		}
	}
	return false
}

func recordKiller(ctx minimaxContext, depthFromRoot int, move Move) {
	if depthFromRoot < 0 || depthFromRoot >= len(ctx.killers) {
		return
	}
	killers := ctx.killers[depthFromRoot]
	if len(killers) == 0 {
		ctx.killers[depthFromRoot] = []Move{move}
		return
	}
	if killers[0].Equals(move) {
		return
	}
	if len(killers) == 1 {
		ctx.killers[depthFromRoot] = []Move{killers[0], move}
		return
	}
	ctx.killers[depthFromRoot] = []Move{move, killers[0]}
}

func recordHistory(ctx minimaxContext, boardSize int, move Move, depthLeft int) {
	if len(ctx.history) == 0 || boardSize <= 0 {
		return
	}
	idx := move.Y*boardSize + move.X
	if idx < 0 || idx >= len(ctx.history) {
		return
	}
	bonus := depthLeft * depthLeft
	ctx.history[idx] += bonus
}

func orderCandidateMoves(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, candidates []candidateMove, maxCandidates int, pvMove *Move) []Move {
	evalSettings := ctx.settings
	evalSettings.Player = currentPlayer
	// Full move simulation + eval for ordering is expensive; keep it to shallow nodes.
	useExpensiveOrdering := depthFromRoot <= 2
	type scoredMove struct {
		score    float64
		priority int
		move     Move
	}
	scored := make([]scoredMove, 0, len(candidates))
	cache := selectCache(ctx)
	opponentHasImmediateWin := false
	if useExpensiveOrdering {
		opponentHasImmediateWin = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)
	}
	for _, cand := range candidates {
		move := cand.move
		priority := cand.priority
		score := 0.0
		if useExpensiveOrdering {
			if isImmediateWinCached(cache, state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
				if prioWin < priority {
					priority = prioWin
				}
			} else if opponentHasImmediateWin {
				blockState := state
				var undo searchMoveUndo
				if applyMoveWithUndo(&blockState, ctx.rules, move, currentPlayer, &undo) {
					if !hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config) {
						if prioBlockWin < priority {
							priority = prioBlockWin
						}
					}
					undoMoveWithUndo(&blockState, undo)
				}
			}
			score = heuristicForMove(state, ctx.rules, evalSettings, move)
		}
		if ctx.settings.Config.AiEnableKillerMoves && isKillerMove(ctx, depthFromRoot, move) {
			boost := float64(ctx.settings.Config.AiKillerBoost)
			if maximizing {
				score += boost
			} else {
				score -= boost
			}
		}
		if ctx.settings.Config.AiEnableHistoryMoves && len(ctx.history) > 0 {
			idx := move.Y*ctx.settings.BoardSize + move.X
			if idx >= 0 && idx < len(ctx.history) {
				boost := float64(ctx.history[idx] * ctx.settings.Config.AiHistoryBoost)
				if maximizing {
					score += boost
				} else {
					score -= boost
				}
			}
		}
		scored = append(scored, scoredMove{score: score, priority: priority, move: move})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].priority != scored[j].priority {
			return scored[i].priority < scored[j].priority
		}
		if maximizing {
			return scored[i].score > scored[j].score
		}
		return scored[i].score < scored[j].score
	})
	if pvMove != nil {
		for i := range scored {
			if scored[i].move.Equals(*pvMove) {
				pvEntry := scored[i]
				copy(scored[i:], scored[i+1:])
				scored = scored[:len(scored)-1]
				scored = append([]scoredMove{pvEntry}, scored...)
				break
			}
		}
	}
	if maxCandidates > 0 && len(scored) > maxCandidates {
		scored = scored[:maxCandidates]
	}
	moves := make([]Move, 0, len(scored))
	for _, entry := range scored {
		moves = append(moves, entry.move)
	}
	return moves
}

func orderCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, maxCandidates int, pvMove *Move) []Move {
	candidates := collectCandidateMoves(state, currentPlayer, ctx.settings.BoardSize)
	return orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, candidates, maxCandidates, pvMove)
}

func orderMovesFromList(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, moves []Move, pvMove *Move, priority int) []Move {
	candidates := make([]candidateMove, 0, len(moves))
	for _, move := range moves {
		candidates = append(candidates, candidateMove{move: move, priority: priority})
	}
	return orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, candidates, 0, pvMove)
}

func isTacticalPosition(state GameState, ctx minimaxContext, currentPlayer PlayerColor) bool {
	cache := selectCache(ctx)
	if len(findImmediateWinMovesCached(cache, state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)) > 0 {
		return true
	}
	if len(findImmediateWinMovesCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)) > 0 {
		return true
	}
	_, urgent := generateThreatMoves(state.Board, ctx.settings.BoardSize, currentPlayer)
	return urgent
}

func tacticalCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor) []candidateMove {
	cache := selectCache(ctx)
	boardSize := ctx.settings.BoardSize
	cellCount := boardSize * boardSize
	var seenPriorityStack [maxSearchBoardCells]int
	seenPriority := seenPriorityStack[:0]
	if cellCount <= len(seenPriorityStack) {
		seenPriority = seenPriorityStack[:cellCount]
	} else {
		seenPriority = make([]int, cellCount)
	}
	for i := range seenPriority {
		seenPriority[i] = maxCandidatePrio
	}

	addMove := func(move Move, prio int) {
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(seenPriority) {
			return
		}
		if prio < seenPriority[idx] {
			seenPriority[idx] = prio
		}
	}

	for _, move := range findImmediateWinMovesCached(cache, state, ctx.rules, currentPlayer, boardSize, ctx.settings.Config) {
		addMove(move, prioWin)
	}
	for _, move := range findImmediateWinMovesCached(cache, state, ctx.rules, otherPlayer(currentPlayer), boardSize, ctx.settings.Config) {
		addMove(move, prioBlockWin)
	}

	threatMoves, _ := generateThreatMoves(state.Board, boardSize, currentPlayer)
	for _, cand := range threatMoves {
		switch cand.priority {
		case prioCreateFour, prioBlockFour:
			addMove(cand.move, cand.priority)
		}
	}
	hasSeen := false
	for i := range seenPriority {
		if seenPriority[i] != maxCandidatePrio {
			hasSeen = true
			break
		}
	}
	if !hasSeen {
		for _, cand := range threatMoves {
			switch cand.priority {
			case prioCreateOpen3, prioBlockOpen3:
				addMove(cand.move, cand.priority)
			}
		}
	}

	moves := make([]candidateMove, 0, 16)
	for idx, prio := range seenPriority {
		if prio == maxCandidatePrio {
			continue
		}
		move := Move{X: idx % boardSize, Y: idx / boardSize}
		if ok, _ := ctx.rules.IsLegal(state, move, currentPlayer); ok {
			moves = append(moves, candidateMove{move: move, priority: prio})
		}
	}
	return moves
}

func hasStoneWithin(board Board, boardSize int) bool {
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) != CellEmpty {
				return true
			}
		}
	}
	return false
}

func evalKey(stateHash uint64, boardSize int, player PlayerColor) uint64 {
	return stateHash ^ mixKey(uint64(boardSize)<<32|uint64(player))
}

func evalBoardCached(state GameState, rules Rules, settings AIScoreSettings, cache *AISearchCache) float64 {
	_ = rules
	board := state.Board
	if settings.SkipQueueBacklog || !settings.Config.AiEnableEvalCache {
		return EvaluateBoard(board, PlayerBlack, settings.Config)
	}
	evalCache := ensureEvalCache(cache, settings.Config)
	stateHash := state.Hash
	if evalCache != nil {
		if settings.Stats != nil {
			settings.Stats.EvalCacheProbes++
		}
		if stateHash != 0 {
			if value, ok := evalCache.Get(evalKey(stateHash, settings.BoardSize, state.ToMove)); ok {
				if settings.Stats != nil {
					settings.Stats.EvalCacheHits++
				}
				return value
			}
		}
	}
	sampleEvalTiming := false
	if stats := settings.Stats; stats != nil {
		nextCall := stats.HeuristicCalls + 1
		sampleEvalTiming = (nextCall & searchTimingSampleMask) == 0
	}
	var evalStart time.Time
	if sampleEvalTiming {
		evalStart = time.Now()
	}
	value := EvaluateBoard(board, PlayerBlack, settings.Config)
	if stats := settings.Stats; stats != nil {
		stats.HeuristicCalls++
		if sampleEvalTiming {
			stats.HeuristicTime += time.Since(evalStart)
		}
	}
	if evalCache != nil && stateHash != 0 {
		if math.Abs(value) >= settings.Config.AiEvalCacheMinAbs {
			evalCache.Put(evalKey(stateHash, settings.BoardSize, state.ToMove), value)
		}
	}
	return value
}

func heuristicForMove(state GameState, rules Rules, settings AIScoreSettings, move Move) float64 {
	if ok, _ := rules.IsLegal(state, move, settings.Player); !ok {
		return illegalScore
	}
	next := state
	var undo searchMoveUndo
	if !applyMoveWithUndo(&next, rules, move, settings.Player, &undo) {
		return illegalScore
	}
	cache := selectCache(minimaxContext{settings: settings})
	score := evalBoardCached(next, rules, settings, cache)
	undoMoveWithUndo(&next, undo)
	return score
}

func evaluateStateHeuristic(state GameState, rules Rules, settings AIScoreSettings) float64 {
	switch state.Status {
	case StatusDraw:
		return 0.0
	case StatusBlackWon:
		return winScore
	case StatusWhiteWon:
		return -winScore
	}
	cache := selectCache(minimaxContext{settings: settings})
	return evalBoardCached(state, rules, settings, cache)
}

func tacticalExtensionScore(state GameState, ctx minimaxContext, currentPlayer PlayerColor, depthFromRoot int) float64 {
	candidates := tacticalCandidates(state, ctx, currentPlayer)
	if len(candidates) == 0 {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	maximizing := currentPlayer == PlayerBlack
	best := math.Inf(-1)
	if !maximizing {
		best = math.Inf(1)
	}
	for _, cand := range candidates {
		move := cand.move
		if timedOut(ctx) {
			break
		}
		next := state
		var undo searchMoveUndo
		if !applyMoveWithUndo(&next, ctx.rules, move, currentPlayer, &undo) {
			continue
		}
		score := evaluateStateHeuristic(next, ctx.rules, ctx.settings)
		undoMoveWithUndo(&next, undo)
		if maximizing {
			if score > best {
				best = score
			}
		} else {
			if score < best {
				best = score
			}
		}
	}
	if math.IsInf(best, 1) || math.IsInf(best, -1) {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	return best
}

func timedOut(ctx minimaxContext) bool {
	if ctx.settings.ShouldStop != nil && ctx.settings.ShouldStop() {
		return true
	}
	if ctx.hasDeadline && !ctx.deadline.IsZero() && time.Now().After(ctx.deadline) {
		return true
	}
	if ctx.settings.TimeoutMs <= 0 {
		return false
	}
	elapsed := time.Since(ctx.start).Milliseconds()
	return int(elapsed) >= ctx.settings.TimeoutMs
}

func initOrderingTables(settings AIScoreSettings) ([][]Move, []int) {
	var killers [][]Move
	var history []int
	if settings.Config.AiEnableKillerMoves {
		killers = make([][]Move, settings.Depth+2)
	}
	if settings.Config.AiEnableHistoryMoves {
		history = make([]int, settings.BoardSize*settings.BoardSize)
	}
	return killers, history
}

func newMinimaxContext(rules Rules, settings AIScoreSettings, start time.Time) minimaxContext {
	killers, history := initOrderingTables(settings)
	ctx := minimaxContext{
		rules:     rules,
		settings:  settings,
		start:     start,
		killers:   killers,
		history:   history,
		logIndent: 0,
	}
	if settings.Config.AiTimeBudgetMs > 0 {
		ctx.deadline = start.Add(time.Duration(settings.Config.AiTimeBudgetMs) * time.Millisecond)
		ctx.hasDeadline = true
	}
	return ctx
}

type searchMoveUndo struct {
	move              Move
	player            PlayerColor
	captures          [8]Move
	captureCount      int
	prevStatus        GameStatus
	prevToMove        PlayerColor
	prevHasLastMove   bool
	prevLastMove      Move
	prevLastMessage   string
	prevCapturedBlack int
	prevCapturedWhite int
	prevHash          uint64
	prevHashSym       [8]uint64
	prevCanonHash     uint64
}

func applyMove(state *GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return false
	}
	prevCapturedBlack := state.CapturedBlack
	prevCapturedWhite := state.CapturedWhite
	prevToMove := state.ToMove
	cell := playerCell(player)
	state.Board.Set(move.X, move.Y, cell)
	state.LastMove = move
	state.HasLastMove = true
	state.LastMessage = ""

	var captureBuf [8]Move
	captures := rules.FindCapturesInto(state.Board, move, cell, captureBuf[:0])
	for _, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
	}
	if len(captures) > 0 {
		capturedCount := len(captures)
		if player == PlayerBlack {
			state.CapturedBlack += capturedCount
		} else {
			state.CapturedWhite += capturedCount
		}
	}

	totalCaptured := state.CapturedBlack
	if player == PlayerWhite {
		totalCaptured = state.CapturedWhite
	}
	if totalCaptured >= rules.CaptureWinStones() {
		if player == PlayerBlack {
			state.Status = StatusBlackWon
		} else {
			state.Status = StatusWhiteWon
		}
	} else if rules.IsWin(state.Board, move) {
		if player == PlayerBlack {
			state.Status = StatusBlackWon
		} else {
			state.Status = StatusWhiteWon
		}
	} else if rules.IsDraw(state.Board) {
		state.Status = StatusDraw
	} else {
		state.Status = StatusRunning
	}

	state.ToMove = otherPlayer(player)
	UpdateHashAfterMove(state, move, player, captures, prevToMove, prevCapturedBlack, prevCapturedWhite)
	return true
}

func applyMoveWithUndo(state *GameState, rules Rules, move Move, player PlayerColor, undo *searchMoveUndo) bool {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return false
	}
	prevCapturedBlack := state.CapturedBlack
	prevCapturedWhite := state.CapturedWhite
	prevToMove := state.ToMove
	if undo != nil {
		undo.move = move
		undo.player = player
		undo.captureCount = 0
		undo.prevStatus = state.Status
		undo.prevToMove = state.ToMove
		undo.prevHasLastMove = state.HasLastMove
		undo.prevLastMove = state.LastMove
		undo.prevLastMessage = state.LastMessage
		undo.prevCapturedBlack = state.CapturedBlack
		undo.prevCapturedWhite = state.CapturedWhite
		undo.prevHash = state.Hash
		undo.prevHashSym = state.HashSym
		undo.prevCanonHash = state.CanonHash
	}
	cell := playerCell(player)
	state.Board.Set(move.X, move.Y, cell)
	state.LastMove = move
	state.HasLastMove = true
	state.LastMessage = ""

	var captureBuf []Move
	if undo != nil {
		captureBuf = undo.captures[:0]
	}
	captures := rules.FindCapturesInto(state.Board, move, cell, captureBuf)
	for i, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
		if undo != nil && i < len(undo.captures) {
			undo.captures[i] = captured
		}
	}
	if undo != nil {
		undo.captureCount = len(captures)
	}
	if len(captures) > 0 {
		capturedCount := len(captures)
		if player == PlayerBlack {
			state.CapturedBlack += capturedCount
		} else {
			state.CapturedWhite += capturedCount
		}
	}

	totalCaptured := state.CapturedBlack
	if player == PlayerWhite {
		totalCaptured = state.CapturedWhite
	}
	if totalCaptured >= rules.CaptureWinStones() {
		if player == PlayerBlack {
			state.Status = StatusBlackWon
		} else {
			state.Status = StatusWhiteWon
		}
	} else if rules.IsWin(state.Board, move) {
		if player == PlayerBlack {
			state.Status = StatusBlackWon
		} else {
			state.Status = StatusWhiteWon
		}
	} else if rules.IsDraw(state.Board) {
		state.Status = StatusDraw
	} else {
		state.Status = StatusRunning
	}

	state.ToMove = otherPlayer(player)
	UpdateHashAfterMove(state, move, player, captures, prevToMove, prevCapturedBlack, prevCapturedWhite)
	return true
}

func undoMoveWithUndo(state *GameState, undo searchMoveUndo) {
	state.Board.Remove(undo.move.X, undo.move.Y)
	capturedCell := playerCell(otherPlayer(undo.player))
	for i := 0; i < undo.captureCount; i++ {
		captured := undo.captures[i]
		state.Board.Set(captured.X, captured.Y, capturedCell)
	}
	state.Status = undo.prevStatus
	state.ToMove = undo.prevToMove
	state.HasLastMove = undo.prevHasLastMove
	state.LastMove = undo.prevLastMove
	state.LastMessage = undo.prevLastMessage
	state.CapturedBlack = undo.prevCapturedBlack
	state.CapturedWhite = undo.prevCapturedWhite
	state.Hash = undo.prevHash
	state.HashSym = undo.prevHashSym
	state.CanonHash = undo.prevCanonHash
}

func shouldApplyLMR(depth int, moveIndex int, quietNode bool) bool {
	if !quietNode {
		return false
	}
	if depth < lmrMinDepth {
		return false
	}
	return moveIndex >= lmrLateMoveStart
}

func isImmediateWin(state GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(state, move, player); !ok {
		return false
	}
	board := state.Board
	cell := playerCell(player)
	board.Set(move.X, move.Y, cell)
	defer board.Remove(move.X, move.Y)
	var captureBuf [8]Move
	captures := rules.FindCapturesInto(board, move, cell, captureBuf[:0])
	capturedCount := len(captures)
	totalCaptured := state.CapturedBlack
	if player == PlayerWhite {
		totalCaptured = state.CapturedWhite
	}
	totalCaptured += capturedCount
	if totalCaptured >= rules.CaptureWinStones() {
		return true
	}
	return rules.IsWin(board, move)
}

func isImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, move Move, player PlayerColor, boardSize int) bool {
	_ = cache
	_ = boardSize
	return isImmediateWin(state, rules, move, player)
}

func findAlignmentWinMoves(board Board, player PlayerColor, winLen int) []Move {
	if winLen <= 0 {
		winLen = 5
	}
	size := board.Size()
	cellCount := size * size
	var seenStack [maxSearchBoardCells]bool
	seen := seenStack[:0]
	if cellCount <= len(seenStack) {
		seen = seenStack[:cellCount]
	} else {
		seen = make([]bool, cellCount)
	}
	moves := make([]Move, 0, 8)
	cell := CellFromPlayer(player)
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !board.IsEmpty(x, y) {
				continue
			}
			for _, dir := range directions {
				left := countContiguous(board, x, y, -dir[0], -dir[1], cell)
				right := countContiguous(board, x, y, dir[0], dir[1], cell)
				if left+right+1 >= winLen {
					idx := y*size + x
					if !seen[idx] {
						seen[idx] = true
						moves = append(moves, Move{X: x, Y: y})
					}
					break
				}
			}
		}
	}
	return moves
}

func wouldCapture(board Board, move Move, playerCell, opponentCell Cell) bool {
	directions := [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}}
	for i := 0; i < 8; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		x1 := move.X + dx
		y1 := move.Y + dy
		x2 := move.X + 2*dx
		y2 := move.Y + 2*dy
		x3 := move.X + 3*dx
		y3 := move.Y + 3*dy
		if !board.InBounds(x3, y3) || !board.InBounds(x2, y2) || !board.InBounds(x1, y1) {
			continue
		}
		if board.At(x1, y1) == opponentCell && board.At(x2, y2) == opponentCell && board.At(x3, y3) == playerCell {
			return true
		}
	}
	return false
}

func findCaptureWinMoves(state GameState, rules Rules, player PlayerColor) []Move {
	remaining := rules.CaptureWinStones()
	if player == PlayerBlack {
		remaining -= state.CapturedBlack
	} else {
		remaining -= state.CapturedWhite
	}
	if remaining > 2 {
		return nil
	}
	board := state.Board
	size := board.Size()
	cellCount := size * size
	var seenStack [maxSearchBoardCells]bool
	seen := seenStack[:0]
	if cellCount <= len(seenStack) {
		seen = seenStack[:cellCount]
	} else {
		seen = make([]bool, cellCount)
	}
	moves := make([]Move, 0, 8)
	playerCell := CellFromPlayer(player)
	opponentCell := CellFromPlayer(otherPlayer(player))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					if chebDist(dx, dy) > 2 {
						continue
					}
					nx := x + dx
					ny := y + dy
					if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
						continue
					}
					idx := ny*size + nx
					if seen[idx] {
						continue
					}
					seen[idx] = true
					move := Move{X: nx, Y: ny}
					if ok, _ := rules.IsLegal(state, move, player); !ok {
						continue
					}
					if wouldCapture(board, move, playerCell, opponentCell) {
						moves = append(moves, move)
					}
				}
			}
		}
	}
	return moves
}

func findImmediateWinMovesCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) []Move {
	if !config.AiUseScanWinIn1 {
		moves := make([]Move, 0, 4)
		board := state.Board
		for y := 0; y < boardSize; y++ {
			for x := 0; x < boardSize; x++ {
				if !board.IsEmpty(x, y) {
					continue
				}
				move := Move{X: x, Y: y}
				if ok, _ := rules.IsLegal(state, move, player); !ok {
					continue
				}
				if isImmediateWinCached(cache, state, rules, move, player, boardSize) {
					moves = append(moves, move)
				}
			}
		}
		return moves
	}
	alignment := findAlignmentWinMoves(state.Board, player, rules.WinLength())
	capture := findCaptureWinMoves(state, rules, player)
	cellCount := boardSize * boardSize
	var seenStack [maxSearchBoardCells]bool
	seen := seenStack[:0]
	if cellCount <= len(seenStack) {
		seen = seenStack[:cellCount]
	} else {
		seen = make([]bool, cellCount)
	}
	candidates := make([]Move, 0, len(alignment)+len(capture))
	for _, move := range alignment {
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(seen) || seen[idx] {
			continue
		}
		seen[idx] = true
		candidates = append(candidates, move)
	}
	for _, move := range capture {
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(seen) || seen[idx] {
			continue
		}
		seen[idx] = true
		candidates = append(candidates, move)
	}
	moves := make([]Move, 0, len(candidates))
	for _, move := range candidates {
		if ok, _ := rules.IsLegal(state, move, player); !ok {
			continue
		}
		if isImmediateWinCached(cache, state, rules, move, player, boardSize) {
			moves = append(moves, move)
		}
	}
	return moves
}

func findBlockingMoves(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) []Move {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	board := state.Board
	moves := make([]Move, 0, 8)
	probeState := state
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if !board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(probeState, move, player); !ok {
				continue
			}
			var undo searchMoveUndo
			if !applyMoveWithUndo(&probeState, rules, move, player, &undo) {
				continue
			}
			if !hasImmediateWinCached(cache, probeState, rules, otherPlayer(player), boardSize, config) {
				moves = append(moves, move)
			}
			undoMoveWithUndo(&probeState, undo)
		}
	}
	return moves
}

func hasImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) bool {
	_ = cache
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	return len(findImmediateWinMovesCached(cache, state, rules, player, boardSize, config)) > 0
}

func formatMoves(moves []Move) string {
	if len(moves) == 0 {
		return "[]"
	}
	out := make([]byte, 0, len(moves)*8)
	out = append(out, '[')
	for i, m := range moves {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, '(')
		out = append(out, []byte(fmt.Sprintf("%d,%d", m.X, m.Y))...)
		out = append(out, ')')
	}
	out = append(out, ']')
	return string(out)
}

func minimax(state *GameState, ctx minimaxContext, depth int, currentPlayer PlayerColor, depthFromRoot int, alpha, beta float64) float64 {
	logAITask(ctx, ctx.logIndent, "minimax enter depth=%d depthFromRoot=%d", depth, depthFromRoot)
	if timedOut(ctx) || state.Status != StatusRunning {
		return evaluateStateHeuristic(*state, ctx.rules, ctx.settings)
	}
	if depth <= 0 {
		if ctx.settings.Config.AiEnableTacticalExt && ctx.settings.Config.AiTacticalExtDepth > 0 {
			if isTacticalPosition(*state, ctx, currentPlayer) {
				return tacticalExtensionScore(*state, ctx, currentPlayer, depthFromRoot)
			}
		}
		return evaluateStateHeuristic(*state, ctx.rules, ctx.settings)
	}

	if ctx.settings.Stats != nil {
		ctx.settings.Stats.Nodes++
		if ctx.settings.Stats.Nodes == 1 || (ctx.settings.Stats.Nodes&searchProgressChunkMask) == 0 {
			reportSearchProgress(ctx.settings.Stats, ctx.settings)
		}
	}
	cache := selectCache(ctx)
	tt := ensureTT(cache, ctx.settings.Config)
	boardSize := ctx.settings.BoardSize
	boardHash := ttKeyFor(*state, boardSize)
	alphaOrig := alpha
	betaOrig := beta
	var pvMove *Move
	if ctx.settings.Stats != nil {
		ctx.settings.Stats.TTProbes++
	}
	trace := ctx.settings.Config.AiLogSearchStats
	var ttStart time.Time
	if trace {
		ttStart = time.Now()
	}
	if tt != nil {
		if entry, ok := tt.Probe(boardHash); ok {
			if trace {
				ttDuration := time.Since(ttStart).Milliseconds()
				logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms hit=true", depth, ttDuration)
			}
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.TTHits++
				switch entry.Flag {
				case TTExact:
					ctx.settings.Stats.TTExactHits++
				case TTLower:
					ctx.settings.Stats.TTLowerHits++
				case TTUpper:
					ctx.settings.Stats.TTUpperHits++
				}
			}
			if entry.BestMove.IsValid(ctx.settings.BoardSize) {
				pv := entry.BestMove
				pvMove = &pv
			}
			if entry.Depth >= depth {
				logAITask(ctx, ctx.logIndent+1, "TT exact entry depth=%d flag=%d value=%.2f", entry.Depth, entry.Flag, entry.ScoreFloat())
				if _, ret, value := applyTTEntry(entry, depth, &alpha, &beta, ctx.settings.Stats); ret {
					logAITask(ctx, ctx.logIndent+1, "TT exact returning value=%.2f", value)
					return value
				}
			}
		} else {
			if trace {
				ttDuration := time.Since(ttStart).Milliseconds()
				logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms hit=false", depth, ttDuration)
			}
		}
	} else {
		if trace {
			ttDuration := time.Since(ttStart).Milliseconds()
			logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms table=nil", depth, ttDuration)
		}
	}
	logAITask(ctx, ctx.logIndent, "No TT hit; continuing search")

	maximizing := currentPlayer == PlayerBlack
	best := math.Inf(-1)
	if !maximizing {
		best = math.Inf(1)
	}
	secondBest := math.Inf(-1)
	secondBestMove := Move{}
	cache = selectCache(ctx)
	checkForcedLines := depthFromRoot <= 2 || depth <= 2
	var immediateWins []Move
	mustBlock := false
	tactical := false
	opponentUrgent := false
	if checkForcedLines {
		immediateWins = findImmediateWinMovesCached(cache, *state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)
		if len(immediateWins) == 0 {
			mustBlock = hasImmediateWinCached(cache, *state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)
		}
		if ctx.settings.Config.AiEnableTacticalK || ctx.settings.Config.AiEnableTacticalMode || ctx.settings.Config.AiEnableTacticalExt {
			tactical = isTacticalPosition(*state, ctx, currentPlayer)
		}
	} else if ctx.settings.Config.AiEnableTacticalK || ctx.settings.Config.AiEnableTacticalMode {
		tactical = hasUrgentThreat(state.Board, ctx.settings.BoardSize, currentPlayer)
		opponentUrgent = hasUrgentThreat(state.Board, ctx.settings.BoardSize, otherPlayer(currentPlayer))
		tactical = tactical || opponentUrgent
	}
	maxCandidates := candidateLimit(ctx, depth, depthFromRoot, tactical)
	var truncatedCandidates []Move
	var candidates []Move
	if len(immediateWins) > 0 {
		candidates = orderMovesFromList(*state, ctx, currentPlayer, maximizing, depthFromRoot, immediateWins, pvMove, prioWin)
	} else if mustBlock {
		blockMoves := findBlockingMoves(cache, *state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)
		candidates = orderMovesFromList(*state, ctx, currentPlayer, maximizing, depthFromRoot, blockMoves, pvMove, prioBlockWin)
	} else if ctx.settings.Config.AiEnableTacticalMode && tactical {
		tacticalMoves := tacticalCandidates(*state, ctx, currentPlayer)
		if opponentUrgent {
			defensiveMoves := defensiveTacticalCandidates(tacticalMoves)
			if len(defensiveMoves) > 0 {
				candidates = orderCandidateMoves(*state, ctx, currentPlayer, maximizing, depthFromRoot, defensiveMoves, 0, pvMove)
			} else {
				blockMoves := findBlockingMoves(cache, *state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)
				if len(blockMoves) > 0 {
					candidates = orderMovesFromList(*state, ctx, currentPlayer, maximizing, depthFromRoot, blockMoves, pvMove, prioBlockWin)
				} else {
					candidates = orderCandidates(*state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove)
				}
			}
		} else if len(tacticalMoves) > 0 {
			candidates = orderCandidateMoves(*state, ctx, currentPlayer, maximizing, depthFromRoot, tacticalMoves, 0, pvMove)
		} else {
			candidates = orderCandidates(*state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove)
		}
	} else {
		candidates = orderCandidates(*state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove)
	}
	candidates = applyCandidateCap(candidates, maxCandidates)
	if ctx.settings.Config.AiLogSearchStats {
		if mustBlock {
			if ctx.settings.Config.AiTopCandidates > 0 {
				truncatedCandidates = orderCandidates(*state, ctx, currentPlayer, maximizing, depthFromRoot, ctx.settings.Config.AiTopCandidates, pvMove)
			}
			fmt.Printf("[ai:must_block] allowed=%s ordered=%s truncated=%s\n", formatMoves(candidates), formatMoves(candidates), formatMoves(truncatedCandidates))
		} else {
			fmt.Printf("[ai:must_block] opponent_win_in_1_wide=%v\n", mustBlock)
		}
	}
	if ctx.settings.Stats != nil {
		ctx.settings.Stats.CandidateCount += int64(len(candidates))
		if depthFromRoot == 0 {
			ctx.settings.Stats.RootCandidates += int64(len(candidates))
			ctx.settings.Stats.RootSamples++
		} else {
			ctx.settings.Stats.DeepCandidates += int64(len(candidates))
			ctx.settings.Stats.DeepSamples++
		}
	}
	bestMove := Move{}
	quietNode := len(immediateWins) == 0 && !mustBlock && !tactical
	for idx, move := range candidates {
		if timedOut(ctx) {
			break
		}
		if ctx.settings.Config.AiQuickWinExit && isImmediateWinCached(cache, *state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
			win := -winScore
			if currentPlayer == PlayerBlack {
				win = winScore
			}
			if tt != nil {
				replaced, overwrote := tt.Store(boardHash, depth, win, TTExact, move)
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.TTStores++
					if replaced || overwrote {
						ctx.settings.Stats.TTOverwrites++
						ctx.settings.Stats.TTReplacements++
					}
				}
			}
			return win
		}
		searchDepth := depth
		reducedSearch := false
		if shouldApplyLMR(depth, idx, quietNode) {
			searchDepth = depth - lmrReduction
			if searchDepth < 1 {
				searchDepth = 1
			}
			reducedSearch = searchDepth < depth
		}
		value := evaluateMoveWithCache(state, ctx, currentPlayer, move, searchDepth, depthFromRoot, boardHash, nil, alpha, beta)
		if reducedSearch {
			needsResearch := false
			if maximizing {
				needsResearch = value > alpha
			} else {
				needsResearch = value < beta
			}
			if needsResearch {
				value = evaluateMoveWithCache(state, ctx, currentPlayer, move, depth, depthFromRoot, boardHash, nil, alpha, beta)
			}
		}
		if maximizing {
			if value > best {
				secondBest = best
				secondBestMove = bestMove
				best = value
				bestMove = move
			} else if value > secondBest {
				secondBest = value
				secondBestMove = move
			}
			if best > alpha {
				alpha = best
			}
		} else {
			if value < best {
				best = value
				bestMove = move
			}
			if best < beta {
				beta = best
			}
		}
		if beta <= alpha {
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.Cutoffs++
				ctx.settings.Stats.ABCutoffs++
			}
			logPrune(ctx, depth, move, best, alpha, beta)
			if ctx.settings.Config.AiEnableKillerMoves {
				recordKiller(ctx, depthFromRoot, move)
			}
			if ctx.settings.Config.AiEnableHistoryMoves {
				recordHistory(ctx, ctx.settings.BoardSize, move, depth)
			}
			break
		}
		if timedOut(ctx) {
			break
		}
	}

	if math.IsInf(best, 1) || math.IsInf(best, -1) {
		return 0.0
	}
	if depthFromRoot == 0 && maximizing {
		if best <= -winScore/2 && secondBest > math.Inf(-1) {
			best = secondBest
			bestMove = secondBestMove
		}
	}
	flag := TTExact
	if best <= alphaOrig {
		flag = TTUpper
	} else if best >= betaOrig {
		flag = TTLower
	}
	if tt != nil {
		replaced, overwrote := tt.Store(boardHash, depth, best, flag, bestMove)
		if ctx.settings.Stats != nil {
			ctx.settings.Stats.TTStores++
			if replaced || overwrote {
				ctx.settings.Stats.TTOverwrites++
				ctx.settings.Stats.TTReplacements++
			}
		}
	}
	return best
}

func applyTTEntry(entry TTEntry, depth int, alpha *float64, beta *float64, stats *SearchStats) (used bool, ret bool, value float64) {
	if entry.Depth < depth {
		return false, false, 0.0
	}
	switch entry.Flag {
	case TTExact:
		return true, true, entry.ScoreFloat()
	case TTLower:
		value := entry.ScoreFloat()
		if value > *alpha {
			*alpha = value
		}
	case TTUpper:
		value := entry.ScoreFloat()
		if value < *beta {
			*beta = value
		}
	}
	if *alpha >= *beta {
		if stats != nil {
			stats.Cutoffs++
			stats.TTCutoffs++
		}
		return true, true, entry.ScoreFloat()
	}
	return true, false, entry.ScoreFloat()
}

func evaluateMoveWithCache(state *GameState, ctx minimaxContext, currentPlayer PlayerColor, move Move, depthLeft int, depthFromRoot int, boardHash uint64, outCached *bool, alpha, beta float64) float64 {
	if timedOut(ctx) {
		return evaluateStateHeuristic(*state, ctx.rules, ctx.settings)
	}
	_ = boardHash

	score := illegalScore
	if ok, _ := ctx.rules.IsLegal(*state, move, currentPlayer); ok {
		sampleBoardTiming := false
		if stats := ctx.settings.Stats; stats != nil {
			nextOp := stats.BoardGenOps + 1
			sampleBoardTiming = (nextOp & searchTimingSampleMask) == 0
		}
		var boardGenStart time.Time
		if sampleBoardTiming {
			boardGenStart = time.Now()
		}
		var undo searchMoveUndo
		applied := applyMoveWithUndo(state, ctx.rules, move, currentPlayer, &undo)
		if stats := ctx.settings.Stats; stats != nil {
			stats.BoardGenOps++
			if sampleBoardTiming {
				stats.BoardGenTime += time.Since(boardGenStart)
			}
			if stats.BoardGenOps == 1 || (stats.BoardGenOps&searchProgressChunkMask) == 0 {
				reportSearchProgress(stats, ctx.settings)
			}
		}
		if applied {
			if ctx.settings.OnGhostUpdate != nil {
				ctx.settings.OnGhostUpdate(state.Clone())
			}
			if depthLeft <= 1 || timedOut(ctx) {
				score = evaluateStateHeuristic(*state, ctx.rules, ctx.settings)
			} else {
				nextCtx := ctx
				nextCtx.logIndent = ctx.logIndent + 1
				score = minimax(state, nextCtx, depthLeft-1, otherPlayer(currentPlayer), depthFromRoot+1, alpha, beta)
			}
			undoMoveWithUndo(state, undo)
		}
	}
	if outCached != nil {
		*outCached = false
	}
	return score
}

func scoreBoardAtDepth(state GameState, settings AIScoreSettings, ctx minimaxContext, depth int, alpha, beta float64, outUsedCache *bool) ([]float64, bool) {
	if timedOut(ctx) {
		return nil, false
	}
	usedCache := false
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	boardHash := ttKeyFor(state, settings.BoardSize)
	cache := selectCache(ctx)
	tt := ensureTT(cache, settings.Config)
	var pvMove *Move
	if tt != nil {
		if entry, ok := tt.Probe(boardHash); ok {
			if entry.BestMove.IsValid(settings.BoardSize) {
				pv := entry.BestMove
				pvMove = &pv
			}
		}
	}
	immediateWins := findImmediateWinMovesCached(cache, state, ctx.rules, settings.Player, settings.BoardSize, settings.Config)
	mustBlock := false
	if len(immediateWins) == 0 {
		mustBlock = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(settings.Player), settings.BoardSize, settings.Config)
	}
	tactical := false
	opponentUrgent := false
	if settings.Config.AiEnableTacticalK || settings.Config.AiEnableTacticalMode || settings.Config.AiEnableTacticalExt {
		opponentUrgent = hasUrgentThreat(state.Board, settings.BoardSize, otherPlayer(settings.Player))
		tactical = isTacticalPosition(state, ctx, settings.Player) || opponentUrgent
	}
	maxCandidates := candidateLimit(ctx, depth, 0, tactical)
	var truncatedCandidates []Move
	var candidates []Move
	rootMaximizing := settings.Player == PlayerBlack
	if len(immediateWins) > 0 {
		candidates = orderMovesFromList(state, ctx, settings.Player, rootMaximizing, 0, immediateWins, pvMove, prioWin)
	} else if mustBlock {
		blockMoves := findBlockingMoves(cache, state, ctx.rules, settings.Player, settings.BoardSize, settings.Config)
		candidates = orderMovesFromList(state, ctx, settings.Player, rootMaximizing, 0, blockMoves, pvMove, prioBlockWin)
	} else if settings.Config.AiEnableTacticalMode && tactical {
		tacticalMoves := tacticalCandidates(state, ctx, settings.Player)
		if opponentUrgent {
			defensiveMoves := defensiveTacticalCandidates(tacticalMoves)
			if len(defensiveMoves) > 0 {
				candidates = orderCandidateMoves(state, ctx, settings.Player, rootMaximizing, 0, defensiveMoves, 0, pvMove)
			} else {
				blockMoves := findBlockingMoves(cache, state, ctx.rules, settings.Player, settings.BoardSize, settings.Config)
				if len(blockMoves) > 0 {
					candidates = orderMovesFromList(state, ctx, settings.Player, rootMaximizing, 0, blockMoves, pvMove, prioBlockWin)
				} else {
					candidates = orderCandidates(state, ctx, settings.Player, rootMaximizing, 0, maxCandidates, pvMove)
				}
			}
		} else if len(tacticalMoves) > 0 {
			candidates = orderCandidateMoves(state, ctx, settings.Player, rootMaximizing, 0, tacticalMoves, 0, pvMove)
		} else {
			candidates = orderCandidates(state, ctx, settings.Player, rootMaximizing, 0, maxCandidates, pvMove)
		}
	} else {
		candidates = orderCandidates(state, ctx, settings.Player, rootMaximizing, 0, maxCandidates, pvMove)
	}
	candidates = applyCandidateCap(candidates, maxCandidates)
	if settings.Config.AiLogSearchStats {
		if mustBlock {
			if settings.Config.AiTopCandidates > 0 {
				truncatedCandidates = orderCandidates(state, ctx, settings.Player, rootMaximizing, 0, settings.Config.AiTopCandidates, pvMove)
			}
			fmt.Printf("[ai:must_block depth=%d] allowed=%s ordered=%s truncated=%s\n", depth, formatMoves(candidates), formatMoves(candidates), formatMoves(truncatedCandidates))
		} else {
			fmt.Printf("[ai:must_block depth=%d] opponent_win_in_1_wide=%v\n", depth, mustBlock)
		}
	}
	if settings.Stats != nil {
		settings.Stats.RootCandidates += int64(len(candidates))
		settings.Stats.RootSamples++
	}
	aspirationAlpha := alpha
	aspirationBeta := beta
	rootAlpha := alpha
	rootBeta := beta
	for _, move := range candidates {
		if timedOut(ctx) {
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return nil, false
		}
		if settings.Config.AiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, settings.Player, settings.BoardSize) {
			win := -winScore
			if settings.Player == PlayerBlack {
				win = winScore
			}
			scores[move.Y*settings.BoardSize+move.X] = win
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return scores, true
		}
		idx := move.Y*settings.BoardSize + move.X
		cached := false
		score := evaluateMoveWithCache(&state, ctx, settings.Player, move, depth, depth, boardHash, &cached, rootAlpha, rootBeta)
		if settings.Config.AiEnableAspiration && (score <= aspirationAlpha || score >= aspirationBeta) {
			if timedOut(ctx) {
				if outUsedCache != nil {
					*outUsedCache = usedCache
				}
				return nil, false
			}
			score = evaluateMoveWithCache(&state, ctx, settings.Player, move, depth, depth, boardHash, &cached, math.Inf(-1), math.Inf(1))
		}
		if cached {
			usedCache = true
		}
		scores[idx] = score
		if rootMaximizing {
			if score > rootAlpha {
				rootAlpha = score
			}
		} else {
			if score < rootBeta {
				rootBeta = score
			}
		}
	}
	if outUsedCache != nil {
		*outUsedCache = usedCache
	}
	return scores, true
}

func mergeSearchStats(dst, src *SearchStats) {
	if dst == nil || src == nil {
		return
	}
	dst.Nodes += src.Nodes
	dst.TTProbes += src.TTProbes
	dst.TTHits += src.TTHits
	dst.TTExactHits += src.TTExactHits
	dst.TTLowerHits += src.TTLowerHits
	dst.TTUpperHits += src.TTUpperHits
	dst.TTStores += src.TTStores
	dst.TTOverwrites += src.TTOverwrites
	dst.TTReplacements += src.TTReplacements
	dst.Cutoffs += src.Cutoffs
	dst.TTCutoffs += src.TTCutoffs
	dst.ABCutoffs += src.ABCutoffs
	dst.CandidateCount += src.CandidateCount
	dst.RootCandidates += src.RootCandidates
	dst.DeepCandidates += src.DeepCandidates
	dst.RootSamples += src.RootSamples
	dst.DeepSamples += src.DeepSamples
	dst.EvalCacheProbes += src.EvalCacheProbes
	dst.EvalCacheHits += src.EvalCacheHits
	dst.HeuristicCalls += src.HeuristicCalls
	dst.HeuristicTime += src.HeuristicTime
	dst.BoardGenOps += src.BoardGenOps
	dst.BoardGenTime += src.BoardGenTime
}

const progressChunk = int64(64)

func reportSearchProgress(stats *SearchStats, settings AIScoreSettings) {
	if stats == nil {
		return
	}
	if settings.OnNodeProgress != nil {
		nodeDelta := stats.Nodes - stats.progressReportedNodes
		if nodeDelta > 0 && stats.progressReportedNodes == 0 {
			settings.OnNodeProgress(1)
			stats.progressReportedNodes = 1
			nodeDelta = stats.Nodes - stats.progressReportedNodes
		}
		if nodeDelta >= progressChunk {
			emit := nodeDelta - (nodeDelta % progressChunk)
			settings.OnNodeProgress(emit)
			stats.progressReportedNodes += emit
		}
		if stats.Nodes == 0 {
			boardDelta := stats.BoardGenOps - stats.progressReportedBoardGen
			if boardDelta > 0 && stats.progressReportedBoardGen == 0 {
				settings.OnNodeProgress(1)
				stats.progressReportedBoardGen = 1
				boardDelta = stats.BoardGenOps - stats.progressReportedBoardGen
			}
			if boardDelta >= progressChunk {
				emit := boardDelta - (boardDelta % progressChunk)
				settings.OnNodeProgress(emit)
				stats.progressReportedBoardGen += emit
			}
		}
	}
	reportSearchMetrics(stats, settings)
}

func flushSearchProgress(stats *SearchStats, settings AIScoreSettings) {
	if stats == nil {
		return
	}
	if settings.OnNodeProgress != nil {
		nodeDelta := stats.Nodes - stats.progressReportedNodes
		if nodeDelta > 0 {
			settings.OnNodeProgress(nodeDelta)
			stats.progressReportedNodes += nodeDelta
		}
		if stats.Nodes == 0 {
			boardDelta := stats.BoardGenOps - stats.progressReportedBoardGen
			if boardDelta > 0 {
				settings.OnNodeProgress(boardDelta)
				stats.progressReportedBoardGen += boardDelta
			}
		}
	}
	reportSearchMetrics(stats, settings)
}

func reportSearchMetrics(stats *SearchStats, settings AIScoreSettings) {
	if stats == nil || settings.OnSearchProgress == nil {
		return
	}
	delta := SearchProgressDelta{
		Nodes:          stats.Nodes - stats.progressMetricNodes,
		CandidateCount: stats.CandidateCount - stats.progressMetricCandidates,
		TTProbes:       stats.TTProbes - stats.progressMetricTTProbes,
		TTHits:         stats.TTHits - stats.progressMetricTTHits,
		TTCutoffs:      stats.TTCutoffs - stats.progressMetricTTCutoffs,
		ABCutoffs:      stats.ABCutoffs - stats.progressMetricABCutoffs,
	}
	if delta.Nodes == 0 && delta.CandidateCount == 0 && delta.TTProbes == 0 && delta.TTHits == 0 && delta.TTCutoffs == 0 && delta.ABCutoffs == 0 {
		return
	}
	settings.OnSearchProgress(delta)
	stats.progressMetricNodes += delta.Nodes
	stats.progressMetricCandidates += delta.CandidateCount
	stats.progressMetricTTProbes += delta.TTProbes
	stats.progressMetricTTHits += delta.TTHits
	stats.progressMetricTTCutoffs += delta.TTCutoffs
	stats.progressMetricABCutoffs += delta.ABCutoffs
}

func ScoreBoardDirectDepthParallel(state GameState, rules Rules, settings AIScoreSettings, workers int) ([]float64, bool) {
	if settings.BoardSize <= 0 {
		settings.BoardSize = state.Board.Size()
	}
	if settings.BoardSize > state.Board.Size() {
		settings.BoardSize = state.Board.Size()
	}
	if settings.Depth < 1 {
		settings.Depth = 1
	}
	if settings.Config == (Config{}) {
		settings.Config = GetConfig()
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}

	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	if !hasStoneWithin(state.Board, settings.BoardSize) {
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		if settings.Stats != nil {
			settings.Stats.CompletedDepths = settings.Depth
		}
		return scores, true
	}
	initialCandidates := collectCandidateMoves(state, settings.Player, settings.BoardSize)
	if len(initialCandidates) == 0 {
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		if settings.Stats != nil {
			settings.Stats.CompletedDepths = settings.Depth
		}
		return scores, true
	}

	start := time.Now()
	baseCtx := newMinimaxContext(rules, settings, start)

	cache := selectCache(baseCtx)
	tt := ensureTT(cache, settings.Config)
	if tt != nil {
		tt.NextGeneration()
	}
	if settings.Config.AiEnableEvalCache {
		if evalCache := ensureEvalCache(cache, settings.Config); evalCache != nil {
			evalCache.NextGeneration()
		}
	}

	boardHash := ttKeyFor(state, settings.BoardSize)
	var pvMove *Move
	if tt != nil {
		if entry, ok := tt.Probe(boardHash); ok && entry.BestMove.IsValid(settings.BoardSize) {
			pv := entry.BestMove
			pvMove = &pv
		}
	}

	immediateWins := findImmediateWinMovesCached(cache, state, rules, settings.Player, settings.BoardSize, settings.Config)
	mustBlock := false
	if len(immediateWins) == 0 {
		mustBlock = hasImmediateWinCached(cache, state, rules, otherPlayer(settings.Player), settings.BoardSize, settings.Config)
	}
	tactical := false
	opponentUrgent := false
	if settings.Config.AiEnableTacticalK || settings.Config.AiEnableTacticalMode || settings.Config.AiEnableTacticalExt {
		opponentUrgent = hasUrgentThreat(state.Board, settings.BoardSize, otherPlayer(settings.Player))
		tactical = isTacticalPosition(state, baseCtx, settings.Player) || opponentUrgent
	}
	maxCandidates := candidateLimit(baseCtx, settings.Depth, 0, tactical)
	rootMaximizing := settings.Player == PlayerBlack
	var candidates []Move
	if len(immediateWins) > 0 {
		candidates = orderMovesFromList(state, baseCtx, settings.Player, rootMaximizing, 0, immediateWins, pvMove, prioWin)
	} else if mustBlock {
		blockMoves := findBlockingMoves(cache, state, rules, settings.Player, settings.BoardSize, settings.Config)
		candidates = orderMovesFromList(state, baseCtx, settings.Player, rootMaximizing, 0, blockMoves, pvMove, prioBlockWin)
	} else if settings.Config.AiEnableTacticalMode && tactical {
		tacticalMoves := tacticalCandidates(state, baseCtx, settings.Player)
		if opponentUrgent {
			defensiveMoves := defensiveTacticalCandidates(tacticalMoves)
			if len(defensiveMoves) > 0 {
				candidates = orderCandidateMoves(state, baseCtx, settings.Player, rootMaximizing, 0, defensiveMoves, 0, pvMove)
			} else {
				blockMoves := findBlockingMoves(cache, state, rules, settings.Player, settings.BoardSize, settings.Config)
				if len(blockMoves) > 0 {
					candidates = orderMovesFromList(state, baseCtx, settings.Player, rootMaximizing, 0, blockMoves, pvMove, prioBlockWin)
				} else {
					candidates = orderCandidates(state, baseCtx, settings.Player, rootMaximizing, 0, maxCandidates, pvMove)
				}
			}
		} else if len(tacticalMoves) > 0 {
			candidates = orderCandidateMoves(state, baseCtx, settings.Player, rootMaximizing, 0, tacticalMoves, 0, pvMove)
		} else {
			candidates = orderCandidates(state, baseCtx, settings.Player, rootMaximizing, 0, maxCandidates, pvMove)
		}
	} else {
		candidates = orderCandidates(state, baseCtx, settings.Player, rootMaximizing, 0, maxCandidates, pvMove)
	}
	candidates = applyCandidateCap(candidates, maxCandidates)
	if len(candidates) == 0 {
		return scores, false
	}

	if settings.Stats != nil {
		settings.Stats.RootCandidates += int64(len(candidates))
		settings.Stats.RootSamples++
	}

	if workers <= 0 {
		workers = 1
	}
	if workers > len(candidates) {
		workers = len(candidates)
	}

	type moveScore struct {
		move  Move
		score float64
	}
	var (
		boundMu    sync.Mutex
		boundAlpha = math.Inf(-1)
		boundBeta  = math.Inf(1)
	)
	readRootBounds := func() (float64, float64) {
		boundMu.Lock()
		defer boundMu.Unlock()
		return boundAlpha, boundBeta
	}
	updateRootBound := func(score float64) {
		boundMu.Lock()
		defer boundMu.Unlock()
		if rootMaximizing {
			if score > boundAlpha {
				boundAlpha = score
			}
			return
		}
		if score < boundBeta {
			boundBeta = score
		}
	}
	evaluateRootMove := func(localState *GameState, localCtx minimaxContext, localSettings AIScoreSettings, localStats *SearchStats, move Move) float64 {
		if settings.Config.AiQuickWinExit && isImmediateWinCached(cache, *localState, rules, move, settings.Player, settings.BoardSize) {
			win := -winScore
			if settings.Player == PlayerBlack {
				win = winScore
			}
			updateRootBound(win)
			if localSettings.OnNodeProgress != nil {
				localSettings.OnNodeProgress(1)
			}
			return win
		}
		alphaBound, betaBound := readRootBounds()
		score := evaluateMoveWithCache(localState, localCtx, settings.Player, move, settings.Depth, settings.Depth, boardHash, nil, alphaBound, betaBound)
		updateRootBound(score)
		flushSearchProgress(localStats, localSettings)
		return score
	}

	if workers == 1 {
		localStats := &SearchStats{}
		localSettings := settings
		localSettings.Stats = localStats
		localCtx := newMinimaxContext(rules, localSettings, start)
		localState := state.Clone()
		for _, move := range candidates {
			score := evaluateRootMove(&localState, localCtx, localSettings, localStats, move)
			scores[move.Y*settings.BoardSize+move.X] = score
		}
		mergeSearchStats(settings.Stats, localStats)
	} else {
		// YBWC-style root split: search first ordered move on the main thread to get a strong bound,
		// then parallelize only the remaining root moves.
		mainStats := &SearchStats{}
		mainSettings := settings
		mainSettings.Stats = mainStats
		mainCtx := newMinimaxContext(rules, mainSettings, start)
		mainState := state.Clone()
		first := candidates[0]
		firstScore := evaluateRootMove(&mainState, mainCtx, mainSettings, mainStats, first)
		scores[first.Y*settings.BoardSize+first.X] = firstScore
		mergeSearchStats(settings.Stats, mainStats)

		remaining := candidates[1:]
		if len(remaining) > 0 {
			workerCount := workers - 1
			if workerCount < 1 {
				workerCount = 1
			}
			if workerCount > len(remaining) {
				workerCount = len(remaining)
			}
			jobs := make(chan Move)
			results := make(chan moveScore, len(remaining))
			statsCh := make(chan SearchStats, workerCount)
			for i := 0; i < workerCount; i++ {
				go func() {
					localStats := &SearchStats{}
					localSettings := settings
					localSettings.Stats = localStats
					localCtx := newMinimaxContext(rules, localSettings, start)
					localState := state.Clone()
					for move := range jobs {
						score := evaluateRootMove(&localState, localCtx, localSettings, localStats, move)
						results <- moveScore{move: move, score: score}
					}
					statsCh <- *localStats
				}()
			}

			for _, move := range remaining {
				jobs <- move
			}
			close(jobs)

			for i := 0; i < len(remaining); i++ {
				result := <-results
				scores[result.move.Y*settings.BoardSize+result.move.X] = result.score
			}
			for i := 0; i < workerCount; i++ {
				local := <-statsCh
				mergeSearchStats(settings.Stats, &local)
			}
		}
	}

	bestScore := math.Inf(-1)
	if !rootMaximizing {
		bestScore = math.Inf(1)
	}
	bestMove := Move{}
	foundBest := false
	for _, move := range candidates {
		score := scores[move.Y*settings.BoardSize+move.X]
		if score == illegalScore {
			continue
		}
		if !foundBest {
			bestScore = score
			bestMove = move
			foundBest = true
			continue
		}
		if rootMaximizing {
			if score > bestScore {
				bestScore = score
				bestMove = move
			}
		} else {
			if score < bestScore {
				bestScore = score
				bestMove = move
			}
		}
	}
	if tt != nil && foundBest {
		replaced, overwrote := tt.Store(boardHash, settings.Depth, bestScore, TTExact, bestMove)
		if settings.Stats != nil {
			settings.Stats.TTStores++
			if replaced || overwrote {
				settings.Stats.TTOverwrites++
				settings.Stats.TTReplacements++
			}
		}
	}
	if settings.Stats != nil {
		settings.Stats.CompletedDepths = settings.Depth
		settings.Stats.DepthDurations = append(settings.Stats.DepthDurations, time.Since(start))
	}
	return scores, true
}

func ScoreBoard(state GameState, rules Rules, settings AIScoreSettings) []float64 {
	if settings.BoardSize <= 0 {
		settings.BoardSize = state.Board.Size()
	}
	if settings.BoardSize > state.Board.Size() {
		settings.BoardSize = state.Board.Size()
	}
	if settings.Depth < 1 {
		settings.Depth = 1
	}
	if settings.Config == (Config{}) {
		settings.Config = GetConfig()
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	queueState := GameState{}
	queueStateReady := false
	if settings.Config.AiQueueEnabled && !settings.SkipQueueBacklog && !settings.DirectDepthOnly {
		// Keep an immutable pre-search snapshot so queue learning always targets
		// the exact board before this AI decision.
		queueState = state.Clone()
		queueStateReady = true
	}
	if settings.Config.AiMaxDepth > 0 {
		settings.Depth = settings.Config.AiMaxDepth
	}
	minDepth := 1
	if settings.Config.AiMinDepth > 0 {
		minDepth = settings.Config.AiMinDepth
	}
	ctx := newMinimaxContext(rules, settings, time.Now())
	if settings.Stats != nil && settings.Stats.Start.IsZero() {
		settings.Stats.Start = ctx.start
	}
	logAITask(ctx, 0, "ScoreBoard start depth=%d board=%d budget=%dms", settings.Depth, settings.BoardSize, settings.Config.AiTimeBudgetMs)
	if !hasStoneWithin(state.Board, settings.BoardSize) {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	initialCandidates := collectCandidateMoves(state, settings.Player, settings.BoardSize)
	if len(initialCandidates) == 0 {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	logAITask(ctx, 1, "Candidate generation complete count=%d", len(initialCandidates))
	startTime := ctx.start
	lastDepthCompleted := 0
	cache := selectCache(ctx)
	tt := ensureTT(cache, settings.Config)
	if tt != nil {
		tt.NextGeneration()
	}
	if settings.Config.AiEnableEvalCache {
		if evalCache := ensureEvalCache(cache, settings.Config); evalCache != nil {
			evalCache.NextGeneration()
		}
	}
	rootHash := ttKeyFor(state, settings.BoardSize)
	var scores []float64
	var lastScores []float64
	var lastBestScore float64
	var fallbackScores []float64
	rootMaximizing := settings.Player == PlayerBlack
	fallbackBestScore := math.Inf(1)
	if rootMaximizing {
		fallbackBestScore = math.Inf(-1)
	}
	haveBest := false
	startDepth := minDepth
	if settings.DirectDepthOnly {
		startDepth = settings.Depth
		if startDepth < minDepth {
			startDepth = minDepth
		}
	}
	if startDepth < minDepth {
		startDepth = minDepth
	}
	if startDepth > settings.Depth {
		if len(fallbackScores) > 0 {
			return fallbackScores
		}
		result := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range result {
			result[i] = illegalScore
		}
		return result
	}
	for depth := startDepth; depth <= settings.Depth; depth++ {
		if timedOut(ctx) && depth > minDepth {
			break
		}
		logAITask(ctx, 1, "Depth %d start", depth)
		depthStart := time.Now()
		if settings.Config.AiQuickWinExit {
			for _, cand := range initialCandidates {
				move := cand.move
				if isImmediateWinCached(cache, state, rules, move, settings.Player, settings.BoardSize) {
					logAITask(ctx, 2, "Immediate win cached move=%v depth=%d", move, depth)
					winScores := make([]float64, settings.BoardSize*settings.BoardSize)
					for i := range winScores {
						winScores[i] = illegalScore
					}
					win := -winScore
					if settings.Player == PlayerBlack {
						win = winScore
					}
					winScores[move.Y*settings.BoardSize+move.X] = win
					if tt != nil {
						replaced, overwrote := tt.Store(rootHash, depth, win, TTExact, move)
						if settings.Stats != nil {
							settings.Stats.TTStores++
							if replaced || overwrote {
								settings.Stats.TTOverwrites++
								settings.Stats.TTReplacements++
							}
						}
					}
					return winScores
				}
			}
		}
		alpha := math.Inf(-1)
		beta := math.Inf(1)
		if settings.Config.AiEnableAspiration && haveBest {
			window := settings.Config.AiAspWindow
			if window > 0 {
				if settings.Config.AiAspWindowMax > 0 && window > settings.Config.AiAspWindowMax {
					window = settings.Config.AiAspWindowMax
				}
				alpha = lastBestScore - window
				beta = lastBestScore + window
			}
		}
		usedCache := false
		var completed bool
		scores, completed = scoreBoardAtDepth(state, settings, ctx, depth, alpha, beta, &usedCache)
		if !completed {
			if settings.Config.AiReturnLastComplete && lastScores != nil {
				break
			}
			if scores != nil {
				lastScores = scores
			}
			break
		}
		if len(scores) > 0 {
			depthBest := math.Inf(1)
			if rootMaximizing {
				depthBest = math.Inf(-1)
			}
			for _, cand := range initialCandidates {
				move := cand.move
				score := scores[move.Y*settings.BoardSize+move.X]
				if score == illegalScore {
					continue
				}
				if rootMaximizing {
					if score > depthBest {
						depthBest = score
					}
				} else {
					if score < depthBest {
						depthBest = score
					}
				}
			}
			if rootMaximizing && depthBest > fallbackBestScore {
				fallbackBestScore = depthBest
				fallbackScores = append([]float64(nil), scores...)
			}
			if !rootMaximizing && depthBest < fallbackBestScore {
				fallbackBestScore = depthBest
				fallbackScores = append([]float64(nil), scores...)
			}
		}
		duration := time.Since(depthStart)
		logAITask(ctx, 1, "Depth %d completed in %dms cached=%v", depth, duration.Milliseconds(), usedCache)
		if settings.Stats != nil {
			settings.Stats.DepthDurations = append(settings.Stats.DepthDurations, time.Since(depthStart))
			settings.Stats.CompletedDepths = depth
		}
		if settings.Config.LogDepthScores {
			for _, cand := range initialCandidates {
				move := cand.move
				score := scores[move.Y*settings.BoardSize+move.X]
				_ = score
			}
		}
		bestScore := math.Inf(-1)
		if !rootMaximizing {
			bestScore = math.Inf(1)
		}
		bestX, bestY := -1, -1
		for y := 0; y < settings.BoardSize; y++ {
			for x := 0; x < settings.BoardSize; x++ {
				score := scores[y*settings.BoardSize+x]
				if score == illegalScore {
					continue
				}
				if rootMaximizing {
					if score > bestScore {
						bestScore = score
						bestX = x
						bestY = y
					}
				} else {
					if score < bestScore {
						bestScore = score
						bestX = x
						bestY = y
					}
				}
			}
		}
		if tt != nil && bestX >= 0 && bestY >= 0 {
			replaced, overwrote := tt.Store(rootHash, depth, bestScore, TTExact, Move{X: bestX, Y: bestY})
			if settings.Stats != nil {
				settings.Stats.TTStores++
				if replaced || overwrote {
					settings.Stats.TTOverwrites++
					settings.Stats.TTReplacements++
				}
			}
		}
		lastDepthCompleted = depth
		lastScores = scores
		lastBestScore = bestScore
		haveBest = true
	}
	totalDuration := time.Since(startTime)
	logAITask(ctx, 0, "ScoreBoard finished depth=%d total=%dms", lastDepthCompleted, totalDuration.Milliseconds())
	if !settings.DirectDepthOnly && lastDepthCompleted < settings.Depth {
		if timedOut(ctx) || (ctx.settings.ShouldStop != nil && ctx.settings.ShouldStop()) {
			if queueStateReady {
				enqueueSearchBacklogTask(queueState, rules)
			}
		}
	}
	if lastScores != nil {
		if rootMaximizing && lastBestScore <= fallbackBestScore && len(fallbackScores) > 0 {
			return fallbackScores
		}
		if !rootMaximizing && lastBestScore >= fallbackBestScore && len(fallbackScores) > 0 {
			return fallbackScores
		}
		return lastScores
	}
	if rootMaximizing && len(fallbackScores) > 0 && lastBestScore <= fallbackBestScore {
		return fallbackScores
	}
	if !rootMaximizing && len(fallbackScores) > 0 && lastBestScore >= fallbackBestScore {
		return fallbackScores
	}
	return scores
}

func TranspositionSize(cache *AISearchCache) int {
	if cache == nil {
		return 0
	}
	cache.mu.Lock()
	tt := cache.TT
	cache.mu.Unlock()
	if tt == nil {
		return 0
	}
	return tt.Count()
}

func RerootCache(cache *AISearchCache, state GameState) {
	_ = cache
	_ = state
}

func ttKeyFor(state GameState, boardSize int) uint64 {
	key := state.CanonHash
	key ^= mixKey(uint64(boardSize)<<32 | uint64(state.Status))
	return key
}

func mixKey(v uint64) uint64 {
	v += 0x9e3779b97f4a7c15
	v = (v ^ (v >> 30)) * 0xbf58476d1ce4e5b9
	v = (v ^ (v >> 27)) * 0x94d049bb133111eb
	return v ^ (v >> 31)
}

func minInt(values ...int) int {
	min := values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
	}
	return min
}
