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
- Added per-player AI caches with re-rooting, PV move ordering from TT, cache size logging, and forced-capture enforcement when alignment can be broken by capture.
- Added Docker launcher and lifecycle workflow (`gomoku` + Makefile targets) and rewrote `README.md` to document the Go web stack instead of C++/SDL flow.
- Replaced SDL entrypoint with a Crow backend server exposing REST routes (`/api/v1/game/*`) and WebSocket state streaming (`/ws`), added server-side tick loop for AI turns, and updated build/docs for backend-only workflow.
- Refactored backend game progression to event-driven mode: removed global ticker loop, added single-worker AI orchestration with revision-safe commit/discard, and added throttled WebSocket `ai_think` snapshots (depth/nodes/topK/PV) plus `ai_done` events.
- Simplified Crow handling: `src/main.cpp` now assumes Crow is present (no runtime fallback branch), and `Makefile` now fails early with a clear error if Crow headers are missing from known include paths.
- Updated build to auto-install Crow locally with CMake when headers are missing (`make` now runs `crow-install` and installs to `third_party/crow/install`).
