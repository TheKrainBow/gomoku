# Troubleshooting Report (backend/*.go only)

A) Critical call chain (Game.Tick -> AI -> ScoreBoard -> candidates -> ordering -> minimax -> EvaluateBoard -> final move)

1) Game loop entry
- Step: `Game.Tick(ghostEnabled bool, ghostSink func(Board)) bool` returns whether a move was applied. Inputs: `ghostEnabled`, `ghostSink`. Branching: status check, player nil, human vs AI, pending moves, AI readiness, pondering, thinking. Output: `bool` applied. [backend/game.go:170-211]

2) AI decision entry (synchronous)
- Step: `AIPlayer.ChooseMove(state GameState, rules Rules) Move` builds `AIScoreSettings`, calls `ScoreBoard`, then `bestMoveFromScores`, and returns the move or empty move. Inputs: `state`, `rules`. Branching: `AiLogSearchStats` toggles logging, and `bestMoveFromScores` `ok` decides return. Output: `Move`. [backend/ai_player.go:48-71]

3) AI decision entry (asynchronous)
- Step: `AIPlayer.StartThinking(state GameState, rules Rules, ghostSink func(GameState))` spawns a goroutine that builds settings, optionally sets `OnGhostUpdate`, calls `ScoreBoard`, and sets `readyMove`. Inputs: `state`, `rules`, `ghostSink`. Branching: already thinking, stop signal, ghost mode, `AiLogSearchStats`, `ok` from `bestMoveFromScores`. Output: side effect (async setting of ready move). [backend/ai_player.go:73-143]

4) AI scoring entry
- Step: `ScoreBoard(state GameState, rules Rules, settings AIScoreSettings) []float64` prepares settings, computes hash, handles empty-board fallback, collects initial candidates, then iteratively deepens and calls `scoreBoardAtDepth`. Inputs: `state`, `rules`, `settings`. Branching: board size normalization, depth normalization, config defaulting, empty-board check, no-candidates check, `AiQuickWinExit`, timeout, cache hit, logging. Output: per-cell score grid `[]float64`. [backend/ai_scoring.go:1011-1108]

5) Candidate generation
- Step: `collectCandidateMoves(state GameState, currentPlayer PlayerColor, boardSize int) []candidateMove` computes bbox and density, generates threats, last-move neighborhood, and proximity moves, then sorts by priority and coordinates. Inputs: `state`, `currentPlayer`, `boardSize`. Branching: boardSize normalization, empty board, single-stone case, density/urgency margin, last move exists. Output: ordered `[]candidateMove`. [backend/ai_scoring.go:411-563]

6) Candidate ordering
- Step: `orderCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, maxCandidates int, pvMove *Move) []Move` scores candidates via `heuristicForMove`, adjusts priorities for immediate win/block, sorts, moves PV to front, truncates top-K. Inputs: `state`, `ctx`, `currentPlayer`, `maximizing`, `maxCandidates`, `pvMove`. Branching: immediate win, opponent immediate win, PV override, maxCandidates truncation, maximizing/minimizing score order. Output: ordered `[]Move`. [backend/ai_scoring.go:565-625]

7) Minimax search
- Step: `minimax(state GameState, ctx minimaxContext, depth int, currentPlayer PlayerColor, depthFromRoot int, alpha, beta float64) float64` applies alpha-beta, TT lookup/store, quick win exit, must-block filtering, and evaluates moves via `evaluateMoveWithCache`. Inputs: `state`, `ctx`, `depth`, `currentPlayer`, `depthFromRoot`, `alpha`, `beta`. Branching: terminal depth/timeout/status, TT hit/flag, maximizing/minimizing, quick win exit, must-block gating, alpha/beta cutoffs, timeout. Output: best score for this node. [backend/ai_scoring.go:777-913]

8) Move evaluation within search
- Step: `evaluateMoveWithCache(...) float64` checks move cache, applies the move, optionally calls `minimax`, and returns the score. Inputs: `state`, `ctx`, `currentPlayer`, `move`, `depthLeft`, `depthFromRoot`, `boardHash`, `outCached`, `alpha`, `beta`. Branching: timeout, cache hit, legality, apply success, depth cutoff. Output: score. [backend/ai_scoring.go:916-950]

9) Heuristic evaluation
- Step: `EvaluateBoard(board Board, sideToMove PlayerColor, config Config) float64` scans lines and returns threat-based score. Inputs: `board`, `sideToMove`, `config`. Branching: win-5, open-4 overrides, otherwise weighted totals. Output: float64 evaluation. [backend/ai_eval.go:112-147]

10) Final move selection
- Step: `bestMoveFromScores(scores []float64, state GameState, rules Rules, size int) (Move, bool)` picks the highest score that is legal for `state.ToMove`. Inputs: `scores`, `state`, `rules`, `size`. Branching: legality filter, `bestScore` Inf check. Output: `(Move, bool)`. [backend/ai_player.go:291-309]

B) Perspective / sign audit

- Side-to-move decided in initial reset: `GameState.Reset` sets `ToMove` based on `settings.BlackStarts`. [backend/game_state.go:41-48]
- Side-to-move after move application in game logic: `g.state.ToMove = otherPlayer(g.state.ToMove)` in `Game.TryApplyMove`. [backend/game.go:159-160]
- Side-to-move used in AI evaluation: `EvaluateBoard(board, sideToMove, config)` uses `sideToMove` to define `me` and `opp`. [backend/ai_eval.go:112-116]
- Evaluation interpreted as “good for X” in terminal heuristic: `evaluateStateHeuristic` returns `winScore` for `settings.Player` win and `-winScore` for loss. [backend/ai_scoring.go:649-663]
- Score difference sign: `score := scoreMe - scoreOpp` in `EvaluateBoard`. [backend/ai_eval.go:141-146]
- Max/min switching in minimax: `maximizing := currentPlayer == ctx.settings.Player`, with best initialized to `-Inf` or `+Inf`. [backend/ai_scoring.go:826-830]
- Quick win sign flip: in minimax, `win := winScore` then `if currentPlayer != ctx.settings.Player { win = -winScore }`. [backend/ai_scoring.go:841-845]
- Player swap in minimax recursion: `minimax(next, ctx, depthLeft-1, otherPlayer(currentPlayer), ...)`. [backend/ai_scoring.go:940-943]
- Player swap in legality and immediate win checks: `otherPlayer(currentPlayer)` used for opponent immediate win detection. [backend/ai_scoring.go:576-593]
- “Return best for current player” logic: `bestMoveFromScores` selects highest `score` for legal moves of `state.ToMove`. [backend/ai_player.go:291-305]

C) Threat override audit

- Immediate win detection (move-level): `isImmediateWin` simulates a move, checks capture win and alignment win. [backend/ai_scoring.go:731-749]
- Immediate win caches: `isImmediateWinCached` and `hasImmediateWinCached` cache move and state results. [backend/ai_scoring.go:751-775]
- Must-block logic in minimax: `mustBlock := hasImmediateWinCached(... opponent ...)` then skip moves that still allow opponent immediate win. [backend/ai_scoring.go:836-865]
- Must-block logic in scoring at depth: same filtering in `scoreBoardAtDepth`. [backend/ai_scoring.go:972-996]
- Open-four override in evaluation: opponent open-4 returns `-900000`, own open-4 returns `900000`. [backend/ai_eval.go:134-139]
- Win-5 override in evaluation: `totalsMe.Win5 > 0` returns `evalInf`, `totalsOpp.Win5 > 0` returns `-evalInf`. [backend/ai_eval.go:128-133]
- Open-three detection in move threat flags: open-three is set when both ends are open after total==3. [backend/ai_scoring.go:329-338]
- Threat pattern detection in board evaluation (open/closed/broken patterns): `accumulatePatterns` includes `MMMMM`, `.MMMM.`, `OMMMM.`, `.MMMMO`, `.MMM.M.`, `.M.MMM.`, `.MMM.`, `.MM.M.`, `.M.MM.`, `.MM.`, `.M.M.`. [backend/ai_eval.go:193-209]
- Threat priority constants (win/block/four/open-three etc.): priorities and radii in constants. [backend/ai_scoring.go:183-196]
- Threat flags to priority assignment: `threatFlagsForMove` returns `(winNow, createFour, openThree)` and `generateThreatMoves` assigns priorities with `prioWin`, `prioCreateFour`, `prioCreateOpen3`, and block equivalents. [backend/ai_scoring.go:313-404]
- Exact heuristic weights (config defaults): `Open4=100000`, `Closed4=15000`, `Broken4=12000`, `Open3=2500`, `Broken3=1200`, `Closed3=400`, `Open2=200`, `Broken2=120`, `ForkOpen3=6000`, `ForkFourPlus=20000`. [backend/config.go:55-66]
- Hard win score used in search: `winScore = 10000.0`. [backend/ai_scoring.go:9-12]

D) Candidate inclusion audit

- Bounding box + density + urgency margin: `bbox := computeBBox(...)`, `density := computeDensity(...)`, `margin := 2`, `if density < 0.15 { margin++ }`, `if urgent { margin++ }`, `if margin > 4 { margin = 4 }`, and clamp `x0..y1` to board. [backend/ai_scoring.go:419-485]
- `rNear` usage (proximity): `proximityRadius = 2` and loops around each stone add candidates within Chebyshev distance. [backend/ai_scoring.go:194-195] [backend/ai_scoring.go:526-549]
- `rLast` usage (last move insurance): `lastMoveRadius = 3` and loops around last move add candidates within Chebyshev distance. [backend/ai_scoring.go:195-523]
- Single-stone case uses `proximityRadius` neighborhood and early return. [backend/ai_scoring.go:424-454]
- Top-K truncation: `if maxCandidates > 0 && len(scored) > maxCandidates { scored = scored[:maxCandidates] }`. [backend/ai_scoring.go:617-619]
- Legality filter in scoring: `heuristicForMove` calls `rules.IsLegal` and returns `illegalScore` if false. [backend/ai_scoring.go:638-641]
- Legality filter in search: `evaluateMoveWithCache` calls `ctx.rules.IsLegal` before applying. [backend/ai_scoring.go:929-933]
- Legality filter for final move: `bestMoveFromScores` checks `rules.IsLegal(state, move, state.ToMove)`. [backend/ai_player.go:295-300]

E) 3 most likely bug points (from code only)

1) Candidate truncation can exclude forced blocks
- Lines: truncation happens after ordering in `orderCandidates`, and must-block filtering happens later in minimax. [backend/ai_scoring.go:565-625] [backend/ai_scoring.go:836-865]
- Why risky: if a required blocking move is not in the top-K after ordering, it will never be evaluated by minimax, so a must-block could be missed. This follows from the top-K truncation before must-block filtering. [backend/ai_scoring.go:617-619] [backend/ai_scoring.go:836-865]

2) Must-block detection depends on candidate generation
- Lines: `hasImmediateWinCached` uses `collectCandidateMoves` to look for immediate wins. [backend/ai_scoring.go:761-774]
- Why risky: if `collectCandidateMoves` does not include a critical immediate win move for the opponent (due to bbox/margin limits or ordering), `mustBlock` will be false, so blocking filters won’t engage. [backend/ai_scoring.go:419-549] [backend/ai_scoring.go:836-865]

3) Terminal win score scale differs from evaluation override scale
- Lines: terminal `winScore` is `10000.0`, while `EvaluateBoard` can return `evalInf` (`1_000_000_000.0`) and open-four overrides `±900000`. [backend/ai_scoring.go:9-12] [backend/ai_eval.go:5] [backend/ai_eval.go:128-139]
- Why risky: in mixed paths where `evaluateStateHeuristic` is used for terminal states and `EvaluateBoard` is used for non-terminal states, the score magnitudes differ by orders of magnitude, which can skew comparisons and cause the search to prefer non-terminal evaluations over terminal wins/losses. [backend/ai_scoring.go:649-665] [backend/ai_eval.go:128-139]
