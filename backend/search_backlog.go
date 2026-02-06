package main

import (
	"fmt"
	"runtime"
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
	if !GetConfig().AiQueueEnabled {
		return
	}
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
	if !GetConfig().AiQueueEnabled {
		return
	}
	workerCount := backlogWorkerCount(GetConfig(), runtime.NumCPU())
	fmt.Printf("[ai:queue] starting workers=%d\n", workerCount)
	searchBacklogManager.startWorkers(controller, workerCount)
}

func backlogWorkerCount(config Config, cpuCount int) int {
	if cpuCount < 1 {
		cpuCount = 1
	}
	workers := config.AiQueueWorkers
	if workers <= 0 {
		workers = 1
	}
	if workers > cpuCount {
		workers = cpuCount
	}
	return workers
}

func backlogAnalyzeThreadCount(config Config, cpuCount int) int {
	if cpuCount < 1 {
		cpuCount = 1
	}
	threads := config.AiQueueAnalyzeThreads
	if threads <= 0 {
		threads = cpuCount / 2
		if threads < 2 {
			threads = 2
		}
		if threads > 8 {
			threads = 8
		}
	}
	if threads > cpuCount {
		threads = cpuCount
	}
	if threads < 1 {
		threads = 1
	}
	return threads
}

const backlogMinUsefulDepth = 6

func backlogDepthRange(config Config) (int, int) {
	target := config.AiDepth
	if config.AiMaxDepth > 0 && config.AiMaxDepth < target {
		target = config.AiMaxDepth
	}
	if target < 1 {
		target = 1
	}
	start := config.AiMinDepth
	if start < backlogMinUsefulDepth {
		start = backlogMinUsefulDepth
	}
	if start < 1 {
		start = 1
	}
	if start > target {
		start = target
	}
	return start, target
}

func (b *searchBacklog) startWorkers(controller *GameController, count int) {
	if count <= 0 {
		count = 1
	}
	for i := 0; i < count; i++ {
		go b.worker(controller, i)
	}
}

func (b *searchBacklog) worker(controller *GameController, _ int) {
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
	config = backlogConfig(config)
	startDepth, targetDepth := backlogDepthRange(config)
	stats := &SearchStats{Start: time.Now()}
	cache := SharedSearchCache()
	analyzeThreads := backlogAnalyzeThreadCount(config, runtime.NumCPU())
	rootCandidates := collectCandidateMoves(task.state, task.state.ToMove, task.state.Board.Size())
	effectiveThreads := analyzeThreads
	if effectiveThreads > len(rootCandidates) {
		effectiveThreads = len(rootCandidates)
	}
	if effectiveThreads < 1 {
		effectiveThreads = 1
	}
	var progressDepth atomic.Int64
	progressDepth.Store(int64(startDepth))
	var progressNodes atomic.Int64
	var progressCandidates atomic.Int64
	var progressTTProbes atomic.Int64
	var progressTTHits atomic.Int64
	var progressTTCutoffs atomic.Int64
	var progressABCutoffs atomic.Int64
	settings := AIScoreSettings{
		Depth:            startDepth,
		TimeoutMs:        0,
		BoardSize:        task.state.Board.Size(),
		Player:           task.state.ToMove,
		Cache:            cache,
		Config:           config,
		Stats:            stats,
		ShouldStop:       b.shouldStop,
		DirectDepthOnly:  true,
		SkipQueueBacklog: true,
	}
	settings.OnNodeProgress = func(delta int64) {
		if delta > 0 {
			progressNodes.Add(delta)
		}
	}
	settings.OnSearchProgress = func(delta SearchProgressDelta) {
		if delta.CandidateCount > 0 {
			progressCandidates.Add(delta.CandidateCount)
		}
		if delta.TTProbes > 0 {
			progressTTProbes.Add(delta.TTProbes)
		}
		if delta.TTHits > 0 {
			progressTTHits.Add(delta.TTHits)
		}
		if delta.TTCutoffs > 0 {
			progressTTCutoffs.Add(delta.TTCutoffs)
		}
		if delta.ABCutoffs > 0 {
			progressABCutoffs.Add(delta.ABCutoffs)
		}
	}
	remaining := b.Len()
	boardHash := ttKeyFor(task.state, task.state.Board.Size())
	fmt.Printf("[ai:queue] analyzing board 0x%x depth [%d->%d] using threads=%d. %d remains in queue\n",
		boardHash, startDepth, targetDepth, effectiveThreads, remaining)
	logMemUsage(fmt.Sprintf("start board 0x%x", boardHash))

	start := time.Now()
	maxElapsedMs := config.AiBacklogEstimateMs
	progressDone := make(chan struct{})
	progressTicker := time.NewTicker(5 * time.Second)
	go func() {
		lastTick := start
		var (
			lastNodes      int64
			lastCandidates int64
			lastTTProbes   int64
			lastTTHits     int64
			lastTTCutoffs  int64
			lastABCutoffs  int64
		)
		defer progressTicker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-progressTicker.C:
				now := time.Now()
				elapsed := now.Sub(start)
				nodesValue := progressNodes.Load()
				if nodesValue == 0 {
					nodesValue = stats.Nodes
				}
				candidatesValue := progressCandidates.Load()
				ttProbesValue := progressTTProbes.Load()
				ttHitsValue := progressTTHits.Load()
				ttCutoffsValue := progressTTCutoffs.Load()
				abCutoffsValue := progressABCutoffs.Load()

				intervalMs := now.Sub(lastTick).Milliseconds()
				if intervalMs <= 0 {
					intervalMs = 1
				}
				deltaNodes := nodesValue - lastNodes
				deltaCandidates := candidatesValue - lastCandidates
				deltaTTProbes := ttProbesValue - lastTTProbes
				deltaTTHits := ttHitsValue - lastTTHits
				deltaTTCutoffs := ttCutoffsValue - lastTTCutoffs
				deltaABCutoffs := abCutoffsValue - lastABCutoffs
				nps := int64(0)
				if deltaNodes > 0 {
					nps = deltaNodes * 1000 / intervalMs
				}
				avgBranch := 0.0
				if deltaNodes > 0 && deltaCandidates > 0 {
					avgBranch = float64(deltaCandidates) / float64(deltaNodes)
				}
				ttHitRate := 0.0
				if deltaTTProbes > 0 && deltaTTHits > 0 {
					ttHitRate = float64(deltaTTHits) * 100.0 / float64(deltaTTProbes)
				}
				cutoffReason := "none"
				if deltaTTCutoffs > deltaABCutoffs && deltaTTCutoffs > 0 {
					cutoffReason = "TT"
				} else if deltaABCutoffs > deltaTTCutoffs && deltaABCutoffs > 0 {
					cutoffReason = "AB"
				} else if deltaTTCutoffs > 0 && deltaABCutoffs > 0 {
					cutoffReason = "tie"
				}
				currentDepth := int(progressDepth.Load())
				if currentDepth < startDepth {
					currentDepth = startDepth
				}
				if currentDepth > targetDepth {
					currentDepth = targetDepth
				}
				fmt.Printf("[ai:queue] hash 0x%x [%d/%d] (%dms, %d nodes, %d nps, b=%.2f, tt=%.1f%%, cutoff=%s)\n",
					boardHash, currentDepth, targetDepth, elapsed.Milliseconds(), nodesValue, nps, avgBranch, ttHitRate, cutoffReason)

				lastTick = now
				lastNodes = nodesValue
				lastCandidates = candidatesValue
				lastTTProbes = ttProbesValue
				lastTTHits = ttHitsValue
				lastTTCutoffs = ttCutoffsValue
				lastABCutoffs = abCutoffsValue
			}
		}
	}()

	completed := true
	completedDepth := startDepth - 1
	for depth := startDepth; depth <= targetDepth; depth++ {
		if b.shouldStop() {
			completed = false
			break
		}
		if maxElapsedMs > 0 && time.Since(start).Milliseconds() >= int64(maxElapsedMs) && completedDepth >= startDepth {
			completed = false
			fmt.Printf("[ai:queue] budget reached board 0x%x at depth [%d/%d], requeuing for deeper analysis\n",
				boardHash, completedDepth, targetDepth)
			break
		}
		progressDepth.Store(int64(depth))
		depthStart := time.Now()
		beforeNodes := stats.Nodes
		beforeTTProbes := stats.TTProbes
		beforeTTHits := stats.TTHits
		beforeTTExactHits := stats.TTExactHits
		beforeTTLowerHits := stats.TTLowerHits
		beforeTTUpperHits := stats.TTUpperHits
		beforeCutoffs := stats.Cutoffs
		beforeTTCutoffs := stats.TTCutoffs
		beforeABCutoffs := stats.ABCutoffs
		depthSettings := settings
		depthSettings.Depth = depth
		if effectiveThreads > 1 {
			_, completed = ScoreBoardDirectDepthParallel(task.state.Clone(), task.rules, depthSettings, effectiveThreads)
		} else {
			_ = ScoreBoard(task.state.Clone(), task.rules, depthSettings)
			completed = stats.CompletedDepths >= depth
		}
		if !completed || stats.CompletedDepths < depth {
			completed = false
			break
		}
		completedDepth = depth
		depthElapsedMs := time.Since(depthStart).Milliseconds()
		deltaNodes := stats.Nodes - beforeNodes
		deltaTTProbes := stats.TTProbes - beforeTTProbes
		deltaTTHits := stats.TTHits - beforeTTHits
		deltaTTExactHits := stats.TTExactHits - beforeTTExactHits
		deltaTTLowerHits := stats.TTLowerHits - beforeTTLowerHits
		deltaTTUpperHits := stats.TTUpperHits - beforeTTUpperHits
		deltaCutoffs := stats.Cutoffs - beforeCutoffs
		deltaTTCutoffs := stats.TTCutoffs - beforeTTCutoffs
		deltaABCutoffs := stats.ABCutoffs - beforeABCutoffs
		nps := int64(0)
		if depthElapsedMs > 0 {
			nps = deltaNodes * 1000 / depthElapsedMs
		}
		fmt.Printf("[ai:queue] depth [%d/%d] complete in %dms nodes=%d nps=%d tt_probe=%d tt_hit=%d tt_hit_flag=(e:%d l:%d u:%d) cutoffs=%d tt_cutoff=%d ab_cutoff=%d\n",
			depth, targetDepth, depthElapsedMs, deltaNodes, nps, deltaTTProbes, deltaTTHits, deltaTTExactHits, deltaTTLowerHits, deltaTTUpperHits, deltaCutoffs, deltaTTCutoffs, deltaABCutoffs)
	}
	close(progressDone)
	progressTicker.Stop()
	logMemUsage(fmt.Sprintf("done board 0x%x", boardHash))

	elapsed := time.Since(start)
	if b.shouldStop() {
		fmt.Printf("[ai:queue] interrupted board 0x%x after %dms (game started), requeuing\n", boardHash, elapsed.Milliseconds())
	} else {
		fmt.Printf("[ai:queue] analyzing board 0x%x finished in %dms depth=[%d/%d] tt_size=%d\n",
			boardHash, elapsed.Milliseconds(), completedDepth, targetDepth, TranspositionSize(cache))
	}
	if b.shouldStop() || !completed || completedDepth < targetDepth {
		b.enqueue(task, true)
		return
	}
}

func backlogConfig(base Config) Config {
	base.AiEnableTacticalMode = false
	base.AiEnableTacticalExt = false
	base.AiEnableTacticalK = false
	base.AiEnableAspiration = false
	base.AiEnableDynamicTopK = false
	base.AiMaxCandidatesRoot = 8
	base.AiMaxCandidatesMid = 4
	base.AiMaxCandidatesDeep = 2
	base.AiTopCandidates = 0
	return base
}

func logMemUsage(prefix string) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("[ai:queue] %s mem alloc=%s heap_alloc=%s total_alloc=%s sys=%s num_gc=%d\n",
		prefix,
		formatBytes(mem.Alloc),
		formatBytes(mem.HeapAlloc),
		formatBytes(mem.TotalAlloc),
		formatBytes(mem.Sys),
		mem.NumGC)
}

func formatBytes(n uint64) string {
	const (
		kb = 1 << (10 * 1)
		mb = 1 << (10 * 2)
		gb = 1 << (10 * 3)
		tb = 1 << (10 * 4)
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.2f TB", float64(n)/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.2f kB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
