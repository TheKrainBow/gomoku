package main

import "fmt"

type Rules struct {
	settings GameSettings
}

func NewRules(settings GameSettings) Rules {
	return Rules{settings: settings}
}

func (r Rules) IsLegal(state GameState, move Move, player PlayerColor) (bool, string) {
	if !move.IsValid(r.settings.BoardSize) {
		return false, "out of bounds"
	}
	if player == state.ToMove && state.MustCapture {
		allowed := false
		for _, forced := range state.ForcedCaptureMoves {
			if forced.Equals(move) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, "must capture"
		}
	}
	if !state.Board.IsEmpty(move.X, move.Y) {
		return false, "occupied"
	}
	forbid := false
	if player == PlayerBlack {
		forbid = r.settings.ForbidDoubleThreeBlack
	} else {
		forbid = r.settings.ForbidDoubleThreeWhite
	}
	if forbid {
		// IsForbiddenDoubleThree mutates board only transiently (set/remove move),
		// so we can run it directly without cloning the whole board.
		if r.IsForbiddenDoubleThree(state.Board, move, player) {
			return false, "forbidden double three"
		}
	}
	return true, ""
}

func (r Rules) IsLegalDefault(state GameState, move Move) (bool, string) {
	return r.IsLegal(state, move, state.ToMove)
}

func (r Rules) IsWin(board Board, lastMove Move) bool {
	if !lastMove.IsValid(r.settings.BoardSize) {
		return false
	}
	if board.At(lastMove.X, lastMove.Y) == CellEmpty {
		return false
	}
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for i := 0; i < 4; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		count := 1
		count += r.countDirection(board, lastMove, dx, dy)
		count += r.countDirection(board, lastMove, -dx, -dy)
		if count >= r.settings.WinLength {
			return true
		}
	}
	return false
}

func (r Rules) IsDraw(board Board) bool {
	return board.CountEmpty() == 0
}

func (r Rules) IsForbiddenDoubleThree(board Board, move Move, player PlayerColor) bool {
	cell := CellFromPlayer(player)
	board.Set(move.X, move.Y, cell)
	openThrees := 0
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for i := 0; i < 4; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		if r.isOpenThreeInDirection(board, move, dx, dy, cell) {
			openThrees++
			if openThrees >= 2 {
				board.Remove(move.X, move.Y)
				return true
			}
		}
	}
	board.Remove(move.X, move.Y)
	return openThrees >= 2
}

func (r Rules) FindCaptures(board Board, move Move, playerCell Cell) []Move {
	return r.FindCapturesInto(board, move, playerCell, nil)
}

func (r Rules) FindCapturesInto(board Board, move Move, playerCell Cell, captures []Move) []Move {
	captures = captures[:0]
	if cap(captures) < 8 {
		captures = make([]Move, 0, 8)
	}
	opponentCell := CellBlack
	if playerCell == CellBlack {
		opponentCell = CellWhite
	}
	directions := [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}}
	for i := 0; i < 8; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		x1 := move.X + dx
		y1 := move.Y + dy
		x2 := move.X + 2*dx
		y2 := move.Y + 2*dy
		x3 := move.X + 3*dx
		y3 := move.Y + 3*dy
		if !board.InBounds(x3, y3) || !board.InBounds(x2, y2) || !board.InBounds(x1, y1) {
			continue
		}
		if board.At(x1, y1) == opponentCell && board.At(x2, y2) == opponentCell && board.At(x3, y3) == playerCell {
			cap1 := Move{X: x1, Y: y1}
			cap2 := Move{X: x2, Y: y2}
			dup1 := false
			dup2 := false
			for _, existing := range captures {
				if existing.Equals(cap1) {
					dup1 = true
				}
				if existing.Equals(cap2) {
					dup2 = true
				}
			}
			if !dup1 {
				captures = append(captures, cap1)
			}
			if !dup2 {
				captures = append(captures, cap2)
			}
		}
	}
	return captures
}

func (r Rules) OpponentCanBreakAlignmentByCapture(afterMoveState GameState, opponent PlayerColor) bool {
	probeState := afterMoveState.Clone()
	probeState.ToMove = opponent
	opponentCell := CellFromPlayer(opponent)
	targetCell := CellFromPlayer(otherPlayer(opponent))
	size := afterMoveState.Board.Size()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !afterMoveState.Board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := r.IsLegal(probeState, move, opponent); !ok {
				continue
			}
			boardCopy := afterMoveState.Board.Clone()
			boardCopy.Set(x, y, opponentCell)
			captures := r.FindCaptures(boardCopy, move, opponentCell)
			if len(captures) == 0 {
				continue
			}
			for _, cap := range captures {
				boardCopy.Remove(cap.X, cap.Y)
			}
			if !r.hasAnyAlignment(boardCopy, targetCell) {
				return true
			}
		}
	}
	return false
}

func (r Rules) FindAlignmentBreakCaptures(afterMoveState GameState, opponent PlayerColor) []Move {
	moves := []Move{}
	probeState := afterMoveState.Clone()
	probeState.ToMove = opponent
	opponentCell := CellFromPlayer(opponent)
	targetCell := CellFromPlayer(otherPlayer(opponent))
	size := afterMoveState.Board.Size()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !afterMoveState.Board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := r.IsLegal(probeState, move, opponent); !ok {
				continue
			}
			boardCopy := afterMoveState.Board.Clone()
			boardCopy.Set(x, y, opponentCell)
			captures := r.FindCaptures(boardCopy, move, opponentCell)
			if len(captures) == 0 {
				continue
			}
			for _, cap := range captures {
				boardCopy.Remove(cap.X, cap.Y)
			}
			if !r.hasAnyAlignment(boardCopy, targetCell) {
				moves = append(moves, move)
			}
		}
	}
	return moves
}

func (r Rules) FindImmediateCaptureWinMove(state GameState, attacker PlayerColor, attackerCaptured int) (Move, []Move, bool) {
	if attackerCaptured+2 < r.settings.CaptureWinStones {
		return Move{}, nil, false
	}
	probeState := state.Clone()
	probeState.ToMove = attacker
	probeState.MustCapture = false
	probeState.ForcedCaptureMoves = nil
	attackerCell := CellFromPlayer(attacker)
	size := state.Board.Size()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !state.Board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := r.IsLegal(probeState, move, attacker); !ok {
				continue
			}
			boardCopy := state.Board.Clone()
			boardCopy.Set(x, y, attackerCell)
			captures := r.FindCaptures(boardCopy, move, attackerCell)
			if len(captures) < 2 {
				continue
			}
			if attackerCaptured+len(captures) < r.settings.CaptureWinStones {
				continue
			}
			return move, append([]Move(nil), captures...), true
		}
	}
	return Move{}, nil, false
}

func (r Rules) FindAlignmentLine(board Board, lastMove Move) ([]Move, bool) {
	line := []Move{}
	if !lastMove.IsValid(r.settings.BoardSize) {
		return line, false
	}
	if board.At(lastMove.X, lastMove.Y) == CellEmpty {
		return line, false
	}
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for i := 0; i < 4; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		line = r.collectLine(board, lastMove, dx, dy)
		if len(line) >= r.settings.WinLength {
			return line, true
		}
	}
	return []Move{}, false
}

func (r Rules) WinLength() int {
	return r.settings.WinLength
}

func (r Rules) CaptureWinStones() int {
	return r.settings.CaptureWinStones
}

func (r Rules) countDirection(board Board, start Move, dx, dy int) int {
	target := board.At(start.X, start.Y)
	x := start.X + dx
	y := start.Y + dy
	count := 0
	for board.InBounds(x, y) && board.At(x, y) == target {
		count++
		x += dx
		y += dy
	}
	return count
}

func (r Rules) collectLine(board Board, start Move, dx, dy int) []Move {
	line := []Move{}
	target := board.At(start.X, start.Y)
	x := start.X
	y := start.Y
	for board.InBounds(x-dx, y-dy) && board.At(x-dx, y-dy) == target {
		x -= dx
		y -= dy
	}
	for board.InBounds(x, y) && board.At(x, y) == target {
		line = append(line, Move{X: x, Y: y})
		x += dx
		y += dy
	}
	return line
}

func (r Rules) isOpenThreeInDirection(board Board, move Move, dx, dy int, playerCell Cell) bool {
	const rng = 5
	const lineSize = rng*2 + 1
	var line [lineSize]byte
	for i := -rng; i <= rng; i++ {
		x := move.X + i*dx
		y := move.Y + i*dy
		value := byte('O')
		if board.InBounds(x, y) {
			cell := board.At(x, y)
			if cell == CellEmpty {
				value = '_'
			} else if cell == playerCell {
				value = 'X'
			} else {
				value = 'O'
			}
		}
		line[i+rng] = value
	}
	center := rng
	for start := 0; start+5 <= lineSize; start++ {
		end := start + 5
		if center < start || center >= end {
			continue
		}
		if line[start] == '_' && line[start+4] == '_' && line[start+1] == 'X' && line[start+2] == 'X' && line[start+3] == 'X' {
			return true
		}
	}
	for start := 0; start+6 <= lineSize; start++ {
		end := start + 6
		if center < start || center >= end {
			continue
		}
		if line[start] != '_' || line[start+5] != '_' {
			continue
		}
		c1 := line[start+1]
		c2 := line[start+2]
		c3 := line[start+3]
		c4 := line[start+4]
		if c1 == 'X' && c2 == 'X' && c3 == '_' && c4 == 'X' {
			return true
		}
		if c1 == 'X' && c2 == '_' && c3 == 'X' && c4 == 'X' {
			return true
		}
	}
	return false
}

func (r Rules) hasAnyAlignment(board Board, playerCell Cell) bool {
	size := board.Size()
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if board.At(x, y) != playerCell {
				continue
			}
			move := Move{X: x, Y: y}
			for i := 0; i < 4; i++ {
				dx := directions[i][0]
				dy := directions[i][1]
				count := 1
				count += r.countDirection(board, move, dx, dy)
				count += r.countDirection(board, move, -dx, -dy)
				if count >= r.settings.WinLength {
					return true
				}
			}
		}
	}
	return false
}

func (r Rules) String() string {
	return fmt.Sprintf("Rules{win=%d, capture=%d}", r.settings.WinLength, r.settings.CaptureWinStones)
}
