package main

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type backlogTask struct {
	state   GameState
	rules   Rules
	created time.Time
}

type searchBacklog struct {
	mu          sync.Mutex
	queue       []backlogTask
	present     map[uint64]struct{}
	currentHash uint64
	currentSet  bool
	stop        atomic.Bool
	limitWarned bool
}

var searchBacklogManager = newSearchBacklog()

func newSearchBacklog() *searchBacklog {
	return &searchBacklog{present: make(map[uint64]struct{})}
}

func enqueueSearchBacklogTask(state GameState, rules Rules) {
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	task := backlogTask{state: state.Clone(), rules: rules, created: time.Now()}
	searchBacklogManager.enqueue(task, false)
}

func (b *searchBacklog) enqueue(task backlogTask, front bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	hash := ttKeyFor(task.state, task.state.Board.Size())
	if _, ok := b.present[hash]; ok {
		return
	}
	limit := GetConfig().AiMinmaxCacheLimit
	if limit > 0 && len(b.queue) >= limit {
		if !b.limitWarned {
			fmt.Printf("[ai:cachewarn] backlog queue size %d exceeded limit %d\n", len(b.queue)+1, limit)
			b.limitWarned = true
		}
	}
	if front {
		b.queue = append([]backlogTask{task}, b.queue...)
		b.present[hash] = struct{}{}
		return
	}
	b.queue = append(b.queue, task)
	b.present[hash] = struct{}{}
}

func (b *searchBacklog) popTask() *backlogTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return nil
	}
	task := b.queue[0]
	b.queue = b.queue[1:]
	hash := ttKeyFor(task.state, task.state.Board.Size())
	delete(b.present, hash)
	return &task
}

func (b *searchBacklog) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

func (b *searchBacklog) setCurrentBoard(hash uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.currentHash = hash
	b.currentSet = true
}

func (b *searchBacklog) clearCurrentBoard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.currentHash = 0
	b.currentSet = false
}

func (b *searchBacklog) currentBoardHash() (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.currentSet {
		return 0, false
	}
	return b.currentHash, true
}

func (b *searchBacklog) RequestStop() {
	if b.stop.CompareAndSwap(false, true) {
		if hash, ok := b.currentBoardHash(); ok {
			fmt.Printf("[ai:queue] stopping board 0x%x because a new game started\n", hash)
		}
	}
}

func (b *searchBacklog) ResetStop() {
	b.stop.Store(false)
}

func (b *searchBacklog) shouldStop() bool {
	return b.stop.Load()
}

func startSearchBacklogWorker(controller *GameController) {
	go searchBacklogManager.worker(controller)
}

func (b *searchBacklog) worker(controller *GameController) {
	pausedLogged := false
	for {
		if controller != nil {
			state := controller.State()
			if state.Status == StatusRunning {
				b.RequestStop()
				if b.Len() > 0 && !pausedLogged {
					fmt.Printf("[ai:queue] game running, pausing backlog (%d queued)\n", b.Len())
					pausedLogged = true
				}
				time.Sleep(150 * time.Millisecond)
				continue
			}
		}
		pausedLogged = false
		task := b.popTask()
		if task == nil {
			time.Sleep(150 * time.Millisecond)
			continue
		}
		hash := ttKeyFor(task.state, task.state.Board.Size())
		b.setCurrentBoard(hash)
		b.ResetStop()
		b.processTask(*task)
		b.clearCurrentBoard()
	}
}

func (b *searchBacklog) processTask(task backlogTask) {
	config := GetConfig()
	config.AiTimeBudgetMs = 0
	stats := &SearchStats{Start: time.Now()}
	settings := AIScoreSettings{
		Depth:            config.AiDepth,
		TimeoutMs:        0,
		BoardSize:        task.state.Board.Size(),
		Player:           task.state.ToMove,
		Cache:            &defaultCache,
		Config:           config,
		Stats:            stats,
		ShouldStop:       b.shouldStop,
		DirectDepthOnly:  true,
		SkipQueueBacklog: true,
	}
	remaining := b.Len()
	boardHash := ttKeyFor(task.state, task.state.Board.Size())
	fmt.Printf("[ai:queue] analyzing board 0x%x with depth %d. %d remains in queue\n", boardHash, settings.Depth, remaining)

	rootCandidates := collectCandidateMoves(task.state, task.state.ToMove, task.state.Board.Size())
	estimatedNodes := estimateNodes(len(rootCandidates), settings.Depth)
	if estimatedNodes <= 0 {
		estimatedNodes = 1
	}

	start := time.Now()
	progressDone := make(chan struct{})
	progressTicker := time.NewTicker(5 * time.Second)
	go func() {
		defer progressTicker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-progressTicker.C:
				elapsed := time.Since(start)
				nodes := float64(stats.Nodes)
				percent := math.Min(100, nodes/estimatedNodes*100)
				fmt.Printf("[ai:queue] progress board 0x%x %.0f%% (elapsed %dms, nodes %.0f)\n", boardHash, percent, elapsed.Milliseconds(), nodes)
			}
		}
	}()

	scores := ScoreBoard(task.state.Clone(), task.rules, settings)
	close(progressDone)
	progressTicker.Stop()
	_ = scores

	elapsed := time.Since(start)
	if b.shouldStop() {
		fmt.Printf("[ai:queue] interrupted board 0x%x after %dms (game started), requeuing\n", boardHash, elapsed.Milliseconds())
	} else {
		fmt.Printf("[ai:queue] analyzing board 0x%x finished in %dms\n", boardHash, elapsed.Milliseconds())
	}
	if b.shouldStop() || stats.CompletedDepths < settings.Depth {
		b.enqueue(task, true)
		return
	}
}

func estimateNodes(rootCandidates, depth int) float64 {
	if depth <= 0 || rootCandidates <= 0 {
		return float64(depth + 1)
	}
	branch := float64(rootCandidates)
	if branch < 1 {
		branch = 1
	}
	total := 0.0
	for d := 0; d <= depth; d++ {
		total += math.Pow(branch, float64(d))
	}
	return total
}
