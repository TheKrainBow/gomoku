package main

import "sync"

type Config struct {
	GhostMode       bool  `json:"ghost_mode"`
	LogDepthScores  bool  `json:"log_depth_scores"`
	AiDepth         int   `json:"ai_depth"`
	AiTimeoutMs     int   `json:"ai_timeout_ms"`
	AiTopCandidates int   `json:"ai_top_candidates"`
	AiQuickWinExit  bool  `json:"ai_quick_win_exit"`
	AiMoveDelayMs   int   `json:"ai_move_delay_ms"`
	AiTtMaxEntries  int64 `json:"ai_tt_max_entries"`
}

type ConfigStore struct {
	mu     sync.RWMutex
	config Config
}

func DefaultConfig() Config {
	return Config{
		GhostMode:       false,
		LogDepthScores:  false,
		AiDepth:         5,
		AiTimeoutMs:     0,
		AiTopCandidates: 6,
		AiQuickWinExit:  true,
		AiMoveDelayMs:   0,
		AiTtMaxEntries:  200000,
	}
}

var configStore = &ConfigStore{config: DefaultConfig()}

func GetConfig() Config {
	return configStore.Get()
}

func (c *ConfigStore) Get() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}

func (c *ConfigStore) Update(newConfig Config) {
	c.mu.Lock()
	c.config = newConfig
	c.mu.Unlock()
}
