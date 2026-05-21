package main

import "math"

type CellIndex uint16

type PatternKind = PatternType

type CellSet struct {
	Count uint8
	Cells [8]CellIndex
}

func (s *CellSet) Add(c CellIndex) {
	for i := uint8(0); i < s.Count; i++ {
		if s.Cells[i] == c {
			return
		}
	}
	if s.Count < uint8(len(s.Cells)) {
		s.Cells[s.Count] = c
		s.Count++
	}
}

func (s *CellSet) Clear() {
	s.Count = 0
}

func (s CellSet) Contains(c CellIndex) bool {
	for i := uint8(0); i < s.Count; i++ {
		if s.Cells[i] == c {
			return true
		}
	}
	return false
}

type TacticalSummary struct {
	WinNowBlue           uint8
	WinNowRed            uint8
	CaptureWinNowBlue    uint8
	CaptureWinNowRed     uint8
	Open4Blue            uint8
	Open4Red             uint8
	Closed4Blue          uint8
	Closed4Red           uint8
	Broken4Blue          uint8
	Broken4Red           uint8
	Open3Blue            uint8
	Open3Red             uint8
	Broken3Blue          uint8
	Broken3Red           uint8
	DoubleThreatBlue     bool
	DoubleThreatRed      bool
	ForcingThreatsBlue   uint8
	ForcingThreatsRed    uint8
	CriticalCapturesBlue uint8
	CriticalCapturesRed  uint8
	MustAnswerForBlue    bool
	MustAnswerForRed     bool
	IsTactical           bool
}

type CaptureThreat struct {
	Owner             PlayerColor
	Move              CellIndex
	CapturedStones    CellSet
	GivesImmediateWin bool
	BreaksEnemyOpen4  bool
	BreaksEnemyThreat bool
	CreatesOwnThreat  bool
	Severity          int16
}

type LineState struct {
	ScoreBlue   int32
	ScoreRed    int32
	NetScore    int32
	CountsBlue  [PatternBroken2 + 1]uint8
	CountsRed   [PatternBroken2 + 1]uint8
	HasFiveBlue bool
	HasFiveRed  bool
}

type MoveDelta struct {
	Move               Move
	Player             PlayerColor
	CapturedCount      uint8
	CapturedCells      [8]CellIndex
	CapturePairsGained uint8
}

type changedEvalCell struct {
	Index CellIndex
	Prev  Cell
}

type changedLineUndo struct {
	Index       uint16
	PrevLine    LineState
	PrevSummary evalLineSummary
}

type captureWindowHit struct {
	Player PlayerColor
	Move   CellIndex
	Valid  bool
}

type changedCaptureWindowUndo struct {
	Index uint16
	Prev  captureWindowHit
}

type EvalUndo struct {
	PrevScore           int32
	PrevStructuralScore int32
	PrevCaptureScore    int32
	PrevComboScore      int32
	PrevSummary         TacticalSummary
	PrevBlueCaptures    uint8
	PrevRedCaptures     uint8
	PrevPatternCounts   [2][PatternBroken2 + 1]uint16
	ChangedCellCount    uint8
	ChangedCells        [9]changedEvalCell
	ChangedLineCount    uint8
	ChangedLines        [48]changedLineUndo
	ChangedCaptureCount uint8
	ChangedCaptures     [160]changedCaptureWindowUndo
}

type EvalState struct {
	Score           int32
	StructuralScore int32
	CaptureScore    int32
	ComboScore      int32
	BlueCaptures    uint8
	RedCaptures     uint8
	SideToMove      PlayerColor
	PatternCounts   [2][PatternBroken2 + 1]uint16
	Summary         TacticalSummary
	Lines           []LineState

	board               Board
	geometry            *evalGeometry
	weights             ThreatWeights
	lineSummaries       []evalLineSummary
	captureHits         []captureWindowHit
	captureRefs         [2][]uint8
	blueAlignmentUse    []uint16
	redAlignmentUse     []uint16
	blueThreatSelfScore []int32
	blueThreatOppScore  []int32
	redThreatSelfScore  []int32
	redThreatOppScore   []int32
	blueThreatSelfFlags []uint32
	blueThreatOppFlags  []uint32
	redThreatSelfFlags  []uint32
	redThreatOppFlags   []uint32
	blueThreatTouches   []uint16
	redThreatTouches    []uint16
	blueRespWin         []int32
	blueRespMustPlay    []int32
	blueRespCounter     []int32
	blueRespFork        []int32
	blueRespCaptureRace []int32
	blueRespMustBlock   []int32
	blueRespPreventFork []int32
	redRespWin          []int32
	redRespMustPlay     []int32
	redRespCounter      []int32
	redRespFork         []int32
	redRespCaptureRace  []int32
	redRespMustBlock    []int32
	redRespPreventFork  []int32
}

type ThreatDetails struct {
	ThreatCount        uint8
	Threats            [16]Threat
	CaptureThreatCount uint8
	CaptureThreats     [8]CaptureThreat
}

func (s *EvalState) BuildQuietThreats(board *Board, player PlayerColor) []Threat {
	if s == nil || board == nil {
		return nil
	}
	return buildQuietThreatObjectsFromLineSummaries(*board, s.lineSummaries, player)
}

func BuildEvalStateFromBoard(board Board, sideToMove PlayerColor, blueCaptures uint8, redCaptures uint8, cfg Config) EvalState {
	geometry := getEvalGeometry(board.Size())
	weights := resolveThreatWeights(cfg)
	state := EvalState{
		BlueCaptures:        blueCaptures,
		RedCaptures:         redCaptures,
		SideToMove:          sideToMove,
		Lines:               make([]LineState, len(geometry.lines)),
		board:               board.Clone(),
		geometry:            geometry,
		weights:             weights,
		lineSummaries:       make([]evalLineSummary, len(geometry.lines)),
		captureHits:         make([]captureWindowHit, len(geometry.captureWindows)),
		blueAlignmentUse:    make([]uint16, board.Size()*board.Size()),
		redAlignmentUse:     make([]uint16, board.Size()*board.Size()),
		blueThreatSelfScore: make([]int32, board.Size()*board.Size()),
		blueThreatOppScore:  make([]int32, board.Size()*board.Size()),
		redThreatSelfScore:  make([]int32, board.Size()*board.Size()),
		redThreatOppScore:   make([]int32, board.Size()*board.Size()),
		blueThreatSelfFlags: make([]uint32, board.Size()*board.Size()),
		blueThreatOppFlags:  make([]uint32, board.Size()*board.Size()),
		redThreatSelfFlags:  make([]uint32, board.Size()*board.Size()),
		redThreatOppFlags:   make([]uint32, board.Size()*board.Size()),
		blueThreatTouches:   make([]uint16, board.Size()*board.Size()),
		redThreatTouches:    make([]uint16, board.Size()*board.Size()),
		blueRespWin:         make([]int32, board.Size()*board.Size()),
		blueRespMustPlay:    make([]int32, board.Size()*board.Size()),
		blueRespCounter:     make([]int32, board.Size()*board.Size()),
		blueRespFork:        make([]int32, board.Size()*board.Size()),
		blueRespCaptureRace: make([]int32, board.Size()*board.Size()),
		blueRespMustBlock:   make([]int32, board.Size()*board.Size()),
		blueRespPreventFork: make([]int32, board.Size()*board.Size()),
		redRespWin:          make([]int32, board.Size()*board.Size()),
		redRespMustPlay:     make([]int32, board.Size()*board.Size()),
		redRespCounter:      make([]int32, board.Size()*board.Size()),
		redRespFork:         make([]int32, board.Size()*board.Size()),
		redRespCaptureRace:  make([]int32, board.Size()*board.Size()),
		redRespMustBlock:    make([]int32, board.Size()*board.Size()),
		redRespPreventFork:  make([]int32, board.Size()*board.Size()),
	}
	state.captureRefs[0] = make([]uint8, board.Size()*board.Size())
	state.captureRefs[1] = make([]uint8, board.Size()*board.Size())
	var tokensBufStack [64]byte
	tokensBuf := tokensBufStack[:board.Size()+2*linePadding()]
	for lineIndex, line := range geometry.lines {
		summary := evaluateLineSummary(board, line, geometry.lineDirs[lineIndex], tokensBuf, weights)
		state.lineSummaries[lineIndex] = summary
		lineState := lineStateFromSummary(summary)
		state.Lines[lineIndex] = lineState
		state.applyLineStateDelta(lineState, 1)
		state.applyAlignmentUseDelta(summary, 1)
		state.applyThreatLUTDelta(summary, 1)
		state.applyThreatResponseDelta(summary, 1)
	}
	for windowIndex, window := range geometry.captureWindows {
		hit := evaluateCaptureWindow(state.board, window)
		state.captureHits[windowIndex] = hit
		state.applyCaptureWindowHitDelta(hit, 1)
	}
	state.recomputeDerived()
	return state
}

func (s *EvalState) ScoreOnly() int32 {
	if s == nil {
		return 0
	}
	return s.Score
}

func (s *EvalState) Snapshot(board *Board) EvalResult {
	if s == nil {
		return EvalResult{}
	}
	result := EvalResult{
		Score:           s.Score,
		StructuralScore: s.StructuralScore,
		CaptureScore:    s.CaptureScore,
		ComboScore:      s.ComboScore,
		Summary:         s.Summary,
	}
	if board != nil && (s.Summary.IsTactical ||
		s.Summary.MustAnswerForBlue ||
		s.Summary.MustAnswerForRed ||
		s.Summary.ForcingThreatsBlue > 0 ||
		s.Summary.ForcingThreatsRed > 0 ||
		s.Summary.Open3Blue > 0 ||
		s.Summary.Open3Red > 0) {
		details := s.BuildThreatDetails(board)
		result.ThreatCount = details.ThreatCount
		result.Threats = details.Threats
		result.CaptureThreatCount = details.CaptureThreatCount
		result.CaptureThreats = details.CaptureThreats
		criticalBlue, criticalRed, captureWinBlue, captureWinRed := summarizeCaptureThreatDetails(details)
		result.Summary.CriticalCapturesBlue = criticalBlue
		result.Summary.CriticalCapturesRed = criticalRed
		if captureWinBlue > result.Summary.CaptureWinNowBlue {
			result.Summary.CaptureWinNowBlue = captureWinBlue
		}
		if captureWinRed > result.Summary.CaptureWinNowRed {
			result.Summary.CaptureWinNowRed = captureWinRed
		}
		if captureWinBlue > 0 {
			result.Summary.MustAnswerForRed = true
		}
		if captureWinRed > 0 {
			result.Summary.MustAnswerForBlue = true
		}
		if criticalBlue > 0 || criticalRed > 0 || captureWinBlue > 0 || captureWinRed > 0 {
			result.Summary.IsTactical = true
		}
	}
	return result
}

func (s *EvalState) BuildThreatDetails(board *Board) ThreatDetails {
	if s == nil || board == nil {
		return ThreatDetails{}
	}
	details := buildStrongThreatDetailsFromLineSummaries(*board, s.lineSummaries)
	captureDetails := buildCaptureThreatDetailsFromLineSummaries(*board, s.lineSummaries, s.BlueCaptures, s.RedCaptures)
	details.CaptureThreatCount = captureDetails.CaptureThreatCount
	details.CaptureThreats = captureDetails.CaptureThreats
	return details
}

func (s *EvalState) BuildStrongThreatDetails(board *Board) ThreatDetails {
	if s == nil || board == nil {
		return ThreatDetails{}
	}
	return buildStrongThreatDetailsFromLineSummaries(*board, s.lineSummaries)
}

func (s *EvalState) BuildCaptureThreatDetails(board *Board) ThreatDetails {
	if s == nil || board == nil {
		return ThreatDetails{}
	}
	return buildCaptureThreatDetailsFromState(*board, s.lineSummaries, s.BlueCaptures, s.RedCaptures, s)
}

func (s *EvalState) ApplyMove(board *Board, delta MoveDelta) EvalUndo {
	undo := EvalUndo{
		PrevScore:           s.Score,
		PrevStructuralScore: s.StructuralScore,
		PrevCaptureScore:    s.CaptureScore,
		PrevComboScore:      s.ComboScore,
		PrevSummary:         s.Summary,
		PrevBlueCaptures:    s.BlueCaptures,
		PrevRedCaptures:     s.RedCaptures,
		PrevPatternCounts:   s.PatternCounts,
	}
	if s == nil || board == nil || s.geometry == nil {
		return undo
	}

	var touchedLines [64]uint16
	touchedLineCount := 0
	var touchedCaptures [160]uint16
	touchedCaptureCount := 0
	recordCell := func(idx int) {
		if idx < 0 || idx >= len(s.board.cells) {
			return
		}
		for i := uint8(0); i < undo.ChangedCellCount; i++ {
			if int(undo.ChangedCells[i].Index) == idx {
				return
			}
		}
		if undo.ChangedCellCount < uint8(len(undo.ChangedCells)) {
			undo.ChangedCells[undo.ChangedCellCount] = changedEvalCell{
				Index: CellIndex(idx),
				Prev:  s.board.cells[idx],
			}
			undo.ChangedCellCount++
		}
		for _, lineIndex := range s.geometry.cellToLines[idx] {
			already := false
			for i := 0; i < touchedLineCount; i++ {
				if touchedLines[i] == uint16(lineIndex) {
					already = true
					break
				}
			}
			if !already && touchedLineCount < len(touchedLines) {
				touchedLines[touchedLineCount] = uint16(lineIndex)
				touchedLineCount++
			}
		}
		for _, captureIndex := range s.geometry.cellToCaptureWindows[idx] {
			already := false
			for i := 0; i < touchedCaptureCount; i++ {
				if touchedCaptures[i] == uint16(captureIndex) {
					already = true
					break
				}
			}
			if !already && touchedCaptureCount < len(touchedCaptures) {
				touchedCaptures[touchedCaptureCount] = uint16(captureIndex)
				touchedCaptureCount++
			}
		}
	}

	moveIdx := delta.Move.Y*s.board.Size() + delta.Move.X
	recordCell(moveIdx)
	s.board.cells[moveIdx] = CellFromPlayer(delta.Player)
	for i := uint8(0); i < delta.CapturedCount; i++ {
		idx := int(delta.CapturedCells[i])
		recordCell(idx)
		s.board.cells[idx] = CellEmpty
	}
	if delta.CapturePairsGained > 0 {
		capturedStones := delta.CapturePairsGained * 2
		if delta.Player == PlayerBlue {
			s.BlueCaptures += capturedStones
		} else {
			s.RedCaptures += capturedStones
		}
	}
	s.SideToMove = otherPlayer(delta.Player)
	for i := 0; i < touchedCaptureCount; i++ {
		captureIndex := int(touchedCaptures[i])
		if undo.ChangedCaptureCount < uint8(len(undo.ChangedCaptures)) {
			undo.ChangedCaptures[undo.ChangedCaptureCount] = changedCaptureWindowUndo{
				Index: uint16(captureIndex),
				Prev:  s.captureHits[captureIndex],
			}
			undo.ChangedCaptureCount++
		}
		s.applyCaptureWindowHitDelta(s.captureHits[captureIndex], -1)
		hit := evaluateCaptureWindow(s.board, s.geometry.captureWindows[captureIndex])
		s.captureHits[captureIndex] = hit
		s.applyCaptureWindowHitDelta(hit, 1)
	}

	var tokensBufStack [64]byte
	tokensBuf := tokensBufStack[:s.board.Size()+2*linePadding()]
	for k := 0; k < touchedLineCount; k++ {
		lineIndex := int(touchedLines[k])
		if undo.ChangedLineCount < uint8(len(undo.ChangedLines)) {
			undo.ChangedLines[undo.ChangedLineCount] = changedLineUndo{
				Index:       uint16(lineIndex),
				PrevLine:    s.Lines[lineIndex],
				PrevSummary: s.lineSummaries[lineIndex],
			}
			undo.ChangedLineCount++
		}
		s.applyLineStateDelta(s.Lines[lineIndex], -1)
		s.applyAlignmentUseDelta(s.lineSummaries[lineIndex], -1)
		s.applyThreatLUTDelta(s.lineSummaries[lineIndex], -1)
		s.applyThreatResponseDelta(s.lineSummaries[lineIndex], -1)
		summary := evaluateLineSummary(s.board, s.geometry.lines[lineIndex], s.geometry.lineDirs[lineIndex], tokensBuf, s.weights)
		s.lineSummaries[lineIndex] = summary
		lineState := lineStateFromSummary(summary)
		s.Lines[lineIndex] = lineState
		s.applyLineStateDelta(lineState, 1)
		s.applyAlignmentUseDelta(summary, 1)
		s.applyThreatLUTDelta(summary, 1)
		s.applyThreatResponseDelta(summary, 1)
	}
	s.recomputeDerived()
	return undo
}

func (s *EvalState) UndoMove(undo EvalUndo) {
	if s == nil {
		return
	}
	for i := uint8(0); i < undo.ChangedCellCount; i++ {
		change := undo.ChangedCells[i]
		idx := int(change.Index)
		if idx >= 0 && idx < len(s.board.cells) {
			s.board.cells[idx] = change.Prev
		}
	}
	for i := uint8(0); i < undo.ChangedLineCount; i++ {
		change := undo.ChangedLines[i]
		lineIndex := int(change.Index)
		if lineIndex < 0 || lineIndex >= len(s.Lines) {
			continue
		}
		s.applyAlignmentUseDelta(s.lineSummaries[lineIndex], -1)
		s.applyThreatLUTDelta(s.lineSummaries[lineIndex], -1)
		s.applyThreatResponseDelta(s.lineSummaries[lineIndex], -1)
		s.Lines[lineIndex] = change.PrevLine
		s.lineSummaries[lineIndex] = change.PrevSummary
		s.applyAlignmentUseDelta(change.PrevSummary, 1)
		s.applyThreatLUTDelta(change.PrevSummary, 1)
		s.applyThreatResponseDelta(change.PrevSummary, 1)
	}
	s.Score = undo.PrevScore
	s.StructuralScore = undo.PrevStructuralScore
	s.CaptureScore = undo.PrevCaptureScore
	s.ComboScore = undo.PrevComboScore
	s.Summary = undo.PrevSummary
	s.BlueCaptures = undo.PrevBlueCaptures
	s.RedCaptures = undo.PrevRedCaptures
	s.PatternCounts = undo.PrevPatternCounts
	for i := uint8(0); i < undo.ChangedCaptureCount; i++ {
		change := undo.ChangedCaptures[i]
		captureIndex := int(change.Index)
		if captureIndex < 0 || captureIndex >= len(s.captureHits) {
			continue
		}
		s.applyCaptureWindowHitDelta(s.captureHits[captureIndex], -1)
		s.captureHits[captureIndex] = change.Prev
		s.applyCaptureWindowHitDelta(change.Prev, 1)
	}
}

func (s *EvalState) applyLineStateDelta(line LineState, sign int) {
	s.StructuralScore += int32(sign) * line.NetScore
	for pattern := PatternNone; pattern <= PatternBroken2; pattern++ {
		s.PatternCounts[0][pattern] = uint16(int(s.PatternCounts[0][pattern]) + sign*int(line.CountsBlue[pattern]))
		s.PatternCounts[1][pattern] = uint16(int(s.PatternCounts[1][pattern]) + sign*int(line.CountsRed[pattern]))
	}
}

func (s *EvalState) applyAlignmentUseDelta(summary evalLineSummary, sign int) {
	if s == nil {
		return
	}
	apply := func(dst []uint16, uses []evalCellCount) {
		for _, use := range uses {
			idx := int(use.idx)
			if idx < 0 || idx >= len(dst) {
				continue
			}
			next := int(dst[idx]) + sign*int(use.count)
			if next < 0 {
				next = 0
			}
			dst[idx] = uint16(next)
		}
	}
	apply(s.blueAlignmentUse, summary.blueAlignmentUses)
	apply(s.redAlignmentUse, summary.redAlignmentUses)
}

func (s *EvalState) applyThreatLUTDelta(summary evalLineSummary, sign int) {
	if s == nil {
		return
	}
	apply := func(uses []evalThreatImpactUse, selfScore []int32, oppScore []int32, selfFlags []uint32, oppFlags []uint32, touches []uint16) {
		for _, use := range uses {
			idx := int(use.idx)
			if idx < 0 || idx >= len(selfScore) {
				continue
			}
			selfScore[idx] += int32(sign) * int32(use.selfScore)
			oppScore[idx] += int32(sign) * int32(use.oppScore)
			if sign > 0 {
				selfFlags[idx] |= uint32(use.selfFlags)
				oppFlags[idx] |= uint32(use.oppFlags)
				touches[idx] = uint16(int(touches[idx]) + int(use.touches))
				continue
			}
			if touches[idx] > uint16(use.touches) {
				touches[idx] -= uint16(use.touches)
			} else {
				touches[idx] = 0
				selfFlags[idx] = 0
				oppFlags[idx] = 0
			}
		}
	}
	apply(summary.blueThreatLUTUses, s.blueThreatSelfScore, s.blueThreatOppScore, s.blueThreatSelfFlags, s.blueThreatOppFlags, s.blueThreatTouches)
	apply(summary.redThreatLUTUses, s.redThreatSelfScore, s.redThreatOppScore, s.redThreatSelfFlags, s.redThreatOppFlags, s.redThreatTouches)
}

func (s *EvalState) applyThreatResponseDelta(summary evalLineSummary, sign int) {
	if s == nil {
		return
	}
	apply := func(uses []evalThreatResponseUse, win []int32, mustPlay []int32, counter []int32, fork []int32, captureRace []int32, mustBlock []int32, preventFork []int32) {
		for _, use := range uses {
			idx := int(use.idx)
			if idx < 0 || idx >= len(win) {
				continue
			}
			win[idx] += int32(sign) * int32(use.selfWin)
			mustPlay[idx] += int32(sign) * int32(use.selfMustPlay)
			counter[idx] += int32(sign) * int32(use.selfCounter)
			fork[idx] += int32(sign) * int32(use.selfFork)
			captureRace[idx] += int32(sign) * int32(use.selfCaptureRace)
			mustBlock[idx] += int32(sign) * int32(use.oppMustBlock)
			preventFork[idx] += int32(sign) * int32(use.oppPreventFork)
		}
	}
	apply(summary.blueResponseUses, s.blueRespWin, s.blueRespMustPlay, s.blueRespCounter, s.blueRespFork, s.blueRespCaptureRace, s.blueRespMustBlock, s.blueRespPreventFork)
	apply(summary.redResponseUses, s.redRespWin, s.redRespMustPlay, s.redRespCounter, s.redRespFork, s.redRespCaptureRace, s.redRespMustBlock, s.redRespPreventFork)
}

func (s *EvalState) AlignmentUseCount(player PlayerColor, move Move) int {
	if s == nil || !move.IsValid(s.board.Size()) {
		return 0
	}
	idx := move.Y*s.board.Size() + move.X
	switch player {
	case PlayerBlue:
		if idx < len(s.blueAlignmentUse) {
			return int(s.blueAlignmentUse[idx])
		}
	case PlayerRed:
		if idx < len(s.redAlignmentUse) {
			return int(s.redAlignmentUse[idx])
		}
	}
	return 0
}

func (s *EvalState) AlignmentUseCounts(move Move) (ownBlue int, ownRed int, total int) {
	if s == nil || !move.IsValid(s.board.Size()) {
		return 0, 0, 0
	}
	idx := move.Y*s.board.Size() + move.X
	if idx < len(s.blueAlignmentUse) {
		ownBlue = int(s.blueAlignmentUse[idx])
	}
	if idx < len(s.redAlignmentUse) {
		ownRed = int(s.redAlignmentUse[idx])
	}
	return ownBlue, ownRed, ownBlue + ownRed
}

func (s *EvalState) AlignmentUseMap() map[Pos]int {
	if s == nil {
		return nil
	}
	size := s.board.Size()
	out := make(map[Pos]int, 32)
	for idx := 0; idx < len(s.board.cells); idx++ {
		if s.board.cells[idx] != CellEmpty {
			continue
		}
		total := 0
		if idx < len(s.blueAlignmentUse) {
			total += int(s.blueAlignmentUse[idx])
		}
		if idx < len(s.redAlignmentUse) {
			total += int(s.redAlignmentUse[idx])
		}
		if total == 0 {
			continue
		}
		out[boardPosFromIndex(size, idx)] = total
	}
	return out
}

func (s *EvalState) recomputeDerived() {
	s.CaptureScore = roundedInt32(captureAdvancementScore(s))
	s.ComboScore = roundedInt32(forkComboScore(s))
	s.Summary = buildTacticalSummary(s.PatternCounts, s.BlueCaptures, s.RedCaptures)
	s.Score = s.StructuralScore + s.CaptureScore + s.ComboScore
}

// forkComboScore adds a bonus when a player has simultaneous threats that
// cannot both be blocked (double-3 fork, double-4 fork). These bonuses were
// defined in ForkOpen3/ForkFourPlus but were never wired into the eval.
func forkComboScore(s *EvalState) float64 {
	if s == nil {
		return 0
	}
	score := 0.0

	// Blue forks penalise the score (score = Red - Blue perspective)
	blueOpen4 := int(s.PatternCounts[0][PatternOpen4])
	blueOther4 := int(s.PatternCounts[0][PatternClosed4]) + int(s.PatternCounts[0][PatternBroken4])
	blueOpen3 := int(s.PatternCounts[0][PatternOpen3])
	if blueOpen4 >= 2 || (blueOpen4 >= 1 && blueOther4 >= 1) {
		score -= s.weights.ForkFourPlus
	} else if blueOpen3 >= 2 {
		score -= s.weights.ForkOpen3
	}

	// Red forks reward the score
	redOpen4 := int(s.PatternCounts[1][PatternOpen4])
	redOther4 := int(s.PatternCounts[1][PatternClosed4]) + int(s.PatternCounts[1][PatternBroken4])
	redOpen3 := int(s.PatternCounts[1][PatternOpen3])
	if redOpen4 >= 2 || (redOpen4 >= 1 && redOther4 >= 1) {
		score += s.weights.ForkFourPlus
	} else if redOpen3 >= 2 {
		score += s.weights.ForkOpen3
	}

	return score
}

func captureAdvancementScore(s *EvalState) float64 {
	if s == nil {
		return 0
	}
	target := DefaultGameSettings().CaptureWinStones
	if target <= 0 {
		target = 10
	}
	blue := capturePlayerEvalScore(s, PlayerBlue, target)
	red := capturePlayerEvalScore(s, PlayerRed, target)
	return red - blue
}

func capturePlayerEvalScore(s *EvalState, player PlayerColor, target int) float64 {
	if s == nil || target <= 0 {
		return 0
	}
	var captured uint8
	var refs []uint8
	switch player {
	case PlayerBlue:
		captured = s.BlueCaptures
		refs = s.captureRefs[0]
	case PlayerRed:
		captured = s.RedCaptures
		refs = s.captureRefs[1]
	default:
		return 0
	}

	pairsCaptured := float64(captured) / 2.0
	score := pairsCaptured * s.weights.CaptureNow
	// Strongly reward states where captures are already secured so search keeps
	// leaning toward capture races and trap conversion instead of quiet structure.
	if pairsCaptured > 0 {
		score += pairsCaptured * pairsCaptured * s.weights.CaptureNow * 0.85
	}

	remainingStones := target - int(captured)
	if remainingStones < 0 {
		remainingStones = 0
	}
	availableCaptureMoves := countAvailableCaptureRefs(refs)
	// Immediate capture opportunities should be strongly preferred over
	// "holding" vulnerable pairs on board. This pushes the engine to convert
	// trap patterns into concrete captures quickly.
	if availableCaptureMoves > 0 {
		score += float64(availableCaptureMoves) * s.weights.CaptureNow * 0.35
	}
	if remainingStones <= 4 {
		score += s.weights.CaptureNearWin * math.Pow(s.weights.CaptureWinSoonScale, float64(remainingStones))
		if availableCaptureMoves > 0 {
			score += float64(minInt(availableCaptureMoves, s.weights.CaptureInTwoLimit)) * s.weights.CaptureInTwo
		}
	}
	if remainingStones <= 2 && availableCaptureMoves > 0 {
		score += s.weights.CaptureDoubleThreat
	}

	// Penalize pairs that can be taken immediately: they often erase structural gains
	// and accelerate the opponent's capture race.
	score -= float64(countCapturablePairs(s.board, player)) * s.weights.HangingPair
	return score
}

func countAvailableCaptureRefs(refs []uint8) int {
	count := 0
	for _, ref := range refs {
		if ref > 0 {
			count++
		}
	}
	return count
}

func (s *EvalState) captureMoves(state GameState, rules Rules, player PlayerColor) []Move {
	if s == nil {
		return findCaptureMovesByScan(state, rules, player)
	}
	playerIndex := capturePlayerArrayIndex(player)
	if playerIndex < 0 || playerIndex >= len(s.captureRefs) {
		return nil
	}
	refs := s.captureRefs[playerIndex]
	if len(refs) == 0 {
		return nil
	}
	size := s.board.Size()
	moves := make([]Move, 0, 8)
	for idx, count := range refs {
		if count == 0 {
			continue
		}
		move := Move{X: idx % size, Y: idx / size}
		if ok, _ := rules.IsLegal(state, move, player); !ok {
			continue
		}
		moves = append(moves, move)
	}
	return moves
}

func (s *EvalState) applyCaptureWindowHitDelta(hit captureWindowHit, sign int) {
	if !hit.Valid {
		return
	}
	playerIndex := capturePlayerArrayIndex(hit.Player)
	if playerIndex < 0 || playerIndex >= len(s.captureRefs) {
		return
	}
	moveIndex := int(hit.Move)
	if moveIndex < 0 || moveIndex >= len(s.captureRefs[playerIndex]) {
		return
	}
	if sign > 0 {
		s.captureRefs[playerIndex][moveIndex]++
		return
	}
	if s.captureRefs[playerIndex][moveIndex] > 0 {
		s.captureRefs[playerIndex][moveIndex]--
	}
}

func capturePlayerArrayIndex(player PlayerColor) int {
	switch player {
	case PlayerBlue:
		return 0
	case PlayerRed:
		return 1
	default:
		return -1
	}
}

func evaluateCaptureWindow(board Board, window captureWindowDef) captureWindowHit {
	cells := window.cells
	c0 := board.cells[cells[0]]
	c1 := board.cells[cells[1]]
	c2 := board.cells[cells[2]]
	c3 := board.cells[cells[3]]
	switch {
	case c0 == CellBlue && c1 == CellRed && c2 == CellRed && c3 == CellEmpty:
		return captureWindowHit{Player: PlayerBlue, Move: CellIndex(cells[3]), Valid: true}
	case c0 == CellEmpty && c1 == CellRed && c2 == CellRed && c3 == CellBlue:
		return captureWindowHit{Player: PlayerBlue, Move: CellIndex(cells[0]), Valid: true}
	case c0 == CellRed && c1 == CellBlue && c2 == CellBlue && c3 == CellEmpty:
		return captureWindowHit{Player: PlayerRed, Move: CellIndex(cells[3]), Valid: true}
	case c0 == CellEmpty && c1 == CellBlue && c2 == CellBlue && c3 == CellRed:
		return captureWindowHit{Player: PlayerRed, Move: CellIndex(cells[0]), Valid: true}
	default:
		return captureWindowHit{}
	}
}

func lineStateFromSummary(summary evalLineSummary) LineState {
	state := LineState{
		ScoreBlue:   roundedInt32(summary.scoreBlue),
		ScoreRed:    roundedInt32(summary.scoreRed),
		HasFiveBlue: summary.blue.Win5 > 0,
		HasFiveRed:  summary.red.Win5 > 0,
	}
	fillPatternCountArray(&state.CountsBlue, summary.blue)
	fillPatternCountArray(&state.CountsRed, summary.red)
	state.NetScore = state.ScoreRed - state.ScoreBlue
	return state
}

func fillPatternCountArray(dst *[PatternBroken2 + 1]uint8, totals ThreatTotals) {
	dst[PatternWin5] = clampUint8(totals.Win5)
	dst[PatternOpen4] = clampUint8(totals.Open4)
	dst[PatternClosed4] = clampUint8(totals.Closed4)
	dst[PatternBroken4] = clampUint8(totals.Broken4)
	dst[PatternOpen3] = clampUint8(totals.Open3)
	dst[PatternBroken3] = clampUint8(totals.Broken3)
	dst[PatternClosed3] = clampUint8(totals.Closed3)
	dst[PatternOpen2] = clampUint8(totals.Open2)
	dst[PatternClosed2] = clampUint8(totals.Closed2)
	dst[PatternBroken2] = clampUint8(totals.Broken2)
}

func buildTacticalSummary(patternCounts [2][PatternBroken2 + 1]uint16, blueCaptures, redCaptures uint8) TacticalSummary {
	summary := TacticalSummary{
		WinNowBlue:  clampUint8(int(patternCounts[0][PatternWin5])),
		WinNowRed:   clampUint8(int(patternCounts[1][PatternWin5])),
		Open4Blue:   clampUint8(int(patternCounts[0][PatternOpen4])),
		Open4Red:    clampUint8(int(patternCounts[1][PatternOpen4])),
		Closed4Blue: clampUint8(int(patternCounts[0][PatternClosed4])),
		Closed4Red:  clampUint8(int(patternCounts[1][PatternClosed4])),
		Broken4Blue: clampUint8(int(patternCounts[0][PatternBroken4])),
		Broken4Red:  clampUint8(int(patternCounts[1][PatternBroken4])),
		Open3Blue:   clampUint8(int(patternCounts[0][PatternOpen3])),
		Open3Red:    clampUint8(int(patternCounts[1][PatternOpen3])),
		Broken3Blue: clampUint8(int(patternCounts[0][PatternBroken3])),
		Broken3Red:  clampUint8(int(patternCounts[1][PatternBroken3])),
	}
	if blueCaptures >= 8 {
		summary.CriticalCapturesBlue = 1
	}
	if redCaptures >= 8 {
		summary.CriticalCapturesRed = 1
	}
	if blueCaptures >= 10 {
		summary.CaptureWinNowBlue = 1
	}
	if redCaptures >= 10 {
		summary.CaptureWinNowRed = 1
	}
	summary.DoubleThreatBlue = summary.Open4Blue >= 2 ||
		(summary.Open4Blue >= 1 && (summary.Broken4Blue >= 1 || summary.Closed4Blue >= 1)) ||
		summary.Open3Blue >= 2
	summary.DoubleThreatRed = summary.Open4Red >= 2 ||
		(summary.Open4Red >= 1 && (summary.Broken4Red >= 1 || summary.Closed4Red >= 1)) ||
		summary.Open3Red >= 2
	summary.ForcingThreatsBlue = summary.Open4Blue + summary.Closed4Blue + summary.Broken4Blue
	summary.ForcingThreatsRed = summary.Open4Red + summary.Closed4Red + summary.Broken4Red
	summary.MustAnswerForBlue = summary.WinNowRed > 0 ||
		summary.CaptureWinNowRed > 0 ||
		summary.Open4Red > 0 ||
		summary.Open3Red > 0 ||
		summary.DoubleThreatRed
	summary.MustAnswerForRed = summary.WinNowBlue > 0 ||
		summary.CaptureWinNowBlue > 0 ||
		summary.Open4Blue > 0 ||
		summary.Open3Blue > 0 ||
		summary.DoubleThreatBlue
	summary.IsTactical = summary.WinNowBlue > 0 ||
		summary.WinNowRed > 0 ||
		summary.CaptureWinNowBlue > 0 ||
		summary.CaptureWinNowRed > 0 ||
		summary.Open4Blue > 0 ||
		summary.Open4Red > 0 ||
		summary.DoubleThreatBlue ||
		summary.DoubleThreatRed ||
		summary.CriticalCapturesBlue > 0 ||
		summary.CriticalCapturesRed > 0
	return summary
}

func buildStrongThreatDetailsFromLineSummaries(board Board, summaries []evalLineSummary) ThreatDetails {
	details := ThreatDetails{}
	appendThreats := func(player PlayerColor) {
		for _, threat := range buildThreatObjectsFromLineSummaries(board, summaries, player) {
			if details.ThreatCount >= uint8(len(details.Threats)) {
				return
			}
			details.Threats[details.ThreatCount] = threat
			details.ThreatCount++
		}
	}
	appendThreats(PlayerRed)
	appendThreats(PlayerBlue)
	return details
}

func buildThreatObjectsFromLineSummaries(board Board, summaries []evalLineSummary, player PlayerColor) []Threat {
	out := make([]Threat, 0, 16)
	for _, line := range summaries {
		var threats []evalLUTThreat
		if player == PlayerBlue {
			threats = line.blueLUTThreats
		} else {
			threats = line.redLUTThreats
		}
		for _, threat := range threats {
			if threat.typ != PatternWin5 && threat.typ != PatternOpen4 && threat.typ != PatternClosed4 && threat.typ != PatternBroken4 && threat.typ != PatternOpen3 {
				continue
			}
			out = append(out, buildThreatObjectFromLUT(board, player, threat))
			if len(out) >= 16 {
				return out
			}
		}
	}
	return out
}

func buildQuietThreatObjectsFromLineSummaries(board Board, summaries []evalLineSummary, player PlayerColor) []Threat {
	out := make([]Threat, 0, 24)
	for _, line := range summaries {
		var threats []evalLUTThreat
		if player == PlayerBlue {
			threats = line.blueLUTThreats
		} else {
			threats = line.redLUTThreats
		}
		for _, threat := range threats {
			switch threat.typ {
			case PatternOpen4, PatternClosed4, PatternBroken4, PatternOpen3, PatternBroken3:
			default:
				continue
			}
			out = append(out, buildThreatObjectFromLUT(board, player, threat))
			if len(out) >= 24 {
				return out
			}
		}
	}
	return out
}

func buildThreatDetailsFromLineSummaries(board Board, summaries []evalLineSummary, blueCaptures, redCaptures uint8) ThreatDetails {
	details := buildStrongThreatDetailsFromLineSummaries(board, summaries)
	captureDetails := buildCaptureThreatDetailsFromLineSummaries(board, summaries, blueCaptures, redCaptures)
	details.CaptureThreatCount = captureDetails.CaptureThreatCount
	details.CaptureThreats = captureDetails.CaptureThreats
	return details
}

func buildCaptureThreatDetailsFromState(board Board, summaries []evalLineSummary, blueCaptures, redCaptures uint8, evalState *EvalState) ThreatDetails {
	details := ThreatDetails{}
	appendCaptureThreats(&details, buildCaptureThreatsForPlayer(board, summaries, PlayerRed, blueCaptures, redCaptures, evalState))
	appendCaptureThreats(&details, buildCaptureThreatsForPlayer(board, summaries, PlayerBlue, blueCaptures, redCaptures, evalState))
	return details
}

func buildCaptureThreatDetailsFromLineSummaries(board Board, summaries []evalLineSummary, blueCaptures, redCaptures uint8) ThreatDetails {
	return buildCaptureThreatDetailsFromState(board, summaries, blueCaptures, redCaptures, nil)
}

func appendCaptureThreats(details *ThreatDetails, threats []CaptureThreat) {
	if details == nil {
		return
	}
	for _, threat := range threats {
		if details.CaptureThreatCount >= uint8(len(details.CaptureThreats)) {
			return
		}
		details.CaptureThreats[details.CaptureThreatCount] = threat
		details.CaptureThreatCount++
	}
}

func threatsForPlayerDetails(details ThreatDetails, player PlayerColor) []Threat {
	if details.ThreatCount == 0 {
		return nil
	}
	out := make([]Threat, 0, details.ThreatCount)
	for i := 0; i < int(details.ThreatCount); i++ {
		if details.Threats[i].Owner == player {
			out = append(out, details.Threats[i])
		}
	}
	return out
}

func buildThreatObject(board Board, player PlayerColor, threat evalThreat) Threat {
	stones := make([]Pos, 0, len(threat.stones))
	patternCells := make([]Pos, 0, len(threat.stones)+len(threat.extensions))
	extensions := make([]Pos, 0, len(threat.extensions))
	defenses := make([]Pos, 0, len(threat.extensions))
	for _, idx := range threat.stones {
		pos := boardPosFromIndex(board.Size(), idx)
		stones = append(stones, pos)
		patternCells = append(patternCells, pos)
	}
	for _, idx := range threat.extensions {
		pos := boardPosFromIndex(board.Size(), idx)
		extensions = append(extensions, pos)
		defenses = append(defenses, pos)
		patternCells = append(patternCells, pos)
	}
	return Threat{
		Owner:            player,
		Type:             ThreatType(threat.typ),
		Tier:             staticThreatTier(ThreatType(threat.typ)),
		Direction:        int(threat.dir),
		Stones:           stones,
		PatternCells:     patternCells,
		ExtensionSquares: extensions,
		DefenseSquares:   defenses,
		TotalStones:      len(threat.stones),
		Stable:           true,
		ForkPotential:    len(threat.extensions) >= 2,
	}
}

func buildThreatObjectFromLUT(board Board, player PlayerColor, threat evalLUTThreat) Threat {
	stones := make([]Pos, 0, len(threat.stones))
	patternCells := make([]Pos, 0, len(threat.stones))
	extensions := make([]Pos, 0, len(threat.extensions))
	defenses := make([]Pos, 0, len(threat.extensions))
	for _, idx := range threat.stones {
		pos := boardPosFromIndex(board.Size(), idx)
		stones = append(stones, pos)
		patternCells = append(patternCells, pos)
	}
	for _, idx := range threat.extensions {
		pos := boardPosFromIndex(board.Size(), idx)
		extensions = append(extensions, pos)
		defenses = append(defenses, pos)
	}
	return Threat{
		Owner:            player,
		Type:             ThreatType(threat.typ),
		Tier:             staticThreatTier(ThreatType(threat.typ)),
		Direction:        int(threat.dir),
		Stones:           stones,
		PatternCells:     patternCells,
		ExtensionSquares: extensions,
		DefenseSquares:   defenses,
		TotalStones:      len(threat.stones),
		Stable:           true,
		ForkPotential:    threat.forkPotential,
	}
}

func summarizeCaptureThreatDetails(details ThreatDetails) (uint8, uint8, uint8, uint8) {
	if details.CaptureThreatCount == 0 {
		return 0, 0, 0, 0
	}
	blueThreats := make([]CaptureThreat, 0, details.CaptureThreatCount)
	redThreats := make([]CaptureThreat, 0, details.CaptureThreatCount)
	for i := 0; i < int(details.CaptureThreatCount); i++ {
		threat := details.CaptureThreats[i]
		if threat.Owner == PlayerBlue {
			blueThreats = append(blueThreats, threat)
		} else if threat.Owner == PlayerRed {
			redThreats = append(redThreats, threat)
		}
	}
	blueWin := uint8(0)
	redWin := uint8(0)
	for _, threat := range blueThreats {
		if threat.GivesImmediateWin {
			blueWin = 1
			break
		}
	}
	for _, threat := range redThreats {
		if threat.GivesImmediateWin {
			redWin = 1
			break
		}
	}
	return clampUint8(len(blueThreats)), clampUint8(len(redThreats)), blueWin, redWin
}

func buildCaptureThreatsForPlayer(board Board, summaries []evalLineSummary, player PlayerColor, blueCaptures, redCaptures uint8, evalState *EvalState) []CaptureThreat {
	if board.Size() <= 0 {
		return nil
	}
	settings := DefaultGameSettings()
	settings.BoardSize = board.Size()
	settings.ForbidDoubleThreeBlue = false
	settings.ForbidDoubleThreeRed = false
	rules := NewRules(settings)
	state := GameState{
		Board:        board,
		ToMove:       player,
		Status:       StatusRunning,
		CapturedBlue: int(blueCaptures),
		CapturedRed:  int(redCaptures),
	}
	enemyThreats := buildThreatObjectsFromLineSummaries(board, summaries, otherPlayer(player))
	moves := findCaptureMoves(state, rules, player, evalState)
	out := make([]CaptureThreat, 0, minCaptureThreats(len(moves), 8))
	for _, move := range moves {
		boardCopy := board.Clone()
		playerCell := CellFromPlayer(player)
		boardCopy.Set(move.X, move.Y, playerCell)
		captures := rules.FindCaptures(boardCopy, move, playerCell)
		if len(captures) == 0 {
			continue
		}
		for _, captured := range captures {
			boardCopy.Remove(captured.X, captured.Y)
		}
		threat := CaptureThreat{
			Owner:    player,
			Move:     CellIndex(move.Y*board.Size() + move.X),
			Severity: int16(30 + len(captures)*5),
		}
		for _, captured := range captures {
			threat.CapturedStones.Add(CellIndex(captured.Y*board.Size() + captured.X))
		}
		currentCaptures := int(blueCaptures)
		if player == PlayerRed {
			currentCaptures = int(redCaptures)
		}
		if currentCaptures+len(captures) >= rules.CaptureWinStones() {
			threat.GivesImmediateWin = true
			threat.Severity += 80
		}
		for _, enemyThreat := range enemyThreats {
			if capturedIntersectsThreat(threat.CapturedStones, enemyThreat, board.Size()) {
				threat.BreaksEnemyThreat = true
				threat.Severity += 20
				if enemyThreat.Type == ThreatOpen4 {
					threat.BreaksEnemyOpen4 = true
					threat.Severity += 25
				}
			}
		}
		nextBlueCaptures := blueCaptures
		nextRedCaptures := redCaptures
		if player == PlayerBlue {
			nextBlueCaptures += uint8(len(captures))
		} else {
			nextRedCaptures += uint8(len(captures))
		}
		nextSummary := summarizeBoardPatterns(boardCopy, nextBlueCaptures, nextRedCaptures, resolveThreatWeights(DefaultConfig()))
		if player == PlayerBlue {
			threat.CreatesOwnThreat = nextSummary.Open4Blue > 0 || nextSummary.WinNowBlue > 0 || nextSummary.DoubleThreatBlue
		} else {
			threat.CreatesOwnThreat = nextSummary.Open4Red > 0 || nextSummary.WinNowRed > 0 || nextSummary.DoubleThreatRed
		}
		if threat.CreatesOwnThreat {
			threat.Severity += 20
		}
		if threat.GivesImmediateWin || threat.BreaksEnemyOpen4 || threat.BreaksEnemyThreat || threat.CreatesOwnThreat {
			out = append(out, threat)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func summarizeBoardPatterns(board Board, blueCaptures, redCaptures uint8, weights ThreatWeights) TacticalSummary {
	if board.Size() <= 0 {
		return TacticalSummary{}
	}
	geometry := getEvalGeometry(board.Size())
	var patternCounts [2][PatternBroken2 + 1]uint16
	var tokensBufStack [64]byte
	tokensBuf := tokensBufStack[:board.Size()+2*linePadding()]
	for lineIndex, line := range geometry.lines {
		summary := evaluateLineSummary(board, line, geometry.lineDirs[lineIndex], tokensBuf, weights)
		lineState := lineStateFromSummary(summary)
		for pattern := PatternNone; pattern <= PatternBroken2; pattern++ {
			patternCounts[0][pattern] += uint16(lineState.CountsBlue[pattern])
			patternCounts[1][pattern] += uint16(lineState.CountsRed[pattern])
		}
	}
	return buildTacticalSummary(patternCounts, blueCaptures, redCaptures)
}

func capturedIntersectsThreat(captured CellSet, threat Threat, boardSize int) bool {
	for _, pos := range threat.Stones {
		idx := CellIndex(pos.Y*boardSize + pos.X)
		if captured.Contains(idx) {
			return true
		}
	}
	return false
}

func minCaptureThreats(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func roundedInt32(v float64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(math.Round(v))
}

func clampUint8(v int) uint8 {
	if v <= 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
