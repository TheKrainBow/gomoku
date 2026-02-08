package main

import (
	"testing"
	"time"
)

func TestUpdateSettingsSwitchToAIVsAIKeepsBoardAndContinuesGame(t *testing.T) {
	prevCfg := GetConfig()
	cfg := prevCfg
	cfg.AiDepth = 1
	cfg.AiMinDepth = 1
	cfg.AiMaxDepth = 1
	cfg.AiTimeBudgetMs = 0
	cfg.AiTimeoutMs = 0
	cfg.AiQueueEnabled = false
	configStore.Update(cfg)
	defer func() {
		configStore.Update(prevCfg)
		FlushGlobalCaches()
	}()

	settings := DefaultGameSettings()
	settings.BlackType = PlayerHuman
	settings.WhiteType = PlayerHuman

	controller := NewGameController(settings)
	controller.StartGame(settings)

	if applied, reason := controller.ApplyHumanMove(Move{X: 9, Y: 9}); !applied {
		t.Fatalf("expected first human move to apply: %s", reason)
	}
	if applied, reason := controller.ApplyHumanMove(Move{X: 10, Y: 9}); !applied {
		t.Fatalf("expected second human move to apply: %s", reason)
	}

	before := controller.State()
	beforeHistorySize := controller.History().Size()
	if beforeHistorySize != 2 {
		t.Fatalf("expected 2 moves before settings switch, got %d", beforeHistorySize)
	}

	updated := controller.Settings()
	updated.BlackType = PlayerAI
	updated.WhiteType = PlayerAI
	controller.UpdateSettings(updated, false)

	after := controller.State()
	if after.Board.At(9, 9) != before.Board.At(9, 9) || after.Board.At(10, 9) != before.Board.At(10, 9) {
		t.Fatalf("expected board stones to be preserved when switching player types")
	}
	if controller.History().Size() != beforeHistorySize {
		t.Fatalf("expected history to be preserved when switching player types")
	}
	if got := controller.Settings(); got.BlackType != PlayerAI || got.WhiteType != PlayerAI {
		t.Fatalf("expected settings to switch to ai_vs_ai, got black=%d white=%d", got.BlackType, got.WhiteType)
	}

	moved := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if controller.Tick() {
			moved = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !moved {
		t.Fatalf("expected AI to make a move after switching to ai_vs_ai")
	}
	if controller.History().Size() <= beforeHistorySize {
		t.Fatalf("expected history to grow after AI move")
	}
}
