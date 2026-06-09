# Gomoku AI — How the Engine Thinks

This document traces every step the AI takes from "it's my turn" to "I place my stone here."  
The goal is to make the logic visible so the code can be refactored around it.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Files at a Glance](#2-files-at-a-glance)
3. [Turn Entry Point](#3-turn-entry-point)
4. [Step 1 — Board Representation & Incremental Eval State](#4-step-1--board-representation--incremental-eval-state)
5. [Step 2 — Threat Pattern Recognition](#5-step-2--threat-pattern-recognition)
6. [Step 3 — Building the Root Move Pool](#6-step-3--building-the-root-move-pool)
7. [Step 4 — Iterative Deepening Loop](#7-step-4--iterative-deepening-loop)
8. [Step 5 — The Minimax Search (Alpha-Beta)](#8-step-5--the-minimax-search-alpha-beta)
9. [Step 6 — Move Ordering Inside the Tree](#9-step-6--move-ordering-inside-the-tree)
10. [Step 7 — Transposition Table (TT)](#10-step-7--transposition-table-tt)
11. [Step 8 — Leaf Evaluation](#11-step-8--leaf-evaluation)
12. [Step 9 — Selecting the Final Move](#12-step-9--selecting-the-final-move)
13. [Supporting Subsystems](#13-supporting-subsystems)
14. [Configuration Reference](#14-configuration-reference)

---

## 1. Overview

The AI is a **pure alpha-beta minimax engine** with iterative deepening, threat-driven move generation, and several pruning / ordering techniques stacked on top:

| Technique | Purpose |
|---|---|
| Alpha-beta pruning | Discard branches that cannot change the result |
| Principal Variation Search (PVS) | Narrow null-window re-search for all non-first moves |
| Late Move Reduction (LMR) | Reduce search depth for moves unlikely to be good |
| Aspiration windows | Start each depth with a narrow score window |
| Transposition table (TT) | Re-use results from previously searched positions |
| Eval cache | Cache the expensive heuristic evaluation at leaves |
| Threat tiers | Skip irrelevant moves when forcing sequences exist |
| Threat LUT | Pre-computed 7-cell threat pattern lookup |
| Iterative deepening | Search depth 1, 2, 3… and always have a best-move ready |
| Lazy SMP | Run parallel search threads sharing the same TT |
| Tactical quiescence | Extend search when the position is tactically unstable |
| Pondering | Think during opponent's turn |
| "Lost mode" | When losing, pick the move that maximises opponent complexity |

There is **no opening book** and **no neural network**. Everything is computed from scratch each turn.

---

## 2. Files at a Glance

| File | Role | Lines |
|---|---|---|
| `ai_scoring.go` | Main search engine: minimax, candidate gen, move ordering, caches | ~8 600 |
| `ai_player.go` | Public API: `ChooseMove`, async `StartThinking`, pondering | ~1 500 |
| `ai_eval.go` | Pattern definitions, `EvaluateBoard`, geometry construction | ~900 |
| `eval_state.go` | Incremental board eval (`ApplyMove` / `UndoMove`) | ~1 250 |
| `search_backlog.go` | Queue of interrupted searches (continue later) | ~900 |
| `tt.go` | Transposition table: set-associative, generation-based | ~577 |
| `zobrist.go` | Zobrist hashing + 8-fold symmetry canonical hash | ~193 |
| `heuristic_hash.go` | FNV-64 hash of all heuristic weights (TT invalidation) | small |
| `threat_lut_eval.go` | Build the pre-computed 7-cell threat LUT | ~287 |
| `threat_lut_integration.go` | Apply LUT during incremental eval | ~286 |
| `config.go` | All tunable knobs + `DefaultConfig()` with exact values | ~261 |
| `rules.go` | Win detection, capture logic, double-three legality | ~382 |
| `game.go` | Game loop, `Tick`, player dispatch | ~731 |
| `bot_hash.go` | Per-bot hash tracking (deduplication across sessions) | small |

---

## 3. Turn Entry Point

When the game loop calls `Game.Tick` and it is the AI's turn, one of two paths runs:

```
Game.Tick()
  ├─ player.HasMoveReady()? → TakeMove()          (pondered result ready)
  └─ AIPlayer.ChooseMove(state, rules, config)     (synchronous search)
        └─ ScoreBoard(state, rules, settings)
```

`AIPlayer.ChooseMove` is also the underlying implementation for the async path (`StartThinking` / `TakeMove`); it just runs in a goroutine when async.

---

## 4. Step 1 — Board Representation & Incremental Eval State

### Board

The board is a flat `[]Cell` of size `N×N`. Each cell is `CellEmpty`, `CellBlue`, or `CellRed`.

### EvalState

Rather than re-evaluating the whole board on every move, the engine maintains an `EvalState` struct that stores the current heuristic score and threat counts. When a stone is placed:

```
EvalState.ApplyMove(board, MoveDelta) → EvalUndo
```

This updates only the lines that pass through the changed cell (rows, columns, 2 diagonals). It returns an `EvalUndo` that lets the engine restore the exact previous state after the move is undone:

```
EvalState.UndoMove(board, EvalUndo)
```

This make-then-undo pattern is the innermost loop of the search and must be very fast.

---

## 5. Step 2 — Threat Pattern Recognition

### Lines and Windows

The board is decomposed into all **lines** (rows, columns, diagonals) of length ≥ 5. Each line is scanned with a **7-cell sliding window** (`evalWindowSize = 7`).

### Cell Encoding

Each cell in a window is encoded as a 3-state value:
- `M` — the player we are evaluating ("mine")
- `O` — the opponent ("opponent")
- `.` — empty

A 7-cell window produces a base-3 number (0–2186), which is used as an index into a pre-computed 2187-entry LUT (`patternLUT`).

### Pattern Definitions

Threats are best understood with two numbers: **moves to win** (how many turns until the threat wins if ignored) and **blocking squares** (how many different cells can neutralise it in one move).

These are the exact patterns recognised (defined in `ai_eval.go`):

| Pattern | Meaning | Moves to win | Blocking squares | Tier |
|---|---|---|---|---|
| `MMMMM` | 5 in a row — game over | 0 | 0 | Winning |
| `.MMMM.` | Open-4 — both ends free | 1 | 0 — unblockable | Critical |
| `OMMMM.` / `.MMMMO` | Closed-4 — one end blocked | 1 | 1 | MustAnswer |
| `.MMM.M.` / `.M.MMM.` | Broken-4 — gap in a four | 1 | 1 | MustAnswer |
| `.MMM.` | Open-3 — both ends free | 2 | 2 | MustAnswer ⚠️ |
| `.MM.M.` / `.M.MM.` | Broken-3 — gap in a three | 2–3 | 1–2 | Strong |
| `.MM.` | Open-2 | 3+ | 2+ | Pressure |
| `OMM.` / `.MMO` | Closed-2 | 3+ | 1+ | Pressure |
| `.M.M.` | Broken-2 | 3+ | 2+ | Pressure |

**The comparison rule:** if your threat has N moves to win and the opponent's has M, you can ignore theirs only if N < M (your threat resolves first). At equal N, whoever has fewer blocking squares forces the win.

**Why Open-3 belongs in MustAnswer:**  
Open-3 and Closed-4 both lose in the same number of moves — 1 extra turn. The defender has 2 choices instead of 1, but both still resolve the same way if ignored: Open-3 → Open-4 (unblockable) → loss. The only reason to leave an Open-3 unanswered is if you have a counter-threat with fewer moves to win.

### Threat Tiers

Tiers control **how the search restricts its candidate set**:

```
TierWinning    — Win5:              terminal, stop searching
TierCritical   — Open-4:            unblockable (0 blocking squares);
                                    search restricts to counter-wins only
TierMustAnswer — Closed-4, Broken-4, Open-3:
                                    wins in 1 extra move (1–2 blocking squares);
                                    search restricts to blocks + counter-threats
TierStrong     — Broken-3:          wins in 2–3 moves; defensive moves added
                                    to pool but search stays open
TierPressure   — Open-2, Closed-2:  long-term pressure, no immediate restriction
```

> ⚠️ **Code note:** the current code places Open-3 in `TierStrong`, not `TierMustAnswer`.  
> This means the search does not restrict to defensive candidates when the opponent has an Open-3.  
> This is a known gap to revisit in the refactor — the correct tier is `TierMustAnswer`.

### Threat LUT

The `threat_lut_eval.go` builds a secondary LUT specifically for fast capture-threat and forcing-sequence detection. It maps 7-cell windows to a bitmask of threat properties, allowing `O(1)` classification of whether a window is a threat, what kind, and where the extension/defense squares are.

---

## 6. Step 3 — Building the Root Move Pool

`buildRootMovePool` constructs the list of candidate moves the root will actually search. This is the most selective step — it determines which moves ever get considered.

### Sources of Root Candidates

**1. Forced / tactical moves** — from `AnalyzeThreats()`:
- Immediate wins (own alignment or capture)
- Must-block moves (opponent has Open-4 or better)
- Capture threats

**2. Locality moves** — cells within ~2 stones of any existing stone, ranked by how many lines they participate in (`collectCandidateMovesWithEval`).

**3. Previous iteration's best moves** — the PV line from the last completed depth (re-inserted at the front).

### Root Move Metadata

Each `RootMove` carries:

```go
type RootMove struct {
    Move              Move
    ShallowScore      float64   // 1-ply evaluation
    TacticalPriority  int       // win / block-win / capture / quiet
    ThreatFlags       uint32    // which threat types this move touches
    ThreatSeverity    int       // urgency score 0–100
    CaptureValue      int       // stones immediately captured
    ChildForcingScore int       // threats created for the opponent
    LastSearchScore   float64   // score from the previous depth iteration
}
```

### Root Bands

Root moves are divided into bands before searching:

| Band | Contents |
|---|---|
| Forced | Immediate wins, must-block moves |
| Principal | Top-K moves by score (K configured by `AiKQuietRoot` / `AiKTactRoot`) |
| Speculative | Secondary moves that might be reconsidered |
| Verification | Shallow-score candidates revisited after deeper search |

Forced moves are always fully searched. Principal moves get the full depth. Speculative and verification bands may be searched at reduced depth or dropped entirely as time runs out.

---

## 7. Step 4 — Iterative Deepening Loop

`ScoreBoard` runs searches from depth 1 up to `AiMaxDepth`, one depth at a time:

```
for depth = AiMinDepth; depth <= AiMaxDepth; depth++ {
    searchRootPoolAtDepth(depth)
    if timeout() { break }
}
return scores from last completed depth
```

Benefits:
- At any point there is a valid best move (from the last completed depth).
- TT entries from shallow depths improve move ordering at deeper depths.
- The engine can stop at any time without losing the previous result (`AiReturnLastComplete = true`).

### Aspiration Windows

At depth ≥ `AiAspMinDepth` (default: 5), each depth starts with a narrow window around the previous depth's score:

```
lo = prevScore - AiAspWindow   (default ±1200 centipawns)
hi = prevScore + AiAspWindow

result = minimax(lo, hi)
if result <= lo: re-search with (−∞, lo)
if result >= hi: re-search with (hi, +∞)
```

If the score falls outside the window (a "fail"), the re-search only widens on the failing side — this is a one-sided re-search that saves time compared to full-window re-search.

---

## 8. Step 5 — The Minimax Search (Alpha-Beta)

`minimax(depth, alpha, beta, player)` is the recursive core. Here is what happens at each node:

### 5.1 — Check the Transposition Table

```
entry = TT.Probe(zobristHash)
if entry.depth >= depth:
    if entry.flag == Exact:  return entry.score
    if entry.flag == Lower:  alpha = max(alpha, entry.score)
    if entry.flag == Upper:  beta  = min(beta,  entry.score)
    if alpha >= beta:        return entry.score  // TT cutoff
```

The TT can return an exact score (skipping the entire subtree) or tighten the alpha-beta window.

### 5.2 — Terminal Check

```
if state.status == Win(Blue):  return +winScore
if state.status == Win(Red):   return -winScore
if depth == 0:                  return leafEval()
```

### 5.3 — Analyze Threats

`AnalyzeThreats(state)` scans the current position for:
- Immediate wins for either player
- Open-4, Closed-4, Broken-4 threats
- Capture threats (windows where a stone can be captured)
- Fork threats (two simultaneous threats)

The result is a `ThreatContext` that determines which candidates are forced.

### 5.4 — Candidate Selection

`chooseNodeCandidatesFromThreatContext` picks which moves to try:

- **Hard-restricted** (only forced): if the opponent has an immediate win or Open-4, only consider blocking moves.
- **Threat-expanded**: if there are MustAnswer threats, add their defense squares.
- **Quiet moves**: locality candidates, capped by the dynamic-K configuration:

| Position depth | Quiet cap | Tactical cap |
|---|---|---|
| Root | `AiKQuietRoot` = 16 | `AiKTactRoot` = 24 |
| Mid (3–6) | `AiKQuietMid` = 12 | `AiKTactMid` = 18 |
| Deep (7+) | `AiKQuietDeep` = 6 | `AiKTactDeep` = 14 |

Hard caps per ply: `AiMaxCandidatesPly7 = 8`, `Ply8 = 7`, `Ply9 = 6`.

### 5.5 — Principal Variation Search (PVS)

For the **first** candidate (the "expected best move"), a full-window search is run:

```
score = -minimax(depth-1, -beta, -alpha, other)
```

For **all subsequent** candidates, a null-window search is run first:

```
score = -minimax(depth-1, -alpha-1, -alpha, other)   // null window
if score > alpha && score < beta:
    score = -minimax(depth-1, -beta, -alpha, other)  // full re-search
```

This assumes the first move is best. If a later move beats it, a full re-search is triggered. In practice this halves the number of expensive full-window searches.

### 5.6 — Late Move Reduction (LMR)

Moves beyond the `lmrLateMoveStart`-th candidate (default: 4th) at depth ≥ `lmrMinDepth` (default: 4) are first searched at reduced depth:

```
if moveIndex >= lmrLateMoveStart && depth >= lmrMinDepth && !forced:
    score = -minimax(depth - 1 - lmrReduction, ...)   // reduced
    if score > alpha:
        score = -minimax(depth - 1, ...)               // full re-search
```

Reduction is 1 ply (`lmrReduction = 1`). This avoids spending time on moves that are unlikely to be good, while still recovering via re-search if they turn out to be.

### 5.7 — Tactical Quiescence

When `depth == 0` and the position is "unstable" (any MustAnswer or higher tier threat exists), the search is extended via `tacticalQuiescence`:

```
if depth == 0 && isTacticallyUnstable(threatContext):
    extend search along forcing moves up to AiTacticalQuiescenceDepth deeper
```

This prevents the engine from stopping at a position where the opponent has an unresolved Open-4.

### 5.8 — Store in TT

After the search, the result is stored:

```
TT.Store(zobristHash, depth, score, flag, bestMove, generation)
```

Flag is `Exact` if no pruning happened, `Lower` if alpha was raised (fail-high), `Upper` if beta was never exceeded (fail-low).

---

## 9. Step 6 — Move Ordering Inside the Tree

Good move ordering is critical: if the best move is tried first, alpha-beta prunes almost everything else. The engine uses several layers:

### Priority 0 — Forced moves (always first)

Immediate wins and must-block moves from `ThreatContext`.

### Priority 1 — TT best move

The `BestMove` field from the TT entry for this position (the PV move from a previous iteration or depth).

### Priority 2 — Killer moves

Two killer moves per depth: moves that caused a beta cutoff at this depth in a different branch of the current search. Configured by `AiKillerBoost = 6000` (added to the ordering score).

### Priority 3 — History heuristic

Every move that causes a beta cutoff gets `AiHistoryBoost = 16` added to a global `history[from][to]` table. This accumulates across the search and biases ordering toward moves that tend to be good regardless of position.

### Priority 4 — Shallow evaluation

For moves at depth-from-root ≤ 2, a full 1-ply evaluation is computed for the top-N candidates and used to rerank them.

---

## 10. Step 7 — Transposition Table (TT)

### Structure

The TT is a **set-associative hash table**:
- Default size: `1 << 19` = 524 288 slots
- Default bucket count: 4 (each slot has 4 entries; replacement is depth-priority within the bucket)
- Max memory: 5 GB (configurable)

Each `TTEntry` stores:

```go
type TTEntry struct {
    Key           uint64    // Zobrist hash
    HeuristicHash uint64    // Hash of current weight config (invalidates on config change)
    Depth         int
    Score         int32
    Flag          TTFlag    // Exact / Lower / Upper
    BestMove      Move
    ProvenExact   bool
    GenWritten    uint32    // Logical generation at write time
    GenLastUsed   uint32    // Logical generation at last read
}
```

### Zobrist Hashing

Every position has a 64-bit hash built from:
- A random value per (cell, color) pair
- A side-to-move toggle bit
- Capture-count contributions (via splitmix64)

The hash is maintained **incrementally**: placing a stone XORs in its value, removing a stone XORs it out again. This makes `ApplyMove` / `UndoMove` O(1) for hash updates.

### Symmetry-aware Root Cache

The root-level transpose cache (`RootTransposeCache`) keys positions by their **shape** (normalized bounding box of stones). This allows reusing a search result when the same relative arrangement of stones appears at a different board position. Conditions for a hit: same relative stone positions, same captures, same side-to-move.

### Generation-based Replacement

Each search increments a logical `generation` counter. When a TT slot is full and a new entry must be written, the oldest entry (lowest `GenLastUsed`) is replaced, with a preference for shallower entries over deeper ones.

### Heuristic Hash

A separate FNV-64 hash of all heuristic weights (`heuristic_hash.go`) is stored in each TT entry. If the weights change mid-session (e.g. via the `/api/heuristics` endpoint), existing TT entries become invalid and are ignored on probe.

---

## 11. Step 8 — Leaf Evaluation

When `depth == 0` (and quiescence does not extend), `evaluateStateHeuristic` computes the position value.

### Score Convention

The score is always **Blue-positive / Red-negative**. A score of `+winScore` means Blue wins; `-winScore` means Red wins.

### Structural Score

Computed by `EvalState.ScoreOnly()` using the incrementally maintained threat counts:

```
structuralScore = Σ(weight[threatType] × count[Blue][threatType])
                - Σ(weight[threatType] × count[Red][threatType])
```

### Heuristic Weights (exact values from `DefaultConfig`)

| Threat | Weight |
|---|---|
| Open-4 | 131 633.82 |
| Fork (four+) | 130 181.77 |
| Fork (Open-3) | 42 035.41 |
| Capture near win | 35 000.00 |
| Open-3 | 19 124.54 |
| Capture now (1 pair) | 18 000.00 |
| Closed-4 | 23 451.26 |
| Capture double threat | 15 000.00 |
| Hanging pair | 14 000.00 |
| Broken-4 | 16 588.89 |
| Broken-3 | 11 377.93 |
| Capture in two | 4 000.00 |
| Open-2 | 400.71 |
| Closed-3 | 802.11 |
| Broken-2 | 215.28 |
| Closed-2 | -600.00 |

Note: `Closed-2` is **negative** — a blocked two-stone sequence is a liability, not an asset.

### Bonus / Penalty Adjustments

On top of the structural score, several adjustments are made:

- **Fork bonus** — when a single move creates two simultaneous threats of tier ≥ Strong, a large bonus is added (`ForkOpen3` or `ForkFourPlus`).
- **Capture urgency** — when Blue's capture count is close to the win threshold, `CaptureNearWin` bonus is added; when it is 2 captures away, `CaptureInTwo` applies.
- **Double-capture threat** — when a move would capture and also set up a second capture fork, `CaptureDoubleThreat` is added.

### Eval Cache

A fast hash cache (`EvalCache`) stores `evalKey(stateHash, boardSize, player) → float64`. It has 64 hash-sharded stripes to avoid lock contention. Entries are only stored when `abs(score) >= AiEvalCacheMinAbs` (default 300.0), filtering out uninformative near-zero positions.

---

## 12. Step 9 — Selecting the Final Move

After the iterative deepening loop completes (or times out), `selectBestMove` picks the final move:

1. **Best score from last complete depth** — the candidate with the highest `LastSearchScore`.
2. **Fallback if search was incomplete** — `FirstLegalCandidate` from the threat analysis (forced win/block).
3. **Lost mode** — if the best score is worse than `-AiLostModeThreshold` (default: half of winScore), the engine switches to "lost mode":
   - Instead of accepting certain defeat, it scans the opponent's reply moves.
   - It picks the move where the opponent has the most sub-optimal choices (maximises the "fragility gap" in opponent replies).
   - This tries to create complications even in a losing position.

---

## 13. Supporting Subsystems

### Pondering

When `AiPonderingEnabled = true`, a background goroutine (`startPonderWorker`) keeps searching the current position even while waiting for the opponent's move. It fills the TT continuously. When the AI's turn arrives:
- If the position matches the pondered position exactly, the result is reused immediately.
- Otherwise the TT benefit is still used by the next search.

### Lazy SMP

`AiLazySMPWorkers = 4` goroutines run `minimax` in parallel, each with its own killer/history tables but sharing the same TT. This is the standard lazy SMP approach: no communication between workers beyond the TT.

### Ghost Mode

When `GhostMode = true`, every time the search makes a move deep in the tree, it can emit an `OnGhostUpdate(GameState)` callback. The server broadcasts these intermediate positions to WebSocket clients for real-time search visualization. Updates are throttled by `AiGhostThrottleMs = 50ms`.

### Search Backlog

When a search is interrupted by timeout mid-depth, the interrupted root board is pushed into `SearchBacklog`. A background worker (`ai_queue_workers`) continues that search and fills the TT, so the next turn benefits from the partial work.

### Cache Persistence

The TT can be persisted to disk (`AiEnableTtPersistence = true`, file: `tt_cache.gob`). On startup the TT is restored, providing instant deep lookup for common opening positions without re-searching.

---

## 14. Configuration Reference

All settings live in `config.go` and can be changed via the `/api/config` endpoint at runtime.

### Time

| Key | Default | Meaning |
|---|---|---|
| `AiTimeBudgetMs` | 500 | Hard wall-clock budget for the entire turn |
| `AiTimeoutMs` | 400 | Soft timeout per depth iteration |
| `AiMinDepth` | 6 | Minimum depth always completed before timeout |
| `AiMaxDepth` | 10 | Maximum depth the iterative loop reaches |
| `AiDepthStep` | 1 | Depth increment per iteration |
| `AiReturnLastComplete` | true | Return scores from last fully completed depth |

### Branching

| Key | Default | Meaning |
|---|---|---|
| `AiMaxCandidates` | 24 | Hard cap on candidates at any node |
| `AiKQuietRoot/Mid/Deep` | 16 / 12 / 6 | Quiet-position K per tree zone |
| `AiKTactRoot/Mid/Deep` | 24 / 18 / 14 | Tactical-position K per tree zone |
| `AiMaxCandidatesPly7/8/9` | 8 / 7 / 6 | Hard per-ply caps for very deep nodes |

### Search Algorithms

| Key | Default | Meaning |
|---|---|---|
| `AiEnablePVS` | true | Principal Variation Search |
| `AiEnableKillerMoves` | true | Killer move heuristic |
| `AiEnableHistoryMoves` | true | History heuristic |
| `AiEnableAspiration` | true | Aspiration windows |
| `AiAspWindow` | 1200.0 | Initial aspiration window half-width |
| `AiAspMinDepth` | 5 | Minimum depth for aspiration |
| `AiEnableTacticalQuiescence` | true | Tactical quiescence extension |
| `AiTacticalQuiescenceDepth` | 6 | Max quiescence extension plies |
| `AiLazySMPWorkers` | 4 | Parallel search threads |

### Transposition Table

| Key | Default | Meaning |
|---|---|---|
| `AiTtSize` | 524 288 (1<<19) | Number of TT slots |
| `AiTtBuckets` | 4 | Set-associative bucket count |
| `AiTtUseSetAssoc` | true | Enable set-associative mode |
| `AiTtMaxMemoryBytes` | 5 GB | Hard memory cap |
| `AiEnableTtPersistence` | true | Persist TT to disk |
| `AiEnableRootTranspose` | true | Root-level symmetry cache |

### Eval Cache

| Key | Default | Meaning |
|---|---|---|
| `AiEnableEvalCache` | true | Cache leaf evaluations |
| `AiEvalCacheSize` | 524 288 (1<<19) | Cache size |
| `AiEvalCacheMinAbs` | 300.0 | Minimum abs(score) to store |

### Lost Mode

| Key | Default | Meaning |
|---|---|---|
| `AiEnableLostMode` | true | Activate complexity-maximisation when losing |
| `AiLostModeThreshold` | winScore / 2 | Score threshold to trigger lost mode |
| `AiLostModeMaxMoves` | 6 | Max moves to evaluate in lost mode |

### Heuristic Weights

All pattern weights are under `Heuristics` in config (see exact values in [Step 8](#11-step-8--leaf-evaluation) above). They can be overridden per-player via `POST /api/start` with `settings.blue_heuristics` / `settings.red_heuristics`.
