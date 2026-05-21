package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGameTurnLoggerCloseFlushesQueuedEntries(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "game-turn-log-*.log")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}

	logger := &gameTurnLogger{
		path: file.Name(),
		file: file,
		ch:   make(chan gameTurnLogEntry, 8),
	}
	logger.wg.Add(1)
	go logger.run()

	logger.enqueue(1, AITurnDecisionLog{
		Player:         PlayerBlue,
		Move:           Move{X: 1, Y: 2},
		BestDepth:      3,
		TimeToPlayMs:   42,
		DecisionSource: "FULL_SEARCH",
		RootEvalLine:   "fixed_root_eval score=12 structural=4 summary=demo own=[] opp=[]",
		RootPoolLine:   "fixed_root pool=2 forced=1 ordered=[(1,2) (2,3)]",
		RootBands: []AIRootBandSummaryLog{
			{
				Depth:             1,
				ForcedCount:       1,
				ForcedMoves:       "[(1,2)]",
				PrincipalCount:    1,
				PrincipalMoves:    "[(2,3)]",
				SpeculativeCount:  0,
				SpeculativeMoves:  "[]",
				VerificationCount: 0,
				VerificationMoves: "[]",
			},
		},
		RootMoves: []AIRootMoveDetailLog{
			{
				Move:           Move{X: 1, Y: 2},
				Priority:       11,
				SourceFlags:    "[imm_block]",
				ThreatFlags:    "[opp_four]",
				ThreatSeverity: 3,
				ChildForcing:   2,
				ShallowScore:   7.5,
				Forced:         true,
				Status:         "OK",
			},
		},
		RootCandidates: []AIRootCandidateLog{
			{Move: Move{X: 1, Y: 2}, Score: 10, ScoreKnown: true},
		},
	})
	logger.enqueue(2, AITurnDecisionLog{
		Player:         PlayerRed,
		Move:           Move{X: 3, Y: 4},
		BestDepth:      4,
		TimeToPlayMs:   84,
		UsedCache:      true,
		DecisionSource: "ROOT_TT_SHORTCUT",
		RootCandidates: []AIRootCandidateLog{
			{Move: Move{X: 3, Y: 4}, Score: 12, ScoreKnown: true},
			{Move: Move{X: 4, Y: 5}, Status: "ILLEGAL_SENTINEL"},
		},
	})

	logger.close()

	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "------ TURN 1 ------") {
		t.Fatalf("expected first turn to be flushed, got %q", content)
	}
	if !strings.Contains(content, "------ TURN 2 ------") {
		t.Fatalf("expected second turn to be flushed, got %q", content)
	}
	if !strings.Contains(content, "Player: Blue") {
		t.Fatalf("expected blue player label in log, got %q", content)
	}
	if !strings.Contains(content, "Score goal: Negative") {
		t.Fatalf("expected negative goal in log, got %q", content)
	}
	if !strings.Contains(content, "Decision source: FULL_SEARCH") {
		t.Fatalf("expected full search decision source in log, got %q", content)
	}
	if !strings.Contains(content, "Player: Red") {
		t.Fatalf("expected red player label in log, got %q", content)
	}
	if !strings.Contains(content, "Score goal: Positive") {
		t.Fatalf("expected positive goal in log, got %q", content)
	}
	if !strings.Contains(content, "Decision source: ROOT_TT_SHORTCUT") {
		t.Fatalf("expected root TT decision source in log, got %q", content)
	}
	if !strings.Contains(content, "fixed_root pool=2 forced=1 ordered=[(1,2) (2,3)]") {
		t.Fatalf("expected fixed root pool details in log, got %q", content)
	}
	if !strings.Contains(content, "fixed_root depth1 forced=1 [(1,2)] principal=1 [(2,3)] speculative=0 [] verification=0 []") {
		t.Fatalf("expected fixed root bands in log, got %q", content)
	}
	if !strings.Contains(content, "fixed_root_move move=(1,2) prio=11 source=[imm_block] threats=[opp_four] severity=3 child_forcing=2 shallow=7.50 forced=true status=OK") {
		t.Fatalf("expected fixed root move details in log, got %q", content)
	}
	if !strings.Contains(content, "(4, 5) = ILLEGAL_SENTINEL") {
		t.Fatalf("expected illegal sentinel marker in log, got %q", content)
	}
	if !strings.Contains(content, "Move choosed: [3, 4] CACHED") {
		t.Fatalf("expected cached marker in log, got %q", content)
	}
}

func TestGameTurnLogModeDir(t *testing.T) {
	settings := DefaultGameSettings()

	settings.BlueType = PlayerAI
	settings.RedType = PlayerAI
	if got, ok := gameTurnLogModeDir(settings); !ok || got != "AIvsAI" {
		t.Fatalf("expected AIvsAI, got %q ok=%v", got, ok)
	}

	settings.BlueType = PlayerAI
	settings.RedType = PlayerHuman
	if got, ok := gameTurnLogModeDir(settings); !ok || got != "AIvsHuman" {
		t.Fatalf("expected AIvsHuman, got %q ok=%v", got, ok)
	}

	settings.BlueType = PlayerHuman
	settings.RedType = PlayerAI
	if got, ok := gameTurnLogModeDir(settings); !ok || got != "HumanvsAI" {
		t.Fatalf("expected HumanvsAI, got %q ok=%v", got, ok)
	}

	settings.BlueType = PlayerHuman
	settings.RedType = PlayerHuman
	if got, ok := gameTurnLogModeDir(settings); ok || got != "" {
		t.Fatalf("expected no log dir for human_vs_human, got %q ok=%v", got, ok)
	}
}

func TestNewGameTurnLoggerUsesModeSubdirAndStartTimestamp(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("GAME_TURN_LOG_DIR", baseDir)

	settings := DefaultGameSettings()
	settings.BlueType = PlayerAI
	settings.RedType = PlayerHuman
	startedAt := time.Date(2026, time.April, 6, 14, 52, 31, 0, time.UTC)

	logger, err := newGameTurnLogger(settings, startedAt)
	if err != nil {
		t.Fatalf("expected logger creation to succeed: %v", err)
	}
	if logger == nil {
		t.Fatalf("expected logger instance")
	}
	defer logger.close()

	expectedPath := filepath.Join(baseDir, "AIvsHuman", "game_06_04_2026__14_52_31.log")
	if logger.path != expectedPath {
		t.Fatalf("expected path %q, got %q", expectedPath, logger.path)
	}
}
