# Gomoku Backend AI

This document explains how the Gomoku AI works in the backend. It focuses on the decision-making pipeline, scoring, search, caches, and the configuration knobs that control behavior.

## High-level flow

1. The game loop (`Game.Tick`) checks whose turn it is.
2. If the current player is an AI, it first tries a pondered move (background search result).
3. If no pondered move is ready, it starts an async search with `AIPlayer.StartThinking`.
4. The AI evaluates the board with `ScoreBoard` to compute a score for each candidate move.
5. The AI selects the legal move with the best score and returns it to the game.

Two paths exist:

- **Synchronous**: `AIPlayer.ChooseMove` computes scores and returns the best move immediately.
- **Asynchronous**: `AIPlayer.StartThinking` runs the same scoring in a goroutine, then exposes the move via `HasMoveReady` / `TakeMove`.

## Search overview

The AI uses a **minimax search with alpha-beta pruning** and **iterative deepening**.

- **Minimax** explores possible move sequences, alternating between maximizing (AI) and minimizing (opponent) outcomes.
- **Alpha-beta pruning** cuts branches that cannot improve the current best outcome.
- **Iterative deepening** repeats searches from depth `1` to `AiDepth`, keeping the best scores found so far and allowing early exit on timeout.

The core entry is `ScoreBoard` in `backend/ai_scoring.go`.

## Candidate move generation

To keep the search tractable, the AI does not explore every empty cell:

- `collectCandidateMoves` builds moves adjacent to existing stones (8-neighborhood).
- If the board is empty, the only candidate is the center.
- Candidates are ordered by a fast heuristic (`orderCandidates`) and optionally truncated to the top `AiTopCandidates`.

This focuses the search on locally relevant moves instead of scanning the full board.

## Heuristic scoring

Moves and states are evaluated with a **board-wide threat evaluation**. Every line (rows, columns, diagonals) is scanned for threat patterns for both players (open-4, closed-4, open-3, broken-3, open-2, etc.). The result is a weighted sum, with hard overrides for must-block cases like opponent open-4.

Implementation details are in `backend/ai_eval.go` and `evaluateStateHeuristic`.

## Win detection and captures

The rules applied in the AI match the game rules:

- **Alignment win**: `rules.IsWin` checks for `WinLength` in a line (4 directions).
- **Capture win**: if captured stones reach `CaptureWinStones`, the player wins.
- **Double-three restriction**: if enabled, the AI treats double-three as illegal for the configured color.

Legal move checks always go through `rules.IsLegal`.

## Immediate win and forced block logic

The AI short-circuits in two key cases:

- **Immediate win exit** (`AiQuickWinExit`): if any candidate move is an immediate win (alignment or capture win), that move is chosen without deeper search.
- **Must-block detection**: before deeper search, the AI checks if the opponent has any immediate winning move. If so, it evaluates only moves that prevent that win.

These checks use cached helpers:

- `isImmediateWinCached`
- `hasImmediateWinCached`

## Minimax details

- Search alternates between players, with the AI as the maximizing player.
- The terminal evaluation is `evaluateStateHeuristic`:
  - Returns a large positive/negative score for a won/lost terminal state.
  - Otherwise, returns the difference between the best AI move and the best opponent move.
- Alpha and beta bounds are updated on each candidate.
- If `AiTimeoutMs` expires, the search stops at the current depth and returns the best known evaluation.

## Caching and transposition table

Multiple caches reduce repeated work:

- **Transposition table (TT)**: fixed-size, set-associative table with bounded eviction. Stores the best value, bound type (exact/lower/upper), and best move at a given depth.
- **Move cache**: stores evaluated move scores for a given `(state, depth, move)`.
- **Immediate win caches**: store whether a move or state yields an immediate win.
- **Depth cache**: stores entire score grids for a `(board hash, depth, board size, player)`.

The TT is never fully cleared; entries are evicted by age/depth to keep memory bounded. This keeps reuse high across moves.

### Hashing

The AI uses **Zobrist hashing** for cache keys. The hash includes:

- Stone placement
- Side to move
- Capture counters (black/white)

This prevents illegal TT collisions between states that look similar but differ in captures or turn.

### Cache re-rooting

After each applied move, `RerootCache` prunes cached states that are no longer reachable from the current root. This keeps the cache aligned with the actual game progression and limits memory growth.

## Pondering (background search)

An AI worker goroutine keeps searching the current root position even when it is not the AI’s turn. This fills the TT and often produces an instant move response when the AI turn arrives.

- The worker reroots whenever a move is applied.
- Searches are interrupted when a new root version arrives.
- Only the AI’s own turn can consume the “pondered” best move; otherwise the work is still reused via TT.

## Ghost mode (search visualization)

If `GhostMode` is enabled:

- The AI streams intermediate boards during search via `OnGhostUpdate`.
- The server publishes these to connected websocket clients (`ghost_ws.go`).
- This is meant for visualization, not for decision changes.
 - Updates are throttled by `AiGhostThrottleMs`.

## AI configuration knobs

The AI is controlled by both **game settings** and **global config**.

### Game settings (per match)
- `BoardSize`, `WinLength`, `CaptureWinStones`: core rules that affect evaluation.
- `ForbidDoubleThreeBlack`, `ForbidDoubleThreeWhite`: legal move restrictions.

### Global config (runtime)

- `AiDepth`: max search depth for iterative deepening.
- `AiTimeoutMs`: time limit for the search (0 disables timeouts).
- `AiTopCandidates`: maximum number of candidate moves searched per depth.
- `AiQuickWinExit`: immediate win short-circuit.
- `AiPonderingEnabled`: enables background search.
- `AiGhostThrottleMs`: throttles ghost update frequency.
- `AiTtSize`: TT table size (rounded to power-of-two).
- `AiTtBuckets`: set-associative bucket count (2 or 4 recommended).
- `AiTtUseSetAssoc`: toggles set-associative buckets (false = direct-mapped).
- `AiLogSearchStats`: logs search stats per move.
- `AiTtMaxEntries`: legacy fallback if `AiTtSize` is unset.
- `GhostMode`: enables ghost updates.
- `Heuristics`: all threat pattern weights and fork bonuses are centralized here (see `backend/config.go`).

Defaults are in `backend/config.go`.

## Threading model

- AI searches run in a goroutine (`StartThinking`).
- Background pondering runs continuously when enabled.
- Atomic flags and mutexes coordinate search state, ghost board updates, and cache access.
- `ResetForConfigChange` can interrupt a search and clear caches.

## Practical implications

- Increasing `AiDepth` improves tactical play but grows exponentially in cost.
- Lowering `AiTopCandidates` speeds search but can miss strong moves.
- `AiTimeoutMs` acts as a safety valve to keep responsiveness.
- Ghost mode adds overhead because it clones and broadcasts boards during search.
- Pondering can reduce latency but increases CPU usage.

## Files of interest

- `backend/ai_player.go`: AI player lifecycle and async search.
- `backend/ai_scoring.go`: scoring, minimax, caches, and heuristics.
- `backend/rules.go`: legality, captures, and win detection.
- `backend/game.go`: integration into the game loop.
- `backend/config.go`: AI configuration.
- `backend/ghost_ws.go`: ghost search streaming.
