package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type backlogTask struct {
	state       GameState
	rules       Rules
	created     time.Time
	knownDepth  int
	targetDepth int
}

type searchBacklog struct {
	mu               sync.Mutex
	queue            []backlogTask
	present          map[uint64]struct{}
	priorityCounts   map[uint64]int
	analytics        map[uint64]backlogAnalyticsEntry
	processing       map[uint64]bool
	analiticsHub     *AnaliticsHub
	currentHash      uint64
	currentSet       bool
	stop             atomic.Bool
	limitWarned      bool
	queueEmptyLogged bool
}

type backlogNeedsInfo struct {
	Needs              bool
	TargetDepth        int
	SolvedDepth        int
	HasTTEntry         bool
	TTEntry            TTEntry
	HasRootTranspose   bool
	RootTransposeEntry RootTransposeEntry
}

var searchBacklogManager = newSearchBacklog()

func newSearchBacklog() *searchBacklog {
	return &searchBacklog{
		present:        make(map[uint64]struct{}),
		processing:     make(map[uint64]bool),
		priorityCounts: make(map[uint64]int),
		analytics:      make(map[uint64]backlogAnalyticsEntry),
	}
}

func enqueueSearchBacklogTask(state GameState, rules Rules) {
	config := GetConfig()
	if !config.AiQueueEnabled {
		return
	}
	config = backlogConfig(config)
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	info := backlogNeedsAnalysis(state, config, SharedSearchCache())
	if !info.Needs {
		logBacklogInfo("backlog skip", state, info, fmt.Sprintf("not enqueued because board 0x%x is a transposition", ttKeyFor(state, state.Board.Size())))
		return
	}
	logBacklogInfo("backlog enqueue", state, info, "")
	task := backlogTask{
		state:       state.Clone(),
		rules:       rules,
		created:     time.Now(),
		knownDepth:  info.SolvedDepth,
		targetDepth: info.TargetDepth,
	}
	searchBacklogManager.enqueue(task, false)
}

func logBacklogInfo(action string, state GameState, info backlogNeedsInfo, suffix string) {
	boardSize := state.Board.Size()
	boardHash := ttKeyFor(state, boardSize)
	shapeHash, bbox, shapeOK := rootShapeKey(state, boardSize)
	if !shapeOK {
		shapeHash = boardHash
	}
	left, right, top, bottom := 0, 0, 0, 0
	wallHits := ""
	if info.HasRootTranspose {
		entry := info.RootTransposeEntry
		left = int(entry.GrowLeft)
		right = int(entry.GrowRight)
		top = int(entry.GrowTop)
		bottom = int(entry.GrowBottom)
		wallHits = fmt.Sprintf(" walls=(l:%t r:%t t:%t b:%t)", entry.HitLeft, entry.HitRight, entry.HitTop, entry.HitBottom)
	} else if info.HasTTEntry {
		entry := info.TTEntry
		left = int(entry.GrowLeft)
		right = int(entry.GrowRight)
		top = int(entry.GrowTop)
		bottom = int(entry.GrowBottom)
		wallHits = fmt.Sprintf(" walls=(l:%t r:%t t:%t b:%t)", entry.HitLeft, entry.HitRight, entry.HitTop, entry.HitBottom)
	}
	bestDepth := info.SolvedDepth
	scoreStr := "none"
	if info.HasTTEntry {
		bestDepth = info.TTEntry.Depth
		scoreStr = fmt.Sprintf("%.2f", info.TTEntry.ScoreFloat())
	} else if info.HasRootTranspose {
		bestDepth = info.RootTransposeEntry.Depth
		scoreStr = fmt.Sprintf("%.2f", info.RootTransposeEntry.ScoreFloat())
	}
	if suffix != "" && suffix[0] != ' ' {
		suffix = " " + suffix
	}
	infoSuffix := wallHits + suffix
	fmt.Printf("[ai:queue] %s board 0x%x playable=0x%x origin=(%d,%d) size=%dx%d growth=(l:%d r:%d t:%d b:%d) depth=%d score=%s target=%d%s\n",
		action,
		boardHash,
		shapeHash,
		bbox.minX,
		bbox.minY,
		bbox.width,
		bbox.height,
		left,
		right,
		top,
		bottom,
		bestDepth,
		scoreStr,
		info.TargetDepth,
		infoSuffix)
}

func (b *searchBacklog) enqueue(task backlogTask, front bool) {
	var eventPayload analiticsPayload
	b.mu.Lock()
	hash := ttKeyFor(task.state, task.state.Board.Size())
	b.priorityCounts[hash]++
	entry := b.analytics[hash]
	if entry.Hash == 0 {
		entry = backlogAnalyticsEntry{
			Hash:         hash,
			Board:        task.state.Board.Clone(),
			Stones:       countBoardStones(task.state.Board),
			Created:      task.created,
			CurrentDepth: task.knownDepth,
			TargetDepth:  task.targetDepth,
		}
	}
	if task.knownDepth > entry.CurrentDepth {
		entry.CurrentDepth = task.knownDepth
	}
	if task.targetDepth > entry.TargetDepth {
		entry.TargetDepth = task.targetDepth
	}
	entry.Hits = b.priorityCounts[hash]
	b.analytics[hash] = entry
	if _, ok := b.present[hash]; ok {
		eventPayload = b.analiticsPayloadLocked("board_hit", hash)
		b.mu.Unlock()
		b.publishAnaliticsEvent(eventPayload)
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
		b.queueEmptyLogged = false
		eventPayload = b.analiticsPayloadLocked("board_added", hash)
		b.mu.Unlock()
		b.publishAnaliticsEvent(eventPayload)
		return
	}
	b.queue = append(b.queue, task)
	b.present[hash] = struct{}{}
	b.queueEmptyLogged = false
	eventPayload = b.analiticsPayloadLocked("board_added", hash)
	b.mu.Unlock()
	b.publishAnaliticsEvent(eventPayload)
}

func (b *searchBacklog) pickTaskForProcessing() (backlogTask, uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return backlogTask{}, 0, false
	}
	bestIdx := -1
	var bestHash uint64
	var bestEntry backlogAnalyticsEntry
	for i, task := range b.queue {
		hash := ttKeyFor(task.state, task.state.Board.Size())
		if b.processing[hash] {
			continue
		}
		entry, ok := b.analytics[hash]
		if !ok || entry.Hash == 0 {
			entry = backlogAnalyticsEntry{
				Hash:         hash,
				Stones:       countBoardStones(task.state.Board),
				Created:      task.created,
				Hits:         b.priorityCounts[hash],
				CurrentDepth: task.knownDepth,
				TargetDepth:  task.targetDepth,
			}
		}
		if bestIdx == -1 || compareAnaliticsPriority(entry, bestEntry) < 0 {
			bestIdx = i
			bestHash = hash
			bestEntry = entry
		}
	}
	if bestIdx == -1 {
		return backlogTask{}, 0, false
	}
	b.processing[bestHash] = true
	return b.queue[bestIdx], bestHash, true
}

func countBoardStones(board Board) int {
	size := board.Size()
	count := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if board.At(x, y) != CellEmpty {
				count++
			}
		}
	}
	return count
}

func (b *searchBacklog) finishTaskProcessing(hash uint64, remove bool) {
	var eventPayload analiticsPayload
	b.mu.Lock()
	delete(b.processing, hash)
	entry := b.analytics[hash]
	if entry.Hash != 0 {
		entry.Analyzing = false
		entry.AnalysisStartedAtMs = 0
		b.analytics[hash] = entry
	}
	if !remove {
		eventPayload = b.analiticsPayloadLocked("board_paused", hash)
		b.mu.Unlock()
		b.publishAnaliticsEvent(eventPayload)
		return
	}
	for i, task := range b.queue {
		if ttKeyFor(task.state, task.state.Board.Size()) == hash {
			b.queue = append(b.queue[:i], b.queue[i+1:]...)
			delete(b.present, hash)
			delete(b.priorityCounts, hash)
			eventPayload = b.analiticsPayloadLocked("board_left", hash)
			delete(b.analytics, hash)
			b.mu.Unlock()
			b.publishAnaliticsEvent(eventPayload)
			return
		}
	}
	b.mu.Unlock()
}

func (b *searchBacklog) logQueueEmptyIfNeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) != 0 || b.queueEmptyLogged {
		return
	}
	fmt.Println("[ai:queue] All boards from the queue as been analyzed")
	b.queueEmptyLogged = true
}

func (b *searchBacklog) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

func (b *searchBacklog) SetAnaliticsHub(hub *AnaliticsHub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.analiticsHub = hub
}

func (b *searchBacklog) TopAnaliticsQueue(limit int) []analiticsQueueEntryDTO {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.topAnaliticsQueueLocked(limit)
}

func (b *searchBacklog) TotalAnaliticsQueue() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.present)
}

func (b *searchBacklog) markBoardStarted(hash uint64) {
	b.mu.Lock()
	entry := b.analytics[hash]
	if entry.Hash != 0 {
		entry.Analyzing = true
		entry.AnalysisStartedAtMs = time.Now().UnixMilli()
		b.analytics[hash] = entry
	}
	payload := b.analiticsPayloadLocked("board_started", hash)
	b.mu.Unlock()
	b.publishAnaliticsEvent(payload)
}

func (b *searchBacklog) markBoardDepth(hash uint64, depth int) {
	b.mu.Lock()
	entry := b.analytics[hash]
	if entry.Hash == 0 || depth <= entry.CurrentDepth {
		b.mu.Unlock()
		return
	}
	entry.CurrentDepth = depth
	b.analytics[hash] = entry
	payload := b.analiticsPayloadLocked("depth_hit", hash)
	b.mu.Unlock()
	b.publishAnaliticsEvent(payload)
}

func (b *searchBacklog) topAnaliticsQueueLocked(limit int) []analiticsQueueEntryDTO {
	if limit <= 0 {
		return []analiticsQueueEntryDTO{}
	}
	items := make([]backlogAnalyticsEntry, 0, len(b.analytics))
	for hash := range b.present {
		entry, ok := b.analytics[hash]
		if !ok || entry.Hash == 0 {
			continue
		}
		items = append(items, entry)
	}
	sortAnaliticsQueue(items)
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]analiticsQueueEntryDTO, 0, len(items))
	for _, item := range items {
		result = append(result, analiticsEntryToDTO(item))
	}
	return result
}

func (b *searchBacklog) analiticsPayloadLocked(event string, hash uint64) analiticsPayload {
	var eventEntry *analiticsQueueEventEntry
	if analyticsEntry, ok := b.analytics[hash]; ok && analyticsEntry.Hash != 0 {
		dto := analiticsEntryToEventEntry(analyticsEntry)
		eventEntry = &dto
	}
	payload := analiticsPayload{
		Event:        event,
		Entry:        eventEntry,
		TotalInQueue: len(b.present),
		UpdatedAt:    time.Now().UnixMilli(),
	}
	return payload
}

func (b *searchBacklog) publishAnaliticsEvent(payload analiticsPayload) {
	b.mu.Lock()
	hub := b.analiticsHub
	b.mu.Unlock()
	if hub == nil {
		return
	}
	hub.Publish(payload)
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

func backlogNeedsAnalysis(state GameState, config Config, cache *AISearchCache) backlogNeedsInfo {
	_, targetDepth := backlogDepthRange(config)
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	var info backlogNeedsInfo
	info.TargetDepth = targetDepth
	info.Needs = true
	tt := ensureTT(cache, config)
	if tt == nil {
		info.Needs = true
		return info
	}
	key := ttKeyFor(state, state.Board.Size())
	entry, ok := tt.Probe(key)
	if ok {
		info.HasTTEntry = true
		info.TTEntry = entry
		if entry.Flag == TTExact {
			if entry.Depth > info.SolvedDepth {
				info.SolvedDepth = entry.Depth
			}
			if entry.Depth >= targetDepth {
				info.Needs = false
			}
		}
	}
	if config.AiEnableRootTranspose {
		if rootTranspose := ensureRootTransposeCache(cache, config); rootTranspose != nil {
			shapeKey, bbox, shapeOK := rootShapeKey(state, state.Board.Size())
			if shapeOK {
				shapeEntry, shapeHit := rootTranspose.Get(shapeKey, 1)
				if shapeHit && rootTransposeFits(bbox, shapeEntry, state.Board.Size()) {
					info.HasRootTranspose = true
					info.RootTransposeEntry = shapeEntry
					if shapeEntry.Depth > info.SolvedDepth {
						info.SolvedDepth = shapeEntry.Depth
					}
					if shapeEntry.Depth >= targetDepth {
						info.Needs = false
					}
				}
			}
		}
	}
	info.Needs = info.Needs || (!info.HasTTEntry && !info.HasRootTranspose)
	return info
}

func backlogStartDepth(baseStart, targetDepth, knownDepth, solvedDepth int) int {
	start := baseStart
	if knownDepth+1 > start {
		start = knownDepth + 1
	}
	if solvedDepth+1 > start {
		start = solvedDepth + 1
	}
	if start < 1 {
		start = 1
	}
	if start > targetDepth {
		start = targetDepth
	}
	return start
}

func rootTransposeFits(bbox boardBBox, entry RootTransposeEntry, boardSize int) bool {
	left := int(entry.GrowLeft)
	right := int(entry.GrowRight)
	top := int(entry.GrowTop)
	bottom := int(entry.GrowBottom)
	if bbox.minX < left || bbox.minY < top {
		return false
	}
	if boardSize-1-bbox.maxX < right || boardSize-1-bbox.maxY < bottom {
		return false
	}
	frameW := bbox.width + left + right
	frameH := bbox.height + top + bottom
	if frameW <= 0 || frameH <= 0 {
		return false
	}
	if frameW != int(entry.FrameW) || frameH != int(entry.FrameH) {
		return false
	}
	originX := bbox.minX - left
	originY := bbox.minY - top
	if entry.HitLeft && originX != 0 {
		return false
	}
	if entry.HitTop && originY != 0 {
		return false
	}
	if entry.HitRight && originX+frameW != boardSize {
		return false
	}
	if entry.HitBottom && originY+frameH != boardSize {
		return false
	}
	return true
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
		task, hash, ok := b.pickTaskForProcessing()
		if !ok {
			b.logQueueEmptyIfNeeded()
			time.Sleep(150 * time.Millisecond)
			continue
		}
		b.setCurrentBoard(hash)
		b.markBoardStarted(hash)
		b.ResetStop()
		completed := b.processTask(task)
		b.finishTaskProcessing(hash, completed)
		b.clearCurrentBoard()
	}
}

func (b *searchBacklog) processTask(task backlogTask) bool {
	config := GetConfig()
	debugLogs := config.AiLogSearchStats
	config.AiTimeBudgetMs = 0
	config = backlogConfig(config)
	baseStartDepth, targetDepth := backlogDepthRange(config)
	stats := &SearchStats{Start: time.Now()}
	cache := SharedSearchCache()
	boardHash := ttKeyFor(task.state, task.state.Board.Size())
	info := backlogNeedsAnalysis(task.state, config, cache)
	if !info.Needs {
		fmt.Printf("[ai:queue] skip board 0x%x (already solved depth=%d target=%d)\n", boardHash, info.SolvedDepth, info.TargetDepth)
		return true
	}
	startDepth := backlogStartDepth(baseStartDepth, targetDepth, task.knownDepth, info.SolvedDepth)
	if info.SolvedDepth >= targetDepth || startDepth >= targetDepth && info.SolvedDepth >= startDepth {
		fmt.Printf("[ai:queue] skip board 0x%x (already solved depth=%d target=%d)\n", boardHash, info.SolvedDepth, targetDepth)
		return true
	}
	analyzeThreads := backlogAnalyzeThreadCount(config, runtime.NumCPU())
	rootCandidates := collectCandidateMoves(task.state, task.state.ToMove, task.state.Board.Size())
	effectiveThreads := analyzeThreads
	if effectiveThreads > len(rootCandidates) {
		effectiveThreads = len(rootCandidates)
	}
	if effectiveThreads < 1 {
		effectiveThreads = 1
	}
	remaining := b.Len()
	fmt.Printf("[ai:queue] analyzing board 0x%x depth [%d->%d] using threads=%d. %d remains in queue\n",
		boardHash, startDepth, targetDepth, effectiveThreads, remaining)
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
	if debugLogs {
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
	}
	if debugLogs {
		logMemUsage(fmt.Sprintf("start board 0x%x", boardHash))
	}

	start := time.Now()
	maxElapsedMs := config.AiBacklogEstimateMs
	progressDone := make(chan struct{})
	var progressTicker *time.Ticker
	if debugLogs {
		progressTicker = time.NewTicker(5 * time.Second)
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
	}

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
		if debugLogs {
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
		b.markBoardDepth(boardHash, depth)
	}
	close(progressDone)
	if progressTicker != nil {
		progressTicker.Stop()
	}
	if debugLogs {
		logMemUsage(fmt.Sprintf("done board 0x%x", boardHash))
	}

	elapsed := time.Since(start)
	shouldStop := b.shouldStop()
	done := completed && completedDepth >= targetDepth && !shouldStop
	if shouldStop {
		fmt.Printf("[ai:queue] interrupted board 0x%x after %dms (game started), keeping for later\n", boardHash, elapsed.Milliseconds())
	} else if !done {
		fmt.Printf("[ai:queue] budget reached board 0x%x at depth [%d/%d], keeping for later\n", boardHash, completedDepth, targetDepth)
	} else {
		fmt.Printf("[ai:queue] analyzing board 0x%x finished in %dms depth=[%d/%d] tt_size=%d\n",
			boardHash, elapsed.Milliseconds(), completedDepth, targetDepth, TranspositionSize(cache))
	}
	if done {
		finalInfo := backlogNeedsAnalysis(task.state, config, cache)
		logBacklogInfo("backlog done", task.state, finalInfo, "")
	}
	return done
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
