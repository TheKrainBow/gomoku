package main

import "sync"

type Config struct {
	GhostMode             bool            `json:"ghost_mode"`
	LogDepthScores        bool            `json:"log_depth_scores"`
	AiDepth               int             `json:"ai_depth"`
	AiTimeoutMs           int             `json:"ai_timeout_ms"`
	AiTimeBudgetMs        int             `json:"ai_time_budget_ms"`
	AiBacklogEstimateMs   int             `json:"ai_backlog_estimate_ms"`
	AiMaxDepth            int             `json:"ai_max_depth"`
	AiMinDepth            int             `json:"ai_min_depth"`
	AiReturnLastComplete  bool            `json:"ai_return_last_complete_depth_only"`
	AiTopCandidates       int             `json:"ai_top_candidates"`
	AiEnableDynamicTopK   bool            `json:"ai_enable_dynamic_top_k"`
	AiMaxCandidatesRoot   int             `json:"ai_max_candidates_root"`
	AiMaxCandidatesMid    int             `json:"ai_max_candidates_mid"`
	AiMaxCandidatesDeep   int             `json:"ai_max_candidates_deep"`
	AiMaxCandidatesPly7   int             `json:"ai_max_candidates_ply7"`
	AiMaxCandidatesPly9   int             `json:"ai_max_candidates_ply9"`
	AiEnableTacticalK     bool            `json:"ai_enable_tactical_k"`
	AiKQuietRoot          int             `json:"ai_k_quiet_root"`
	AiKQuietMid           int             `json:"ai_k_quiet_mid"`
	AiKQuietDeep          int             `json:"ai_k_quiet_deep"`
	AiKTactRoot           int             `json:"ai_k_tact_root"`
	AiKTactMid            int             `json:"ai_k_tact_mid"`
	AiKTactDeep           int             `json:"ai_k_tact_deep"`
	AiQuickWinExit        bool            `json:"ai_quick_win_exit"`
	AiEnableAspiration    bool            `json:"ai_enable_aspiration"`
	AiAspWindow           float64         `json:"ai_asp_window"`
	AiAspWindowMax        float64         `json:"ai_asp_window_max"`
	AiTtMaxEntries        int64           `json:"ai_tt_max_entries"`
	AiPonderingEnabled    bool            `json:"ai_pondering_enabled"`
	AiGhostThrottleMs     int             `json:"ai_ghost_throttle_ms"`
	AiTtSize              int             `json:"ai_tt_size"`
	AiTtBuckets           int             `json:"ai_tt_buckets"`
	AiTtUseSetAssoc       bool            `json:"ai_tt_use_set_assoc"`
	AiLogSearchStats      bool            `json:"ai_log_search_stats"`
	AiMinmaxCacheLimit    int             `json:"ai_minmax_cache_limit"`
	AiEnableKillerMoves   bool            `json:"ai_enable_killer_moves"`
	AiEnableHistoryMoves  bool            `json:"ai_enable_history_moves"`
	AiKillerBoost         int             `json:"ai_killer_boost"`
	AiHistoryBoost        int             `json:"ai_history_boost"`
	AiUseScanWinIn1       bool            `json:"ai_use_scan_win_in_1"`
	AiEnableTacticalMode  bool            `json:"ai_enable_tactical_mode"`
	AiEnableTacticalExt   bool            `json:"ai_enable_tactical_extension"`
	AiTacticalExtDepth    int             `json:"ai_tactical_extension_depth"`
	AiEnableEvalCache     bool            `json:"ai_enable_eval_cache"`
	AiEvalCacheSize       int             `json:"ai_eval_cache_size"`
	AiEvalCacheMinAbs     float64         `json:"ai_eval_cache_min_abs"`
	AiEnableLostMode      bool            `json:"ai_enable_lost_mode"`
	AiLostModeThreshold   float64         `json:"ai_lost_mode_threshold"`
	AiLostModeMaxMoves    int             `json:"ai_lost_mode_max_moves"`
	AiLostModeReplyLimit  int             `json:"ai_lost_mode_reply_limit"`
	AiLostModeMinDepth    int             `json:"ai_lost_mode_min_depth"`
	AiQueueWorkers        int             `json:"ai_queue_workers"`
	AiQueueAnalyzeThreads int             `json:"ai_queue_analyze_threads"`
	AiQueueEnabled        bool            `json:"ai_enable_queue"`
	Heuristics            HeuristicConfig `json:"heuristics"`
}

type HeuristicConfig struct {
	Open4        float64 `json:"open_4"`
	Closed4      float64 `json:"closed_4"`
	Broken4      float64 `json:"broken_4"`
	Open3        float64 `json:"open_3"`
	Broken3      float64 `json:"broken_3"`
	Closed3      float64 `json:"closed_3"`
	Open2        float64 `json:"open_2"`
	Broken2      float64 `json:"broken_2"`
	ForkOpen3    float64 `json:"fork_open_3"`
	ForkFourPlus float64 `json:"fork_four_plus"`
}

type ConfigStore struct {
	mu     sync.RWMutex
	config Config
}

func DefaultConfig() Config {
	return Config{
		GhostMode:      false,
		LogDepthScores: false,

		// Time budget mode
		AiTimeBudgetMs:       500,
		AiBacklogEstimateMs:  120000,
		AiTimeoutMs:          0,
		AiDepth:              10,
		AiMinDepth:           1,
		AiMaxDepth:           10, // allow deeper only if tree collapses
		AiReturnLastComplete: true,

		// IMPORTANT: branching control (primary speed lever)
		AiEnableDynamicTopK: true,
		AiEnableTacticalK:   true,

		// Hard caps (safety net)
		AiMaxCandidatesRoot: 24, // was 40
		AiMaxCandidatesMid:  14, // was 25
		AiMaxCandidatesDeep: 8,  // was 14
		AiMaxCandidatesPly7: 14,
		AiMaxCandidatesPly9: 9,

		// Quiet positions (most common)
		AiKQuietRoot: 18, // was 24
		AiKQuietMid:  10, // was 14
		AiKQuietDeep: 6,  // was 8

		// Tactical positions (forced lines) — keep slightly larger
		AiKTactRoot: 24, // was 30
		AiKTactMid:  12, // was 18
		AiKTactDeep: 8,  // was 10

		// If you still have legacy “AiTopCandidates”, keep it aligned or unused
		AiTopCandidates: 0, // disable legacy top-candidates if still read anywhere

		// Tactical mode: ON (but should restrict to forcing moves)
		AiEnableTacticalMode: true,

		// Tactical extension: OFF by default for 500ms stability
		// Turn it ON only if your tacticalCandidates set is very small (<= 6)
		AiEnableTacticalExt: false,
		AiTacticalExtDepth:  0,

		// Must-block / win-in-1 correctness + speed
		AiUseScanWinIn1: true,
		AiQuickWinExit:  true,

		// Aspiration windows: OFF until everything is stable (it can cause re-searches)
		AiEnableAspiration: false,
		AiAspWindow:        2000.0,
		AiAspWindowMax:     2000000000.0,

		// Caches
		AiEnableEvalCache:     true,
		AiEvalCacheSize:       1 << 19, // 524288 entries (bigger than 1<<18)
		AiEvalCacheMinAbs:     300.0,
		AiEnableLostMode:      true,
		AiLostModeThreshold:   winScore / 2,
		AiLostModeMaxMoves:    6,
		AiLostModeReplyLimit:  12,
		AiLostModeMinDepth:    2,
		AiQueueWorkers:        1,
		AiQueueAnalyzeThreads: 0,
		AiQueueEnabled:        true,

		// TT: increase size for better hit rate under iterative deepening
		AiTtUseSetAssoc: true,
		AiTtBuckets:     4,       // was 2
		AiTtSize:        1 << 18, // 262144 (was 1<<17)
		AiTtMaxEntries:  0,       // avoid conflicting limiter if both are used

		// Move ordering helpers
		AiEnableKillerMoves:  true,
		AiEnableHistoryMoves: true,

		// Killer/history boosts should be modest; huge boosts can wreck ordering
		AiKillerBoost:  8000, // was 50000
		AiHistoryBoost: 16,   // was 4

		// Background pondering can steal CPU and hurt 500ms latency
		AiPonderingEnabled: false,

		AiGhostThrottleMs:  50,
		AiLogSearchStats:   false, // turn ON temporarily to tune; disable later
		AiMinmaxCacheLimit: 1000,

		Heuristics: HeuristicConfig{
			Open4:        100000.0,
			Closed4:      15000.0,
			Broken4:      12000.0,
			Open3:        2500.0,
			Broken3:      1200.0,
			Closed3:      400.0,
			Open2:        200.0,
			Broken2:      120.0,
			ForkOpen3:    6000.0,
			ForkFourPlus: 20000.0,
		},
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
