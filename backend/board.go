package main

import "fmt"

type Cell int

const (
	CellEmpty Cell = iota
	CellBlue
	CellRed
)

type Board struct {
	size  int
	cells []Cell
}

func NewBoard(boardSize int) Board {
	b := Board{}
	b.Reset(boardSize)
	return b
}

func (b *Board) Reset(boardSize int) {
	b.size = boardSize
	b.cells = make([]Cell, boardSize*boardSize)
}

func (b Board) At(x, y int) Cell {
	return b.cells[b.index(x, y)]
}

func (b *Board) Set(x, y int, value Cell) {
	b.cells[b.index(x, y)] = value
}

func (b *Board) Remove(x, y int) {
	b.cells[b.index(x, y)] = CellEmpty
}

func (b Board) InBounds(x, y int) bool {
	return x >= 0 && y >= 0 && x < b.size && y < b.size
}

func (b Board) IsEmpty(x, y int) bool {
	return b.InBounds(x, y) && b.At(x, y) == CellEmpty
}

func (b Board) CountEmpty() int {
	count := 0
	for _, cell := range b.cells {
		if cell == CellEmpty {
			count++
		}
	}
	return count
}

func (b Board) Size() int {
	return b.size
}

func (b Board) Clone() Board {
	clone := Board{size: b.size}
	clone.cells = make([]Cell, len(b.cells))
	copy(clone.cells, b.cells)
	return clone
}

func (b Board) index(x, y int) int {
	return y*b.size + x
}

func (c Cell) String() string {
	switch c {
	case CellBlue:
		return "Blue"
	case CellRed:
		return "Red"
	default:
		return "Empty"
	}
}

func CellFromPlayer(player PlayerColor) Cell {
	if player == PlayerBlue {
		return CellBlue
	}
	return CellRed
}

func PlayerFromCell(cell Cell) (PlayerColor, error) {
	switch cell {
	case CellBlue:
		return PlayerBlue, nil
	case CellRed:
		return PlayerRed, nil
	default:
		return PlayerBlue, fmt.Errorf("empty cell has no player")
	}
}
