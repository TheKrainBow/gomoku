GOMOKU AI SPEEDUP PLAN (Go backend) — Implementation Tasks for Codex
Constraint: Base everything on current backend/*.go structure (minimax, TT, candidates, heuristicForMove, EvaluateBoard).
Goal: Depth 5 should be significantly faster (target < 1s typical midgame), without breaking correctness (must-block).

========================================================
0) PRINCIPLES
========================================================
- Biggest speed lever: reduce branching factor (b) and increase alpha-beta cutoffs.
- Never prune away forced moves (must-block / winning move).
- Avoid expensive per-node operations (full-board eval scans, heavy cloning).

========================================================
1) DYNAMIC TOP-K (Depth-based) + MUST-BLOCK SAFE PRUNING
========================================================
Problem:
- orderCandidates truncates to maxCandidates globally; minimax must-block happens after ordering and can miss blocks.

Tasks:
1.1 Add a function that returns recommended K based on depthFromRoot and depthLeft:
    - Example policy:
      - depthFromRoot == 0: K_root = config.AiMaxCandidatesRoot (e.g. 30-50)
      - depthLeft >= 3: K_deep = config.AiMaxCandidatesDeep (e.g. 10-20)
      - else: K_mid = config.AiMaxCandidatesMid (e.g. 15-30)
    - Implement as:
      func candidateLimit(ctx minimaxContext, depthLeft, depthFromRoot int) int

1.2 In minimax(), when generating ordered moves:
    - If mustBlock == true:
        - DO NOT truncate by top-K
        - OR better: restrict to ONLY blocking moves (moves that prevent opponent immediate win)
          i.e. build filtered list = {m | !hasImmediateWinCached(nextStateAfter(m), opponent)}
          then search ONLY those.
    - If there exists an immediate winning move for current player:
        - Search ONLY winning moves (or return immediately).
    - Otherwise:
        - Use candidateLimit(...) to truncate.

1.3 Ensure ScoreBoard root uses a larger K, deeper nodes use smaller K.
    - Update orderCandidates call sites in:
      - scoreBoardAtDepth(...)
      - minimax(...)
    - Make sure PV move still goes first after sorting.

Add Config knobs in backend/config.go:
- AiMaxCandidatesRoot (default ~40)
- AiMaxCandidatesMid  (default ~25)
- AiMaxCandidatesDeep (default ~14)
- AiEnableDynamicTopK (bool)

========================================================
2) STRONGER MOVE ORDERING (MORE CUTS)
========================================================
2.1 TT best-move ordering
- Extend TT entries to store a "best move" (principal variation move) for that hash.
- On TT lookup in minimax:
  - if TT hit includes bestMove, pass pvMove=bestMove into orderCandidates
  - ensure orderCandidates promotes pvMove to front (already supports pvMove)

2.2 Killer move heuristic (per ply)
- In minimaxContext, store killers[depthFromRoot][2] (two best cutoff moves)
- When a move causes beta cutoff, record it as killer for that ply.
- In orderCandidates, boost killer moves priority (just below immediate wins / blocks).

2.3 History heuristic
- Maintain a map move->score (or [19][19] int) increased when move causes cutoff.
- In orderCandidates, add this as tie-break (higher first for maximizing, lower for minimizing as appropriate).

Add Config knobs:
- AiEnableKillerMoves
- AiEnableHistoryMoves
- AiKillerBoost (int)
- AiHistoryBoost (int)

========================================================
3) CHEAPER "WIN-IN-1 / MUST-BLOCK" DETECTION (AVOID SIMULATING MANY MOVES)
========================================================
Problem:
- hasImmediateWinCached uses collectCandidateMoves + isImmediateWin (simulate move + capture/alignment). This is expensive and can miss if candidates are pruned.

Tasks:
3.1 Implement a pure line-scan function to find all "complete 5" winning cells for a given player:
    func findAlignmentWinMoves(board Board, player PlayerColor, winLen int) []Move
- Scan 4 directions.
- For each line, detect patterns where placing one stone yields >= winLen contiguous.
- Return the empty cells that complete the line.
- Must be O(board^2 * directions) but with small constant, no move application.

3.2 Implement a local capture-win candidate detector (optional but recommended):
    func findCaptureWinMoves(state GameState, rules Rules, player PlayerColor) []Move
- Only consider empty cells adjacent within 2 of existing stones (small radius),
  because captures require local pattern player-opp-opp-player around the placed stone.
- For each candidate, compute captures locally (reuse FindCaptures logic with hypothetical placement),
  and if capture count would reach threshold => winning move.

3.3 Rewrite hasImmediateWinCached to NOT depend on collectCandidateMoves:
    - Build candidate wins from:
      - alignment win moves from findAlignmentWinMoves
      - capture win moves from findCaptureWinMoves (or keep the old method but widen)
    - For each such move:
      - Check legality (rules.IsLegal)
      - Confirm using isImmediateWinCached if needed (should be fast now because list is small)
    - Cache result per state hash as before.

Add Config knob:
- AiUseScanWinIn1 (default true)

========================================================
4) EVAL SPEED: ADD EVAL CACHE (EASY WIN) OR INCREMENTAL EVAL (BIGGER WIN)
========================================================
Option A (easy): Eval cache by hash
4A.1 Implement a small set-associative eval cache:
  - key: zobrist hash + sideToMove
  - value: float64 score
  - store/retrieve in evaluateStateHeuristic (or wherever EvaluateBoard is called)
4A.2 On hit: skip EvaluateBoard scan completely.
4A.3 Clear/reroot similarly to TT if needed, or just overwrite in ring.

Config:
- AiEvalCacheSize (default e.g. 1<<18 entries)
- AiEnableEvalCache (default true)

Option B (harder): Incremental eval (only if time)
4B.1 Maintain pattern totals incrementally when applying moves:
  - Update only the 4 lines crossing the move.
  - Recompute those line segments and update running totals.
(This is larger refactor; do Option A first.)

========================================================
5) AVOID HEAVY CLONING: APPLY/UNDO SEARCH (IF CURRENTLY COPYING)
========================================================
Check current code:
- If minimax/evaluateMoveWithCache clones board/state per move, replace with apply/undo.

Tasks:
5.1 Create ApplyMoveInPlace(state *GameState, move Move, player PlayerColor, rules Rules) (undo UndoInfo, ok bool)
- UndoInfo includes:
  - captured stones positions + previous cell values
  - previous hash
  - previous ToMove, lastMove, forced capture fields, captures counts, status/winning line if touched
5.2 Create UndoMoveInPlace(state *GameState, undo UndoInfo)
5.3 Use in minimax recursion to avoid allocations.

(If code already applies/undoes cheaply, skip.)

========================================================
6) DEBUG / METRICS (TO CONFIRM SPEEDUPS)
========================================================
Add optional logging counters guarded by config:
- nodesVisited
- ttHits
- evalCacheHits
- avgCandidatesRoot / avgCandidatesDeep
- cutoffs count

Print these when AiLogSearchStats is enabled.

========================================================
ACCEPTANCE CHECKLIST
========================================================
- Must-block is correct even with truncation enabled:
  - when mustBlock true, engine searches blocking moves (no top-K loss).
- hasImmediateWinCached does not rely on pruned collectCandidateMoves.
- Dynamic top-K reduces candidate count at deeper depths.
- TT best move influences ordering.
- Eval cache reduces calls to EvaluateBoard.
- Search depth 5 is measurably faster (lower nodes, faster wall-clock).

========================================================
DELIVERABLES
========================================================
- PR with code changes + updated config defaults
- Short markdown note: “AI speed improvements” explaining new knobs and behavior
