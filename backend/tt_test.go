package main

import (
	"sync"
	"testing"
)

func TestTTConcurrentProbeStore(t *testing.T) {
	tt := NewTranspositionTable(1<<12, 2)
	heuristicHash := heuristicHashFromConfig(DefaultConfig())
	var wg sync.WaitGroup

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			for i := 0; i < 4000; i++ {
				key := mixKey(seed ^ uint64(i))
				depth := (i % 8) + 1
				move := Move{X: i % 19, Y: (i / 19) % 19}
				tt.Store(key, heuristicHash, depth, float64(i), TTExact, move, TTMeta{})
				tt.Probe(key, heuristicHash)
				tt.Probe(key^0x9e3779b97f4a7c15, heuristicHash)
			}
		}(uint64(g + 1))
	}

	wg.Wait()
	if tt.Count() == 0 {
		t.Fatalf("expected TT to contain entries after concurrent traffic")
	}
}

func TestTTGenerationWrapStaysNonZero(t *testing.T) {
	tt := NewTranspositionTable(16, 1)
	tt.gen.Store(^uint32(0))
	tt.NextGeneration()
	if got := tt.Generation(); got == 0 {
		t.Fatalf("generation must never be zero")
	}
}

func TestTTScopesEntriesByHeuristicHash(t *testing.T) {
	tt := NewTranspositionTable(64, 4)
	key := uint64(0x1234)
	hashA := uint64(0xaaa)
	hashB := uint64(0xbbb)

	tt.Store(key, hashA, 4, 100, TTExact, Move{X: 1, Y: 1}, TTMeta{})
	tt.Store(key, hashB, 4, 200, TTExact, Move{X: 2, Y: 2}, TTMeta{})

	entryA, okA := tt.Probe(key, hashA)
	if !okA || entryA.BestMove.X != 1 || entryA.BestMove.Y != 1 {
		t.Fatalf("expected heuristic A entry, got ok=%v entry=%+v", okA, entryA)
	}
	entryB, okB := tt.Probe(key, hashB)
	if !okB || entryB.BestMove.X != 2 || entryB.BestMove.Y != 2 {
		t.Fatalf("expected heuristic B entry, got ok=%v entry=%+v", okB, entryB)
	}

	deleted := tt.DeleteByHeuristicHash(hashA)
	if deleted <= 0 {
		t.Fatalf("expected entries for heuristic A to be pruned")
	}
	if _, ok := tt.Probe(key, hashA); ok {
		t.Fatalf("expected heuristic A entry to be gone after prune")
	}
	if _, ok := tt.Probe(key, hashB); !ok {
		t.Fatalf("expected heuristic B entry to remain after pruning A")
	}
}
