package main

import "sync"

type ZobristTable struct {
	size  int
	cells []uint64
	side  uint64
}

type zobristStore struct {
	mu     sync.Mutex
	tables map[int]*ZobristTable
}

var zobristTables = &zobristStore{tables: make(map[int]*ZobristTable)}

func GetZobrist(size int) *ZobristTable {
	zobristTables.mu.Lock()
	defer zobristTables.mu.Unlock()
	if table, ok := zobristTables.tables[size]; ok {
		return table
	}
	rng := splitmix64{state: uint64(0x9e3779b97f4a7c15) ^ uint64(size)}
	table := &ZobristTable{size: size, cells: make([]uint64, size*size*2)}
	for i := range table.cells {
		table.cells[i] = rng.next()
	}
	table.side = rng.next()
	zobristTables.tables[size] = table
	return table
}

func (z *ZobristTable) stone(x, y int, player PlayerColor) uint64 {
	idx := (y*z.size + x) * 2
	if player == PlayerWhite {
		idx++
	}
	return z.cells[idx]
}

func ComputeHash(state GameState) uint64 {
	z := GetZobrist(state.Board.Size())
	var hash uint64
	size := state.Board.Size()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			cell := state.Board.At(x, y)
			if cell == CellEmpty {
				continue
			}
			player := PlayerBlack
			if cell == CellWhite {
				player = PlayerWhite
			}
			hash ^= z.stone(x, y, player)
		}
	}
	if state.ToMove == PlayerWhite {
		hash ^= z.side
	}
	hash ^= captureHash(PlayerBlack, state.CapturedBlack)
	hash ^= captureHash(PlayerWhite, state.CapturedWhite)
	return hash
}

func UpdateHashAfterMove(state *GameState, move Move, player PlayerColor, captures []Move, prevToMove PlayerColor, prevCapturedBlack, prevCapturedWhite int) {
	z := GetZobrist(state.Board.Size())
	hash := state.Hash
	if prevToMove == PlayerWhite {
		hash ^= z.side
	}
	hash ^= z.stone(move.X, move.Y, player)
	opp := otherPlayer(player)
	for _, captured := range captures {
		hash ^= z.stone(captured.X, captured.Y, opp)
	}
	hash ^= captureHash(PlayerBlack, prevCapturedBlack)
	hash ^= captureHash(PlayerBlack, state.CapturedBlack)
	hash ^= captureHash(PlayerWhite, prevCapturedWhite)
	hash ^= captureHash(PlayerWhite, state.CapturedWhite)
	if state.ToMove == PlayerWhite {
		hash ^= z.side
	}
	state.Hash = hash
}

func captureHash(player PlayerColor, count int) uint64 {
	seed := uint64(count)<<1 | uint64(player&1)
	rng := splitmix64{state: seed + 0x9e3779b97f4a7c15}
	return rng.next()
}

type splitmix64 struct {
	state uint64
}

func (s *splitmix64) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}
