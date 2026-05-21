package main

import (
	"fmt"
	"log"
	"sort"
	"time"
)

type Game struct {
	settings           GameSettings
	rules              Rules
	state              GameState
	history            MoveHistory
	bluePlayer         IPlayer
	redPlayer          IPlayer
	moveSuggestionAI   *AIPlayer
	moveSuggestionHash uint64
	turnStart          time.Time
	coordWidth         int
	captureWidth       int
	timeWidth          int
	turnLogger         *gameTurnLogger
}

func NewGame(settings GameSettings) Game {
	g := Game{}
	g.Reset(settings)
	return g
}

func (g *Game) Reset(settings GameSettings) {
	g.stopMoveSuggestion(nil)
	if g.turnLogger != nil {
		g.turnLogger.close()
		g.turnLogger = nil
	}
	g.settings = settings
	g.rules = NewRules(settings)
	g.state.Reset(settings)
	g.history.Clear()
	g.createPlayers()
	g.computeLogWidths()
	g.turnStart = time.Now()
	g.logMatchup()
}

func (g *Game) Start() {
	if g.state.Status == StatusNotStarted {
		g.state.Status = StatusRunning
		g.turnStart = time.Now()
		g.stopMoveSuggestion(nil)
		if g.turnLogger == nil {
			if logger, err := newGameTurnLogger(g.settings, g.turnStart); err == nil {
				g.turnLogger = logger
			} else {
				log.Printf("[game-log] logger init failed: %v", err)
			}
		}
		g.syncAIPlayersToCurrentState()
	}
}

func (g *Game) State() GameState {
	return g.state.Clone()
}

func (g *Game) History() MoveHistory {
	return g.history
}

func (g *Game) TurnStartedAtMs() int64 {
	if g.turnStart.IsZero() {
		return 0
	}
	return g.turnStart.UnixMilli()
}

func (g *Game) TryApplyMove(move Move) (bool, string) {
	if g.state.Status != StatusRunning {
		return false, "game not running"
	}
	prevCapturedBlue := g.state.CapturedBlue
	prevCapturedRed := g.state.CapturedRed
	prevToMove := g.state.ToMove
	notifyAiCaches := func() {
		if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok {
			aiBlue.OnMoveApplied(g.state, g.rules)
		}
		if aiRed, ok := g.redPlayer.(*AIPlayer); ok {
			aiRed.OnMoveApplied(g.state, g.rules)
		}
	}
	player := g.currentPlayer()
	isAiMove := player != nil && !player.IsHuman()
	ok, reason := g.rules.IsLegalDefault(g.state, move)
	if !ok {
		g.state.LastMessage = "Illegal move: " + reason
		return false, g.state.LastMessage
	}
	g.stopMoveSuggestion(nil)
	g.state.LastMessage = ""
	elapsedMs := float64(time.Since(g.turnStart).Milliseconds())
	cell := CellFromPlayer(g.state.ToMove)
	g.state.Board.Set(move.X, move.Y, cell)
	g.state.LastMove = move
	g.state.HasLastMove = true
	g.state.MustCapture = false
	g.state.ForcedCaptureMoves = nil
	g.state.WinningLine = nil
	g.state.WinningCapturePair = nil

	entry := HistoryEntry{Move: move, Player: g.state.ToMove, ElapsedMs: elapsedMs, IsAi: isAiMove, Depth: move.Depth}
	entry.CapturedPositions = g.rules.FindCaptures(g.state.Board, move, cell)
	entry.CapturedCount = len(entry.CapturedPositions)
	for _, captured := range entry.CapturedPositions {
		g.state.Board.Remove(captured.X, captured.Y)
	}
	capturedCount := entry.CapturedCount
	if capturedCount > 0 {
		if g.state.ToMove == PlayerBlue {
			g.state.CapturedBlue += capturedCount
		} else {
			g.state.CapturedRed += capturedCount
		}
	}
	var totalCaptured int
	if g.state.ToMove == PlayerBlue {
		totalCaptured = g.state.CapturedBlue
	} else {
		totalCaptured = g.state.CapturedRed
	}
	g.logMovePlayed(move, elapsedMs, isAiMove, totalCaptured, capturedCount)
	g.history.Push(entry)
	requireCapture := false
	forcedCaptures := []Move{}

	captureCount := g.state.CapturedBlue
	if g.state.ToMove == PlayerRed {
		captureCount = g.state.CapturedRed
	}
	if captureCount >= g.settings.CaptureWinStones {
		g.logWin(g.state.ToMove, "capture")
		if g.state.ToMove == PlayerBlue {
			g.state.Status = StatusBlueWon
		} else {
			g.state.Status = StatusRedWon
		}
		g.state.WinningLine = nil
		g.state.WinningCapturePair = nil
		UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlue, prevCapturedRed)
		notifyAiCaches()
		return true, ""
	}

	opponent := otherPlayer(g.state.ToMove)
	if g.rules.IsWin(g.state.Board, move) {
		if !g.rules.OpponentCanBreakAlignmentByCapture(g.state, opponent) {
			line, ok := g.rules.FindAlignmentLine(g.state.Board, move)
			if ok {
				g.state.WinningLine = line
			}
			g.state.WinningCapturePair = nil
			g.logWin(g.state.ToMove, "alignment")
			if g.state.ToMove == PlayerBlue {
				g.state.Status = StatusBlueWon
			} else {
				g.state.Status = StatusRedWon
			}
			UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlue, prevCapturedRed)
			notifyAiCaches()
			return true, ""
		}
		forcedCaptures = g.rules.FindAlignmentBreakCaptures(g.state, opponent)
		requireCapture = len(forcedCaptures) > 0
	}
	opponentCaptureCount := g.state.CapturedBlue
	if opponent == PlayerRed {
		opponentCaptureCount = g.state.CapturedRed
	}
	if forcedMove, forcedCaptures, ok := g.rules.FindImmediateCaptureWinMove(g.state, opponent, opponentCaptureCount); ok {
		// Commit current move first so forced opponent capture is applied on top of it.
		UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlue, prevCapturedRed)

		forcedPrevCapturedBlue := g.state.CapturedBlue
		forcedPrevCapturedRed := g.state.CapturedRed
		g.state.ToMove = opponent
		g.state.Board.Set(forcedMove.X, forcedMove.Y, CellFromPlayer(opponent))
		for _, captured := range forcedCaptures {
			g.state.Board.Remove(captured.X, captured.Y)
		}
		if opponent == PlayerBlue {
			g.state.CapturedBlue += len(forcedCaptures)
		} else {
			g.state.CapturedRed += len(forcedCaptures)
		}
		forcedEntry := HistoryEntry{
			Move:              forcedMove,
			Player:            opponent,
			ElapsedMs:         0,
			IsAi:              !g.playerForColor(opponent).IsHuman(),
			CapturedCount:     len(forcedCaptures),
			CapturedPositions: append([]Move(nil), forcedCaptures...),
		}
		g.history.Push(forcedEntry)
		g.logMovePlayed(forcedMove, 0, forcedEntry.IsAi, func() int {
			if opponent == PlayerBlue {
				return g.state.CapturedBlue
			}
			return g.state.CapturedRed
		}(), len(forcedCaptures))
		g.logWin(opponent, "capture-threat")
		if opponent == PlayerBlue {
			g.state.Status = StatusBlueWon
		} else {
			g.state.Status = StatusRedWon
		}
		g.state.LastMove = forcedMove
		g.state.HasLastMove = true
		g.state.WinningLine = nil
		g.state.WinningCapturePair = append([]Move(nil), forcedCaptures...)
		UpdateHashAfterMove(&g.state, forcedMove, opponent, forcedCaptures, opponent, forcedPrevCapturedBlue, forcedPrevCapturedRed)
		notifyAiCaches()
		return true, ""
	}
	if g.rules.IsDraw(g.state.Board) {
		g.state.Status = StatusDraw
		g.state.WinningLine = nil
		g.state.WinningCapturePair = nil
		UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlue, prevCapturedRed)
		notifyAiCaches()
		return true, ""
	}

	g.state.ToMove = otherPlayer(g.state.ToMove)
	UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlue, prevCapturedRed)
	if requireCapture {
		g.state.MustCapture = true
		g.state.ForcedCaptureMoves = forcedCaptures
	}
	g.turnStart = time.Now()
	notifyAiCaches()
	return true, ""
}

func (g *Game) UndoToHistoryIndex(index int) (bool, string) {
	if index < -1 {
		return false, "invalid history index"
	}
	currentSize := g.history.Size()
	targetSize := index + 1
	if targetSize > currentSize {
		return false, "invalid history index"
	}
	if targetSize == currentSize {
		return true, ""
	}

	g.stopMoveSuggestion(nil)
	if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok {
		aiBlue.StopThinking()
	}
	if aiRed, ok := g.redPlayer.(*AIPlayer); ok {
		aiRed.StopThinking()
	}

	for g.history.Size() > targetSize {
		entry, ok := g.history.Pop()
		if !ok {
			break
		}
		g.undoHistoryEntry(entry)
	}

	if last, ok := g.history.Last(); ok {
		g.state.LastMove = last.Move
		g.state.HasLastMove = true
	} else {
		g.state.LastMove = Move{X: -1, Y: -1}
		g.state.HasLastMove = false
	}

	g.state.Status = StatusRunning
	g.state.MustCapture = false
	g.state.ForcedCaptureMoves = nil
	g.state.LastMessage = ""
	g.state.WinningLine = nil
	g.state.WinningCapturePair = nil
	g.state.recomputeHashes()
	g.turnStart = time.Now()
	g.syncAIPlayersToCurrentState()
	return true, ""
}

func (g *Game) undoHistoryEntry(entry HistoryEntry) {
	g.state.Board.Remove(entry.Move.X, entry.Move.Y)

	restoredCell := CellFromPlayer(otherPlayer(entry.Player))
	for _, captured := range entry.CapturedPositions {
		g.state.Board.Set(captured.X, captured.Y, restoredCell)
	}

	if entry.Player == PlayerBlue {
		g.state.CapturedBlue -= entry.CapturedCount
		if g.state.CapturedBlue < 0 {
			g.state.CapturedBlue = 0
		}
	} else {
		g.state.CapturedRed -= entry.CapturedCount
		if g.state.CapturedRed < 0 {
			g.state.CapturedRed = 0
		}
	}

	g.state.ToMove = entry.Player
}

func (g *Game) ApplyBoardSnapshot(payload applyBoardPayload) (bool, string) {
	size := g.settings.BoardSize
	if payload.NextPlayer != 1 && payload.NextPlayer != 2 {
		return false, "invalid next player"
	}
	if payload.CapturedBlue < 0 || payload.CapturedRed < 0 {
		return false, "capture counts must be positive"
	}

	g.stopMoveSuggestion(nil)
	if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok {
		aiBlue.StopThinking()
	}
	if aiRed, ok := g.redPlayer.(*AIPlayer); ok {
		aiRed.StopThinking()
	}

	g.state.Board.Reset(size)
	g.history.Clear()

	occupied := make(map[int]struct{}, len(payload.Cells))
	entries := make([]applyBoardCellPayload, 0, len(payload.Cells))
	for _, cell := range payload.Cells {
		if !(Move{X: cell.X, Y: cell.Y}).IsValid(size) {
			return false, "cell outside board"
		}
		if cell.Cell != "CellBlue" && cell.Cell != "CellRed" {
			return false, "invalid cell value"
		}
		key := cell.Y*size + cell.X
		if _, exists := occupied[key]; exists {
			return false, "duplicate cell"
		}
		occupied[key] = struct{}{}
		entries = append(entries, cell)
		if cell.Cell == "CellBlue" {
			g.state.Board.Set(cell.X, cell.Y, CellBlue)
		} else {
			g.state.Board.Set(cell.X, cell.Y, CellRed)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		left := entries[i]
		right := entries[j]
		leftMove := left.MoveNumber
		rightMove := right.MoveNumber
		if leftMove <= 0 && rightMove <= 0 {
			if left.Y != right.Y {
				return left.Y < right.Y
			}
			return left.X < right.X
		}
		if leftMove <= 0 {
			return false
		}
		if rightMove <= 0 {
			return true
		}
		return leftMove < rightMove
	})
	for _, entry := range entries {
		player := PlayerBlue
		if entry.Cell == "CellRed" {
			player = PlayerRed
		}
		g.history.Push(HistoryEntry{
			Move:   Move{X: entry.X, Y: entry.Y},
			Player: player,
		})
	}

	g.state.ToMove = intToPlayer(payload.NextPlayer)
	g.state.CapturedBlue = payload.CapturedBlue
	g.state.CapturedRed = payload.CapturedRed
	g.state.Status = StatusRunning
	g.state.MustCapture = false
	g.state.ForcedCaptureMoves = nil
	g.state.LastMessage = ""
	g.state.WinningLine = nil
	g.state.WinningCapturePair = nil
	if last, ok := g.history.Last(); ok {
		g.state.LastMove = last.Move
		g.state.HasLastMove = true
	} else {
		g.state.LastMove = Move{X: -1, Y: -1}
		g.state.HasLastMove = false
	}
	g.state.recomputeHashes()
	g.turnStart = time.Now()
	g.syncAIPlayersToCurrentState()
	return true, ""
}

func (g *Game) Tick(ghostEnabled bool, ghostSink func(ghostPayload)) bool {
	if g.state.Status != StatusRunning {
		g.stopMoveSuggestion(ghostSink)
		return false
	}
	player := g.currentPlayer()
	if player == nil {
		g.stopMoveSuggestion(ghostSink)
		return false
	}
	if player.IsHuman() {
		if ghostEnabled && ghostSink != nil {
			g.startMoveSuggestion(ghostSink)
		} else {
			g.stopMoveSuggestion(ghostSink)
		}
		human, ok := player.(*HumanPlayer)
		if ok && human.HasPendingMove() {
			move := human.TakePendingMove()
			applied, _ := g.TryApplyMove(move)
			return applied
		}
		return false
	}
	if ghostEnabled && ghostSink != nil {
		g.stopMoveSuggestion(nil)
	} else {
		g.stopMoveSuggestion(ghostSink)
	}
	ai, ok := player.(*AIPlayer)
	if ok {
		if ai.HasMoveReady() {
			turnNumber := g.history.Size() + 1
			move, decision := ai.TakeMoveWithDecision()
			applied, _ := g.TryApplyMove(move)
			if applied {
				g.enqueueTurnDecisionLog(turnNumber, decision)
			}
			return applied
		}
		if move, decision, ok := ai.TakePonderedMoveWithDecision(g.state.Clone(), g.rules); ok {
			turnNumber := g.history.Size() + 1
			applied, _ := g.TryApplyMove(move)
			if applied {
				g.enqueueTurnDecisionLog(turnNumber, decision)
			}
			return applied
		}
		if !ai.IsThinking() {
			var depthSink func(Move, int, float64)
			if ghostEnabled && ghostSink != nil {
				historyLen := g.history.Size()
				toMove := playerToInt(g.state.ToMove)
				depthSink = func(move Move, depth int, score float64) {
					ghostSink(ghostPayload{
						Mode:       "best_move",
						Best:       &ghostCell{X: move.X, Y: move.Y, Player: toMove},
						Depth:      depth,
						Score:      score,
						NextPlayer: toMove,
						HistoryLen: historyLen,
						Active:     true,
					})
				}
			}
			ai.StartThinking(g.state.Clone(), g.rules, nil, depthSink)
		}
		return false
	}
	turnNumber := g.history.Size() + 1
	move := player.ChooseMove(g.state.Clone(), g.rules)
	applied, _ := g.TryApplyMove(move)
	if applied {
		if ai, ok := player.(*AIPlayer); ok {
			g.enqueueTurnDecisionLog(turnNumber, ai.TakeLastDecision())
		}
	}
	return applied
}

func (g *Game) SubmitHumanMove(move Move) bool {
	player := g.currentPlayer()
	if player == nil || !player.IsHuman() {
		return false
	}
	human, ok := player.(*HumanPlayer)
	if !ok {
		return false
	}
	human.SetPendingMove(move)
	return true
}

func (g *Game) CurrentPlayerIsHuman() bool {
	player := g.currentPlayer()
	return player != nil && player.IsHuman()
}

func (g *Game) currentPlayer() IPlayer {
	return g.playerForColor(g.state.ToMove)
}

func (g *Game) playerForColor(color PlayerColor) IPlayer {
	if color == PlayerBlue {
		return g.bluePlayer
	}
	return g.redPlayer
}

func (g *Game) createPlayers() {
	if g.settings.BlueType == PlayerHuman {
		g.bluePlayer = NewHumanPlayer()
	} else {
		ai := NewAIPlayer()
		ai.SetHeuristicsOverride(g.settings.BlueHeuristics)
		g.bluePlayer = ai
	}
	if g.settings.RedType == PlayerHuman {
		g.redPlayer = NewHumanPlayer()
	} else {
		ai := NewAIPlayer()
		ai.SetHeuristicsOverride(g.settings.RedHeuristics)
		g.redPlayer = ai
	}
	if g.moveSuggestionAI == nil {
		g.moveSuggestionAI = NewAIPlayer()
	}
}

func (g *Game) syncAIPlayersToCurrentState() {
	if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok {
		aiBlue.OnMoveApplied(g.state, g.rules)
	}
	if aiRed, ok := g.redPlayer.(*AIPlayer); ok {
		aiRed.OnMoveApplied(g.state, g.rules)
	}
}

func (g *Game) logMatchup() {
	label := func(t PlayerType) string {
		if t == PlayerAI {
			return "AI"
		}
		return "Human"
	}
	_ = fmt.Sprintf("Red (%s) vs Blue (%s)", label(g.settings.RedType), label(g.settings.BlueType))
}

func (g *Game) logMovePlayed(move Move, elapsedMs float64, isAiMove bool, totalCaptured int, capturedDelta int) {
	_ = move
	_ = elapsedMs
	_ = isAiMove
	_ = totalCaptured
	_ = capturedDelta
}

func (g *Game) logWin(player PlayerColor, reason string) {
	_ = player
	_ = reason
}

func (g *Game) enqueueTurnDecisionLog(turnNumber int, decision AITurnDecisionLog) {
	if g.turnLogger == nil {
		return
	}
	if !decision.Move.IsValid(g.settings.BoardSize) {
		return
	}
	if len(decision.RootCandidates) == 0 {
		return
	}
	g.turnLogger.enqueue(turnNumber, decision)
}

func (g *Game) computeLogWidths() {
	digits := func(value int) int {
		width := 1
		for value >= 10 {
			value /= 10
			width++
		}
		return width
	}
	maxCoord := g.settings.BoardSize - 1
	if maxCoord < 0 {
		maxCoord = 0
	}
	g.coordWidth = digits(maxCoord)
	g.captureWidth = digits(g.settings.CaptureWinStones)
	g.timeWidth = 0
}

func (g *Game) HasGhostBoard() bool {
	if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok && aiBlue.HasGhostBoard() {
		return true
	}
	if aiRed, ok := g.redPlayer.(*AIPlayer); ok && aiRed.HasGhostBoard() {
		return true
	}
	return false
}

func (g *Game) AiThinking() bool {
	player := g.currentPlayer()
	ai, ok := player.(*AIPlayer)
	if ok {
		return ai.IsThinking()
	}
	return false
}

func (g *Game) GhostBoard() (Board, bool) {
	if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok && aiBlue.HasGhostBoard() {
		return aiBlue.GhostBoardCopy(), true
	}
	if aiRed, ok := g.redPlayer.(*AIPlayer); ok && aiRed.HasGhostBoard() {
		return aiRed.GhostBoardCopy(), true
	}
	return Board{}, false
}

func (g *Game) ResetForConfigChange() {
	g.stopMoveSuggestion(nil)
	if aiBlue, ok := g.bluePlayer.(*AIPlayer); ok {
		aiBlue.ResetForConfigChange()
	}
	if aiRed, ok := g.redPlayer.(*AIPlayer); ok {
		aiRed.ResetForConfigChange()
	}
	if g.moveSuggestionAI != nil {
		g.moveSuggestionAI.ResetForConfigChange()
	}
}

func suggestionAIConfig(config Config, boardSize int) Config {
	adjusted := config
	adjusted.AiTimeBudgetMs = 0
	adjusted.AiTimeoutMs = 0
	adjusted.AiPonderingEnabled = false
	adjusted.AiQueueEnabled = false
	adjusted.AiEnableTtPersistence = false
	if adjusted.AiMinDepth < 1 {
		adjusted.AiMinDepth = 1
	}
	maxDepth := boardSize * boardSize
	if maxDepth < adjusted.AiDepth {
		maxDepth = adjusted.AiDepth
	}
	if maxDepth < adjusted.AiMaxDepth {
		maxDepth = adjusted.AiMaxDepth
	}
	adjusted.AiDepth = maxDepth
	adjusted.AiMaxDepth = maxDepth
	return adjusted
}

func (g *Game) startMoveSuggestion(ghostSink func(ghostPayload)) {
	if g.moveSuggestionAI == nil {
		g.moveSuggestionAI = NewAIPlayer()
	}
	state := g.state.Clone()
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	hash := ttKeyFor(state, state.Board.Size())
	if g.moveSuggestionHash == hash && (g.moveSuggestionAI.IsThinking() || g.moveSuggestionAI.HasMoveReady()) {
		return
	}
	g.moveSuggestionAI.StopThinking()
	g.moveSuggestionHash = hash
	historyLen := g.history.Size()
	toMove := playerToInt(state.ToMove)
	suggestionConfig := suggestionAIConfig(GetConfig(), state.Board.Size())
	heuristicHash := heuristicHashFromConfig(suggestionConfig)
	if tt := ensureTT(SharedSearchCache(), suggestionConfig); tt != nil {
		if entry, ok := tt.Probe(hash, heuristicHash); ok && entry.Flag == TTExact && entry.BestMove.IsValid(state.Board.Size()) {
			if legal, _ := g.rules.IsLegal(state, entry.BestMove, state.ToMove); legal {
				knownDepth := entry.Depth
				if knownDepth > 0 {
					ghostSink(ghostPayload{
						Mode:       "best_move",
						Best:       &ghostCell{X: entry.BestMove.X, Y: entry.BestMove.Y, Player: toMove},
						Depth:      knownDepth,
						Score:      entry.ScoreFloat(),
						NextPlayer: toMove,
						HistoryLen: historyLen,
						Active:     true,
					})
					if knownDepth+1 > suggestionConfig.AiMinDepth {
						suggestionConfig.AiMinDepth = knownDepth + 1
					}
				}
			}
		}
	}
	g.moveSuggestionAI.StartThinkingWithRawConfig(state, g.rules, nil, func(move Move, depth int, score float64) {
		ghostSink(ghostPayload{
			Mode:       "best_move",
			Best:       &ghostCell{X: move.X, Y: move.Y, Player: toMove},
			Depth:      depth,
			Score:      score,
			NextPlayer: toMove,
			HistoryLen: historyLen,
			Active:     true,
		})
	}, suggestionConfig)
}

func (g *Game) stopMoveSuggestion(ghostSink func(ghostPayload)) {
	g.moveSuggestionHash = 0
	if g.moveSuggestionAI != nil {
		g.moveSuggestionAI.StopThinking()
	}
	if ghostSink != nil {
		ghostSink(ghostPayload{
			Mode:   "best_move",
			Active: false,
		})
	}
}
