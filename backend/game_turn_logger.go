package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type gameTurnLogEntry struct {
	TurnNumber int
	Decision   AITurnDecisionLog
}

type gameTurnLogger struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	ch     chan gameTurnLogEntry
	wg     sync.WaitGroup
	closed bool
}

func newGameTurnLogger(settings GameSettings, startedAt time.Time) (*gameTurnLogger, error) {
	modeDir, ok := gameTurnLogModeDir(settings)
	if !ok {
		return nil, nil
	}
	baseDir := os.Getenv("GAME_TURN_LOG_DIR")
	if baseDir == "" {
		baseDir = filepath.Join("..", "logs")
	}
	dir := filepath.Join(baseDir, modeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	path := filepath.Join(dir, fmt.Sprintf("game_%s.log", startedAt.Format("02_01_2006__15_04_05")))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	l := &gameTurnLogger{
		path: path,
		file: file,
		ch:   make(chan gameTurnLogEntry, 64),
	}
	l.wg.Add(1)
	go l.run()
	return l, nil
}

func gameTurnLogModeDir(settings GameSettings) (string, bool) {
	switch {
	case settings.BlueType == PlayerAI && settings.RedType == PlayerAI:
		return "AIvsAI", true
	case settings.BlueType == PlayerAI && settings.RedType == PlayerHuman:
		return "AIvsHuman", true
	case settings.BlueType == PlayerHuman && settings.RedType == PlayerAI:
		return "HumanvsAI", true
	default:
		return "", false
	}
}

func (l *gameTurnLogger) run() {
	defer l.wg.Done()
	for entry := range l.ch {
		if l.file == nil {
			continue
		}
		if _, err := l.file.WriteString(formatGameTurnLogEntry(entry)); err != nil {
			log.Printf("[game-log] write failed for %s: %v", l.path, err)
		}
	}
}

func (l *gameTurnLogger) enqueue(turnNumber int, decision AITurnDecisionLog) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.ch <- gameTurnLogEntry{TurnNumber: turnNumber, Decision: decision}
}

func (l *gameTurnLogger) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.ch)
	file := l.file
	l.mu.Unlock()
	l.wg.Wait()
	if file != nil {
		_ = file.Sync()
		_ = file.Close()
	}
}

func formatGameTurnLogEntry(entry gameTurnLogEntry) string {
	var sb strings.Builder
	decision := entry.Decision
	fmt.Fprintf(&sb, "------ TURN %d ------\n", entry.TurnNumber)
	fmt.Fprintf(&sb, "Player: %s\n", gameLogPlayerColorLabel(decision.Player))
	fmt.Fprintf(&sb, "Score goal: %s\n", gameLogScoreGoalLabel(decision.Player))
	if decision.DecisionSource != "" {
		fmt.Fprintf(&sb, "Decision source: %s\n", decision.DecisionSource)
	}
	fmt.Fprintf(&sb, "Best depth: %d\n", decision.BestDepth)
	if decision.RootEvalLine != "" {
		sb.WriteString(decision.RootEvalLine)
		sb.WriteByte('\n')
	}
	if decision.RootPoolLine != "" {
		sb.WriteString(decision.RootPoolLine)
		sb.WriteByte('\n')
	}
	for _, band := range decision.RootBands {
		fmt.Fprintf(&sb, "fixed_root depth%d forced=%d %s principal=%d %s speculative=%d %s verification=%d %s\n",
			band.Depth,
			band.ForcedCount,
			band.ForcedMoves,
			band.PrincipalCount,
			band.PrincipalMoves,
			band.SpeculativeCount,
			band.SpeculativeMoves,
			band.VerificationCount,
			band.VerificationMoves,
		)
	}
	for _, rootMove := range decision.RootMoves {
		fmt.Fprintf(&sb, "fixed_root_move move=(%d,%d) prio=%d source=%s threats=%s severity=%d child_forcing=%d shallow=%.2f forced=%t status=%s\n",
			rootMove.Move.X,
			rootMove.Move.Y,
			rootMove.Priority,
			rootMove.SourceFlags,
			rootMove.ThreatFlags,
			rootMove.ThreatSeverity,
			rootMove.ChildForcing,
			rootMove.ShallowScore,
			rootMove.Forced,
			gameLogRootStatusLabel(rootMove.Status),
		)
	}
	sb.WriteString("Root candidates:\n")
	for _, cand := range decision.RootCandidates {
		if cand.ScoreKnown {
			fmt.Fprintf(&sb, "(%d, %d) = %.2f score\n", cand.Move.X, cand.Move.Y, cand.Score)
		} else if cand.Status != "" {
			fmt.Fprintf(&sb, "(%d, %d) = %s\n", cand.Move.X, cand.Move.Y, cand.Status)
		} else {
			fmt.Fprintf(&sb, "(%d, %d) = ? score\n", cand.Move.X, cand.Move.Y)
		}
	}
	fmt.Fprintf(&sb, "Time to play: %dms\n\n", decision.TimeToPlayMs)
	fmt.Fprintf(&sb, "Move choosed: [%d, %d]", decision.Move.X, decision.Move.Y)
	if decision.UsedCache {
		sb.WriteString(" CACHED")
	}
	sb.WriteString("\n\n")
	return sb.String()
}

func gameLogPlayerColorLabel(player PlayerColor) string {
	if player == PlayerRed {
		return "Red"
	}
	return "Blue"
}

func gameLogScoreGoalLabel(player PlayerColor) string {
	if player == PlayerRed {
		return "Positive"
	}
	return "Negative"
}

func gameLogRootStatusLabel(status string) string {
	if status == "" {
		return "UNKNOWN"
	}
	return status
}
