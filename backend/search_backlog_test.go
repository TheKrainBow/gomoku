package main

import "testing"

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

	needs, target, solved := backlogNeedsAnalysis(state, cfg, &cache)
	if !needs {
		t.Fatalf("expected analysis to be needed without TT entry")
	}
	if target <= 0 {
		t.Fatalf("expected positive target depth, got %d", target)
	}
	if solved != 0 {
		t.Fatalf("expected solved depth 0 without TT entry, got %d", solved)
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
	tt.Store(key, target, 42, TTExact, Move{X: 0, Y: 0})

	needs, gotTarget, solved := backlogNeedsAnalysis(state, cfg, &cache)
	if needs {
		t.Fatalf("expected analysis to be skipped for exact depth>=target entry")
	}
	if gotTarget != target {
		t.Fatalf("expected target depth %d, got %d", target, gotTarget)
	}
	if solved < target {
		t.Fatalf("expected solved depth >= target depth, got solved=%d target=%d", solved, target)
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
	tt.Store(key, target+2, 42, TTLower, Move{X: 0, Y: 0})

	needs, _, solved := backlogNeedsAnalysis(state, cfg, &cache)
	if !needs {
		t.Fatalf("expected non-exact entry to still require analysis")
	}
	if solved != 0 {
		t.Fatalf("expected solved depth 0 for non-exact entry, got %d", solved)
	}
}
