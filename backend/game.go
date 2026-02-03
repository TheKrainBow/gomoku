package main

import (
	"fmt"
	"time"
)

type Game struct {
	settings     GameSettings
	rules        Rules
	state        GameState
	history      MoveHistory
	blackPlayer  IPlayer
	whitePlayer  IPlayer
	turnStart    time.Time
	coordWidth   int
	captureWidth int
	timeWidth    int
}

func NewGame(settings GameSettings) Game {
	g := Game{}
	g.Reset(settings)
	return g
}

func (g *Game) Reset(settings GameSettings) {
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
	}
}

func (g *Game) State() GameState {
	return g.state.Clone()
}

func (g *Game) History() MoveHistory {
	return g.history
}

func (g *Game) TryApplyMove(move Move) (bool, string) {
	if g.state.Status != StatusRunning {
		return false, "game not running"
	}
	notifyAiCaches := func() {
		if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok {
			aiBlack.OnMoveApplied(g.state)
		}
		if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok {
			aiWhite.OnMoveApplied(g.state)
		}
	}
	player := g.currentPlayer()
	isAiMove := player != nil && !player.IsHuman()
	ok, reason := g.rules.IsLegalDefault(g.state, move)
	if !ok {
		g.state.LastMessage = "Illegal move: " + reason
		return false, g.state.LastMessage
	}
	g.state.LastMessage = ""
	elapsedMs := float64(time.Since(g.turnStart).Milliseconds())
	cell := CellFromPlayer(g.state.ToMove)
	g.state.Board.Set(move.X, move.Y, cell)
	g.state.LastMove = move
	g.state.HasLastMove = true
	g.state.MustCapture = false
	g.state.ForcedCaptureMoves = nil

	entry := HistoryEntry{Move: move, Player: g.state.ToMove, ElapsedMs: elapsedMs, IsAi: isAiMove}
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
		notifyAiCaches()
		return true, ""
	}

	if g.rules.IsWin(g.state.Board, move) {
		opponent := otherPlayer(g.state.ToMove)
		if !g.rules.OpponentCanBreakAlignmentByCapture(g.state, opponent) {
			line, ok := g.rules.FindAlignmentLine(g.state.Board, move)
			if ok {
				g.state.WinningLine = line
			}
			g.logWin(g.state.ToMove, "alignment")
			if g.state.ToMove == PlayerBlack {
				g.state.Status = StatusBlackWon
			} else {
				g.state.Status = StatusWhiteWon
			}
			notifyAiCaches()
			return true, ""
		}
		forcedCaptures = g.rules.FindAlignmentBreakCaptures(g.state, opponent)
		requireCapture = len(forcedCaptures) > 0
	}
	if g.rules.IsDraw(g.state.Board) {
		g.state.Status = StatusDraw
		notifyAiCaches()
		return true, ""
	}

	g.state.ToMove = otherPlayer(g.state.ToMove)
	if requireCapture {
		g.state.MustCapture = true
		g.state.ForcedCaptureMoves = forcedCaptures
	}
	g.turnStart = time.Now()
	notifyAiCaches()
	return true, ""
}

func (g *Game) Tick(ghostEnabled bool, ghostSink func(Board)) bool {
	if g.state.Status != StatusRunning {
		return false
	}
	player := g.currentPlayer()
	if player == nil {
		return false
	}
	if player.IsHuman() {
		human, ok := player.(*HumanPlayer)
		if ok && human.HasPendingMove() {
			move := human.TakePendingMove()
			applied, _ := g.TryApplyMove(move)
			return applied
		}
		return false
	}
	ai, ok := player.(*AIPlayer)
	if ok {
		if ai.HasMoveReady() {
			move := ai.TakeMove()
			applied, _ := g.TryApplyMove(move)
			return applied
		}
		if !ai.IsThinking() {
			var sink func(GameState)
			if ghostEnabled && ghostSink != nil {
				sink = func(gs GameState) {
					ghostSink(gs.Board.Clone())
				}
			}
			ai.StartThinking(g.state.Clone(), g.rules, sink)
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
		g.blackPlayer = NewAIPlayer(g.settings.AiMoveDelayMs)
	}
	if g.settings.WhiteType == PlayerHuman {
		g.whitePlayer = NewHumanPlayer()
	} else {
		g.whitePlayer = NewAIPlayer(g.settings.AiMoveDelayMs)
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
	if aiBlack, ok := g.blackPlayer.(*AIPlayer); ok {
		aiBlack.ResetForConfigChange()
	}
	if aiWhite, ok := g.whitePlayer.(*AIPlayer); ok {
		aiWhite.ResetForConfigChange()
	}
}
