# AI Speed Improvements

This update focuses on reducing branching factor and improving alpha-beta cutoffs while keeping must-block correctness.

## What changed
1. Dynamic top-K per depth:
`AiEnableDynamicTopK` gates depth-aware candidate limits.
`AiMaxCandidatesRoot`, `AiMaxCandidatesMid`, `AiMaxCandidatesDeep` control the limits.
Root uses a larger K; deeper nodes use smaller K.

2. Must-block safety and win-in-1 handling:
Immediate win and must-block checks no longer depend on pruned candidate lists.
When a winning move exists, the search only considers winning moves.
When must-block is true, the search only considers blocking moves (no top-K truncation).
`AiUseScanWinIn1` enables fast scan-based win-in-1 detection.

3. Stronger move ordering:
Killer moves and history heuristic are supported via:
`AiEnableKillerMoves`, `AiEnableHistoryMoves`, `AiKillerBoost`, `AiHistoryBoost`.

4. Eval cache:
`AiEnableEvalCache` and `AiEvalCacheSize` cache `EvaluateBoard` results by hash.
This skips repeated full-board scans.

## Notes
- `AiTopCandidates` is still used when dynamic top-K is disabled.
- Search stats logging now includes eval cache hits and average root/deep branching.
