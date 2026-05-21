package main

import "sync"

const evalInf = 1_000_000_000.0
const evalWindowSize = 7

type ThreatTotals struct {
	Win5    int
	Open4   int
	Closed4 int
	Broken4 int
	Open3   int
	Broken3 int
	Closed3 int
	Open2   int
	Closed2 int
	Broken2 int
}

type ThreatWeights struct {
	Open4               float64
	Closed4             float64
	Broken4             float64
	Open3               float64
	Broken3             float64
	Closed3             float64
	Open2               float64
	Closed2             float64
	Broken2             float64
	CaptureNow          float64
	CaptureDoubleThreat float64
	CaptureNearWin      float64
	CaptureInTwo        float64
	HangingPair         float64
	CaptureWinSoonScale float64
	CaptureInTwoLimit   int
	ForkOpen3           float64
	ForkFourPlus        float64
}

type patternMatch struct {
	pattern string
	apply   func(*ThreatTotals)
}

type PatternType int

const (
	PatternNone PatternType = iota
	PatternWin5
	PatternOpen4
	PatternClosed4
	PatternBroken4
	PatternOpen3
	PatternBroken3
	PatternClosed3
	PatternOpen2
	PatternClosed2
	PatternBroken2
)

type ThreatType = PatternType

const (
	ThreatNone    = PatternNone
	ThreatWin5    = PatternWin5
	ThreatOpen4   = PatternOpen4
	ThreatClosed4 = PatternClosed4
	ThreatBroken4 = PatternBroken4
	ThreatOpen3   = PatternOpen3
	ThreatBroken3 = PatternBroken3
	ThreatOpen2   = PatternOpen2
	ThreatClosed2 = PatternClosed2
)

type ThreatTier int

const (
	TierNone ThreatTier = iota
	TierPressure
	TierStrong
	TierMustAnswer
	TierCritical
	TierWinning
)

type Pos struct {
	X int
	Y int
}

type Threat struct {
	Owner               PlayerColor
	Type                ThreatType
	Tier                ThreatTier
	Direction           int
	Stones              []Pos
	PatternCells        []Pos
	ExtensionSquares    []Pos
	DefenseSquares      []Pos
	CapturableCount     int
	TotalStones         int
	CapturableRatio     float64
	Stable              bool
	BestFollowupTier    ThreatTier
	NumStrongExtensions int
	RealDefenseCount    int
	ForkPotential       bool
	UrgencyScore        float64
}

type EvalResult struct {
	Score              int32
	StructuralScore    int32
	CaptureScore       int32
	ComboScore         int32
	Summary            TacticalSummary
	ThreatCount        uint8
	Threats            [16]Threat
	CaptureThreatCount uint8
	CaptureThreats     [8]CaptureThreat
}

type PatternInfo struct {
	Type       PatternType
	StoneMask  uint8
	Extensions uint8
	BaseScore  float64
}

type seenPattern struct {
	typ    PatternType
	stones uint64
}

type evalLineSummary struct {
	blue              ThreatTotals
	red               ThreatTotals
	scoreBlue         float64
	scoreRed          float64
	blueThreats       []evalThreat
	redThreats        []evalThreat
	blueAlignmentUses []evalCellCount
	redAlignmentUses  []evalCellCount
	blueThreatLUTUses []evalThreatImpactUse
	redThreatLUTUses  []evalThreatImpactUse
	blueResponseUses  []evalThreatResponseUse
	redResponseUses   []evalThreatResponseUse
	blueLUTThreats    []evalLUTThreat
	redLUTThreats     []evalLUTThreat
}

type evalGeometry struct {
	lineDefs             []LineDef
	lines                [][]int
	cellToLines          [][]int
	lineDirs             []threatDirection
	captureWindows       []captureWindowDef
	cellToCaptureWindows [][]int
}

type threatDirection uint8

const (
	threatDirRow threatDirection = iota
	threatDirCol
	threatDirDiagDown
	threatDirDiagUp
)

type evalThreat struct {
	typ        PatternType
	stones     []int
	extensions []int
	dir        threatDirection
}

type evalCellCount struct {
	idx   uint16
	count uint8
}

type LineDef struct {
	start int
	step  int
	len   int
}

type captureWindowDef struct {
	cells [4]int
}

var evalPatterns = [...]patternMatch{
	{pattern: "MMMMM", apply: func(t *ThreatTotals) { t.Win5++ }},
	{pattern: ".MMMM.", apply: func(t *ThreatTotals) { t.Open4++ }},
	{pattern: "OMMMM.", apply: func(t *ThreatTotals) { t.Closed4++ }},
	{pattern: ".MMMMO", apply: func(t *ThreatTotals) { t.Closed4++ }},
	{pattern: ".MMM.M.", apply: func(t *ThreatTotals) { t.Broken4++ }},
	{pattern: ".M.MMM.", apply: func(t *ThreatTotals) { t.Broken4++ }},
	{pattern: ".MMM.", apply: func(t *ThreatTotals) { t.Open3++ }},
	{pattern: ".MM.M.", apply: func(t *ThreatTotals) { t.Broken3++ }},
	{pattern: ".M.MM.", apply: func(t *ThreatTotals) { t.Broken3++ }},
	{pattern: ".MM.", apply: func(t *ThreatTotals) { t.Open2++ }},
	{pattern: "OMM.", apply: func(t *ThreatTotals) { t.Closed2++ }},
	{pattern: ".MMO", apply: func(t *ThreatTotals) { t.Closed2++ }},
	{pattern: ".M.M.", apply: func(t *ThreatTotals) { t.Broken2++ }},
}

var (
	patternLUTOnce sync.Once
	patternLUT     [2187]PatternInfo
)

type lineCache struct {
	mu    sync.Mutex
	lines map[int]*evalGeometry
}

var cachedLines = &lineCache{lines: make(map[int]*evalGeometry)}

func getLinesForSize(size int) [][]int {
	return getEvalGeometry(size).lines
}

func getEvalGeometry(size int) *evalGeometry {
	cachedLines.mu.Lock()
	defer cachedLines.mu.Unlock()
	if geometry, ok := cachedLines.lines[size]; ok {
		return geometry
	}
	geometry := buildEvalGeometry(size)
	cachedLines.lines[size] = geometry
	return geometry
}

func buildEvalGeometry(size int) *evalGeometry {
	lineDefs := buildLineDefs(size)
	lines := materializeLines(lineDefs)
	cellToLines := make([][]int, size*size)
	lineDirs := make([]threatDirection, len(lines))
	captureWindows := buildCaptureWindowDefs(lineDefs)
	cellToCaptureWindows := make([][]int, size*size)
	for lineIndex, line := range lines {
		lineDirs[lineIndex] = classifyLineDirection(line, size)
		for _, cellIndex := range line {
			cellToLines[cellIndex] = append(cellToLines[cellIndex], lineIndex)
		}
	}
	for windowIndex, window := range captureWindows {
		for _, cellIndex := range window.cells {
			cellToCaptureWindows[cellIndex] = append(cellToCaptureWindows[cellIndex], windowIndex)
		}
	}
	return &evalGeometry{
		lineDefs:             lineDefs,
		lines:                lines,
		cellToLines:          cellToLines,
		lineDirs:             lineDirs,
		captureWindows:       captureWindows,
		cellToCaptureWindows: cellToCaptureWindows,
	}
}

func buildCaptureWindowDefs(lineDefs []LineDef) []captureWindowDef {
	total := 0
	for _, def := range lineDefs {
		if def.len >= 4 {
			total += def.len - 3
		}
	}
	windows := make([]captureWindowDef, 0, total)
	for _, def := range lineDefs {
		if def.len < 4 {
			continue
		}
		for offset := 0; offset+4 <= def.len; offset++ {
			start := def.start + offset*def.step
			windows = append(windows, captureWindowDef{
				cells: [4]int{
					start,
					start + def.step,
					start + 2*def.step,
					start + 3*def.step,
				},
			})
		}
	}
	return windows
}

func buildLineDefs(size int) []LineDef {
	lines := []LineDef{}
	if size <= 0 {
		return lines
	}
	for y := 0; y < size; y++ {
		lines = append(lines, LineDef{start: y * size, step: 1, len: size})
	}
	for x := 0; x < size; x++ {
		lines = append(lines, LineDef{start: x, step: size, len: size})
	}
	for x := 0; x < size; x++ {
		if l := diagLineDef(size, x, 0, 1, 1); l.len >= 5 {
			lines = append(lines, l)
		}
	}
	for y := 1; y < size; y++ {
		if l := diagLineDef(size, 0, y, 1, 1); l.len >= 5 {
			lines = append(lines, l)
		}
	}
	for x := 0; x < size; x++ {
		if l := diagLineDef(size, x, 0, -1, 1); l.len >= 5 {
			lines = append(lines, l)
		}
	}
	for y := 1; y < size; y++ {
		if l := diagLineDef(size, size-1, y, -1, 1); l.len >= 5 {
			lines = append(lines, l)
		}
	}
	return lines
}

func diagLineDef(size, startX, startY, dx, dy int) LineDef {
	length := 0
	x := startX
	y := startY
	for x >= 0 && y >= 0 && x < size && y < size {
		length++
		x += dx
		y += dy
	}
	return LineDef{start: startY*size + startX, step: dy*size + dx, len: length}
}

func materializeLines(defs []LineDef) [][]int {
	lines := make([][]int, 0, len(defs))
	for _, def := range defs {
		line := make([]int, 0, def.len)
		idx := def.start
		for i := 0; i < def.len; i++ {
			line = append(line, idx)
			idx += def.step
		}
		lines = append(lines, line)
	}
	return lines
}

func EvaluateBoard(board Board, sideToMove PlayerColor, config Config) EvalResult {
	return EvaluateBoardWithContext(board, sideToMove, 0, 0, config)
}

func EvaluateBoardWithContext(board Board, sideToMove PlayerColor, blueCaptures uint8, redCaptures uint8, config Config) EvalResult {
	state := BuildEvalStateFromBoard(board, sideToMove, blueCaptures, redCaptures, config)
	return state.Snapshot(&board)
}

func EvaluateBoardScore(board Board, sideToMove PlayerColor, config Config) float64 {
	state := BuildEvalStateFromBoard(board, sideToMove, 0, 0, config)
	return float64(state.ScoreOnly())
}

func EvalBoardAfterMove(board Board, config Config, color PlayerColor, x, y int) float64 {
	state := BuildEvalStateFromBoard(board, color, 0, 0, config)
	updated := board.Clone()
	updated.Set(x, y, CellFromPlayer(color))
	delta := state.ApplyMove(&updated, MoveDelta{
		Move:   Move{X: x, Y: y},
		Player: color,
	})
	return float64(state.Score - delta.PrevScore)
}

func resolveThreatWeights(config Config) ThreatWeights {
	config.Heuristics = resolvedHeuristicConfig(config)
	return ThreatWeights{
		Open4:               config.Heuristics.Open4,
		Closed4:             config.Heuristics.Closed4,
		Broken4:             config.Heuristics.Broken4,
		Open3:               config.Heuristics.Open3,
		Broken3:             config.Heuristics.Broken3,
		Closed3:             config.Heuristics.Closed3,
		Open2:               config.Heuristics.Open2,
		Closed2:             config.Heuristics.Closed2,
		Broken2:             config.Heuristics.Broken2,
		CaptureNow:          config.Heuristics.CaptureNow,
		CaptureDoubleThreat: config.Heuristics.CaptureDoubleThreat,
		CaptureNearWin:      config.Heuristics.CaptureNearWin,
		CaptureInTwo:        config.Heuristics.CaptureInTwo,
		HangingPair:         config.Heuristics.HangingPair,
		CaptureWinSoonScale: config.Heuristics.CaptureWinSoonScale,
		CaptureInTwoLimit:   config.Heuristics.CaptureInTwoLimit,
		ForkOpen3:           config.Heuristics.ForkOpen3,
		ForkFourPlus:        config.Heuristics.ForkFourPlus,
	}
}

func staticThreatTier(typ ThreatType) ThreatTier {
	switch typ {
	case ThreatWin5:
		return TierWinning
	case ThreatOpen4:
		return TierCritical
	case ThreatClosed4, ThreatBroken4:
		return TierMustAnswer
	case ThreatOpen3, ThreatBroken3:
		return TierStrong
	case ThreatOpen2, ThreatClosed2:
		return TierPressure
	default:
		return TierNone
	}
}

func boardPosFromIndex(boardSize, idx int) Pos {
	if boardSize <= 0 || idx < 0 {
		return Pos{}
	}
	return Pos{X: idx % boardSize, Y: idx / boardSize}
}

func evaluateLineSummary(board Board, line []int, dir threatDirection, buf []byte, weights ThreatWeights) evalLineSummary {
	summary := evalLineSummary{}
	tokensBlue := buildTokensInto(board, line, PlayerBlue, buf)
	summary.scoreBlue, summary.blueThreats = accumulatePatterns(tokensBlue, line, dir, weights, &summary.blue)
	summary.blueThreatLUTUses, summary.blueResponseUses, summary.blueLUTThreats = summarizeThreatLUTForLine(tokensBlue, line, dir)
	tokensRed := buildTokensInto(board, line, PlayerRed, buf)
	summary.scoreRed, summary.redThreats = accumulatePatterns(tokensRed, line, dir, weights, &summary.red)
	summary.redThreatLUTUses, summary.redResponseUses, summary.redLUTThreats = summarizeThreatLUTForLine(tokensRed, line, dir)
	summary.blueAlignmentUses = summarizeAlignmentUses(summary.blueThreats)
	summary.redAlignmentUses = summarizeAlignmentUses(summary.redThreats)
	return summary
}

func scoreFromThreatTotals(totalsBlue, totalsRed ThreatTotals, scoreBlue, scoreRed float64, weights ThreatWeights) float64 {
	_ = weights
	if totalsRed.Win5 > 0 {
		return evalInf
	}
	if totalsBlue.Win5 > 0 {
		return -evalInf
	}
	return scoreRed - scoreBlue
}

func buildTokensInto(board Board, line []int, player PlayerColor, buf []byte) []byte {
	needed := len(line) + 2*linePadding()
	if cap(buf) < needed {
		buf = make([]byte, needed)
	} else {
		buf = buf[:needed]
	}
	padding := linePadding()
	for i := 0; i < padding; i++ {
		buf[i] = 'O'
	}
	for i, idx := range line {
		cell := board.cells[idx]
		switch cell {
		case CellEmpty:
			buf[i+padding] = '.'
		case CellBlue:
			if player == PlayerBlue {
				buf[i+padding] = 'M'
			} else {
				buf[i+padding] = 'O'
			}
		case CellRed:
			if player == PlayerRed {
				buf[i+padding] = 'M'
			} else {
				buf[i+padding] = 'O'
			}
		}
	}
	for i := needed - padding; i < needed; i++ {
		buf[i] = 'O'
	}
	return buf
}

func accumulatePatterns(tokens []byte, line []int, dir threatDirection, weights ThreatWeights, totals *ThreatTotals) (float64, []evalThreat) {
	initPatternLUT()
	if len(tokens) < evalWindowSize {
		return 0.0, nil
	}
	seen := make([]seenPattern, 0, len(tokens))
	score := 0.0
	threats := make([]evalThreat, 0, 8)
	code := encodeWindow(tokens[:evalWindowSize])
	for start := 0; start+evalWindowSize <= len(tokens); start++ {
		info := patternLUT[code]
		if info.Type != PatternNone && info.StoneMask != 0 {
			stones := projectedStoneMask(start, info.StoneMask)
			if stones != 0 && !hasSeenPattern(seen, info.Type, stones) {
				seen = append(seen, seenPattern{typ: info.Type, stones: stones})
				applyPatternInfo(totals, info)
				score += adjustedPatternScore(info, weights)
				if threat, ok := buildThreat(info, line, start, dir); ok {
					threats = append(threats, threat)
				}
			}
		}
		if start+evalWindowSize >= len(tokens) {
			continue
		}
		code = rollWindowCode(code, tokens[start], tokens[start+evalWindowSize])
	}
	return score, threats
}

func linePadding() int {
	return evalWindowSize / 2
}

func initPatternLUT() {
	patternLUTOnce.Do(func() {
		for code := 0; code < len(patternLUT); code++ {
			patternLUT[code] = classifyEncodedWindow(code)
		}
	})
}

func classifyEncodedWindow(code int) PatternInfo {
	window := decodeWindow(code)
	for _, spec := range []struct {
		typ     PatternType
		pattern string
	}{
		{PatternWin5, "MMMMM"},
		{PatternOpen4, ".MMMM."},
		{PatternClosed4, "OMMMM."},
		{PatternClosed4, ".MMMMO"},
		{PatternBroken4, ".MMM.M."},
		{PatternBroken4, ".MM.MM."},
		{PatternBroken4, ".M.MMM."},
		{PatternOpen3, ".MMM."},
		{PatternBroken3, ".MM.M."},
		{PatternBroken3, ".M.MM."},
		{PatternClosed3, "OMMM."},
		{PatternClosed3, ".MMMO"},
		{PatternOpen2, ".MM."},
		{PatternClosed2, "OMM."},
		{PatternClosed2, ".MMO"},
		{PatternBroken2, ".M.M."},
	} {
		for start := 0; start+len(spec.pattern) <= len(window); start++ {
			if matchPatternBytes(window[start:start+len(spec.pattern)], spec.pattern) {
				baseScore := patternBaseScore(spec.typ, resolveThreatWeights(DefaultConfig()))
				return PatternInfo{
					Type:       spec.typ,
					StoneMask:  windowStoneMask(spec.pattern, start),
					Extensions: windowExtensionMask(spec.pattern, start),
					BaseScore:  baseScore,
				}
			}
		}
	}
	return PatternInfo{}
}

func decodeWindow(code int) []byte {
	window := make([]byte, evalWindowSize)
	for i := evalWindowSize - 1; i >= 0; i-- {
		switch code % 3 {
		case 0:
			window[i] = '.'
		case 1:
			window[i] = 'M'
		default:
			window[i] = 'O'
		}
		code /= 3
	}
	return window
}

func encodeWindow(window []byte) int {
	code := 0
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

func matchPatternBytes(window []byte, pattern string) bool {
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

func windowStoneMask(pattern string, offset int) uint8 {
	var mask uint8
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == 'M' {
			mask |= 1 << (offset + i)
		}
	}
	return mask
}

func windowExtensionMask(pattern string, offset int) uint8 {
	var mask uint8
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '.' {
			mask |= 1 << (offset + i)
		}
	}
	return mask
}

func projectedStoneMask(windowStart int, stoneMask uint8) uint64 {
	var projected uint64
	for i := 0; i < evalWindowSize; i++ {
		if stoneMask&(1<<i) == 0 {
			continue
		}
		abs := windowStart + i
		if abs < linePadding() {
			continue
		}
		projected |= 1 << (abs - linePadding())
	}
	return projected
}

func hasSeenPattern(seen []seenPattern, typ PatternType, stones uint64) bool {
	for _, entry := range seen {
		if entry.typ == typ && entry.stones == stones {
			return true
		}
	}
	return false
}

func applyPatternInfo(totals *ThreatTotals, info PatternInfo) {
	switch info.Type {
	case PatternWin5:
		totals.Win5++
	case PatternOpen4:
		totals.Open4++
	case PatternClosed4:
		totals.Closed4++
	case PatternBroken4:
		totals.Broken4++
	case PatternOpen3:
		totals.Open3++
	case PatternBroken3:
		totals.Broken3++
	case PatternClosed3:
		totals.Closed3++
	case PatternOpen2:
		totals.Open2++
	case PatternClosed2:
		totals.Closed2++
	case PatternBroken2:
		totals.Broken2++
	}
}

func adjustedPatternScore(info PatternInfo, weights ThreatWeights) float64 {
	base := info.BaseScore
	if base == 0 {
		base = patternBaseScore(info.Type, weights)
	}
	return base
}

func patternBaseScore(patternType PatternType, weights ThreatWeights) float64 {
	switch patternType {
	case PatternWin5:
		return evalInf
	case PatternOpen4:
		return weights.Open4
	case PatternClosed4:
		return weights.Closed4
	case PatternBroken4:
		return weights.Broken4
	case PatternOpen3:
		return weights.Open3
	case PatternBroken3:
		return weights.Broken3
	case PatternClosed3:
		return weights.Closed3
	case PatternOpen2:
		return weights.Open2
	case PatternClosed2:
		return weights.Closed2
	case PatternBroken2:
		return weights.Broken2
	default:
		return 0
	}
}

func buildThreat(info PatternInfo, line []int, windowStart int, dir threatDirection) (evalThreat, bool) {
	switch info.Type {
	case PatternOpen3, PatternBroken3, PatternClosed3, PatternOpen2, PatternClosed2, PatternBroken2, PatternOpen4, PatternClosed4, PatternBroken4:
	default:
		return evalThreat{}, false
	}
	extensions := collectExtensionCells(info, line, windowStart)
	if len(extensions) == 0 {
		return evalThreat{}, false
	}
	stones := collectStoneCells(info, line, windowStart)
	if len(stones) == 0 {
		return evalThreat{}, false
	}
	return evalThreat{
		typ:        info.Type,
		stones:     stones,
		extensions: extensions,
		dir:        dir,
	}, true
}

func summarizeAlignmentUses(threats []evalThreat) []evalCellCount {
	if len(threats) == 0 {
		return nil
	}
	counts := make(map[int]uint8, 16)
	for _, threat := range threats {
		switch threat.typ {
		case PatternOpen4, PatternClosed4, PatternBroken4, PatternOpen3, PatternBroken3, PatternClosed3, PatternOpen2, PatternClosed2, PatternBroken2:
		default:
			continue
		}
		for _, idx := range threat.extensions {
			counts[idx]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]evalCellCount, 0, len(counts))
	for idx, count := range counts {
		if count == 0 {
			continue
		}
		out = append(out, evalCellCount{idx: uint16(idx), count: count})
	}
	return out
}

func threatSignature(player PlayerColor, threat evalThreat) string {
	key := make([]byte, 0, 32)
	key = append(key, byte(player), byte(threat.typ), byte(threat.dir), byte(len(threat.stones)), byte(len(threat.extensions)))
	for _, stone := range threat.stones {
		key = append(key, byte(stone>>8), byte(stone))
	}
	for _, ext := range threat.extensions {
		key = append(key, byte(ext>>8), byte(ext))
	}
	return string(key)
}

func sameExtensions(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func collectStoneCells(info PatternInfo, line []int, windowStart int) []int {
	padding := linePadding()
	stones := make([]int, 0, 5)
	for i := 0; i < evalWindowSize; i++ {
		if info.StoneMask&(1<<i) == 0 {
			continue
		}
		abs := windowStart + i
		local := abs - padding
		if local < 0 || local >= len(line) {
			continue
		}
		stones = append(stones, line[local])
	}
	return stones
}

func rollWindowCode(code int, outgoing, incoming byte) int {
	return (code-encodeToken(outgoing)*pow3(evalWindowSize-1))*3 + encodeToken(incoming)
}

func encodeToken(cell byte) int {
	switch cell {
	case '.':
		return 0
	case 'M':
		return 1
	default:
		return 2
	}
}

func pow3(exp int) int {
	result := 1
	for i := 0; i < exp; i++ {
		result *= 3
	}
	return result
}

func collectExtensionCells(info PatternInfo, line []int, windowStart int) []int {
	padding := linePadding()
	ext := make([]int, 0, 2)
	seen := make(map[int]struct{}, 2)
	for i := 0; i < evalWindowSize; i++ {
		if info.Extensions&(1<<i) == 0 {
			continue
		}
		abs := windowStart + i
		local := abs - padding
		if local < 0 || local >= len(line) {
			continue
		}
		cell := line[local]
		if _, ok := seen[cell]; ok {
			continue
		}
		seen[cell] = struct{}{}
		ext = append(ext, cell)
	}
	return ext
}

func isFourThreat(t PatternType) bool {
	return t == PatternOpen4 || t == PatternClosed4 || t == PatternBroken4
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func classifyLineDirection(line []int, size int) threatDirection {
	if len(line) < 2 {
		return threatDirRow
	}
	diff := line[1] - line[0]
	switch diff {
	case 1:
		return threatDirRow
	case size:
		return threatDirCol
	case size + 1:
		return threatDirDiagDown
	case size - 1:
		return threatDirDiagUp
	default:
		return threatDirRow
	}
}

func (t *ThreatTotals) add(other ThreatTotals) {
	t.Win5 += other.Win5
	t.Open4 += other.Open4
	t.Closed4 += other.Closed4
	t.Broken4 += other.Broken4
	t.Open3 += other.Open3
	t.Broken3 += other.Broken3
	t.Closed3 += other.Closed3
	t.Open2 += other.Open2
	t.Broken2 += other.Broken2
}

func (t *ThreatTotals) sub(other ThreatTotals) {
	t.Win5 -= other.Win5
	t.Open4 -= other.Open4
	t.Closed4 -= other.Closed4
	t.Broken4 -= other.Broken4
	t.Open3 -= other.Open3
	t.Broken3 -= other.Broken3
	t.Closed3 -= other.Closed3
	t.Open2 -= other.Open2
	t.Broken2 -= other.Broken2
}
