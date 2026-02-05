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
	hash, _ := computeSymmetricHashes(state)
	return hash
}

func UpdateHashAfterMove(state *GameState, move Move, player PlayerColor, captures []Move, prevToMove PlayerColor, prevCapturedBlack, prevCapturedWhite int) {
	z := GetZobrist(state.Board.Size())
	hash := state.Hash
	if prevToMove == PlayerWhite {
		hash ^= z.side
		xorAllSymHashes(&state.HashSym, z.side)
	}
	hash ^= z.stone(move.X, move.Y, player)
	xorMoveSymmetry(&state.HashSym, state.Board.Size(), move.X, move.Y, player, z)
	opp := otherPlayer(player)
	for _, captured := range captures {
		hash ^= z.stone(captured.X, captured.Y, opp)
		xorMoveSymmetry(&state.HashSym, state.Board.Size(), captured.X, captured.Y, opp, z)
	}
	hash ^= captureHash(PlayerBlack, prevCapturedBlack)
	hash ^= captureHash(PlayerBlack, state.CapturedBlack)
	hash ^= captureHash(PlayerWhite, prevCapturedWhite)
	hash ^= captureHash(PlayerWhite, state.CapturedWhite)
	xorCaptureHashes(&state.HashSym, prevCapturedBlack, state.CapturedBlack, prevCapturedWhite, state.CapturedWhite)
	if state.ToMove == PlayerWhite {
		hash ^= z.side
		xorAllSymHashes(&state.HashSym, z.side)
	}
	state.Hash = hash
	state.CanonHash = canonicalSymHash(state.HashSym)
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

type symmetryTransform struct {
	rot  int
	flip bool
}

var symmetryTransforms = [8]symmetryTransform{
	{rot: 0, flip: false},
	{rot: 1, flip: false},
	{rot: 2, flip: false},
	{rot: 3, flip: false},
	{rot: 0, flip: true},
	{rot: 1, flip: true},
	{rot: 2, flip: true},
	{rot: 3, flip: true},
}

func transformCoord(x, y, size int, transform symmetryTransform) (int, int) {
	var tx, ty int
	switch transform.rot {
	case 0:
		tx, ty = x, y
	case 1:
		tx, ty = size-1-y, x
	case 2:
		tx, ty = size-1-x, size-1-y
	default:
		tx, ty = y, size-1-x
	}
	if transform.flip {
		tx = size - 1 - tx
	}
	return tx, ty
}

func computeSymmetricHashes(state GameState) (uint64, [8]uint64) {
	hash := uint64(0)
	var sym [8]uint64
	z := GetZobrist(state.Board.Size())
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
			for i, transform := range symmetryTransforms {
				tx, ty := transformCoord(x, y, size, transform)
				sym[i] ^= z.stone(tx, ty, player)
			}
		}
	}
	if state.ToMove == PlayerWhite {
		hash ^= z.side
		for i := range sym {
			sym[i] ^= z.side
		}
	}
	hash ^= captureHash(PlayerBlack, state.CapturedBlack)
	hash ^= captureHash(PlayerWhite, state.CapturedWhite)
	for i := range sym {
		sym[i] ^= captureHash(PlayerBlack, state.CapturedBlack)
		sym[i] ^= captureHash(PlayerWhite, state.CapturedWhite)
	}
	return hash, sym
}

func xorAllSymHashes(sym *[8]uint64, value uint64) {
	for i := range sym {
		sym[i] ^= value
	}
}

func xorMoveSymmetry(sym *[8]uint64, size, x, y int, player PlayerColor, z *ZobristTable) {
	for i, transform := range symmetryTransforms {
		tx, ty := transformCoord(x, y, size, transform)
		sym[i] ^= z.stone(tx, ty, player)
	}
}

func xorCaptureHashes(sym *[8]uint64, prevBlack, newBlack, prevWhite, newWhite int) {
	for i := range sym {
		sym[i] ^= captureHash(PlayerBlack, prevBlack)
		sym[i] ^= captureHash(PlayerBlack, newBlack)
		sym[i] ^= captureHash(PlayerWhite, prevWhite)
		sym[i] ^= captureHash(PlayerWhite, newWhite)
	}
}

func canonicalSymHash(sym [8]uint64) uint64 {
	min := sym[0]
	for i := 1; i < len(sym); i++ {
		if sym[i] < min {
			min = sym[i]
		}
	}
	return min
}
