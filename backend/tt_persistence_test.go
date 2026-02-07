package main

import (
	"path/filepath"
	"testing"
)

func TestResolveTTPersistencePathKeepsAbsolutePath(t *testing.T) {
	absolute := "/tmp/tt_cache.gob"
	got := resolveTTPersistencePath(absolute)
	if got != absolute {
		t.Fatalf("expected absolute path unchanged, got %q", got)
	}
}

func TestResolveTTPersistencePathUsesDockerCacheDirWhenPresent(t *testing.T) {
	temp := t.TempDir()
	old := dockerCacheDir
	dockerCacheDir = temp
	t.Cleanup(func() { dockerCacheDir = old })

	got := resolveTTPersistencePath("tt_cache.gob")
	want := filepath.Join(temp, "tt_cache.gob")
	if got != want {
		t.Fatalf("expected docker cache path %q, got %q", want, got)
	}
}

func TestResolveTTPersistencePathFallsBackToRelativeWhenDockerCacheDirMissing(t *testing.T) {
	old := dockerCacheDir
	dockerCacheDir = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { dockerCacheDir = old })

	got := resolveTTPersistencePath("tt_cache.gob")
	if got != "tt_cache.gob" {
		t.Fatalf("expected relative path fallback, got %q", got)
	}
}

func TestRootTransposePersistenceRoundTrip(t *testing.T) {
	temp := t.TempDir()
	old := dockerCacheDir
	dockerCacheDir = temp
	t.Cleanup(func() { dockerCacheDir = old })

	cfg := DefaultConfig()
	cfg.AiEnableTtPersistence = true
	cfg.AiTtPersistencePath = "tt_cache.gob"
	cfg.AiTtUseSetAssoc = true
	cfg.AiTtBuckets = 2
	cfg.AiTtSize = 16
	cfg.AiEnableRootTranspose = true
	cfg.AiRootTransposeSize = 16

	cache := newAISearchCache()
	tt := ensureTT(&cache, cfg)
	if tt == nil {
		t.Fatalf("expected TT")
	}
	ttKey := uint64(0x12345)
	tt.Store(ttKey, 7, 42, TTExact, Move{X: 3, Y: 3}, TTMeta{})

	rt := ensureRootTransposeCache(&cache, cfg)
	if rt == nil {
		t.Fatalf("expected root transpose cache")
	}
	rtKey := uint64(0x998877)
	rt.Put(rtKey, 9, 123, TTExact, Move{X: 2, Y: 2}, TTMeta{
		GrowLeft:   1,
		GrowRight:  1,
		GrowTop:    1,
		GrowBottom: 1,
		FrameW:     5,
		FrameH:     5,
	})

	persistTTPersistence(cfg, &cache)

	loaded := newAISearchCache()
	loadTTPersistence(cfg, &loaded)

	loadedTT := ensureTT(&loaded, cfg)
	if loadedTT == nil {
		t.Fatalf("expected loaded TT")
	}
	ttEntry, ok := loadedTT.Probe(ttKey)
	if !ok || !ttEntry.Valid {
		t.Fatalf("expected TT entry to be restored")
	}
	if ttEntry.Depth != 7 || ttEntry.Flag != TTExact {
		t.Fatalf("unexpected restored TT entry: %+v", ttEntry)
	}

	loadedRT := ensureRootTransposeCache(&loaded, cfg)
	if loadedRT == nil {
		t.Fatalf("expected loaded root transpose cache")
	}
	rtEntry, ok := loadedRT.Get(rtKey, 9)
	if !ok || !rtEntry.Valid {
		t.Fatalf("expected root transpose entry to be restored")
	}
	if rtEntry.Depth != 9 || rtEntry.Flag != TTExact {
		t.Fatalf("unexpected restored root transpose entry: %+v", rtEntry)
	}
}
