package main

import (
	"encoding/gob"
	"log"
	"os"
	"path/filepath"
)

var dockerCacheDir = "/cache_logs"

type ttPersistenceSnapshot struct {
	Size    int
	Buckets int
	Entries []TTEntry

	RootTransposeSize    int
	RootTransposeBuckets int
	RootTransposeEntries []RootTransposeEntry
}

func countValidTTEntries(entries []TTEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Valid {
			count++
		}
	}
	return count
}

func countValidRootTransposeEntries(entries []RootTransposeEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Valid {
			count++
		}
	}
	return count
}

func loadTTPersistence(cfg Config, cache *AISearchCache) {
	if cache == nil || !cfg.AiEnableTtPersistence || cfg.AiTtPersistencePath == "" {
		log.Printf("[ai:cache] restored TT persistence: 0 entries (disabled or no path)")
		return
	}
	path := resolveTTPersistencePath(cfg.AiTtPersistencePath)
	file, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[ai:cache] failed to open TT persistence %s: %v", path, err)
			log.Printf("[ai:cache] restored TT persistence: 0 entries")
			return
		}
		log.Printf("[ai:cache] restored TT persistence: 0 entries (file not found: %s)", path)
		return
	}
	defer file.Close()

	var snapshot ttPersistenceSnapshot
	if err := gob.NewDecoder(file).Decode(&snapshot); err != nil {
		log.Printf("[ai:cache] failed to decode TT persistence %s: %v", path, err)
		log.Printf("[ai:cache] restored TT persistence: 0 entries")
		return
	}
	buckets := cfg.AiTtBuckets
	if !cfg.AiTtUseSetAssoc {
		buckets = 1
	}
	ttLoaded := false
	if snapshot.Size != cfg.AiTtSize || snapshot.Buckets != buckets {
		log.Printf("[ai:cache] TT persistence (%d/%d) does not match current TT config (%d/%d); skipping",
			snapshot.Size, snapshot.Buckets, cfg.AiTtSize, buckets)
	} else {
		tt := NewTranspositionTable(uint64(snapshot.Size), snapshot.Buckets)
		tt.loadEntries(snapshot.Entries)
		cache.mu.Lock()
		cache.TT = tt
		cache.TTSize = snapshot.Size
		cache.TTBuckets = snapshot.Buckets
		cache.mu.Unlock()
		validEntries := countValidTTEntries(snapshot.Entries)
		log.Printf("[ai:cache] restored TT persistence from %s (%d/%d valid entries)", path, validEntries, len(snapshot.Entries))
		ttLoaded = true
	}
	if !ttLoaded {
		log.Printf("[ai:cache] restored TT persistence: 0 entries")
	}

	if !cfg.AiEnableRootTranspose {
		log.Printf("[ai:cache] restored root-transpose persistence: 0 entries (disabled)")
		return
	}
	rootBuckets := 2
	if snapshot.RootTransposeSize <= 0 || len(snapshot.RootTransposeEntries) == 0 {
		log.Printf("[ai:cache] restored root-transpose persistence: 0 entries (not found in snapshot)")
		return
	}
	if snapshot.RootTransposeSize != cfg.AiRootTransposeSize || snapshot.RootTransposeBuckets != rootBuckets {
		log.Printf("[ai:cache] root-transpose persistence (%d/%d) does not match current root-transpose config (%d/%d); skipping",
			snapshot.RootTransposeSize, snapshot.RootTransposeBuckets, cfg.AiRootTransposeSize, rootBuckets)
		log.Printf("[ai:cache] restored root-transpose persistence: 0 entries")
		return
	}
	rootTranspose := NewRootTransposeCache(uint64(snapshot.RootTransposeSize), snapshot.RootTransposeBuckets)
	rootTranspose.loadEntries(snapshot.RootTransposeEntries)
	cache.mu.Lock()
	cache.RootTranspose = rootTranspose
	cache.RootTransposeSize = snapshot.RootTransposeSize
	cache.RootTransposeBucks = snapshot.RootTransposeBuckets
	cache.mu.Unlock()
	validRootEntries := countValidRootTransposeEntries(snapshot.RootTransposeEntries)
	log.Printf("[ai:cache] restored root-transpose persistence from %s (%d/%d valid entries)", path, validRootEntries, len(snapshot.RootTransposeEntries))
}

func persistTTPersistence(cfg Config, cache *AISearchCache) {
	if cache == nil || !cfg.AiEnableTtPersistence || cfg.AiTtPersistencePath == "" {
		log.Printf("[ai:cache] stored TT persistence: 0 entries (disabled or no path)")
		return
	}
	cache.mu.Lock()
	tt := cache.TT
	size := cache.TTSize
	buckets := cache.TTBuckets
	rootTranspose := cache.RootTranspose
	rootTransposeSize := cache.RootTransposeSize
	rootTransposeBuckets := cache.RootTransposeBucks
	cache.mu.Unlock()
	if tt == nil || size == 0 || buckets == 0 {
		log.Printf("[ai:cache] stored TT persistence: 0 entries (TT not initialized)")
	} else {
		entries := tt.snapshotEntries()
		validEntries := countValidTTEntries(entries)
		path := resolveTTPersistencePath(cfg.AiTtPersistencePath)
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				log.Printf("[ai:cache] unable to create TT persistence directory %s: %v", dir, err)
				return
			}
		}
		rootEntries := []RootTransposeEntry(nil)
		validRootEntries := 0
		if cfg.AiEnableRootTranspose && rootTranspose != nil && rootTransposeSize > 0 && rootTransposeBuckets > 0 {
			rootEntries = rootTranspose.snapshotEntries()
			validRootEntries = countValidRootTransposeEntries(rootEntries)
		}
		file, err := os.Create(path)
		if err != nil {
			log.Printf("[ai:cache] failed to create TT persistence %s: %v", path, err)
			return
		}
		defer file.Close()
		snapshot := ttPersistenceSnapshot{
			Size:    size,
			Buckets: buckets,
			Entries: entries,

			RootTransposeSize:    rootTransposeSize,
			RootTransposeBuckets: rootTransposeBuckets,
			RootTransposeEntries: rootEntries,
		}
		if err := gob.NewEncoder(file).Encode(&snapshot); err != nil {
			log.Printf("[ai:cache] failed to encode TT persistence %s: %v", path, err)
			return
		}
		log.Printf("[ai:cache] stored TT persistence to %s (%d/%d valid entries)", path, validEntries, len(entries))
		log.Printf("[ai:cache] stored root-transpose persistence to %s (%d/%d valid entries)", path, validRootEntries, len(rootEntries))
		return
	}
	log.Printf("[ai:cache] stored root-transpose persistence: 0 entries (TT not initialized)")
}

func resolveTTPersistencePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if stat, err := os.Stat(dockerCacheDir); err == nil && stat.IsDir() {
		return filepath.Join(dockerCacheDir, path)
	}
	return path
}
