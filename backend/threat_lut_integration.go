package main

import (
	"sort"

	"gomoku-backend/internal/ai/threatlut"
)

type MoveThreatImpact struct {
	Pos            Move
	OffensiveScore int32
	DefensiveScore int32
	TotalScore     int32
	TouchCount     int16
	Flags          uint32
}

func collectThreatLUTImpacts(state GameState, currentPlayer PlayerColor, boardSize int, evalState *EvalState) []MoveThreatImpact {
	if evalState == nil {
		return nil
	}
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	var selfScore []int32
	var oppScore []int32
	var selfFlags []uint32
	var oppFlags []uint32
	var selfTouches []uint16
	var oppTouches []uint16
	if currentPlayer == PlayerBlue {
		selfScore = evalState.blueThreatSelfScore
		oppScore = evalState.redThreatOppScore
		selfFlags = evalState.blueThreatSelfFlags
		oppFlags = evalState.redThreatOppFlags
		selfTouches = evalState.blueThreatTouches
		oppTouches = evalState.redThreatTouches
	} else {
		selfScore = evalState.redThreatSelfScore
		oppScore = evalState.blueThreatOppScore
		selfFlags = evalState.redThreatSelfFlags
		oppFlags = evalState.blueThreatOppFlags
		selfTouches = evalState.redThreatTouches
		oppTouches = evalState.blueThreatTouches
	}
	out := make([]MoveThreatImpact, 0, 32)
	for idx := 0; idx < len(selfScore) && idx < len(state.Board.cells); idx++ {
		if state.Board.cells[idx] != CellEmpty {
			continue
		}
		total := selfScore[idx] + oppScore[idx]
		if total <= 0 {
			continue
		}
		move := moveFromCellIndex(boardSize, idx)
		out = append(out, MoveThreatImpact{
			Pos:            move,
			OffensiveScore: selfScore[idx],
			DefensiveScore: oppScore[idx],
			TotalScore:     total,
			TouchCount:     int16(selfTouches[idx] + oppTouches[idx]),
			Flags:          selfFlags[idx] | (oppFlags[idx] << 16),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalScore != out[j].TotalScore {
			return out[i].TotalScore > out[j].TotalScore
		}
		if out[i].DefensiveScore != out[j].DefensiveScore {
			return out[i].DefensiveScore > out[j].DefensiveScore
		}
		if out[i].OffensiveScore != out[j].OffensiveScore {
			return out[i].OffensiveScore > out[j].OffensiveScore
		}
		if out[i].TouchCount != out[j].TouchCount {
			return out[i].TouchCount > out[j].TouchCount
		}
		if out[i].Pos.Y != out[j].Pos.Y {
			return out[i].Pos.Y < out[j].Pos.Y
		}
		return out[i].Pos.X < out[j].Pos.X
	})
	return out
}

func topAlignmentLocalityMoveSet(state GameState, currentPlayer PlayerColor, boardSize int, evalState *EvalState, config Config) map[int]struct{} {
	if evalState == nil {
		return nil
	}
	alignLimit := config.AiLocalityTopAlignments
	if alignLimit <= 0 {
		alignLimit = 2
	}
	selected := make(map[int]struct{}, 16)
	addThreatMoves := func(owner PlayerColor, threats []Threat) {
		top := selectTopQuietThreats(owner, currentPlayer, threats, alignLimit)
		for _, threat := range top {
			for _, move := range rootLocalityMovesForThreat(state, boardSize, owner, currentPlayer, threat) {
				selected[move.Y*boardSize+move.X] = struct{}{}
			}
		}
	}
	addThreatMoves(currentPlayer, buildAlignmentThreatObjects(state.Board, evalState.lineSummaries, currentPlayer))
	addThreatMoves(otherPlayer(currentPlayer), buildAlignmentThreatObjects(state.Board, evalState.lineSummaries, otherPlayer(currentPlayer)))
	return selected
}

func offensiveTransitionScore(result threatlut.EvolutionResult) int32 {
	score := int32(result.DeltaScore)
	if score < 0 {
		score = 0
	}
	if result.Flags&threatlut.FlagCreatesImmediateThreat != 0 {
		score += 3000
	}
	if result.Flags&threatlut.FlagCreatesFork != 0 {
		score += 1200
	}
	if result.Flags&threatlut.FlagOffensiveCritical != 0 {
		score += 800
	}
	return score
}

func defensiveTransitionScore(baseType threatlut.PatternType, result threatlut.EvolutionResult) int32 {
	score := int32(0)
	if result.DeltaScore < 0 {
		score += int32(-result.DeltaScore)
	}
	baseScore := int32(threatlut.BaseScore(baseType))
	newScore := int32(threatlut.BaseScore(result.NewType))
	if baseScore > newScore {
		score += (baseScore - newScore) / 3
	}
	if result.Flags&threatlut.FlagBlocksImmediateThreat != 0 {
		score += 3500
	}
	if result.Flags&threatlut.FlagKillsPattern != 0 {
		score += 1500
	}
	if result.Flags&threatlut.FlagDefensiveCritical != 0 {
		score += 900
	}
	return score
}

func priorityForThreatImpact(impact MoveThreatImpact) int {
	switch {
	case impact.DefensiveScore >= 7000:
		return prioBlockFour
	case impact.OffensiveScore >= 7000:
		return prioCreateFour
	case impact.DefensiveScore >= 2500:
		return prioBlockOpen3
	case impact.OffensiveScore >= 2500:
		return prioCreateOpen3
	case impact.DefensiveScore > impact.OffensiveScore:
		return prioQuietOpp2
	case impact.OffensiveScore > 0:
		return prioQuietOwn2
	default:
		return prioDefault
	}
}

func buildThreatLUTCandidates(state GameState, currentPlayer PlayerColor, boardSize int, evalState *EvalState, config Config) []candidateMove {
	impacts := collectThreatLUTImpacts(state, currentPlayer, boardSize, evalState)
	if len(impacts) == 0 {
		return nil
	}
	// Restrict to cells within 3 Chebyshev steps of any existing stone.
	// The bbox approach is too coarse when stones are spread out: a position
	// in the bbox corner can still be 4+ cells from the nearest stone.
	const lutProximityRadius = 3
	proxMask := buildStoneProximityMask(state.Board, boardSize, lutProximityRadius)
	filtered := impacts[:0]
	for _, imp := range impacts {
		idx := imp.Pos.Y*boardSize + imp.Pos.X
		if idx >= 0 && idx < len(proxMask) && proxMask[idx] {
			filtered = append(filtered, imp)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	impacts = filtered
	primaryMoves := topAlignmentLocalityMoveSet(state, currentPlayer, boardSize, evalState, config)
	type scoredCandidate struct {
		impact     MoveThreatImpact
		priority   int
		quietScore int
		primary    bool
		alignTotal int
		alignOwn   int
		alignOpp   int
		multiAlign bool
		multiTouch bool
		defensive  bool
	}
	scored := make([]scoredCandidate, 0, len(impacts))
	bestTotal := int32(0)
	for _, impact := range impacts {
		if impact.TotalScore > bestTotal {
			bestTotal = impact.TotalScore
		}
	}
	for _, impact := range impacts {
		blueCount, redCount, totalAlign := evalState.AlignmentUseCounts(impact.Pos)
		ownAlign := redCount
		oppAlign := blueCount
		if currentPlayer == PlayerBlue {
			ownAlign = blueCount
			oppAlign = redCount
		}
		idx := impact.Pos.Y*boardSize + impact.Pos.X
		_, primary := primaryMoves[idx]
		multiAlign := totalAlign >= 2
		multiTouch := impact.TouchCount >= 3
		defensive := impact.DefensiveScore > 0
		strong := impact.TotalScore*10 >= bestTotal*6
		if !primary && !multiAlign && !multiTouch && !defensive && !strong {
			continue
		}
		scored = append(scored, scoredCandidate{
			impact:     impact,
			priority:   priorityForThreatImpact(impact),
			quietScore: int(impact.TotalScore),
			primary:    primary,
			alignTotal: totalAlign,
			alignOwn:   ownAlign,
			alignOpp:   oppAlign,
			multiAlign: multiAlign,
			multiTouch: multiTouch,
			defensive:  defensive,
		})
	}
	if len(scored) == 0 {
		return nil
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].primary != scored[j].primary {
			return scored[i].primary
		}
		if scored[i].multiAlign != scored[j].multiAlign {
			return scored[i].multiAlign
		}
		if scored[i].defensive != scored[j].defensive {
			return scored[i].defensive
		}
		if scored[i].alignTotal != scored[j].alignTotal {
			return scored[i].alignTotal > scored[j].alignTotal
		}
		if scored[i].multiTouch != scored[j].multiTouch {
			return scored[i].multiTouch
		}
		if scored[i].impact.TotalScore != scored[j].impact.TotalScore {
			return scored[i].impact.TotalScore > scored[j].impact.TotalScore
		}
		if scored[i].impact.DefensiveScore != scored[j].impact.DefensiveScore {
			return scored[i].impact.DefensiveScore > scored[j].impact.DefensiveScore
		}
		if scored[i].impact.OffensiveScore != scored[j].impact.OffensiveScore {
			return scored[i].impact.OffensiveScore > scored[j].impact.OffensiveScore
		}
		if scored[i].impact.Pos.Y != scored[j].impact.Pos.Y {
			return scored[i].impact.Pos.Y < scored[j].impact.Pos.Y
		}
		return scored[i].impact.Pos.X < scored[j].impact.Pos.X
	})
	limit := config.AiLocalityTopAlignments * 4
	if limit < 8 {
		limit = 8
	}
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]candidateMove, 0, len(scored))
	for _, entry := range scored {
		out = append(out, candidateMove{
			move:       entry.impact.Pos,
			priority:   entry.priority,
			quietScore: entry.quietScore,
		})
	}
	return out
}
