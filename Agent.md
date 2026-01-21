# Agent.md

This file tracks AI-assisted changes and serves as context for future agents. Update it whenever new work is done.

## Summary of AI work (to date)
- Implemented Gomoku subject rules: captures, capture win threshold, double-three forbidden for Black (with settings toggle), alignment win with break-by-capture check, and capture-aware legality flow.
- Added capture counters to game state, capture lists to move history, and Board::remove for capture removal.
- Added rule helpers to find captures, detect open threes, detect winning lines, and check if an alignment can be broken by capture.
- Added debug test harness (compile-time flag) for capture and double-three checks.
- Enhanced logging: illegal move reasons, matchup line, move logs with timing and capture totals, win reasons, alignment-break warning, and draw message; added colored output.
- Added win-line rendering in UI (red outline around aligned stones).
- Added timing for human/AI turns (turn timer from start of player’s turn to move applied); AI logs include “IA played…” and colored timing thresholds.
- Dynamic log alignment: coordinate, capture, and time columns sized based on board size and capture target.
- Reorganized source tree into logical folders:
  - `src/Core` (rules/game/board/state/controller/settings/history/move)
  - `src/Players` (IPlayer/AI/Human)
  - `src/UI` (SDL app/renderer/layout/mapper)
  - `src/Debug` (debug tests)
- Updated Makefile paths and include directories; made object directory creation handle subfolders.

## Files added/updated (high-level)
- Added `Agent.md`.
- Core logic changes under `src/Core/*`.
- Player implementations moved to `src/Players/*`.
- UI code moved to `src/UI/*`.
- Debug tests moved to `src/Debug/*`.
- Build updates in `Makefile`.

## Notes for future agents
- Always append new work here with a short summary and key files touched.
- Keep logs readable and aligned; prefer updating `src/Core/Game.cpp` for log formatting.
- If rules change, update `src/Core/Rules.cpp` and add/adjust debug tests in `src/Debug/DebugTests.cpp`.

## Latest update
- Updated `README.md` to document game rules, logging/timing, UI behavior, project structure, debug tests, and configuration.
