package main

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

const (
	boardCachePersistPath = "/cache_logs/board_cache.gob"
	depthCachePersistPath = "/cache_logs/depth_cache.gob"
)

type depthCacheDump struct {
	Entries []depthCacheEntry
	Best    map[cacheSignature]int
}

type depthCacheEntry struct {
	Key    cacheKey
	Scores []float64
}

type boardCacheDump struct {
	Entries  map[uint64]float64
	Patterns map[patternKey]float64
}

var persistOnce sync.Once

func init() {
	loadPersistedCaches()
	startCachePersistenceHandler()
}

func loadPersistedCaches() {
	if !GetConfig().AiPersistCaches {
		return
	}
	if err := boardCache.loadFromFile(boardCachePersistPath); err != nil {
		fmt.Printf("[ai:cache] load board cache error: %v\n", err)
	}
	if err := loadDepthCache(depthCachePersistPath); err != nil {
		fmt.Printf("[ai:cache] load depth cache error: %v\n", err)
	}
}

func startCachePersistenceHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		persistCaches()
		os.Exit(0)
	}()
}

func persistCaches() {
	if !GetConfig().AiPersistCaches {
		return
	}
	persistOnce.Do(func() {
		if err := ensureCacheDir(boardCachePersistPath); err != nil {
			fmt.Printf("[ai:cache] ensure dir board cache: %v\n", err)
			return
		}
		if err := boardCache.saveToFile(boardCachePersistPath); err != nil {
			fmt.Printf("[ai:cache] persist board cache: %v\n", err)
		}
		if err := ensureCacheDir(depthCachePersistPath); err != nil {
			fmt.Printf("[ai:cache] ensure dir depth cache: %v\n", err)
			return
		}
		if err := saveDepthCache(depthCachePersistPath); err != nil {
			fmt.Printf("[ai:cache] persist depth cache: %v\n", err)
		}
	})
}

func ensureCacheDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o755)
}

func (c *boardScoreCache) saveToFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	c.mu.RLock()
	dump := boardCacheDump{
		Entries:  make(map[uint64]float64, len(c.entries)),
		Patterns: make(map[patternKey]float64, len(c.patternEntries)),
	}
	for h, score := range c.entries {
		dump.Entries[h] = score
	}
	for k, score := range c.patternEntries {
		dump.Patterns[k] = score
	}
	c.mu.RUnlock()
	enc := gob.NewEncoder(file)
	return enc.Encode(dump)
}

func (c *boardScoreCache) loadFromFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	var dump boardCacheDump
	if err := gob.NewDecoder(file).Decode(&dump); err != nil {
		if isEOFError(err) {
			file.Close()
			os.Remove(path)
			return nil
		}
		return err
	}
	c.mu.Lock()
	if dump.Entries != nil {
		c.entries = dump.Entries
	}
	if dump.Patterns != nil {
		c.patternEntries = dump.Patterns
	}
	c.mu.Unlock()
	return nil
}

func saveDepthCache(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	entries := make([]depthCacheEntry, 0, len(depthCache))
	for key, scores := range depthCache {
		if len(scores) == 0 {
			continue
		}
		scoresCopy := append([]float64(nil), scores...)
		entries = append(entries, depthCacheEntry{Key: key, Scores: scoresCopy})
	}
	dump := depthCacheDump{
		Entries: entries,
		Best:    make(map[cacheSignature]int, len(depthCacheBest)),
	}
	for sig, depth := range depthCacheBest {
		dump.Best[sig] = depth
	}
	enc := gob.NewEncoder(file)
	return enc.Encode(&dump)
}

func loadDepthCache(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	var dump depthCacheDump
	if err := gob.NewDecoder(file).Decode(&dump); err != nil {
		if isEOFError(err) {
			file.Close()
			os.Remove(path)
			return nil
		}
		return err
	}
	depthCache = make(map[cacheKey][]float64, len(dump.Entries))
	for _, entry := range dump.Entries {
		depthCache[entry.Key] = entry.Scores
	}
	depthCacheBest = make(map[cacheSignature]int, len(dump.Best))
	for sig, depth := range dump.Best {
		depthCacheBest[sig] = depth
	}
	return nil
}

func isEOFError(err error) bool {
	return err == io.EOF || err == io.ErrUnexpectedEOF
}
