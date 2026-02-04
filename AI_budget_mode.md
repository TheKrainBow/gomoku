# AI Budget Mode

This mode enforces a strict time budget (default `500ms`) and returns the best move from the last fully completed depth.

## Budget Enforcement
1. `AiTimeBudgetMs` sets the per-move deadline.
2. Iterative deepening stops before starting a new depth if time is up.
3. If time runs out mid-depth, the partial results are discarded and the last completed depth is returned when `AiReturnLastCompleteDepthOnly` is true.

## Dynamic K (Quiet vs Tactical)
When `AiEnableTacticalK` is enabled, candidate limits are chosen from separate tables:
- Quiet: `AiKQuietRoot`, `AiKQuietMid`, `AiKQuietDeep`
- Tactical: `AiKTactRoot`, `AiKTactMid`, `AiKTactDeep`

## Tactical Mode and Extension
- If the position is tactical, `AiEnableTacticalMode` restricts candidates to forcing moves:
  win-in-1, blocks, create-four, and (if needed) open-threes.
- `AiEnableTacticalExtension` extends depth by one ply in tactical positions.

## Aspiration Window
When `AiEnableAspiration` is enabled:
- The root search uses a window around the last completed score.
- If a move fails low or high, it is re-searched with a full window.

## Stats to Watch
Enable `AiLogSearchStats` and watch:
`t=...ms`, `nodes`, `nps`, `tt_hit`, `eval_hit`, and average root/deep branching.
