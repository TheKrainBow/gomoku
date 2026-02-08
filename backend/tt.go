package main

import (
	"math"
	"sort"
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
	Key           uint64
	HeuristicHash uint64
	Depth         int
	Score         int32
	Flag          TTFlag
	BestMove      Move
	Hits          uint32
	GenWritten    uint32
	GenLastUsed   uint32
	Valid         bool
	GrowLeft      uint8
	GrowRight     uint8
	GrowTop       uint8
	GrowBottom    uint8
	HitLeft       bool
	HitRight      bool
	HitTop        bool
	HitBottom     bool
	FrameW        uint8
	FrameH        uint8
}

type TTMeta struct {
	GrowLeft   int
	GrowRight  int
	GrowTop    int
	GrowBottom int
	FrameW     int
	FrameH     int
	HitLeft    bool
	HitRight   bool
	HitTop     bool
	HitBottom  bool
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

func (tt *TranspositionTable) Probe(key uint64, heuristicHash uint64) (TTEntry, bool) {
	stripe := tt.stripeIndexForKey(key)
	tt.stripeLocks[stripe].Lock()
	defer tt.stripeLocks[stripe].Unlock()
	gen := tt.currentGeneration()
	start := tt.bucketIndex(key)
	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		entry := tt.entries[idx]
		if !entry.Valid || entry.Key != key || entry.HeuristicHash != heuristicHash {
			continue
		}
		entry.Hits++
		entry.GenLastUsed = gen
		tt.entries[idx] = entry
		return entry, true
	}
	return TTEntry{}, false
}

func (tt *TranspositionTable) Store(key uint64, heuristicHash uint64, depth int, value float64, flag TTFlag, best Move, meta TTMeta) (replaced bool, overwrote bool) {
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
		if !entry.Valid || entry.Key != key || entry.HeuristicHash != heuristicHash {
			continue
		}
		if !shouldReplaceByRules(entry, depth, flag, gen) {
			return false, false
		}
		tt.entries[idx] = TTEntry{
			Key:           key,
			HeuristicHash: heuristicHash,
			Depth:         depth,
			Score:         score,
			Flag:          flag,
			BestMove:      best,
			Hits:          0,
			GrowLeft:      clampToUint8(meta.GrowLeft),
			GrowRight:     clampToUint8(meta.GrowRight),
			GrowTop:       clampToUint8(meta.GrowTop),
			GrowBottom:    clampToUint8(meta.GrowBottom),
			HitLeft:       meta.HitLeft,
			HitRight:      meta.HitRight,
			HitTop:        meta.HitTop,
			HitBottom:     meta.HitBottom,
			FrameW:        clampToUint8(meta.FrameW),
			FrameH:        clampToUint8(meta.FrameH),
			GenWritten:    gen,
			GenLastUsed:   gen,
			Valid:         true,
		}
		return false, true
	}

	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		if tt.entries[idx].Valid {
			continue
		}
		tt.entries[idx] = TTEntry{
			Key:           key,
			HeuristicHash: heuristicHash,
			Depth:         depth,
			Score:         score,
			Flag:          flag,
			BestMove:      best,
			Hits:          0,
			GrowLeft:      clampToUint8(meta.GrowLeft),
			GrowRight:     clampToUint8(meta.GrowRight),
			GrowTop:       clampToUint8(meta.GrowTop),
			GrowBottom:    clampToUint8(meta.GrowBottom),
			HitLeft:       meta.HitLeft,
			HitRight:      meta.HitRight,
			HitTop:        meta.HitTop,
			HitBottom:     meta.HitBottom,
			FrameW:        clampToUint8(meta.FrameW),
			FrameH:        clampToUint8(meta.FrameH),
			GenWritten:    gen,
			GenLastUsed:   gen,
			Valid:         true,
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
		Key:           key,
		HeuristicHash: heuristicHash,
		Depth:         depth,
		Score:         score,
		Flag:          flag,
		BestMove:      best,
		Hits:          0,
		GenWritten:    gen,
		GenLastUsed:   gen,
		Valid:         true,
	}
	return true, false
}

func (tt *TranspositionTable) DeleteByHeuristicHash(heuristicHash uint64) int {
	tt.lockAllStripes()
	defer tt.unlockAllStripes()
	deleted := 0
	for i := range tt.entries {
		entry := tt.entries[i]
		if !entry.Valid || entry.HeuristicHash != heuristicHash {
			continue
		}
		tt.entries[i] = TTEntry{}
		deleted++
	}
	return deleted
}

func (tt *TranspositionTable) DeleteByKey(key uint64) bool {
	stripe := tt.stripeIndexForKey(key)
	tt.stripeLocks[stripe].Lock()
	defer tt.stripeLocks[stripe].Unlock()
	start := tt.bucketIndex(key)
	deleted := false
	for i := 0; i < tt.buckets; i++ {
		idx := start + i
		entry := tt.entries[idx]
		if !entry.Valid || entry.Key != key {
			continue
		}
		tt.entries[idx] = TTEntry{}
		deleted = true
	}
	return deleted
}

func (tt *TranspositionTable) TopEntriesByHits(offset int, limit int) ([]TTEntry, int) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	entries := tt.snapshotEntries()
	valid := make([]TTEntry, 0, len(entries))
	for i := range entries {
		if entries[i].Valid {
			valid = append(valid, entries[i])
		}
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Hits != valid[j].Hits {
			return valid[i].Hits > valid[j].Hits
		}
		if valid[i].Depth != valid[j].Depth {
			return valid[i].Depth > valid[j].Depth
		}
		if valid[i].GenLastUsed != valid[j].GenLastUsed {
			return valid[i].GenLastUsed > valid[j].GenLastUsed
		}
		return valid[i].Key < valid[j].Key
	})
	total := len(valid)
	if offset >= total {
		return []TTEntry{}, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return valid[offset:end], total
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

func (tt *TranspositionTable) Capacity() int {
	if tt == nil {
		return 0
	}
	return len(tt.entries)
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

func (tt *TranspositionTable) snapshotEntries() []TTEntry {
	tt.lockAllStripes()
	defer tt.unlockAllStripes()
	entries := make([]TTEntry, len(tt.entries))
	copy(entries, tt.entries)
	return entries
}

func (tt *TranspositionTable) loadEntries(entries []TTEntry) {
	tt.lockAllStripes()
	defer tt.unlockAllStripes()
	if len(entries) > len(tt.entries) {
		entries = entries[:len(tt.entries)]
	}
	copy(tt.entries[:len(entries)], entries)
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
