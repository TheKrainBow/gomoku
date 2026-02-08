package main

import (
	"fmt"
	"time"
)

type Game struct {
	settings           GameSettings
	rules              Rules
	state              GameState
	history            MoveHistory
	blackPlayer        IPlayer
	whitePlayer        IPlayer
	moveSuggestionAI   *AIPlayer
	moveSuggestionHash uint64
	turnStart          time.Time
	coordWidth         int
	captureWidth       int
	timeWidth          int
}

func NewGame(settings GameSettings) Game {
	g := Game{}
	g.Reset(settings)
	return g
}

func (g *Game) Reset(settings GameSettings) {
	g.stopMoveSuggestion(nil)
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
	prevCapturedBlack := g.state.CapturedBlack
	prevCapturedWhite := g.state.CapturedWhite
	prevToMove := g.state.ToMove
	notifyAiCaches := func() {
		if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok {
			aiBlack.OnMoveApplied(g.state, g.rules)
		}
		if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok {
			aiWhite.OnMoveApplied(g.state, g.rules)
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
		if g.state.ToMove == PlayerBlack {
			g.state.CapturedBlack += capturedCount
		} else {
			g.state.CapturedWhite += capturedCount
		}
	}
	var totalCaptured int
	if g.state.ToMove == PlayerBlack {
		totalCaptured = g.state.CapturedBlack
	} else {
		totalCaptured = g.state.CapturedWhite
	}
	g.logMovePlayed(move, elapsedMs, isAiMove, totalCaptured, capturedCount)
	g.history.Push(entry)
	requireCapture := false
	forcedCaptures := []Move{}

	captureCount := g.state.CapturedBlack
	if g.state.ToMove == PlayerWhite {
		captureCount = g.state.CapturedWhite
	}
	if captureCount >= g.settings.CaptureWinStones {
		g.logWin(g.state.ToMove, "capture")
		if g.state.ToMove == PlayerBlack {
			g.state.Status = StatusBlackWon
		} else {
			g.state.Status = StatusWhiteWon
		}
		g.state.WinningLine = nil
		g.state.WinningCapturePair = nil
		UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlack, prevCapturedWhite)
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
			if g.state.ToMove == PlayerBlack {
				g.state.Status = StatusBlackWon
			} else {
				g.state.Status = StatusWhiteWon
			}
			UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlack, prevCapturedWhite)
			notifyAiCaches()
			return true, ""
		}
		forcedCaptures = g.rules.FindAlignmentBreakCaptures(g.state, opponent)
		requireCapture = len(forcedCaptures) > 0
	}
	opponentCaptureCount := g.state.CapturedBlack
	if opponent == PlayerWhite {
		opponentCaptureCount = g.state.CapturedWhite
	}
	if forcedMove, forcedCaptures, ok := g.rules.FindImmediateCaptureWinMove(g.state, opponent, opponentCaptureCount); ok {
		// Commit current move first so forced opponent capture is applied on top of it.
		UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlack, prevCapturedWhite)

		forcedPrevCapturedBlack := g.state.CapturedBlack
		forcedPrevCapturedWhite := g.state.CapturedWhite
		g.state.ToMove = opponent
		g.state.Board.Set(forcedMove.X, forcedMove.Y, CellFromPlayer(opponent))
		for _, captured := range forcedCaptures {
			g.state.Board.Remove(captured.X, captured.Y)
		}
		if opponent == PlayerBlack {
			g.state.CapturedBlack += len(forcedCaptures)
		} else {
			g.state.CapturedWhite += len(forcedCaptures)
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
			if opponent == PlayerBlack {
				return g.state.CapturedBlack
			}
			return g.state.CapturedWhite
		}(), len(forcedCaptures))
		g.logWin(opponent, "capture-threat")
		if opponent == PlayerBlack {
			g.state.Status = StatusBlackWon
		} else {
			g.state.Status = StatusWhiteWon
		}
		g.state.LastMove = forcedMove
		g.state.HasLastMove = true
		g.state.WinningLine = nil
		g.state.WinningCapturePair = append([]Move(nil), forcedCaptures...)
		UpdateHashAfterMove(&g.state, forcedMove, opponent, forcedCaptures, opponent, forcedPrevCapturedBlack, forcedPrevCapturedWhite)
		notifyAiCaches()
		return true, ""
	}
	if g.rules.IsDraw(g.state.Board) {
		g.state.Status = StatusDraw
		g.state.WinningLine = nil
		g.state.WinningCapturePair = nil
		UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlack, prevCapturedWhite)
		notifyAiCaches()
		return true, ""
	}

	g.state.ToMove = otherPlayer(g.state.ToMove)
	UpdateHashAfterMove(&g.state, move, prevToMove, entry.CapturedPositions, prevToMove, prevCapturedBlack, prevCapturedWhite)
	if requireCapture {
		g.state.MustCapture = true
		g.state.ForcedCaptureMoves = forcedCaptures
	}
	g.turnStart = time.Now()
	notifyAiCaches()
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
	g.stopMoveSuggestion(ghostSink)
	ai, ok := player.(*AIPlayer)
	if ok {
		if ai.HasMoveReady() {
			move := ai.TakeMove()
			applied, _ := g.TryApplyMove(move)
			return applied
		}
		if move, ok := ai.TakePonderedMove(g.state.Clone(), g.rules); ok {
			applied, _ := g.TryApplyMove(move)
			return applied
		}
		if !ai.IsThinking() {
			var sink func(GameState)
			if ghostEnabled && ghostSink != nil {
				sink = func(gs GameState) {
					ghostSink(ghostPayload{
						Mode:      "preview_board",
						Positions: ghostPositionsFromBoard(gs.Board),
						Active:    true,
					})
				}
			}
			ai.StartThinking(g.state.Clone(), g.rules, sink, nil)
		}
		return false
	}
	move := player.ChooseMove(g.state.Clone(), g.rules)
	applied, _ := g.TryApplyMove(move)
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
	if color == PlayerBlack {
		return g.blackPlayer
	}
	return g.whitePlayer
}

func (g *Game) createPlayers() {
	if g.settings.BlackType == PlayerHuman {
		g.blackPlayer = NewHumanPlayer()
	} else {
		ai := NewAIPlayer()
		ai.SetHeuristicsOverride(g.settings.BlackHeuristics)
		g.blackPlayer = ai
	}
	if g.settings.WhiteType == PlayerHuman {
		g.whitePlayer = NewHumanPlayer()
	} else {
		ai := NewAIPlayer()
		ai.SetHeuristicsOverride(g.settings.WhiteHeuristics)
		g.whitePlayer = ai
	}
	if g.moveSuggestionAI == nil {
		g.moveSuggestionAI = NewAIPlayer()
	}
}

func (g *Game) syncAIPlayersToCurrentState() {
	if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok {
		aiBlack.OnMoveApplied(g.state, g.rules)
	}
	if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok {
		aiWhite.OnMoveApplied(g.state, g.rules)
	}
}

func (g *Game) logMatchup() {
	label := func(t PlayerType) string {
		if t == PlayerAI {
			return "AI"
		}
		return "Human"
	}
	_ = fmt.Sprintf("White (%s) vs Black (%s)", label(g.settings.WhiteType), label(g.settings.BlackType))
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
	if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok && aiBlack.HasGhostBoard() {
		return true
	}
	if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok && aiWhite.HasGhostBoard() {
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
	if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok && aiBlack.HasGhostBoard() {
		return aiBlack.GhostBoardCopy(), true
	}
	if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok && aiWhite.HasGhostBoard() {
		return aiWhite.GhostBoardCopy(), true
	}
	return Board{}, false
}

func (g *Game) ResetForConfigChange() {
	g.stopMoveSuggestion(nil)
	if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok {
		aiBlack.ResetForConfigChange()
	}
	if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok {
		aiWhite.ResetForConfigChange()
	}
	if g.moveSuggestionAI != nil {
		g.moveSuggestionAI.ResetForConfigChange()
	}
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
	suggestionConfig := GetConfig()
	suggestionConfig.AiDepth = 10
	suggestionConfig.AiMaxDepth = 10
	suggestionConfig.AiMinDepth = 1
	suggestionConfig.AiTimeoutMs = 0
	suggestionConfig.AiTimeBudgetMs = 0
	heuristicHash := heuristicHashFromConfig(suggestionConfig)
	if tt := ensureTT(SharedSearchCache(), suggestionConfig); tt != nil {
		if entry, ok := tt.Probe(hash, heuristicHash); ok && entry.Flag == TTExact && entry.BestMove.IsValid(state.Board.Size()) {
			if legal, _ := g.rules.IsLegal(state, entry.BestMove, state.ToMove); legal {
				knownDepth := entry.Depth
				if knownDepth > 10 {
					knownDepth = 10
				}
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
					if knownDepth >= 10 {
						return
					}
					if knownDepth+1 > suggestionConfig.AiMinDepth {
						suggestionConfig.AiMinDepth = knownDepth + 1
					}
				}
			}
		}
	}
	g.moveSuggestionAI.StartThinkingWithConfig(state, g.rules, nil, func(move Move, depth int, score float64) {
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
