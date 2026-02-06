package main

import (
	"math"
	"sync"
	"sync/atomic"
)

type TTFlag uint8

const (
	TTExact TTFlag = iota
	TTLower
	TTUpper
)

const ttVeryOldGenerations = 8

type TTEntry struct {
	Key         uint64
	Depth       int
	Score       int32
	Flag        TTFlag
	BestMove    Move
	GenWritten  uint32
	GenLastUsed uint32
	Valid       bool
}

func (e TTEntry) ScoreFloat() float64 {
	return float64(e.Score)
}

type TranspositionTable struct {
	mask        uint64
	buckets     int
	entries     []TTEntry
	stripeLocks []sync.RWMutex
	stripeMask  uint64
	gen         atomic.Uint32
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
	maxStripes := 64
	if int(size) < maxStripes {
		maxStripes = int(size)
	}
	stripes := 1
	for stripes*2 <= maxStripes {
		stripes *= 2
	}
	tt := &TranspositionTable{
		mask:        size - 1,
		buckets:     buckets,
		entries:     make([]TTEntry, int(size)*buckets),
		stripeLocks: make([]sync.RWMutex, stripes),
		stripeMask:  uint64(stripes - 1),
	}
	tt.gen.Store(1)
	return tt
}

func (tt *TranspositionTable) NextGeneration() {
	gen := tt.gen.Add(1)
	if gen == 0 {
		tt.gen.CompareAndSwap(0, 1)
	}
}

func (tt *TranspositionTable) Generation() uint32 {
	return tt.currentGeneration()
}

func (tt *TranspositionTable) Clear() {
	tt.lockAllStripes()
	defer tt.unlockAllStripes()
	for i := range tt.entries {
		tt.entries[i] = TTEntry{}
	}
	tt.gen.Store(1)
}

func (tt *TranspositionTable) bucketIndex(key uint64) int {
	return int(key&tt.mask) * tt.buckets
}

func (tt *TranspositionTable) stripeIndexForKey(key uint64) int {
	return int((key & tt.mask) & tt.stripeMask)
}

func (tt *TranspositionTable) Probe(key uint64) (TTEntry, bool) {
	stripe := tt.stripeIndexForKey(key)
	tt.stripeLocks[stripe].Lock()
	defer tt.stripeLocks[stripe].Unlock()
	gen := tt.currentGeneration()
	start := tt.bucketIndex(key)
	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		entry := tt.entries[idx]
		if !entry.Valid || entry.Key != key {
			continue
		}
		entry.GenLastUsed = gen
		tt.entries[idx] = entry
		return entry, true
	}
	return TTEntry{}, false
}

func (tt *TranspositionTable) Store(key uint64, depth int, value float64, flag TTFlag, best Move) (replaced bool, overwrote bool) {
	stripe := tt.stripeIndexForKey(key)
	tt.stripeLocks[stripe].Lock()
	defer tt.stripeLocks[stripe].Unlock()
	gen := tt.currentGeneration()
	score := scoreToTT(value)
	start := tt.bucketIndex(key)

	// Exact key hit: only replace under strict policy.
	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		entry := tt.entries[idx]
		if !entry.Valid || entry.Key != key {
			continue
		}
		if !shouldReplaceByRules(entry, depth, flag, gen) {
			return false, false
		}
		tt.entries[idx] = TTEntry{
			Key:         key,
			Depth:       depth,
			Score:       score,
			Flag:        flag,
			BestMove:    best,
			GenWritten:  gen,
			GenLastUsed: gen,
			Valid:       true,
		}
		return false, true
	}

	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		if tt.entries[idx].Valid {
			continue
		}
		tt.entries[idx] = TTEntry{
			Key:         key,
			Depth:       depth,
			Score:       score,
			Flag:        flag,
			BestMove:    best,
			GenWritten:  gen,
			GenLastUsed: gen,
			Valid:       true,
		}
		return false, false
	}

	victim := -1
	victimClass := 0
	victimAge := uint32(0)
	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		entry := tt.entries[idx]
		class := replacementClass(entry, depth, flag, gen)
		if class == 0 {
			continue
		}
		age := entryAge(gen, entry)
		if victim == -1 || class < victimClass || (class == victimClass && age > victimAge) {
			victim = idx
			victimClass = class
			victimAge = age
		}
	}
	if victim == -1 {
		return false, false
	}

	tt.entries[victim] = TTEntry{
		Key:         key,
		Depth:       depth,
		Score:       score,
		Flag:        flag,
		BestMove:    best,
		GenWritten:  gen,
		GenLastUsed: gen,
		Valid:       true,
	}
	return true, false
}

func (tt *TranspositionTable) Count() int {
	tt.lockAllStripesRead()
	defer tt.unlockAllStripesRead()
	count := 0
	for i := range tt.entries {
		if tt.entries[i].Valid {
			count++
		}
	}
	return count
}

func (tt *TranspositionTable) currentGeneration() uint32 {
	gen := tt.gen.Load()
	if gen != 0 {
		return gen
	}
	if tt.gen.CompareAndSwap(0, 1) {
		return 1
	}
	gen = tt.gen.Load()
	if gen == 0 {
		return 1
	}
	return gen
}

func (tt *TranspositionTable) lockAllStripes() {
	for i := range tt.stripeLocks {
		tt.stripeLocks[i].Lock()
	}
}

func (tt *TranspositionTable) unlockAllStripes() {
	for i := len(tt.stripeLocks) - 1; i >= 0; i-- {
		tt.stripeLocks[i].Unlock()
	}
}

func (tt *TranspositionTable) lockAllStripesRead() {
	for i := range tt.stripeLocks {
		tt.stripeLocks[i].RLock()
	}
}

func (tt *TranspositionTable) unlockAllStripesRead() {
	for i := len(tt.stripeLocks) - 1; i >= 0; i-- {
		tt.stripeLocks[i].RUnlock()
	}
}

func replacementClass(entry TTEntry, depth int, flag TTFlag, gen uint32) int {
	if depth > entry.Depth {
		return 1
	}
	if depth == entry.Depth && flag == TTExact && entry.Flag != TTExact {
		return 2
	}
	if depth == entry.Depth && flag == entry.Flag && entryAge(gen, entry) >= ttVeryOldGenerations {
		return 3
	}
	return 0
}

func shouldReplaceByRules(entry TTEntry, depth int, flag TTFlag, gen uint32) bool {
	return replacementClass(entry, depth, flag, gen) != 0
}

func entryAge(gen uint32, entry TTEntry) uint32 {
	last := entry.GenLastUsed
	if last == 0 {
		last = entry.GenWritten
	}
	return gen - last
}

func scoreToTT(value float64) int32 {
	rounded := math.Round(value)
	if rounded > math.MaxInt32 {
		return math.MaxInt32
	}
	if rounded < math.MinInt32 {
		return math.MinInt32
	}
	return int32(rounded)
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
