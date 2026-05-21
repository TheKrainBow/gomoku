package main

import "gomoku-backend/internal/ai/threatlut"

type evalThreatImpactUse struct {
	idx       uint16
	selfScore int16
	oppScore  int16
	selfFlags uint16
	oppFlags  uint16
	touches   uint8
}

type evalThreatResponseUse struct {
	idx             uint16
	selfWin         int16
	selfMustPlay    int16
	selfCounter     int16
	selfFork        int16
	selfCaptureRace int16
	oppMustBlock    int16
	oppPreventFork  int16
}

type evalLUTThreat struct {
	typ           PatternType
	stones        []int
	extensions    []int
	dir           threatDirection
	forkPotential bool
}

func summarizeThreatLUTForLine(tokens []byte, line []int, dir threatDirection) ([]evalThreatImpactUse, []evalThreatResponseUse, []evalLUTThreat) {
	if len(tokens) < threatlut.WindowSize || len(line) == 0 {
		return nil, nil, nil
	}
	selfScores := make([]int16, len(line))
	oppScores := make([]int16, len(line))
	selfFlags := make([]uint16, len(line))
	oppFlags := make([]uint16, len(line))
	touches := make([]uint8, len(line))
	selfWin := make([]int16, len(line))
	selfMustPlay := make([]int16, len(line))
	selfCounter := make([]int16, len(line))
	selfFork := make([]int16, len(line))
	selfCaptureRace := make([]int16, len(line))
	oppMustBlock := make([]int16, len(line))
	oppPreventFork := make([]int16, len(line))
	threats := make([]evalLUTThreat, 0, 8)
	seenThreats := make(map[string]struct{}, 8)

	code := int(threatlut.EncodeCanonicalWindow(tokens[:threatlut.WindowSize]))
	for start := 0; start+threatlut.WindowSize <= len(tokens); start++ {
		entry, ok := threatlut.LookupThreatWindow(uint32(code))
		if ok && entry.PlayableMask != 0 {
			selfMoves := make([]int, 0, 4)
			bestSelfPriority := int32(-1)
			bestOppPriority := int32(-1)
			transitions := threatlut.TransitionsForEntry(entry)
			for _, tr := range transitions {
				if pr, relevant := threatMovePriority(entry.BaseType, tr.ForSelf); relevant && pr > bestSelfPriority {
					bestSelfPriority = pr
				}
				if pr, relevant := threatDefensePriority(tr.OppResponse); relevant && pr > bestOppPriority {
					bestOppPriority = pr
				}
			}
			for _, tr := range transitions {
				abs := start + int(tr.RelPos)
				local := abs - threatlut.WindowPadding
				if local < 0 || local >= len(line) {
					continue
				}
				selfScores[local] += int16(offensiveTransitionScore(tr.ForSelf))
				oppScores[local] += int16(defensiveTransitionScore(entry.BaseType, tr.ForOpp))
				selfFlags[local] |= tr.ForSelf.Flags
				oppFlags[local] |= tr.ForOpp.Flags
				touches[local]++
				if pr, relevant := threatMovePriority(entry.BaseType, tr.ForSelf); relevant {
					if pr == bestSelfPriority {
						accumulateThreatResponse(&selfWin[local], &selfMustPlay[local], &selfCounter[local], &selfFork[local], &selfCaptureRace[local], tr.SelfResponse)
					}
					switch {
					case pr > bestSelfPriority:
						bestSelfPriority = pr
						selfMoves = selfMoves[:0]
						selfMoves = append(selfMoves, line[local])
					case pr == bestSelfPriority:
						selfMoves = appendUniqueCell(selfMoves, line[local])
					}
				}
				if pr, relevant := threatDefensePriority(tr.OppResponse); relevant && pr == bestOppPriority {
					accumulateThreatDefense(&oppMustBlock[local], &oppPreventFork[local], tr.OppResponse)
				}
			}
			if typ, include := mapLUTThreatType(entry.BaseType); include && len(selfMoves) > 0 {
				stones := collectThreatLUTStoneCells(entry, line, start)
				key := threatLUTKey(typ, dir, stones, selfMoves)
				if _, exists := seenThreats[key]; !exists {
					seenThreats[key] = struct{}{}
					threats = append(threats, evalLUTThreat{
						typ:           typ,
						stones:        stones,
						extensions:    selfMoves,
						dir:           dir,
						forkPotential: len(selfMoves) >= 2,
					})
				}
			}
		}
		if start+threatlut.WindowSize >= len(tokens) {
			continue
		}
		code = rollWindowCode(code, tokens[start], tokens[start+threatlut.WindowSize])
	}

	out := make([]evalThreatImpactUse, 0, len(line))
	resp := make([]evalThreatResponseUse, 0, len(line))
	for local, idx := range line {
		if selfScores[local] == 0 && oppScores[local] == 0 && selfFlags[local] == 0 && oppFlags[local] == 0 {
		} else {
			out = append(out, evalThreatImpactUse{
				idx:       uint16(idx),
				selfScore: selfScores[local],
				oppScore:  oppScores[local],
				selfFlags: selfFlags[local],
				oppFlags:  oppFlags[local],
				touches:   maxUint8(1, touches[local]),
			})
		}
		if selfWin[local] == 0 && selfMustPlay[local] == 0 && selfCounter[local] == 0 && selfFork[local] == 0 && selfCaptureRace[local] == 0 &&
			oppMustBlock[local] == 0 && oppPreventFork[local] == 0 {
			continue
		}
		resp = append(resp, evalThreatResponseUse{
			idx:             uint16(idx),
			selfWin:         selfWin[local],
			selfMustPlay:    selfMustPlay[local],
			selfCounter:     selfCounter[local],
			selfFork:        selfFork[local],
			selfCaptureRace: selfCaptureRace[local],
			oppMustBlock:    oppMustBlock[local],
			oppPreventFork:  oppPreventFork[local],
		})
	}
	return out, resp, threats
}

func accumulateThreatResponse(selfWin, selfMustPlay, selfCounter, selfFork, selfCaptureRace *int16, response threatlut.TacticalResponse) {
	if response.Flags&threatlut.ResponseWinning != 0 {
		*selfWin += response.Severity
	}
	if response.Flags&threatlut.ResponseMustPlay != 0 {
		*selfMustPlay += response.Severity
	}
	if response.Flags&threatlut.ResponseCounterThreat != 0 {
		*selfCounter += response.Severity
	}
	if response.Flags&threatlut.ResponseCreateFork != 0 {
		*selfFork += response.Severity
	}
	if response.Flags&threatlut.ResponseCaptureRace != 0 {
		*selfCaptureRace += response.Severity
	}
}

func accumulateThreatDefense(oppMustBlock, oppPreventFork *int16, response threatlut.TacticalResponse) {
	if response.Flags&threatlut.ResponseMustBlock != 0 {
		*oppMustBlock += response.Severity
	}
	if response.Flags&threatlut.ResponsePreventFork != 0 {
		*oppPreventFork += response.Severity
	}
}

func threatDefensePriority(response threatlut.TacticalResponse) (int32, bool) {
	if response.Flags == 0 || response.Severity <= 0 {
		return 0, false
	}
	priority := int32(response.Severity)
	tempo := int(threatlut.BestTempo(response.WinTempo, response.ForceTempo))
	if tempo == int(threatlut.NoTempo) {
		tempo = int(response.Tempo)
	}
	if tempo > 0 && tempo < 16 {
		priority += int32(16-tempo) << 14
	}
	if response.Flags&threatlut.ResponseMustBlock != 0 {
		priority += 1 << 20
	}
	if response.Flags&threatlut.ResponsePreventFork != 0 {
		priority += 1 << 19
	}
	return priority, true
}

func maxUint8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}

func appendUniqueCell(dst []int, cell int) []int {
	for _, existing := range dst {
		if existing == cell {
			return dst
		}
	}
	return append(dst, cell)
}

func mapLUTThreatType(typ threatlut.PatternType) (PatternType, bool) {
	switch typ {
	case threatlut.PatternWin5:
		return PatternWin5, true
	case threatlut.PatternOpen4:
		return PatternOpen4, true
	case threatlut.PatternClosed4:
		return PatternClosed4, true
	case threatlut.PatternBroken4:
		return PatternBroken4, true
	case threatlut.PatternOpen3:
		return PatternOpen3, true
	case threatlut.PatternBroken3:
		return PatternBroken3, true
	case threatlut.PatternClosed3:
		return PatternClosed3, true
	case threatlut.PatternOpen2:
		return PatternOpen2, true
	case threatlut.PatternClosed2:
		return PatternClosed2, true
	case threatlut.PatternBroken2:
		return PatternBroken2, true
	default:
		return PatternNone, false
	}
}

func collectThreatLUTStoneCells(entry threatlut.ThreatLUTEntry, line []int, windowStart int) []int {
	stones := make([]int, 0, 5)
	for i := 0; i < threatlut.WindowSize; i++ {
		if entry.StoneMask&(1<<i) == 0 {
			continue
		}
		abs := windowStart + i
		local := abs - threatlut.WindowPadding
		if local < 0 || local >= len(line) {
			continue
		}
		stones = appendUniqueCell(stones, line[local])
	}
	return stones
}

func threatMovePriority(baseType threatlut.PatternType, result threatlut.EvolutionResult) (int32, bool) {
	if result.NewType == threatlut.PatternNone {
		return 0, false
	}
	if result.NewType <= baseType &&
		result.Flags&(threatlut.FlagCreatesImmediateThreat|threatlut.FlagCreatesFork|threatlut.FlagOffensiveCritical) == 0 {
		return 0, false
	}
	priority := int32(threatlut.BaseScore(result.NewType))*16 + int32(result.DeltaScore)
	if result.Flags&threatlut.FlagCreatesImmediateThreat != 0 {
		priority += 1 << 20
	}
	if result.Flags&threatlut.FlagCreatesFork != 0 {
		priority += 1 << 19
	}
	if result.Flags&threatlut.FlagOffensiveCritical != 0 {
		priority += 1 << 18
	}
	return priority, true
}

func threatLUTKey(typ PatternType, dir threatDirection, stones []int, self []int) string {
	buf := make([]byte, 0, 64)
	buf = append(buf, byte(typ), byte(dir), byte(len(stones)), byte(len(self)))
	for _, v := range stones {
		buf = append(buf, byte(v>>8), byte(v))
	}
	for _, v := range self {
		buf = append(buf, byte(v>>8), byte(v))
	}
	return string(buf)
}
