package main

import "sync"

type GameController struct {
	mu             sync.Mutex
	game           Game
	ghostEnabled   func() bool
	ghostPublisher func(ghostPayload)
}

func NewGameController(settings GameSettings) *GameController {
	return &GameController{game: NewGame(settings)}
}

func (gc *GameController) SetGhostPublisher(enabled func() bool, publisher func(ghostPayload)) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.ghostEnabled = enabled
	gc.ghostPublisher = publisher
}

func (gc *GameController) OnCellClicked(x, y int) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	_ = gc.game.SubmitHumanMove(Move{X: x, Y: y})
}

func (gc *GameController) ApplyHumanMove(move Move) (bool, string) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if !gc.game.CurrentPlayerIsHuman() {
		return false, "not human turn"
	}
	return gc.game.TryApplyMove(move)
}

func (gc *GameController) Tick() bool {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	ghostEnabled := false
	if gc.ghostEnabled != nil {
		ghostEnabled = gc.ghostEnabled()
	}
	return gc.game.Tick(ghostEnabled, gc.ghostPublisher)
}

func (gc *GameController) State() GameState {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.State()
}

func (gc *GameController) Settings() GameSettings {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.settings
}

func (gc *GameController) History() MoveHistory {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.History()
}

func (gc *GameController) CurrentTurnStartedAtMs() int64 {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.TurnStartedAtMs()
}

func (gc *GameController) LatestHistoryEntry() (HistoryEntry, bool) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	history := gc.game.History()
	if history.Size() == 0 {
		return HistoryEntry{}, false
	}
	entries := history.All()
	return entries[len(entries)-1], true
}

func (gc *GameController) AiThinking() bool {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.AiThinking()
}

func (gc *GameController) HasGhostBoard() bool {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.HasGhostBoard()
}

func (gc *GameController) GhostBoard() (Board, bool) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.game.GhostBoard()
}

func (gc *GameController) Reset(settings GameSettings) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.game.Reset(settings)
}

func (gc *GameController) StartGame(settings GameSettings) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.game.Reset(settings)
	gc.game.Start()
}

func (gc *GameController) UpdateSettings(update GameSettings, reset bool) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if reset {
		gc.game.Reset(update)
		return
	}
	gc.game.settings = update
	gc.game.createPlayers()
}

func (gc *GameController) ResetForConfigChange() {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.game.ResetForConfigChange()
}
