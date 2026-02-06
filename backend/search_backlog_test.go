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
