package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"

	"gomoku-backend/internal/ai/threatlut"
)

type patternSpec struct {
	typ     threatlut.PatternType
	pattern string
}

var specs = []patternSpec{
	{typ: threatlut.PatternWin5, pattern: "MMMMM"},
	{typ: threatlut.PatternOpen4, pattern: ".MMMM."},
	{typ: threatlut.PatternBroken4, pattern: ".MMM.M."},
	{typ: threatlut.PatternBroken4, pattern: ".MM.MM."},
	{typ: threatlut.PatternBroken4, pattern: ".M.MMM."},
	{typ: threatlut.PatternClosed4, pattern: "OMMMM."},
	{typ: threatlut.PatternClosed4, pattern: ".MMMMO"},
	{typ: threatlut.PatternOpen3, pattern: ".MMM."},
	{typ: threatlut.PatternBroken3, pattern: "MM.M"},
	{typ: threatlut.PatternBroken3, pattern: "M.MM"},
	{typ: threatlut.PatternBroken3, pattern: "M..MM"},
	{typ: threatlut.PatternBroken3, pattern: "MM..M"},
	{typ: threatlut.PatternClosed3, pattern: "OMMM."},
	{typ: threatlut.PatternClosed3, pattern: ".MMMO"},
	{typ: threatlut.PatternOpen2, pattern: ".MM."},
	{typ: threatlut.PatternClosed2, pattern: "OMM."},
	{typ: threatlut.PatternClosed2, pattern: ".MMO"},
	{typ: threatlut.PatternBroken2, pattern: ".M.M."},
}

func main() {
	entries := [threatlut.MaxWindowKey]threatlut.ThreatLUTEntry{}
	transitions := make([]threatlut.MoveTransition, 0, threatlut.MaxWindowKey*3)

	for key := 0; key < threatlut.MaxWindowKey; key++ {
		windowArr := threatlut.DecodeCanonicalWindow(uint32(key))
		window := windowArr[:]
		baseType, stoneMask, emptyMask := classifyWindow(window)
		start := len(transitions)
		playableMask := uint16(0)

		for relPos, cell := range window {
			if cell != '.' {
				continue
			}
			selfWindow := windowArr
			selfWindow[relPos] = 'M'
			oppWindow := windowArr
			oppWindow[relPos] = 'O'

			selfType, _, _ := classifyWindow(selfWindow[:])
			oppType, _, _ := classifyWindow(oppWindow[:])
			selfResult := buildEvolution(baseType, selfType, true, selfWindow[:])
			oppResult := buildEvolution(baseType, oppType, false, oppWindow[:])

			if !isRelevantTransition(baseType, selfResult, oppResult) {
				continue
			}
			playableMask |= 1 << relPos
			transitions = append(transitions, threatlut.MoveTransition{
				RelPos:       int8(relPos),
				ForSelf:      selfResult,
				ForOpp:       oppResult,
				SelfResponse: buildSelfResponse(window, baseType, selfResult),
				OppResponse:  buildOppResponse(baseType, selfResult, oppResult),
			})
		}

		entries[key] = threatlut.ThreatLUTEntry{
			Key:             uint32(key),
			BaseType:        baseType,
			StoneMask:       stoneMask,
			EmptyMask:       emptyMask,
			PlayableMask:    playableMask,
			TransitionStart: uint32(start),
			TransitionCount: uint16(len(transitions) - start),
		}
	}

	if err := writeGenerated(entries, transitions); err != nil {
		fmt.Fprintf(os.Stderr, "generate threat lut: %v\n", err)
		os.Exit(1)
	}
}

func classifyWindow(window []byte) (threatlut.PatternType, uint16, uint16) {
	best := threatlut.PatternNone
	bestScore := int16(0)
	stoneMask := uint16(0)
	emptyMask := uint16(0)
	for i, cell := range window {
		if cell == 'M' {
			stoneMask |= 1 << i
		}
		if cell == '.' {
			emptyMask |= 1 << i
		}
	}
	for _, spec := range specs {
		for start := 0; start+len(spec.pattern) <= len(window); start++ {
			if !match(window[start:start+len(spec.pattern)], spec.pattern) {
				continue
			}
			score := threatlut.BaseScore(spec.typ)
			if score > bestScore {
				best = spec.typ
				bestScore = score
			}
		}
	}
	return best, stoneMask, emptyMask
}

func match(window []byte, pattern string) bool {
	if len(window) != len(pattern) {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		if window[i] != pattern[i] {
			return false
		}
	}
	return true
}

func buildEvolution(baseType, newType threatlut.PatternType, forSelf bool, window []byte) threatlut.EvolutionResult {
	baseScore := int(threatlut.BaseScore(baseType))
	newScore := int(threatlut.BaseScore(newType))
	delta := newScore - baseScore
	flags := uint16(0)
	if newType >= threatlut.PatternClosed4 {
		flags |= threatlut.FlagCreatesImmediateThreat
	}
	if baseType >= threatlut.PatternClosed4 && newType < threatlut.PatternClosed4 {
		flags |= threatlut.FlagBlocksImmediateThreat
	}
	if baseType != threatlut.PatternNone && newType == threatlut.PatternNone {
		flags |= threatlut.FlagKillsPattern
	}
	if newScore > baseScore {
		flags |= threatlut.FlagExtendsPattern
	}
	if forSelf && (newType >= threatlut.PatternOpen4 || forkPotential(window, newType)) {
		flags |= threatlut.FlagOffensiveCritical
	}
	if !forSelf && baseType >= threatlut.PatternOpen3 && newType < baseType {
		flags |= threatlut.FlagDefensiveCritical
	}
	if forkPotential(window, newType) {
		flags |= threatlut.FlagCreatesFork
	}
	return threatlut.EvolutionResult{
		NewType:    newType,
		DeltaScore: int16(delta),
		Flags:      flags,
	}
}

func forkPotential(window []byte, target threatlut.PatternType) bool {
	if target < threatlut.PatternOpen3 {
		return false
	}
	count := 0
	for i, cell := range window {
		if cell != '.' {
			continue
		}
		next := append([]byte(nil), window...)
		next[i] = 'M'
		typ, _, _ := classifyWindow(next)
		if typ >= target {
			count++
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

func isRelevantTransition(baseType threatlut.PatternType, self, opp threatlut.EvolutionResult) bool {
	if baseType != threatlut.PatternNone {
		return true
	}
	return self.NewType != threatlut.PatternNone ||
		opp.NewType != threatlut.PatternNone ||
		self.Flags != 0 ||
		opp.Flags != 0
}

func buildSelfResponse(window []byte, baseType threatlut.PatternType, result threatlut.EvolutionResult) threatlut.TacticalResponse {
	if result.NewType == threatlut.PatternNone {
		return threatlut.TacticalResponse{}
	}
	flags := threatlut.ResponseFlags(0)
	switch staticTier(result.NewType) {
	case tierWinning:
		flags |= threatlut.ResponseWinning
	case tierCritical:
		flags |= threatlut.ResponseMustPlay
	case tierMustAnswer, tierStrong:
		flags |= threatlut.ResponseCounterThreat
	}
	if result.NewType >= threatlut.PatternOpen4 &&
		flags&(threatlut.ResponseWinning|threatlut.ResponseMustPlay) != 0 &&
		result.NewType >= threatlut.PatternOpen3 &&
		result.Flags&threatlut.FlagCreatesFork == 0 {
		flags |= threatlut.ResponseCounterThreat
	}
	if countStones(window) == 2 && result.NewType >= threatlut.PatternOpen3 {
		flags |= threatlut.ResponseCaptureRace
	}
	if result.Flags&threatlut.FlagCreatesFork != 0 {
		flags |= threatlut.ResponseCreateFork
	}
	if flags == 0 {
		return threatlut.TacticalResponse{}
	}
	return threatlut.TacticalResponse{
		Flags:      flags,
		Severity:   int16(threatSeverityForType(result.NewType)),
		Tempo:      bestTempoForPattern(result.NewType),
		WinTempo:   winTempoForPattern(result.NewType),
		ForceTempo: forceTempoForPattern(result.NewType),
	}
}

func countStones(window []byte) int {
	count := 0
	for _, cell := range window {
		if cell == 'M' {
			count++
		}
	}
	return count
}

func buildOppResponse(baseType threatlut.PatternType, selfResult, oppResult threatlut.EvolutionResult) threatlut.TacticalResponse {
	if baseType == threatlut.PatternNone && selfResult.NewType < threatlut.PatternClosed4 {
		return threatlut.TacticalResponse{}
	}
	degrades := oppResult.NewType < baseType ||
		oppResult.Flags&(threatlut.FlagBlocksImmediateThreat|threatlut.FlagKillsPattern|threatlut.FlagDefensiveCritical) != 0
	if !degrades {
		if selfResult.NewType < threatlut.PatternClosed4 {
			return threatlut.TacticalResponse{}
		}
	}
	flags := threatlut.ResponseFlags(0)
	severityType := baseType
	if selfResult.NewType >= threatlut.PatternClosed4 {
		flags |= threatlut.ResponseMustBlock
		if selfResult.NewType > severityType {
			severityType = selfResult.NewType
		}
	}
	switch staticTier(baseType) {
	case tierWinning, tierCritical, tierMustAnswer:
		flags |= threatlut.ResponseMustBlock
	case tierStrong:
		flags |= threatlut.ResponsePreventFork
	}
	if oppResult.Flags&threatlut.FlagDefensiveCritical != 0 || oppResult.Flags&threatlut.FlagBlocksImmediateThreat != 0 {
		flags |= threatlut.ResponsePreventFork
	}
	if flags == 0 {
		return threatlut.TacticalResponse{}
	}
	return threatlut.TacticalResponse{
		Flags:      flags,
		Severity:   int16(threatSeverityForType(severityType)),
		Tempo:      bestTempoForPattern(severityType),
		WinTempo:   winTempoForPattern(severityType),
		ForceTempo: forceTempoForPattern(severityType),
	}
}

func bestTempoForPattern(pattern threatlut.PatternType) uint8 {
	return threatlut.BestTempo(winTempoForPattern(pattern), forceTempoForPattern(pattern))
}

func winTempoForPattern(pattern threatlut.PatternType) uint8 {
	switch pattern {
	case threatlut.PatternWin5:
		return 0
	case threatlut.PatternOpen4:
		return 1
	default:
		return threatlut.NoTempo
	}
}

func forceTempoForPattern(pattern threatlut.PatternType) uint8 {
	switch pattern {
	case threatlut.PatternWin5:
		return 0
	case threatlut.PatternOpen4, threatlut.PatternClosed4, threatlut.PatternBroken4:
		return 1
	case threatlut.PatternOpen3:
		return 2
	case threatlut.PatternBroken3, threatlut.PatternClosed3:
		return 3
	case threatlut.PatternOpen2:
		return 4
	case threatlut.PatternClosed2:
		return 5
	case threatlut.PatternBroken2:
		return 6
	default:
		return threatlut.NoTempo
	}
}

type localTier uint8

const (
	tierNone localTier = iota
	tierPressure
	tierStrong
	tierMustAnswer
	tierCritical
	tierWinning
)

func staticTier(typ threatlut.PatternType) localTier {
	switch typ {
	case threatlut.PatternWin5:
		return tierWinning
	case threatlut.PatternOpen4:
		return tierCritical
	case threatlut.PatternClosed4, threatlut.PatternBroken4, threatlut.PatternOpen3:
		return tierMustAnswer
	case threatlut.PatternBroken3:
		return tierStrong
	case threatlut.PatternOpen2, threatlut.PatternClosed2:
		return tierPressure
	default:
		return tierNone
	}
}

func threatSeverityForType(pattern threatlut.PatternType) int {
	switch pattern {
	case threatlut.PatternWin5:
		return 100
	case threatlut.PatternOpen4:
		return 90
	case threatlut.PatternClosed4, threatlut.PatternBroken4:
		return 70
	case threatlut.PatternOpen3:
		return 50
	case threatlut.PatternBroken3, threatlut.PatternClosed3:
		return 35
	case threatlut.PatternOpen2:
		return 10
	case threatlut.PatternClosed2:
		return 4
	case threatlut.PatternBroken2:
		return 6
	default:
		return 0
	}
}

func writeGenerated(entries [threatlut.MaxWindowKey]threatlut.ThreatLUTEntry, transitions []threatlut.MoveTransition) error {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by gen_threat_lut; DO NOT EDIT.\n")
	buf.WriteString("package threatlut\n\n")
	buf.WriteString("func init() {\n")
	buf.WriteString("\tThreatLUTEntries = [MaxWindowKey]ThreatLUTEntry{\n")
	for _, entry := range entries {
		if entry.BaseType == threatlut.PatternNone && entry.TransitionCount == 0 {
			continue
		}
		fmt.Fprintf(&buf, "\t\t%d: {Key:%d, BaseType:%s, StoneMask:%d, EmptyMask:%d, PlayableMask:%d, TransitionStart:%d, TransitionCount:%d},\n",
			entry.Key, entry.Key, patternName(entry.BaseType), entry.StoneMask, entry.EmptyMask, entry.PlayableMask, entry.TransitionStart, entry.TransitionCount)
	}
	buf.WriteString("\t}\n")
	buf.WriteString("\tThreatLUTTransitions = []MoveTransition{\n")
	for _, tr := range transitions {
		fmt.Fprintf(&buf, "\t\t{RelPos:%d, ForSelf:%s, ForOpp:%s, SelfResponse:%s, OppResponse:%s},\n",
			tr.RelPos, evoString(tr.ForSelf), evoString(tr.ForOpp), responseString(tr.SelfResponse), responseString(tr.OppResponse))
	}
	buf.WriteString("\t}\n")
	buf.WriteString("}\n")

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format generated file: %w", err)
	}
	outPath := filepath.Join("internal", "ai", "threatlut", "generated_threat_lut.go")
	return os.WriteFile(outPath, formatted, 0o644)
}

func patternName(typ threatlut.PatternType) string {
	switch typ {
	case threatlut.PatternBroken2:
		return "PatternBroken2"
	case threatlut.PatternOpen2:
		return "PatternOpen2"
	case threatlut.PatternClosed2:
		return "PatternClosed2"
	case threatlut.PatternClosed3:
		return "PatternClosed3"
	case threatlut.PatternBroken3:
		return "PatternBroken3"
	case threatlut.PatternOpen3:
		return "PatternOpen3"
	case threatlut.PatternClosed4:
		return "PatternClosed4"
	case threatlut.PatternBroken4:
		return "PatternBroken4"
	case threatlut.PatternOpen4:
		return "PatternOpen4"
	case threatlut.PatternWin5:
		return "PatternWin5"
	default:
		return "PatternNone"
	}
}

func evoString(e threatlut.EvolutionResult) string {
	return fmt.Sprintf("EvolutionResult{NewType:%s, DeltaScore:%d, Flags:%s}", patternName(e.NewType), e.DeltaScore, flagString(e.Flags))
}

func flagString(flags uint16) string {
	if flags == 0 {
		return "0"
	}
	parts := make([]string, 0, 4)
	appendIf := func(flag uint16, name string) {
		if flags&flag != 0 {
			parts = append(parts, name)
		}
	}
	appendIf(threatlut.FlagCreatesImmediateThreat, "FlagCreatesImmediateThreat")
	appendIf(threatlut.FlagBlocksImmediateThreat, "FlagBlocksImmediateThreat")
	appendIf(threatlut.FlagCreatesFork, "FlagCreatesFork")
	appendIf(threatlut.FlagKillsPattern, "FlagKillsPattern")
	appendIf(threatlut.FlagExtendsPattern, "FlagExtendsPattern")
	appendIf(threatlut.FlagDefensiveCritical, "FlagDefensiveCritical")
	appendIf(threatlut.FlagOffensiveCritical, "FlagOffensiveCritical")
	return strings.Join(parts, "|")
}

func responseString(r threatlut.TacticalResponse) string {
	return fmt.Sprintf(
		"TacticalResponse{Flags:%s, Severity:%d, Tempo:%d, WinTempo:%d, ForceTempo:%d}",
		responseFlagString(r.Flags),
		r.Severity,
		r.Tempo,
		r.WinTempo,
		r.ForceTempo,
	)
}

func responseFlagString(flags threatlut.ResponseFlags) string {
	if flags == 0 {
		return "0"
	}
	parts := make([]string, 0, 4)
	appendIf := func(flag threatlut.ResponseFlags, name string) {
		if flags&flag != 0 {
			parts = append(parts, name)
		}
	}
	appendIf(threatlut.ResponseWinning, "ResponseWinning")
	appendIf(threatlut.ResponseMustPlay, "ResponseMustPlay")
	appendIf(threatlut.ResponseMustBlock, "ResponseMustBlock")
	appendIf(threatlut.ResponseCounterThreat, "ResponseCounterThreat")
	appendIf(threatlut.ResponseCreateFork, "ResponseCreateFork")
	appendIf(threatlut.ResponsePreventFork, "ResponsePreventFork")
	appendIf(threatlut.ResponseCaptureRace, "ResponseCaptureRace")
	return strings.Join(parts, "|")
}
