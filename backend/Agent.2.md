GOMOKU AI (Go backend) — 500ms Budget Mode Additions (Complement to previous speedup plan)

Context
- You already implemented / are implementing: dynamic top-K, must-block safety, stronger ordering, win-in-1 scan, eval cache, etc.
- New requirement: AI must answer in < 500ms on average.
Goal
- Convert search into a strict time-budgeted engine:
  - iterative deepening + aspiration windows
  - aggressive depth-dependent branching
  - tactical/forced-line mode
  - always return best move from last completed depth

========================================================
1) HARD TIME BUDGET: ITERATIVE DEEPENING THAT STOPS CLEANLY
========================================================
Task 1.1: Add a “time controller” to minimaxContext
- Store:
  - deadline time.Time
  - stopFlag atomic/bool
  - start time
  - node counters
- Provide:
  - func (ctx *minimaxContext) timeUp() bool
    returns true when now >= deadline or stopFlag set.
- Ensure ALL deep loops check timeUp() frequently:
  - at top of minimax()
  - inside move loops
  - inside evaluateMoveWithCache
  - inside scoreBoardAtDepth root loops

Task 1.2: ScoreBoard uses iterative deepening under budget
- For depth = 1..AiMaxDepth:
  - if timeUp(): break BEFORE starting a new depth
  - run depth search
  - only if fully completed (no timeout) => commit:
    - bestMove
    - bestScore
    - PV line (optional)
    - principal variation move for next iteration
- If timeout mid-depth:
  - discard partial depth result
  - return last completed depth’s bestMove/scores
- Must be explicit: “best completed depth” only.

Config knobs:
- AiTimeBudgetMs (default 500)
- AiMaxDepth (still exists)
- AiMinDepth (optional; default 1)
- AiReturnLastCompleteDepthOnly (default true)

========================================================
2) ASPIRATION WINDOWS (ITERATIVE DEEPENING SPEEDUP)
========================================================
Task 2.1: Use last iteration score to narrow alpha/beta at root
- For depth d >= 2:
  - center = lastCompletedScore
  - window = config.AiAspWindow (e.g. 2000)
  - Run root search with alpha=center-window, beta=center+window
  - If it “fails low” or “fails high”, re-search with expanded window
    (e.g. double window up to full range).
- Implement only at root (simpler + most benefit).

Config:
- AiEnableAspiration (default true)
- AiAspWindow (default 2000)
- AiAspWindowMax (default 2000000000)

========================================================
3) DEPTH-DEPENDENT BRANCHING (K) TUNED FOR 500ms
========================================================
You already added dynamic top-K; now tune it explicitly for 500ms average.

Task 3.1: Add separate K tables for:
- quiet positions (no urgent threats)
- tactical positions (urgent threats / must-block / create-four)
Example defaults:
- Quiet:
  - Root: 24
  - Ply 1-2: 14
  - Ply 3+: 8
- Tactical:
  - Root: 30 (allow more forcing lines at root)
  - Ply 1-2: 18
  - Ply 3+: 10
- Must-block override:
  - If mustBlock: search ONLY blocking moves (ignore K)
- Win-now override:
  - If have immediate winning move(s): search ONLY those moves

Task 3.2: Determine “urgent/tactical” cheaply
- Use existing threat flags + win-in-1 scan:
  urgent if any of:
  - hasImmediateWin(currentPlayer) OR hasImmediateWin(opponent)
  - create-four moves exist
  - open-four detected in EvaluateBoard override OR threat scan

Config:
- AiKQuietRoot, AiKQuietMid, AiKQuietDeep
- AiKTactRoot, AiKTactMid, AiKTactDeep
- AiEnableTacticalK (default true)

========================================================
4) TACTICAL MODE MOVE GENERATION (FORCED-LINE SEARCH)
========================================================
Task 4.1: Add a “tacticalCandidates” generator used when urgent
When urgent:
- Generate ONLY:
  - win-in-1 moves for current player
  - blocks of opponent win-in-1
  - create-four moves (and blocks of opponent create-four)
  - (optional) create-open-three moves if nothing else exists
- If this set is non-empty => search only these.
- Else fallback to normal candidate generator.

Important:
- This is NOT correctness pruning in quiet positions; it is selective search
  to go deeper inside forcing sequences under time constraints.

Config:
- AiEnableTacticalMode (default true)

========================================================
5) QUIESCENCE-LIKE EXTENSION (OPTIONAL, CHEAP VERSION)
========================================================
To reduce horizon effect at shallow depth under 500ms:
- If depthLeft == 0 but position is “tactical” (immediate threats exist):
  - extend by 1 ply but ONLY with tacticalCandidates
This is a limited quiescence.

Config:
- AiEnableTacticalExtension (default true)
- AiTacticalExtensionDepth (default 1)

========================================================
6) TT / CACHE POLICY FOR BUDGET MODE
========================================================
Task 6.1: Reuse TT across moves aggressively
- Keep TT between turns.
- Ensure TT entries store bestMove for ordering.
- Consider “aging” or “generation” counter; replace older entries first.

Task 6.2: Cache “root move ordering” and PV line across iterative deepening
- Keep PV move from last completed depth and feed into next depth ordering.

Config:
- AiTTReuseAcrossMoves (default true)
- AiTTGenerationAging (default true)

========================================================
7) INSTRUMENTATION TO HIT 500ms CONSISTENTLY
========================================================
Task 7.1: Add search stats gathered per move
- nodes
- nps (nodes/sec)
- completedDepth
- avgMovesSearchedPerPly (or at least root candidate count + effective searched moves)
- ttHits
- evalCacheHits
- cutoffs

Task 7.2: Log a single compact line when AiLogSearchStats enabled:
Example:
AI: t=492ms depth=7 nodes=1.2M nps=2.4M ttHit=38% evalHit=62% rootMoves=24 effRootSearched=18

This is essential to tune K tables.

========================================================
8) ACCEPTANCE CRITERIA (500ms)
========================================================
- Average move time under 500ms on midgame positions.
- Depth reached varies:
  - quiet: typically 5-7
  - tactical/forced: often 8-12
- Must-block correctness preserved:
  - never loses to a 1-move threat due to truncation.
- Engine always returns best move from last completed depth if time runs out.

========================================================
DELIVERABLES
========================================================
- Code changes + config defaults for budget mode
- A short markdown “AI Budget Mode” explaining:
  - how time budget is enforced
  - how dynamic K works
  - tactical mode / extension behavior
  - what stats to watch and how to tune
