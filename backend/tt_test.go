package main

import (
	"sync"
	"testing"
)

func TestTTConcurrentProbeStore(t *testing.T) {
	tt := NewTranspositionTable(1<<12, 2)
	var wg sync.WaitGroup

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			for i := 0; i < 4000; i++ {
				key := mixKey(seed ^ uint64(i))
				depth := (i % 8) + 1
				move := Move{X: i % 19, Y: (i / 19) % 19}
				tt.Store(key, depth, float64(i), TTExact, move)
				tt.Probe(key)
				tt.Probe(key ^ 0x9e3779b97f4a7c15)
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
