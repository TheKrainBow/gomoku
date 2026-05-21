package threatlut

import "testing"

func TestEncodeDecodeCanonicalWindow(t *testing.T) {
	window := []byte("M..MM..")
	key := EncodeCanonicalWindow(window)
	decoded := DecodeCanonicalWindow(key)
	if string(decoded[:]) != string(window) {
		t.Fatalf("roundtrip mismatch: got %q want %q", string(decoded[:]), string(window))
	}
}

func TestKnownWindowsHaveExpectedClassificationAndTransitions(t *testing.T) {
	tests := []struct {
		window   string
		baseType PatternType
	}{
		{window: "...MM..", baseType: PatternOpen2},
		{window: "M..MM..", baseType: PatternBroken3},
		{window: "..MMM..", baseType: PatternOpen3},
		{window: ".MM.M..", baseType: PatternBroken3},
		{window: "OMMM...", baseType: PatternClosed3},
		{window: ".MMMO..", baseType: PatternClosed3},
		{window: ".MMO...", baseType: PatternClosed2},
		{window: ".MMM.M.", baseType: PatternBroken4},
		{window: ".MM.MM.", baseType: PatternBroken4},
		{window: ".M.MMM.", baseType: PatternBroken4},
	}
	for _, tc := range tests {
		entry, ok := LookupThreatWindow(EncodeCanonicalWindow([]byte(tc.window)))
		if !ok {
			t.Fatalf("%q not found in LUT", tc.window)
		}
		if entry.BaseType != tc.baseType {
			t.Fatalf("%q base type=%v want %v", tc.window, entry.BaseType, tc.baseType)
		}
		if entry.TransitionCount == 0 {
			t.Fatalf("%q expected transitions", tc.window)
		}
	}
}

func TestOpenTwoTransitionCreatesOpenThreeAndOpponentDegrades(t *testing.T) {
	entry, ok := LookupThreatWindow(EncodeCanonicalWindow([]byte("...MM..")))
	if !ok {
		t.Fatalf("missing open-two window")
	}
	transitions := TransitionsForEntry(entry)
	found := false
	for _, tr := range transitions {
		if tr.RelPos != 2 {
			continue
		}
		found = true
		if tr.ForSelf.NewType < PatternOpen3 {
			t.Fatalf("expected self move to create at least open3, got %v", tr.ForSelf.NewType)
		}
		if tr.ForOpp.DeltaScore >= 0 {
			t.Fatalf("expected opponent move to degrade or kill, got delta=%d", tr.ForOpp.DeltaScore)
		}
	}
	if !found {
		t.Fatalf("expected transition on relPos 2")
	}
}

func TestClosedFourCreatingTransitionsAreMustBlock(t *testing.T) {
	for key := uint32(0); key < MaxWindowKey; key++ {
		entry, ok := LookupThreatWindow(key)
		if !ok {
			continue
		}
		for _, tr := range TransitionsForEntry(entry) {
			if tr.ForSelf.NewType < PatternClosed4 {
				continue
			}
			if tr.OppResponse.Flags&ResponseMustBlock == 0 {
				window := DecodeCanonicalWindow(key)
				t.Fatalf("window %q relPos=%d creates %v but opp response=%v", string(window[:]), tr.RelPos, tr.ForSelf.NewType, tr.OppResponse.Flags)
			}
		}
	}
}

func TestResponseTimingByPatternKind(t *testing.T) {
	entry, ok := LookupThreatWindow(EncodeCanonicalWindow([]byte("...MM..")))
	if !ok {
		t.Fatalf("missing open-two window")
	}
	foundOpen4 := false
	for _, tr := range TransitionsForEntry(entry) {
		if tr.ForSelf.NewType != PatternOpen3 {
			continue
		}
		if tr.SelfResponse.ForceTempo != 2 {
			t.Fatalf("open3 force tempo=%d want 2", tr.SelfResponse.ForceTempo)
		}
	}

	entry, ok = LookupThreatWindow(EncodeCanonicalWindow([]byte(".MMM...")))
	if !ok {
		t.Fatalf("missing open-three-like window")
	}
	for _, tr := range TransitionsForEntry(entry) {
		if tr.ForSelf.NewType != PatternOpen4 {
			continue
		}
		foundOpen4 = true
		if tr.SelfResponse.WinTempo != 1 {
			t.Fatalf("open4 win tempo=%d want 1", tr.SelfResponse.WinTempo)
		}
		if tr.SelfResponse.ForceTempo != 1 {
			t.Fatalf("open4 force tempo=%d want 1", tr.SelfResponse.ForceTempo)
		}
	}
	if !foundOpen4 {
		t.Fatalf("expected an open4 transition from open3 window")
	}
}
