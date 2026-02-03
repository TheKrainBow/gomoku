package main

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type AIPlayer struct {
	delayMs     int
	ghostMutex  sync.Mutex
	moveMutex   sync.Mutex
	cacheMutex  sync.Mutex
	workerDone  chan struct{}
	thinking    atomic.Bool
	moveReady   atomic.Bool
	ghostActive atomic.Bool
	stopSignal  atomic.Bool
	readyMove   Move
	ghostBoard  Board
	cache       AISearchCache
}

func NewAIPlayer(moveDelayMs int) *AIPlayer {
	return &AIPlayer{
		delayMs: moveDelayMs,
		cache:   newAISearchCache(),
	}
}

func (a *AIPlayer) IsHuman() bool {
	return false
}

func (a *AIPlayer) ChooseMove(state GameState, rules Rules) Move {
	config := GetConfig()
	delay := a.delayMs
	if config.AiMoveDelayMs > 0 {
		delay = config.AiMoveDelayMs
	}
	if delay > 0 {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	settings := AIScoreSettings{
		Depth:     config.AiDepth,
		TimeoutMs: config.AiTimeoutMs,
		BoardSize: state.Board.Size(),
		Player:    state.ToMove,
		Cache:     &a.cache,
		Config:    config,
	}
	a.cacheMutex.Lock()
	scores := ScoreBoard(state, rules, settings)
	a.cacheMutex.Unlock()
	bestScore := math.Inf(-1)
	bestMove := Move{}
	size := settings.BoardSize
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			move := Move{X: x, Y: y}
			score := scores[y*size+x]
			if score > bestScore {
				if ok, _ := rules.IsLegal(state, move, state.ToMove); ok {
					bestScore = score
					bestMove = move
				}
			}
		}
	}
	if !math.IsInf(bestScore, -1) {
		return bestMove
	}
	return Move{}
}

func (a *AIPlayer) StartThinking(state GameState, rules Rules, ghostSink func(GameState)) {
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
	config := GetConfig()
	go func() {
		defer close(done)
		delay := a.delayMs
		if config.AiMoveDelayMs > 0 {
			delay = config.AiMoveDelayMs
		}
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		settings := AIScoreSettings{
			Depth:      config.AiDepth,
			TimeoutMs:  config.AiTimeoutMs,
			BoardSize:  stateCopy.Board.Size(),
			Player:     stateCopy.ToMove,
			Cache:      &a.cache,
			Config:     config,
			ShouldStop: func() bool { return a.stopSignal.Load() },
		}
		if config.GhostMode && ghostSink != nil {
			settings.OnGhostUpdate = func(gs GameState) {
				a.ghostMutex.Lock()
				a.ghostBoard = gs.Board.Clone()
				a.ghostMutex.Unlock()
				a.ghostActive.Store(true)
				ghostSink(gs)
			}
		}
		a.cacheMutex.Lock()
		scores := ScoreBoard(stateCopy, rulesCopy, settings)
		a.cacheMutex.Unlock()
		if a.stopSignal.Load() {
			a.moveReady.Store(false)
			a.ghostActive.Store(false)
			a.thinking.Store(false)
			return
		}
		bestScore := math.Inf(-1)
		bestMove := Move{}
		size := settings.BoardSize
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				move := Move{X: x, Y: y}
				score := scores[y*size+x]
				if score > bestScore {
					if ok, _ := rulesCopy.IsLegal(stateCopy, move, stateCopy.ToMove); ok {
						bestScore = score
						bestMove = move
					}
				}
			}
		}
		a.moveMutex.Lock()
		a.readyMove = bestMove
		a.moveMutex.Unlock()
		a.moveReady.Store(true)
		a.ghostActive.Store(false)
		a.thinking.Store(false)
	}()
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

func (a *AIPlayer) OnMoveApplied(state GameState) {
	a.cacheMutex.Lock()
	defer a.cacheMutex.Unlock()
	RerootCache(&a.cache, state)
}

func (a *AIPlayer) CacheSize() int {
	a.cacheMutex.Lock()
	defer a.cacheMutex.Unlock()
	return TranspositionSize(a.cache)
}

func (a *AIPlayer) ResetForConfigChange() {
	a.stopSignal.Store(true)
	a.cacheMutex.Lock()
	a.cache = newAISearchCache()
	a.cacheMutex.Unlock()
}
