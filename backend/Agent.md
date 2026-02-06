Gomoku Backend – FULL cache & TT rework (authoritative instructions)
===================================================================

IMPORTANT
---------
This is a HARD RESET of the cache system.
→ Delete / ignore / replace any existing cache or TT logic.
→ Do NOT try to “adapt” the old system.
→ Rebuild from the concepts below only.

Goal
----
- Stop backend memory blowups
- Store fewer but MUCH more valuable entries
- Separate concerns cleanly
- Make the engine improve over time instead of bloating
- Keep correctness of minimax / alpha-beta

We will implement:
1) ONE real Transposition Table (Search TT)
2) OPTIONAL lightweight Eval cache (strictly controlled)
3) NO other board caches
4) Keep the off-game self-learning / exploration system (explicitly desired)

------------------------------------------------------------
1) SEARCH TRANSPOSITION TABLE (MANDATORY)
------------------------------------------------------------

Purpose
-------
Cache results of minimax search to avoid re-searching identical game states
reached via different move orders.

This is the ONLY cache allowed to influence alpha-beta cutoffs.

Key
---
FULL GAME STATE KEY:
- board stones
- side to move
- capture counts
- (anything affecting legality or win condition)

Use Zobrist (64-bit or 128-bit).
Do NOT attempt board-only keys here.

Value (TT Entry)
----------------
Each entry MUST store:
- depth          : int   (search depth this result is valid for)
- score          : int   (minimax score, Black/Player1 perspective)
- flag           : enum  (EXACT | LOWERBOUND | UPPERBOUND)
- bestMove       : Move  (for move ordering)
- generation     : int   (optional aging / replacement)

Rules
-----
- Depth matters: deeper > shallower
- EXACT entries are more valuable than bounds
- Bounds are still useful for pruning

Replacement policy (STRICT)
---------------------------
When inserting:
- Prefer higher depth
- Prefer EXACT over LOWER/UPPER
- Prefer newer generation
- Never keep unlimited entries (fixed-size table)

Do NOT:
- store multiple entries per key
- store shallow junk forever
- store heuristic-only values here

------------------------------------------------------------
2) EVAL CACHE (OPTIONAL, STRICTLY LIMITED)
------------------------------------------------------------

Purpose
-------
Avoid recomputing expensive heuristic evaluation (ScoreBoard).

This cache MUST NOT affect alpha-beta correctness.
It is purely an optimization.

Key
---
Depends on evaluation design:

IF evaluation is turn-agnostic:
- board stones (+ captures if used)

IF evaluation depends on side-to-move (likely in Gomoku):
- board stones + side-to-move (+ captures)

Value
-----
- heuristic score only
- NO depth
- NO bounds
- NO best move

Storage rules (VERY IMPORTANT)
------------------------------
To prevent memory explosion:
- Fixed size
- Replacement policy (LRU / aging)
- OPTIONAL selective storage:
  - store only if abs(score) >= THRESHOLD
  - OR store only if eval was expensive
  - OR store only if called many times

If memory pressure exists:
→ disable Eval cache entirely (Search TT must still work).

------------------------------------------------------------
3) WHAT MUST BE REMOVED
------------------------------------------------------------

DELETE / DO NOT IMPLEMENT:
- board-only → minimax score cache
- mixed-purpose caches
- storing every board ever evaluated
- unlimited maps keyed by board hash
- storing evaluation + search results together

One cache = one responsibility.

------------------------------------------------------------
4) TRANSLATED / SYMMETRY TT (IF PRESENT)
------------------------------------------------------------

If translation-based or symmetry-based TT is used:
- It MUST be part of the Search TT system
- It MUST have explicit validity guards
- It MUST obey depth + replacement rules
- It MUST NOT bypass correctness rules

If unsafe, disable translation TT entirely.
Correct > clever.

------------------------------------------------------------
5) OFF-GAME SELF-IMPROVEMENT (KEEP THIS)
------------------------------------------------------------

YES — this is a GOOD IDEA and should be KEPT.

Purpose
-------
When no live game is running, the engine may:
- explore partial boards
- deepen known positions
- refine Search TT entries
- improve move ordering knowledge

Rules (CRITICAL)
----------------
- Off-game exploration may ONLY populate the Search TT
- It must respect all TT rules (depth, flags, replacement)
- It must NOT create unbounded growth
- It must NOT store speculative or heuristic-only values as EXACT

Good use cases
--------------
- Deepen early-game positions
- Re-evaluate high-value TT entries at greater depth
- Improve bestMove ordering data

Bad use cases
-------------
- Blindly enumerate all boards
- Store shallow garbage
- Ignore depth / validity

------------------------------------------------------------
6) SCORE SEMANTICS (MANDATORY CONSISTENCY)
------------------------------------------------------------

All scores everywhere:
- Positive = Player1 / Black advantage
- Negative = Player2 / White advantage

Evaluation and minimax MUST use the same convention.

------------------------------------------------------------
7) DEBUG / SAFETY REQUIREMENTS
------------------------------------------------------------

Add logging / counters for:
- TT size
- TT hit rate
- TT replacement rate
- Eval cache hit rate
- Memory usage

It must be possible to:
- disable Eval cache
- disable translated TT
- flush all caches safely

------------------------------------------------------------
SUMMARY (DO NOT DEVIATE)
------------------------------------------------------------

FINAL CACHE ARCHITECTURE:

1) Search TT
   - full game state key
   - depth-aware
   - bounded size
   - correct alpha-beta semantics

2) Eval cache (optional)
   - heuristic-only
   - bounded
   - aggressively filtered

3) Off-game exploration
   - KEEP
   - feeds Search TT only
   - respects all TT rules

Anything else MUST be removed.

This rework is about:
- intelligence over volume
- correctness over optimism
- long-term engine strength

Implement exactly this system.

ADDENDUM – TT usage / eviction policy (MUST FOLLOW)
==================================================

IMPORTANT
---------
Do NOT use wall-clock time (time.Now()) in the Transposition Table.
This is forbidden.

Rationale:
- Too slow in hot paths
- Increases memory footprint per entry
- Unnecessary nondeterminism
- No added value over logical “age”

------------------------------------------------------------
USAGE / AGE TRACKING (REQUIRED APPROACH)
------------------------------------------------------------

Use a LOGICAL GENERATION COUNTER.

Global state:
- Maintain a global generation counter (int / uint32).
- Increment it:
  - once per root search, OR
  - once per move, OR
  - once per fixed number of nodes
  (exact policy up to Codex, but MUST be consistent)

Per TT entry, store ONE (or both) of the following:
- genWritten     : generation when the entry was stored
- genLastUsed    : generation when the entry was last hit (optional)

These are SMALL integers, not timestamps.

------------------------------------------------------------
PROBE RULES
------------------------------------------------------------

On TT probe HIT:
- If genLastUsed exists → update it to current generation
- Do NOT modify depth / score / flags

------------------------------------------------------------
STORE / REPLACEMENT RULES (STRICT PRIORITY)
------------------------------------------------------------

When inserting into an occupied slot, replace ONLY if one of the following is true
(checked in this order):

1) New entry has GREATER search depth
2) Depth equal AND new entry flag is EXACT while old is not
3) Depth and flag equal AND old entry is VERY OLD
   (based on genWritten / genLastUsed)

OPTIONAL:
- If storing hit-count:
  - use it ONLY as a last tiebreaker
  - counter must be small (uint8, saturating)

------------------------------------------------------------
WHAT NOT TO DO
------------------------------------------------------------

FORBIDDEN:
- Storing time.Time
- Storing Unix timestamps
- Implementing full LRU with lists/pointers
- Letting usage override depth or EXACT correctness

------------------------------------------------------------
WHY THIS WORKS
------------------------------------------------------------

- Generation-based aging gives you “oldest eviction” behavior
- Costs only a few bytes per entry
- Deterministic and fast
- Industry-standard approach in game engines

This rule applies to:
- Search TT
- Translated / symmetry TT (if enabled)

Eval cache may also use generation-based aging if implemented.

DO NOT DEVIATE.