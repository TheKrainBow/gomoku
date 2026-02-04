package main

import "math"

type TTFlag uint8

const (
	TTExact TTFlag = iota
	TTLower
	TTUpper
)

type TTEntry struct {
	Key      uint64
	Depth    int
	Value    float64
	Flag     TTFlag
	BestMove Move
	Gen      uint32
	Valid    bool
}

type TranspositionTable struct {
	mask    uint64
	buckets int
	entries []TTEntry
	gen     uint32
}

func NewTranspositionTable(size uint64, buckets int) *TranspositionTable {
	if buckets <= 0 {
		buckets = 2
	}
	if size < 1 {
		size = 1
	}
	if (size & (size - 1)) != 0 {
		size = nextPowerOfTwo(size)
	}
	return &TranspositionTable{
		mask:    size - 1,
		buckets: buckets,
		entries: make([]TTEntry, int(size)*buckets),
		gen:     1,
	}
}

func (tt *TranspositionTable) NextGeneration() {
	tt.gen++
	if tt.gen == 0 {
		tt.gen = 1
	}
}

func (tt *TranspositionTable) bucketIndex(key uint64) int {
	return int(key&tt.mask) * tt.buckets
}

func (tt *TranspositionTable) Probe(key uint64) (TTEntry, bool) {
	start := tt.bucketIndex(key)
	for i := 0; i < tt.buckets; i++ {
		entry := tt.entries[start+i]
		if entry.Valid && entry.Key == key {
			return entry, true
		}
	}
	return TTEntry{}, false
}

func (tt *TranspositionTable) Store(key uint64, depth int, value float64, flag TTFlag, best Move) (replaced bool, overwrote bool) {
	start := tt.bucketIndex(key)
	victim := -1
	oldestAge := uint32(0)
	shallowest := math.MaxInt
	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		entry := tt.entries[idx]
		if entry.Valid && entry.Key == key {
			if shouldReplace(entry, depth, flag, tt.gen) {
				tt.entries[idx] = TTEntry{Key: key, Depth: depth, Value: value, Flag: flag, BestMove: best, Gen: tt.gen, Valid: true}
				return false, true
			}
			return false, false
		}
		if !entry.Valid {
			victim = idx
			break
		}
		age := tt.gen - entry.Gen
		if victim == -1 || age > oldestAge || (age == oldestAge && entry.Depth < shallowest) {
			victim = idx
			oldestAge = age
			shallowest = entry.Depth
		}
	}
	if victim >= 0 {
		tt.entries[victim] = TTEntry{Key: key, Depth: depth, Value: value, Flag: flag, BestMove: best, Gen: tt.gen, Valid: true}
		return true, false
	}
	return false, false
}

func (tt *TranspositionTable) Count() int {
	count := 0
	for i := range tt.entries {
		if tt.entries[i].Valid {
			count++
		}
	}
	return count
}

func shouldReplace(entry TTEntry, depth int, flag TTFlag, gen uint32) bool {
	if depth > entry.Depth {
		return true
	}
	if depth == entry.Depth && flag == TTExact && entry.Flag != TTExact {
		return true
	}
	age := gen - entry.Gen
	if age > 2 && depth >= entry.Depth {
		return true
	}
	return false
}

func nextPowerOfTwo(v uint64) uint64 {
	v--
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v |= v >> 32
	v++
	return v
}
