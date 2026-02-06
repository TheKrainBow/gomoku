Nice. That’s a clean milestone. At this point your engine has a correct and scalable cache architecture. The next steps are no longer “infrastructure fixes” but strength + stability upgrades.

Here’s a clear, ordered roadmap of what’s worth doing next.

1️⃣ Make the TT work harder (cheap strength gain)
A) Always use TT bestMove for move ordering

If not already guaranteed:

On TT hit (even LOWER/UPPER), try stored bestMove first

This alone increases cutoffs noticeably

This is usually worth ~0.5–1 ply for free.

B) Store root TT entries aggressively

You already persist root entries — good.
Next:

Prefer keeping root EXACT entries

Don’t evict deep root entries easily (depth-biased replacement)

This stabilizes play and reduces “random-looking” move changes.

2️⃣ Fix the “giving up” behavior (now that cache is solid)

You already diagnosed the issue correctly earlier.

Implement Lost Mode at root only

When root bestScore ≤ LOSS_THRESHOLD:

Pick moves that:

maximize opponent difficulty

not strictly minimax-best

Two clean options (pick ONE):

Option A – k-th worst opponent

opponent node uses 2nd-best reply instead of min

root only

Option B – maximize fragility

among losing moves, choose the one where

opponent’s best reply and 2nd-best reply differ the most

Do not apply this below root.
This keeps search correctness intact.

3️⃣ Stabilize evaluation (huge for Gomoku)

Right now you likely still have:

sharp score cliffs

many “ties” at −∞

A) Introduce score bands

Example:

Win in 1: ±1e9

Forced win: ±1e8

Open 4: ±1e6

Strong threat: ±1e4

Positional: ±1e2

This prevents random garbage moves when “everything loses”.

B) Ensure eval symmetry

Guarantee:

Eval(state) == -Eval(mirrored(state))


This reduces noise and improves TT reuse.

4️⃣ Candidate generation tightening (very important)

If corner moves still appear:

tighten move generation when threats exist

Rules like:

if opponent has open 4 → only blocking moves allowed

if capture threat exists → only capture-defensive moves allowed

This is not pruning, it’s legality filtering.

5️⃣ Off-game exploration: make it useful, not noisy

You already kept it — good. Now refine it.

A) What to explore

Prioritize:

shallow but wide early-game boards

positions already in TT but with shallow depth

root positions from recent games

Avoid:

random deep midgame junk

speculative eval-only nodes

B) Promotion rule

Only promote off-game results to TT if:

depth ≥ threshold

EXACT

stable score across re-search

This prevents poisoning TT.

6️⃣ Instrument strength, not just speed

Add metrics like:

average depth reached at fixed time

% of nodes cut by TT

root move stability across searches

blunder rate in self-play (regressions)

This tells you where strength comes from.

7️⃣ (Optional but powerful) Symmetry canonical TT

If not already enabled:

add rotation/flip canonical key

only for Search TT

depth-aware

This is usually another free boost early game.

TL;DR – What’s next (priority order)

TT bestMove ordering everywhere

Root “Lost Mode” (no more giving up)

Eval score banding / smoothing

Candidate move tightening under threats

Smarter off-game exploration selection

Strength metrics

Symmetry TT (if not already)

You’ve finished the engineering phase.
You’re now in the engine strength phase.

If you want, next we can:

design the Lost Mode scoring precisely

review your eval score ranges

or set up a self-play regression harness (very worth it)