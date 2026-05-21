package threatlut

const (
	WindowSize    = 7
	WindowPadding = WindowSize / 2
	MaxWindowKey  = 2187 // 3^7
)

type PatternType uint8

const (
	PatternNone PatternType = iota
	PatternBroken2
	PatternClosed2
	PatternOpen2
	PatternClosed3
	PatternBroken3
	PatternOpen3
	PatternClosed4
	PatternBroken4
	PatternOpen4
	PatternWin5
)

const (
	FlagCreatesImmediateThreat uint16 = 1 << iota
	FlagBlocksImmediateThreat
	FlagCreatesFork
	FlagKillsPattern
	FlagExtendsPattern
	FlagDefensiveCritical
	FlagOffensiveCritical
)

type EvolutionResult struct {
	NewType    PatternType
	DeltaScore int16
	Flags      uint16
}

type ResponseFlags uint16

const (
	ResponseWinning ResponseFlags = 1 << iota
	ResponseMustPlay
	ResponseMustBlock
	ResponseCounterThreat
	ResponseCreateFork
	ResponsePreventFork
	ResponseCaptureRace
)

type TacticalResponse struct {
	Flags      ResponseFlags
	Severity   int16
	Tempo      uint8
	WinTempo   uint8
	ForceTempo uint8
}

const NoTempo uint8 = 0xFF

func BestTempo(winTempo, forceTempo uint8) uint8 {
	best := NoTempo
	if winTempo != NoTempo {
		best = winTempo
	}
	if forceTempo != NoTempo && (best == NoTempo || forceTempo < best) {
		best = forceTempo
	}
	return best
}

type MoveTransition struct {
	RelPos       int8
	ForSelf      EvolutionResult
	ForOpp       EvolutionResult
	SelfResponse TacticalResponse
	OppResponse  TacticalResponse
}

type ThreatLUTEntry struct {
	Key             uint32
	BaseType        PatternType
	StoneMask       uint16
	EmptyMask       uint16
	PlayableMask    uint16
	TransitionStart uint32
	TransitionCount uint16
}

var ThreatLUTEntries [MaxWindowKey]ThreatLUTEntry
var ThreatLUTTransitions []MoveTransition

func EncodeCanonicalWindow(window []byte) uint32 {
	var code uint32
	for _, cell := range window {
		code *= 3
		switch cell {
		case '.':
		case 'M':
			code += 1
		default:
			code += 2
		}
	}
	return code
}

func DecodeCanonicalWindow(key uint32) [WindowSize]byte {
	var window [WindowSize]byte
	for i := WindowSize - 1; i >= 0; i-- {
		switch key % 3 {
		case 0:
			window[i] = '.'
		case 1:
			window[i] = 'M'
		default:
			window[i] = 'O'
		}
		key /= 3
	}
	return window
}

func LookupThreatWindow(key uint32) (ThreatLUTEntry, bool) {
	if key >= uint32(len(ThreatLUTEntries)) {
		return ThreatLUTEntry{}, false
	}
	entry := ThreatLUTEntries[key]
	if entry.BaseType == PatternNone && entry.TransitionCount == 0 {
		return entry, false
	}
	return entry, true
}

func TransitionsForEntry(entry ThreatLUTEntry) []MoveTransition {
	start := int(entry.TransitionStart)
	end := start + int(entry.TransitionCount)
	if start < 0 || end < start || end > len(ThreatLUTTransitions) {
		return nil
	}
	return ThreatLUTTransitions[start:end]
}

func BaseScore(typ PatternType) int16 {
	switch typ {
	case PatternWin5:
		return 30000
	case PatternOpen4:
		return 12000
	case PatternBroken4:
		return 9000
	case PatternClosed4:
		return 8500
	case PatternOpen3:
		return 4000
	case PatternBroken3:
		return 2800
	case PatternClosed3:
		return 2200
	case PatternOpen2:
		return 700
	case PatternClosed2:
		return 500
	case PatternBroken2:
		return 350
	default:
		return 0
	}
}
