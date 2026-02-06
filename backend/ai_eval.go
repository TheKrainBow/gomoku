package main

import "sync"

const evalInf = 1_000_000_000.0

type ThreatTotals struct {
	Win5    int
	Open4   int
	Closed4 int
	Broken4 int
	Open3   int
	Broken3 int
	Closed3 int
	Open2   int
	Broken2 int
}

type ThreatWeights struct {
	Open4        float64
	Closed4      float64
	Broken4      float64
	Open3        float64
	Broken3      float64
	Closed3      float64
	Open2        float64
	Broken2      float64
	ForkOpen3    float64
	ForkFourPlus float64
}

type patternMatch struct {
	pattern string
	apply   func(*ThreatTotals)
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
	{pattern: ".M.M.", apply: func(t *ThreatTotals) { t.Broken2++ }},
}

type lineCache struct {
	mu    sync.Mutex
	lines map[int][][]int
}

var cachedLines = &lineCache{lines: make(map[int][][]int)}

func getLinesForSize(size int) [][]int {
	cachedLines.mu.Lock()
	defer cachedLines.mu.Unlock()
	if lines, ok := cachedLines.lines[size]; ok {
		return lines
	}
	lines := buildLines(size)
	cachedLines.lines[size] = lines
	return lines
}

func buildLines(size int) [][]int {
	lines := [][]int{}
	if size <= 0 {
		return lines
	}
	// Rows.
	for y := 0; y < size; y++ {
		line := make([]int, 0, size)
		for x := 0; x < size; x++ {
			line = append(line, y*size+x)
		}
		lines = append(lines, line)
	}
	// Cols.
	for x := 0; x < size; x++ {
		line := make([]int, 0, size)
		for y := 0; y < size; y++ {
			line = append(line, y*size+x)
		}
		lines = append(lines, line)
	}
	// Diagonals (\)
	for x := 0; x < size; x++ {
		line := collectDiag(size, x, 0, 1, 1)
		if len(line) >= 5 {
			lines = append(lines, line)
		}
	}
	for y := 1; y < size; y++ {
		line := collectDiag(size, 0, y, 1, 1)
		if len(line) >= 5 {
			lines = append(lines, line)
		}
	}
	// Anti-diagonals (/)
	for x := 0; x < size; x++ {
		line := collectDiag(size, x, 0, -1, 1)
		if len(line) >= 5 {
			lines = append(lines, line)
		}
	}
	for y := 1; y < size; y++ {
		line := collectDiag(size, size-1, y, -1, 1)
		if len(line) >= 5 {
			lines = append(lines, line)
		}
	}
	return lines
}

func collectDiag(size, startX, startY, dx, dy int) []int {
	line := []int{}
	x := startX
	y := startY
	for x >= 0 && y >= 0 && x < size && y < size {
		line = append(line, y*size+x)
		x += dx
		y += dy
	}
	return line
}

func EvaluateBoard(board Board, sideToMove PlayerColor, config Config) float64 {
	weights := resolveThreatWeights(config)
	lines := getLinesForSize(board.Size())
	me := sideToMove
	opp := otherPlayer(sideToMove)
	var tokensBufStack [64]byte
	tokensBuf := tokensBufStack[:board.Size()+2]

	var totalsMe ThreatTotals
	var totalsOpp ThreatTotals

	for _, line := range lines {
		tokensMe := buildTokensInto(board, line, me, tokensBuf)
		accumulatePatterns(tokensMe, &totalsMe)
		tokensOpp := buildTokensInto(board, line, opp, tokensBuf)
		accumulatePatterns(tokensOpp, &totalsOpp)
	}

	if totalsMe.Win5 > 0 {
		return evalInf
	}
	if totalsOpp.Win5 > 0 {
		return -evalInf
	}
	if totalsOpp.Open4 > 0 {
		return -900000.0
	}
	if totalsMe.Open4 > 0 {
		return 900000.0
	}

	scoreMe := weightedSum(totalsMe, weights)
	scoreOpp := weightedSum(totalsOpp, weights)
	score := scoreMe - scoreOpp

	score += forkBonus(totalsMe, weights) - forkBonus(totalsOpp, weights)
	return score
}

func resolveThreatWeights(config Config) ThreatWeights {
	if config.Heuristics == (HeuristicConfig{}) {
		config.Heuristics = DefaultConfig().Heuristics
	}
	return ThreatWeights{
		Open4:        config.Heuristics.Open4,
		Closed4:      config.Heuristics.Closed4,
		Broken4:      config.Heuristics.Broken4,
		Open3:        config.Heuristics.Open3,
		Broken3:      config.Heuristics.Broken3,
		Closed3:      config.Heuristics.Closed3,
		Open2:        config.Heuristics.Open2,
		Broken2:      config.Heuristics.Broken2,
		ForkOpen3:    config.Heuristics.ForkOpen3,
		ForkFourPlus: config.Heuristics.ForkFourPlus,
	}
}

func buildTokensInto(board Board, line []int, player PlayerColor, buf []byte) []byte {
	needed := len(line) + 2
	if cap(buf) < needed {
		buf = make([]byte, needed)
	} else {
		buf = buf[:needed]
	}
	buf[0] = 'O'
	for i, idx := range line {
		cell := board.cells[idx]
		switch cell {
		case CellEmpty:
			buf[i+1] = '.'
		case CellBlack:
			if player == PlayerBlack {
				buf[i+1] = 'M'
			} else {
				buf[i+1] = 'O'
			}
		case CellWhite:
			if player == PlayerWhite {
				buf[i+1] = 'M'
			} else {
				buf[i+1] = 'O'
			}
		}
	}
	buf[needed-1] = 'O'
	return buf
}

func accumulatePatterns(tokens []byte, totals *ThreatTotals) {
	for i := 0; i < len(tokens); i++ {
		matched := false
		for _, entry := range evalPatterns {
			if matchAt(tokens, entry.pattern, i) {
				entry.apply(totals)
				i += len(entry.pattern) - 1
				matched = true
				break
			}
		}
		if matched {
			continue
		}
	}
}

func matchAt(tokens []byte, pattern string, start int) bool {
	if start+len(pattern) > len(tokens) {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		if tokens[start+i] != pattern[i] {
			return false
		}
	}
	return true
}

func weightedSum(t ThreatTotals, w ThreatWeights) float64 {
	return float64(t.Open4)*w.Open4 +
		float64(t.Closed4)*w.Closed4 +
		float64(t.Broken4)*w.Broken4 +
		float64(t.Open3)*w.Open3 +
		float64(t.Broken3)*w.Broken3 +
		float64(t.Closed3)*w.Closed3 +
		float64(t.Open2)*w.Open2 +
		float64(t.Broken2)*w.Broken2
}

func forkBonus(t ThreatTotals, w ThreatWeights) float64 {
	bonus := 0.0
	if t.Open3 >= 2 {
		bonus += w.ForkOpen3
	}
	if t.Closed4+t.Broken4 >= 2 {
		bonus += w.ForkFourPlus
	}
	return bonus
}
