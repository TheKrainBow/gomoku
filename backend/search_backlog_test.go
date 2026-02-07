package main

import (
	"testing"
	"time"
)

func TestBacklogWorkerCountDefaultsToSingleWorker(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiQueueWorkers = 0
	got := backlogWorkerCount(cfg, 8)
	if got != 1 {
		t.Fatalf("expected 1 worker by default, got %d", got)
	}
}

func TestBacklogWorkerCountCapsAtCPUCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiQueueWorkers = 64
	got := backlogWorkerCount(cfg, 6)
	if got != 6 {
		t.Fatalf("expected worker count capped to cpu count, got %d", got)
	}
}

func TestBacklogWorkerCountRespectsConfiguredValue(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiQueueWorkers = 3
	got := backlogWorkerCount(cfg, 8)
	if got != 3 {
		t.Fatalf("expected configured worker count, got %d", got)
	}
}

func TestBacklogAnalyzeThreadCountAutoUsesHalfCPUs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiQueueAnalyzeThreads = 0
	got := backlogAnalyzeThreadCount(cfg, 8)
	if got != 4 {
		t.Fatalf("expected auto thread count to use half cpu count, got %d", got)
	}
}

func TestBacklogAnalyzeThreadCountAutoCapsAtEight(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiQueueAnalyzeThreads = 0
	got := backlogAnalyzeThreadCount(cfg, 64)
	if got != 8 {
		t.Fatalf("expected auto thread count to cap at 8, got %d", got)
	}
}

func TestBacklogAnalyzeThreadCountRespectsConfiguredValue(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiQueueAnalyzeThreads = 3
	got := backlogAnalyzeThreadCount(cfg, 8)
	if got != 3 {
		t.Fatalf("expected configured analyze thread count, got %d", got)
	}
}

func TestBacklogDepthRangeDefaultsToSixToTarget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiMinDepth = 1
	cfg.AiDepth = 10
	cfg.AiMaxDepth = 10
	start, target := backlogDepthRange(cfg)
	if start != 6 || target != 10 {
		t.Fatalf("expected depth range 6..10, got %d..%d", start, target)
	}
}

func TestBacklogDepthRangeRespectsHigherConfiguredMinDepth(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiMinDepth = 7
	cfg.AiDepth = 10
	cfg.AiMaxDepth = 10
	start, target := backlogDepthRange(cfg)
	if start != 7 || target != 10 {
		t.Fatalf("expected depth range 7..10, got %d..%d", start, target)
	}
}

func TestBacklogDepthRangeClampsWhenTargetBelowSix(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiMinDepth = 1
	cfg.AiDepth = 5
	cfg.AiMaxDepth = 5
	start, target := backlogDepthRange(cfg)
	if start != 5 || target != 5 {
		t.Fatalf("expected depth range 5..5, got %d..%d", start, target)
	}
}

func TestBacklogConfigKeepsKillerHistorySettings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	got := backlogConfig(cfg)
	if got.AiEnableKillerMoves {
		t.Fatalf("expected killer setting to be preserved")
	}
	if got.AiEnableHistoryMoves {
		t.Fatalf("expected history setting to be preserved")
	}

	cfg.AiEnableKillerMoves = true
	cfg.AiEnableHistoryMoves = true
	got = backlogConfig(cfg)
	if !got.AiEnableKillerMoves {
		t.Fatalf("expected killer setting to be preserved")
	}
	if !got.AiEnableHistoryMoves {
		t.Fatalf("expected history setting to be preserved")
	}
}

func TestBacklogNeedsAnalysisWhenNoTTEntry(t *testing.T) {
	cfg := backlogConfig(DefaultConfig())
	settings := DefaultGameSettings()
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.recomputeHashes()
	cache := newAISearchCache()

	info := backlogNeedsAnalysis(state, cfg, &cache)
	if !info.Needs {
		t.Fatalf("expected analysis to be needed without TT entry")
	}
	if info.TargetDepth <= 0 {
		t.Fatalf("expected positive target depth, got %d", info.TargetDepth)
	}
	if info.SolvedDepth != 0 {
		t.Fatalf("expected solved depth 0 without TT entry, got %d", info.SolvedDepth)
	}
}

func TestBacklogNeedsAnalysisSkipsWhenExactEntryMeetsTarget(t *testing.T) {
	cfg := backlogConfig(DefaultConfig())
	settings := DefaultGameSettings()
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.recomputeHashes()
	cache := newAISearchCache()
	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	_, target := backlogDepthRange(cfg)
	key := ttKeyFor(state, state.Board.Size())
	tt.Store(key, target, 42, TTExact, Move{X: 0, Y: 0}, TTMeta{})

	info := backlogNeedsAnalysis(state, cfg, &cache)
	if info.Needs {
		t.Fatalf("expected analysis to be skipped for exact depth>=target entry")
	}
	if info.TargetDepth != target {
		t.Fatalf("expected target depth %d, got %d", target, info.TargetDepth)
	}
	if info.SolvedDepth < target {
		t.Fatalf("expected solved depth >= target depth, got solved=%d target=%d", info.SolvedDepth, target)
	}
}

func TestSuggestionDepthTenStoresTTAndSkipsBacklogEnqueue(t *testing.T) {
	prev := GetConfig()
	cfg := prev
	cfg.AiDepth = 10
	cfg.AiMaxDepth = 10
	cfg.AiMinDepth = 1
	cfg.AiTimeoutMs = 0
	cfg.AiTimeBudgetMs = 0
	cfg.AiQuickWinExit = false
	cfg.AiEnableAspiration = false
	cfg.AiEnableDynamicTopK = false
	cfg.AiEnableHardPlyCaps = true
	cfg.AiMaxCandidatesRoot = 1
	cfg.AiMaxCandidatesMid = 1
	cfg.AiMaxCandidatesDeep = 1
	cfg.AiEnableKillerMoves = false
	cfg.AiEnableHistoryMoves = false
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prev)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BoardSize = 7
	rules := NewRules(settings)
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack
	state.Board.Set(3, 3, CellBlack)
	state.Board.Set(2, 3, CellWhite)
	state.Board.Set(3, 2, CellBlack)
	state.Board.Set(4, 3, CellWhite)
	state.recomputeHashes()

	cache := newAISearchCache()
	stats := &SearchStats{}
	_ = ScoreBoard(state, rules, AIScoreSettings{
		Depth:           10,
		TimeoutMs:       0,
		BoardSize:       settings.BoardSize,
		Player:          state.ToMove,
		Cache:           &cache,
		Config:          cfg,
		Stats:           stats,
		DirectDepthOnly: false,
	})

	if stats.CompletedDepths != 10 {
		t.Fatalf("expected completed depth 10, got %d", stats.CompletedDepths)
	}

	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	rootKey := ttKeyFor(state, settings.BoardSize)
	entry, hit := tt.Probe(rootKey)
	if !hit {
		t.Fatalf("expected root board entry in TT")
	}
	if entry.Flag != TTExact {
		t.Fatalf("expected TT exact flag, got %d", entry.Flag)
	}
	if entry.Depth < 10 {
		t.Fatalf("expected TT depth >= 10, got %d", entry.Depth)
	}

	info := backlogNeedsAnalysis(state, backlogConfig(cfg), &cache)
	if info.Needs {
		t.Fatalf("expected backlog to skip enqueue after depth-10 TT solve, solved=%d target=%d", info.SolvedDepth, info.TargetDepth)
	}
}

func TestBacklogNeedsAnalysisDoesNotSkipNonExactEntry(t *testing.T) {
	cfg := backlogConfig(DefaultConfig())
	settings := DefaultGameSettings()
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.recomputeHashes()
	cache := newAISearchCache()
	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	_, target := backlogDepthRange(cfg)
	key := ttKeyFor(state, state.Board.Size())
	tt.Store(key, target+2, 42, TTLower, Move{X: 0, Y: 0}, TTMeta{})

	info := backlogNeedsAnalysis(state, cfg, &cache)
	if !info.Needs {
		t.Fatalf("expected non-exact entry to still require analysis")
	}
	if info.SolvedDepth != 0 {
		t.Fatalf("expected solved depth 0 for non-exact entry, got %d", info.SolvedDepth)
	}
}

func TestBacklogNeedsAnalysisTracksExactSolvedDepthBelowTarget(t *testing.T) {
	cfg := backlogConfig(DefaultConfig())
	settings := DefaultGameSettings()
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.recomputeHashes()
	cache := newAISearchCache()
	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT to be initialized")
	}
	_, target := backlogDepthRange(cfg)
	if target < 2 {
		t.Fatalf("expected target depth >= 2, got %d", target)
	}
	key := ttKeyFor(state, state.Board.Size())
	tt.Store(key, target-1, 42, TTExact, Move{X: 0, Y: 0}, TTMeta{})

	info := backlogNeedsAnalysis(state, cfg, &cache)
	if !info.Needs {
		t.Fatalf("expected analysis to still be needed when exact depth is below target")
	}
	if info.SolvedDepth != target-1 {
		t.Fatalf("expected solved depth %d, got %d", target-1, info.SolvedDepth)
	}
}

func TestBacklogNeedsAnalysisSkipsWhenRootTransposeEntryMeetsTarget(t *testing.T) {
	cfg := backlogConfig(DefaultConfig())
	cfg.AiEnableRootTranspose = true
	settings := DefaultGameSettings()
	state := DefaultGameState(settings)
	state.Status = StatusRunning
	state.ToMove = PlayerBlack
	state.Board.Set(6, 6, CellBlack)
	state.Board.Set(7, 6, CellWhite)
	state.Board.Set(6, 7, CellBlack)
	state.recomputeHashes()

	cache := newAISearchCache()
	rootTranspose := ensureRootTransposeCache(&cache, cfg)
	if rootTranspose == nil {
		t.Fatalf("expected root transpose cache to be initialized")
	}
	_, target := backlogDepthRange(cfg)
	key, _, ok := rootShapeKey(state, state.Board.Size())
	if !ok {
		t.Fatalf("expected shape key for non-empty board")
	}
	rootTranspose.Put(key, target+1, 42, TTExact, Move{X: 1, Y: 1}, TTMeta{
		GrowLeft:   0,
		GrowRight:  0,
		GrowTop:    0,
		GrowBottom: 0,
		FrameW:     2,
		FrameH:     2,
	})

	info := backlogNeedsAnalysis(state, cfg, &cache)
	if info.Needs {
		t.Fatalf("expected analysis to be skipped for solved root transpose entry")
	}
	if info.TargetDepth != target {
		t.Fatalf("expected target depth %d, got %d", target, info.TargetDepth)
	}
	if info.SolvedDepth < target {
		t.Fatalf("expected solved depth >= target depth, got solved=%d target=%d", info.SolvedDepth, target)
	}
}

func TestTopAnaliticsQueueOrdersByHits(t *testing.T) {
	b := newSearchBacklog()
	settings := DefaultGameSettings()
	stateA := DefaultGameState(settings)
	stateA.Board.Set(3, 3, CellBlack)
	stateA.recomputeHashes()
	stateB := DefaultGameState(settings)
	stateB.Board.Set(4, 4, CellWhite)
	stateB.recomputeHashes()

	b.enqueue(backlogTask{state: stateA, created: time.Unix(1, 0), targetDepth: 8}, false)
	b.enqueue(backlogTask{state: stateB, created: time.Unix(2, 0), targetDepth: 8}, false)
	b.enqueue(backlogTask{state: stateB, created: time.Unix(3, 0), targetDepth: 8}, false)

	queue := b.TopAnaliticsQueue(10)
	if len(queue) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(queue))
	}
	if queue[0].Hits != 2 {
		t.Fatalf("expected first entry to have 2 hits, got %d", queue[0].Hits)
	}
	if queue[1].Hits != 1 {
		t.Fatalf("expected second entry to have 1 hit, got %d", queue[1].Hits)
	}
}

func TestBacklogStartDepthUsesKnownAndSolvedDepth(t *testing.T) {
	start := backlogStartDepth(6, 10, 7, 8)
	if start != 9 {
		t.Fatalf("expected start depth 9, got %d", start)
	}
	start = backlogStartDepth(6, 10, 0, 10)
	if start != 10 {
		t.Fatalf("expected clamped start depth 10, got %d", start)
	}
}

func TestTopAnaliticsQueueTieBreaksByStonesThenRemainingDepthThenCreated(t *testing.T) {
	b := newSearchBacklog()
	settings := DefaultGameSettings()

	stateManyShallow := DefaultGameState(settings)
	stateManyShallow.Board.Set(4, 4, CellBlack)
	stateManyShallow.Board.Set(5, 5, CellWhite)
	stateManyShallow.Board.Set(6, 6, CellBlack)
	stateManyShallow.recomputeHashes()

	stateManyDeep := DefaultGameState(settings)
	stateManyDeep.Board.Set(4, 4, CellBlack)
	stateManyDeep.Board.Set(5, 5, CellWhite)
	stateManyDeep.recomputeHashes()

	stateFew := DefaultGameState(settings)
	stateFew.Board.Set(3, 3, CellBlack)
	stateFew.recomputeHashes()

	b.enqueue(backlogTask{state: stateManyShallow, created: time.Unix(3, 0), knownDepth: 7, targetDepth: 8}, false)
	b.enqueue(backlogTask{state: stateManyDeep, created: time.Unix(2, 0), knownDepth: 6, targetDepth: 12}, false)
	b.enqueue(backlogTask{state: stateFew, created: time.Unix(1, 0), knownDepth: 6, targetDepth: 12}, false)

	queue := b.TopAnaliticsQueue(10)
	if len(queue) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(queue))
	}

	expectedFirst := hashToBoardID(ttKeyFor(stateManyShallow, stateManyShallow.Board.Size()))
	expectedSecond := hashToBoardID(ttKeyFor(stateManyDeep, stateManyDeep.Board.Size()))
	expectedThird := hashToBoardID(ttKeyFor(stateFew, stateFew.Board.Size()))

	if queue[0].ID != expectedFirst {
		t.Fatalf("expected first board %s (more stones), got %s", expectedFirst, queue[0].ID)
	}
	if queue[1].ID != expectedSecond {
		t.Fatalf("expected second board %s (more remaining depth), got %s", expectedSecond, queue[1].ID)
	}
	if queue[2].ID != expectedThird {
		t.Fatalf("expected third board %s, got %s", expectedThird, queue[2].ID)
	}
}

func TestPickTaskForProcessingTieBreaksByStonesThenRemainingDepthThenCreated(t *testing.T) {
	b := newSearchBacklog()
	settings := DefaultGameSettings()

	stateOlderButSmaller := DefaultGameState(settings)
	stateOlderButSmaller.Board.Set(3, 3, CellBlack)
	stateOlderButSmaller.recomputeHashes()

	stateNewerButBigger := DefaultGameState(settings)
	stateNewerButBigger.Board.Set(4, 4, CellBlack)
	stateNewerButBigger.Board.Set(5, 5, CellWhite)
	stateNewerButBigger.recomputeHashes()

	b.enqueue(backlogTask{state: stateOlderButSmaller, created: time.Unix(1, 0), knownDepth: 6, targetDepth: 12}, false)
	b.enqueue(backlogTask{state: stateNewerButBigger, created: time.Unix(2, 0), knownDepth: 6, targetDepth: 12}, false)

	task, pickedHash, ok := b.pickTaskForProcessing()
	if !ok {
		t.Fatalf("expected a task to be picked")
	}
	expectedHash := ttKeyFor(stateNewerButBigger, stateNewerButBigger.Board.Size())
	if pickedHash != expectedHash {
		t.Fatalf("expected bigger board hash 0x%x, got 0x%x", expectedHash, pickedHash)
	}
	if ttKeyFor(task.state, task.state.Board.Size()) != expectedHash {
		t.Fatalf("expected picked task to match hash 0x%x", expectedHash)
	}
}
