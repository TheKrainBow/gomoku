package main

import (
	"math"
	"sort"
	"time"
)

const (
	illegalScore = -1e9
	winScore     = 10000.0
)

type AISearchCache struct {
	TT                map[TTKey]TTEntry
	MoveCache         map[MoveCacheKey]float64
	ImmediateWinMove  map[ImmediateWinKey]bool
	ImmediateWinState map[ImmediateWinStateKey]bool
	Edges             map[StateKey][]StateKey
	Root              StateKey
	HasRoot           bool
	TTSize            int
}

type StateKey struct {
	Hash          uint64
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	CurrentPlayer PlayerColor
}

type TTKey struct {
	Hash          uint64
	DepthLeft     int
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	CurrentPlayer PlayerColor
}

type TTEntry struct {
	Value     float64
	DepthLeft int
	BestMove  Move
}

type MoveCacheKey struct {
	Hash          uint64
	DepthLeft     int
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	CurrentPlayer PlayerColor
	X             int
	Y             int
}

type ImmediateWinKey struct {
	Hash          uint64
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	Player        PlayerColor
	X             int
	Y             int
}

type ImmediateWinStateKey struct {
	Hash          uint64
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	Player        PlayerColor
}

type AIScoreSettings struct {
	Depth         int
	TimeoutMs     int
	BoardSize     int
	Player        PlayerColor
	OnGhostUpdate func(GameState)
	Cache         *AISearchCache
	Config        Config
	ShouldStop    func() bool
}

type minimaxContext struct {
	rules    Rules
	settings AIScoreSettings
	start    time.Time
}

type cacheKey struct {
	Hash      uint64
	Depth     int
	BoardSize int
	Player    PlayerColor
}

var depthCache = map[cacheKey][]float64{}
var defaultCache = newAISearchCache()

func newAISearchCache() AISearchCache {
	return AISearchCache{
		TT:                make(map[TTKey]TTEntry),
		MoveCache:         make(map[MoveCacheKey]float64),
		ImmediateWinMove:  make(map[ImmediateWinKey]bool),
		ImmediateWinState: make(map[ImmediateWinStateKey]bool),
		Edges:             make(map[StateKey][]StateKey),
	}
}

func hashBoard(board Board, boardSize int) uint64 {
	var hash uint64 = 1469598103934665603
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			v := uint64(board.At(x, y))
			hash ^= (v + 1)
			hash *= 1099511628211
		}
	}
	return hash
}

func selectCache(ctx minimaxContext) *AISearchCache {
	if ctx.settings.Cache != nil {
		return ctx.settings.Cache
	}
	return &defaultCache
}

func makeStateKey(state GameState, boardSize int, player PlayerColor) StateKey {
	return StateKey{
		Hash:          hashBoard(state.Board, boardSize),
		BoardSize:     boardSize,
		CapturedBlack: state.CapturedBlack,
		CapturedWhite: state.CapturedWhite,
		Status:        state.Status,
		CurrentPlayer: player,
	}
}

func storeTtEntry(cache *AISearchCache, key TTKey, entry TTEntry, config Config) {
	if existing, ok := cache.TT[key]; ok {
		if existing.DepthLeft < entry.DepthLeft {
			cache.TT[key] = entry
		}
		return
	}
	cache.TT[key] = entry
	cache.TTSize++
	if int64(cache.TTSize) > config.AiTtMaxEntries {
		cache.TT = make(map[TTKey]TTEntry)
		cache.TTSize = 0
	}
}

func addEdge(cache *AISearchCache, parent, child StateKey) {
	children := cache.Edges[parent]
	for _, existing := range children {
		if existing == child {
			return
		}
	}
	cache.Edges[parent] = append(children, child)
}

func playerCell(player PlayerColor) Cell {
	return CellFromPlayer(player)
}

func countDirection(board Board, x, y, dx, dy int, cell Cell, limit int) int {
	count := 0
	for step := 1; step <= limit; step++ {
		nx := x + step*dx
		ny := y + step*dy
		if !board.InBounds(nx, ny) || board.At(nx, ny) != cell {
			break
		}
		count++
	}
	return count
}

func collectCandidateMoves(board Board, boardSize int) []Move {
	moves := []Move{}
	seen := make([]bool, boardSize*boardSize)
	hasStone := false
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) != CellEmpty {
				hasStone = true
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						nx := x + dx
						ny := y + dy
						if !board.InBounds(nx, ny) {
							continue
						}
						if !board.IsEmpty(nx, ny) {
							continue
						}
						idx := ny*boardSize + nx
						if !seen[idx] {
							seen[idx] = true
							moves = append(moves, Move{X: nx, Y: ny})
						}
					}
				}
			}
		}
	}
	if !hasStone {
		center := boardSize / 2
		moves = append(moves, Move{X: center, Y: center})
	}
	return moves
}

func orderCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, maxCandidates int, pvMove *Move) []Move {
	moves := collectCandidateMoves(state.Board, ctx.settings.BoardSize)
	evalSettings := ctx.settings
	evalSettings.Player = currentPlayer
	type scoredMove struct {
		score float64
		move  Move
	}
	scored := make([]scoredMove, 0, len(moves))
	for _, move := range moves {
		score := heuristicForMove(state, ctx.rules, evalSettings, move)
		scored = append(scored, scoredMove{score: score, move: move})
	}
	sort.SliceStable(scored, func(i, j int) bool {
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
	moves = moves[:0]
	for _, entry := range scored {
		moves = append(moves, entry.move)
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

func isBlockedEnd(board Board, x, y, dx, dy, distance int) bool {
	bx := x + (distance+1)*dx
	by := y + (distance+1)*dy
	if !board.InBounds(bx, by) {
		return true
	}
	return board.At(bx, by) != CellEmpty
}

func heuristicForMove(state GameState, rules Rules, settings AIScoreSettings, move Move) float64 {
	if ok, _ := rules.IsLegal(state, move, settings.Player); !ok {
		return illegalScore
	}
	board := state.Board
	selfCell := playerCell(settings.Player)
	opponentCell := playerCell(otherPlayer(settings.Player))
	score := 0.0
	size := settings.BoardSize
	minEdgeDist := minInt(move.X, move.Y, size-1-move.X, size-1-move.Y)
	const edgeMargin = 2
	if minEdgeDist < edgeMargin {
		score -= float64((edgeMargin - minEdgeDist) * 2)
	}

	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	addsWin := false
	for i := 0; i < 4; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		left := countDirection(board, move.X, move.Y, -dx, -dy, selfCell, settings.BoardSize)
		right := countDirection(board, move.X, move.Y, dx, dy, selfCell, settings.BoardSize)
		length := 1 + left + right
		if left+right > 0 {
			score += float64(length)
		}
		if length >= rules.WinLength() {
			addsWin = true
		}

		oppLeft := countDirection(board, move.X, move.Y, -dx, -dy, opponentCell, settings.BoardSize)
		if oppLeft > 0 {
			score += float64(oppLeft)
			if isBlockedEnd(board, move.X, move.Y, -dx, -dy, oppLeft) {
				score += 5.0
			}
		}
		oppRight := countDirection(board, move.X, move.Y, dx, dy, opponentCell, settings.BoardSize)
		if oppRight > 0 {
			score += float64(oppRight)
			if isBlockedEnd(board, move.X, move.Y, dx, dy, oppRight) {
				score += 5.0
			}
		}
	}
	if addsWin {
		score += 100.0
	}

	captures := rules.FindCaptures(board, move, selfCell)
	if len(captures) > 0 {
		pairs := len(captures) / 2
		score += 10.0 * float64(pairs)
		currentCaptured := state.CapturedBlack
		if settings.Player == PlayerWhite {
			currentCaptured = state.CapturedWhite
		}
		if currentCaptured+len(captures) >= rules.CaptureWinStones() {
			score += 100.0
		}
	}
	return score
}

func evaluateStateHeuristic(state GameState, rules Rules, settings AIScoreSettings) float64 {
	switch state.Status {
	case StatusDraw:
		return 0.0
	case StatusBlackWon:
		if settings.Player == PlayerBlack {
			return winScore
		}
		return -winScore
	case StatusWhiteWon:
		if settings.Player == PlayerWhite {
			return winScore
		}
		return -winScore
	}

	bestSelf := illegalScore
	bestOpp := illegalScore
	opponentSettings := settings
	opponentSettings.Player = otherPlayer(settings.Player)
	size := settings.BoardSize
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			move := Move{X: x, Y: y}
			scoreSelf := heuristicForMove(state, rules, settings, move)
			if scoreSelf > bestSelf {
				bestSelf = scoreSelf
			}
			scoreOpp := heuristicForMove(state, rules, opponentSettings, move)
			if scoreOpp > bestOpp {
				bestOpp = scoreOpp
			}
		}
	}
	if bestSelf == illegalScore {
		bestSelf = 0.0
	}
	if bestOpp == illegalScore {
		bestOpp = 0.0
	}
	return bestSelf - bestOpp
}

func timedOut(ctx minimaxContext) bool {
	if ctx.settings.ShouldStop != nil && ctx.settings.ShouldStop() {
		return true
	}
	if ctx.settings.TimeoutMs <= 0 {
		return false
	}
	elapsed := time.Since(ctx.start).Milliseconds()
	return int(elapsed) >= ctx.settings.TimeoutMs
}

func applyMove(state *GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return false
	}
	cell := playerCell(player)
	state.Board.Set(move.X, move.Y, cell)
	state.LastMove = move
	state.HasLastMove = true
	state.LastMessage = ""

	captures := rules.FindCaptures(state.Board, move, cell)
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
	return true
}

func isImmediateWin(state GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(state, move, player); !ok {
		return false
	}
	probe := state.Clone()
	cell := playerCell(player)
	probe.Board.Set(move.X, move.Y, cell)
	captures := rules.FindCaptures(probe.Board, move, cell)
	capturedCount := len(captures)
	totalCaptured := state.CapturedBlack
	if player == PlayerWhite {
		totalCaptured = state.CapturedWhite
	}
	totalCaptured += capturedCount
	if totalCaptured >= rules.CaptureWinStones() {
		return true
	}
	return rules.IsWin(probe.Board, move)
}

func isImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, move Move, player PlayerColor, boardSize int) bool {
	boardHash := hashBoard(state.Board, boardSize)
	key := ImmediateWinKey{Hash: boardHash, BoardSize: boardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, Player: player, X: move.X, Y: move.Y}
	if value, ok := cache.ImmediateWinMove[key]; ok {
		return value
	}
	result := isImmediateWin(state, rules, move, player)
	cache.ImmediateWinMove[key] = result
	return result
}

func hasImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int) bool {
	boardHash := hashBoard(state.Board, boardSize)
	key := ImmediateWinStateKey{Hash: boardHash, BoardSize: boardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, Player: player}
	if value, ok := cache.ImmediateWinState[key]; ok {
		return value
	}
	candidates := collectCandidateMoves(state.Board, boardSize)
	for _, move := range candidates {
		if isImmediateWin(state, rules, move, player) {
			cache.ImmediateWinState[key] = true
			return true
		}
	}
	cache.ImmediateWinState[key] = false
	return false
}

func minimax(state GameState, ctx minimaxContext, depth int, currentPlayer PlayerColor, depthFromRoot int, alpha, beta float64) float64 {
	if depth <= 0 || timedOut(ctx) || state.Status != StatusRunning {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}

	boardHash := hashBoard(state.Board, ctx.settings.BoardSize)
	ttKey := TTKey{Hash: boardHash, DepthLeft: depth, BoardSize: ctx.settings.BoardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, CurrentPlayer: currentPlayer}
	cache := selectCache(ctx)
	var pvMove *Move
	if entry, ok := cache.TT[ttKey]; ok {
		if entry.DepthLeft >= depth {
			return entry.Value
		}
		pv := entry.BestMove
		pvMove = &pv
	}

	maximizing := currentPlayer == ctx.settings.Player
	best := math.Inf(-1)
	if !maximizing {
		best = math.Inf(1)
	}
	candidates := orderCandidates(state, ctx, currentPlayer, maximizing, ctx.settings.Config.AiTopCandidates, pvMove)
	bestMove := Move{}
	mustBlock := hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize)
	for _, move := range candidates {
		if timedOut(ctx) {
			break
		}
		if ctx.settings.Config.AiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
			win := winScore
			if currentPlayer != ctx.settings.Player {
				win = -winScore
			}
			storeTtEntry(cache, ttKey, TTEntry{Value: win, DepthLeft: depth, BestMove: move}, ctx.settings.Config)
			return win
		}
		if mustBlock {
			blockState := state.Clone()
			if !applyMove(&blockState, ctx.rules, move, currentPlayer) {
				continue
			}
			if hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize) {
				continue
			}
		}
		value := evaluateMoveWithCache(state, ctx, currentPlayer, move, depth, depthFromRoot, boardHash, nil, alpha, beta)
		if maximizing {
			if value > best {
				best = value
				bestMove = move
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
			break
		}
		if timedOut(ctx) {
			break
		}
	}

	if math.IsInf(best, 1) || math.IsInf(best, -1) {
		return 0.0
	}
	storeTtEntry(cache, ttKey, TTEntry{Value: best, DepthLeft: depth, BestMove: bestMove}, ctx.settings.Config)
	return best
}

func evaluateMoveWithCache(state GameState, ctx minimaxContext, currentPlayer PlayerColor, move Move, depthLeft int, depthFromRoot int, boardHash uint64, outCached *bool, alpha, beta float64) float64 {
	if timedOut(ctx) {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	cache := selectCache(ctx)
	key := MoveCacheKey{Hash: boardHash, DepthLeft: depthLeft, BoardSize: ctx.settings.BoardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, CurrentPlayer: currentPlayer, X: move.X, Y: move.Y}
	if value, ok := cache.MoveCache[key]; ok {
		if outCached != nil {
			*outCached = true
		}
		return value
	}

	score := illegalScore
	if ok, _ := ctx.rules.IsLegal(state, move, currentPlayer); ok {
		next := state.Clone()
		if applyMove(&next, ctx.rules, move, currentPlayer) {
			parentKey := makeStateKey(state, ctx.settings.BoardSize, currentPlayer)
			childKey := makeStateKey(next, ctx.settings.BoardSize, next.ToMove)
			addEdge(cache, parentKey, childKey)
			if ctx.settings.OnGhostUpdate != nil {
				ctx.settings.OnGhostUpdate(next)
			}
			if depthLeft <= 1 || timedOut(ctx) {
				score = evaluateStateHeuristic(next, ctx.rules, ctx.settings)
			} else {
				score = minimax(next, ctx, depthLeft-1, otherPlayer(currentPlayer), depthFromRoot+1, alpha, beta)
			}
		}
	}
	cache.MoveCache[key] = score
	if outCached != nil {
		*outCached = false
	}
	return score
}

func scoreBoardAtDepth(state GameState, settings AIScoreSettings, ctx minimaxContext, depth int, outUsedCache *bool) []float64 {
	usedCache := false
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	boardHash := hashBoard(state.Board, settings.BoardSize)
	ttKey := TTKey{Hash: boardHash, DepthLeft: depth, BoardSize: settings.BoardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, CurrentPlayer: settings.Player}
	cache := selectCache(ctx)
	var pvMove *Move
	if entry, ok := cache.TT[ttKey]; ok {
		pv := entry.BestMove
		pvMove = &pv
	}
	candidates := orderCandidates(state, ctx, settings.Player, true, settings.Config.AiTopCandidates, pvMove)
	mustBlock := hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(settings.Player), settings.BoardSize)
	for _, move := range candidates {
		if timedOut(ctx) {
			break
		}
		if settings.Config.AiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, settings.Player, settings.BoardSize) {
			win := winScore
			if settings.Player != ctx.settings.Player {
				win = -winScore
			}
			scores[move.Y*settings.BoardSize+move.X] = win
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return scores
		}
		if mustBlock {
			blockState := state.Clone()
			if !applyMove(&blockState, ctx.rules, move, settings.Player) {
				continue
			}
			if hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(settings.Player), settings.BoardSize) {
				continue
			}
		}
		idx := move.Y*settings.BoardSize + move.X
		cached := false
		score := evaluateMoveWithCache(state, ctx, settings.Player, move, depth, depth, boardHash, &cached, math.Inf(-1), math.Inf(1))
		if cached {
			usedCache = true
		}
		scores[idx] = score
	}
	if outUsedCache != nil {
		*outUsedCache = usedCache
	}
	return scores
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
	ctx := minimaxContext{rules: rules, settings: settings, start: time.Now()}
	if !hasStoneWithin(state.Board, settings.BoardSize) {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	initialCandidates := collectCandidateMoves(state.Board, settings.BoardSize)
	if len(initialCandidates) == 0 {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	cache := selectCache(ctx)
	boardHash := hashBoard(state.Board, settings.BoardSize)
	var scores []float64
	for depth := 1; depth <= settings.Depth; depth++ {
		if timedOut(ctx) {
			break
		}
		if settings.Config.AiQuickWinExit {
			for _, move := range initialCandidates {
				if isImmediateWinCached(cache, state, rules, move, settings.Player, settings.BoardSize) {
					winScores := make([]float64, settings.BoardSize*settings.BoardSize)
					for i := range winScores {
						winScores[i] = illegalScore
					}
					winScores[move.Y*settings.BoardSize+move.X] = winScore
					return winScores
				}
			}
		}
		key := cacheKey{Hash: boardHash, Depth: depth, BoardSize: settings.BoardSize, Player: settings.Player}
		cachedScores, ok := depthCache[key]
		cached := ok
		if !cached {
			usedCache := false
			scores = scoreBoardAtDepth(state, settings, ctx, depth, &usedCache)
			depthCache[key] = scores
			cached = usedCache
		} else {
			scores = cachedScores
		}
		if settings.Config.LogDepthScores {
			for _, move := range initialCandidates {
				score := scores[move.Y*settings.BoardSize+move.X]
				_ = score
			}
		}
		bestScore := math.Inf(-1)
		bestX, bestY := -1, -1
		for y := 0; y < settings.BoardSize; y++ {
			for x := 0; x < settings.BoardSize; x++ {
				score := scores[y*settings.BoardSize+x]
				if score > bestScore {
					bestScore = score
					bestX = x
					bestY = y
				}
			}
		}
		_ = bestX
		_ = bestY
		_ = cached
	}
	return scores
}

func TranspositionSize(cache AISearchCache) int {
	return cache.TTSize
}

func RerootCache(cache *AISearchCache, state GameState) {
	boardSize := state.Board.Size()
	cache.Root = makeStateKey(state, boardSize, state.ToMove)
	cache.HasRoot = true

	reachable := make(map[StateKey]struct{})
	stack := []StateKey{cache.Root}
	for len(stack) > 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := reachable[key]; ok {
			continue
		}
		reachable[key] = struct{}{}
		children, ok := cache.Edges[key]
		if !ok {
			continue
		}
		for _, child := range children {
			stack = append(stack, child)
		}
	}

	stateFromTT := func(key TTKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.CurrentPlayer}
	}
	stateFromMove := func(key MoveCacheKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.CurrentPlayer}
	}
	stateFromImmediateMove := func(key ImmediateWinKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.Player}
	}
	stateFromImmediateState := func(key ImmediateWinStateKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.Player}
	}

	for key := range cache.TT {
		if _, ok := reachable[stateFromTT(key)]; !ok {
			delete(cache.TT, key)
		}
	}
	cache.TTSize = len(cache.TT)

	for key := range cache.MoveCache {
		if _, ok := reachable[stateFromMove(key)]; !ok {
			delete(cache.MoveCache, key)
		}
	}
	for key := range cache.ImmediateWinMove {
		if _, ok := reachable[stateFromImmediateMove(key)]; !ok {
			delete(cache.ImmediateWinMove, key)
		}
	}
	for key := range cache.ImmediateWinState {
		if _, ok := reachable[stateFromImmediateState(key)]; !ok {
			delete(cache.ImmediateWinState, key)
		}
	}
	for key, children := range cache.Edges {
		if _, ok := reachable[key]; !ok {
			delete(cache.Edges, key)
			continue
		}
		filtered := children[:0]
		for _, child := range children {
			if _, ok := reachable[child]; ok {
				filtered = append(filtered, child)
			}
		}
		cache.Edges[key] = filtered
	}
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
