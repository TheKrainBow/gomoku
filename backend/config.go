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
	AiEnableHardPlyCaps   bool            `json:"ai_enable_hard_ply_caps"`
	AiMaxCandidatesRoot   int             `json:"ai_max_candidates_root"`
	AiMaxCandidatesMid    int             `json:"ai_max_candidates_mid"`
	AiMaxCandidatesDeep   int             `json:"ai_max_candidates_deep"`
	AiMaxCandidatesPly7   int             `json:"ai_max_candidates_ply7"`
	AiMaxCandidatesPly8   int             `json:"ai_max_candidates_ply8"`
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
	AiUseTtCache          bool            `json:"ai_use_tt_cache"`
	AiTtMaxMemoryBytes    int64           `json:"ai_tt_max_memory_bytes"`
	AiEnableTtPersistence bool            `json:"ai_enable_tt_persistence"`
	AiTtPersistencePath   string          `json:"ai_tt_persistence_path"`
	AiEnableRootTranspose bool            `json:"ai_enable_root_transpose_tt"`
	AiRootTransposeSize   int             `json:"ai_root_transpose_tt_size"`
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
	AiAnaliticsTopBoards  int             `json:"ai_analitics_top_boards"`
	Heuristics            HeuristicConfig `json:"heuristics"`
}

type HeuristicConfig struct {
	Open4               float64 `json:"open_4"`
	Closed4             float64 `json:"closed_4"`
	Broken4             float64 `json:"broken_4"`
	Open3               float64 `json:"open_3"`
	Broken3             float64 `json:"broken_3"`
	Closed3             float64 `json:"closed_3"`
	Open2               float64 `json:"open_2"`
	Broken2             float64 `json:"broken_2"`
	ForkOpen3           float64 `json:"fork_open_3"`
	ForkFourPlus        float64 `json:"fork_four_plus"`
	CaptureNow          float64 `json:"capture_now"`
	CaptureDoubleThreat float64 `json:"capture_double_threat"`
	CaptureNearWin      float64 `json:"capture_near_win"`
	CaptureInTwo        float64 `json:"capture_in_two"`
	HangingPair         float64 `json:"hanging_pair"`
	CaptureWinSoonScale float64 `json:"capture_win_soon_scale"`
	CaptureInTwoLimit   int     `json:"capture_in_two_limit"`
}

func cloneHeuristicConfigPtr(src *HeuristicConfig) *HeuristicConfig {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
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
		AiMinDepth:           3,
		AiMaxDepth:           10,
		AiReturnLastComplete: true,

		// Branching control
		AiEnableDynamicTopK: true,
		AiEnableTacticalK:   true,
		AiEnableHardPlyCaps: true,

		// Hard caps (loosened vs your last one to avoid missing defenses)
		AiMaxCandidatesRoot: 24,
		AiMaxCandidatesMid:  24,
		AiMaxCandidatesDeep: 24,

		// Key change: give deep plies a bit more air
		AiMaxCandidatesPly7: 8,
		AiMaxCandidatesPly8: 7,
		AiMaxCandidatesPly9: 6,

		// Quiet positions (dynamic K)
		AiKQuietRoot: 16,
		AiKQuietMid:  12,
		AiKQuietDeep: 6,

		// Tactical positions (don’t over-cap tactics)
		AiKTactRoot: 24,
		AiKTactMid:  18,
		AiKTactDeep: 14,

		// Legacy
		AiTopCandidates: 0,

		// Tactical mode ON (assumed to restrict to forcing moves)
		AiEnableTacticalMode: true,

		// Tactical extension: keep OFF unless you can guarantee very small tactical set
		AiEnableTacticalExt: false,
		AiTacticalExtDepth:  0,

		// Win-in-1 and quick win
		AiUseScanWinIn1: true,
		AiQuickWinExit:  true,

		// Aspiration ON (small window -> fewer nodes, usually faster)
		// If it causes too many re-searches, increase window (not disable immediately).
		AiEnableAspiration: true,
		AiAspWindow:        1200.0,
		AiAspWindowMax:     2000000000.0,

		// Caches
		AiEnableEvalCache: true,
		AiEvalCacheSize:   1 << 19, // 524288
		AiEvalCacheMinAbs: 300.0,

		// Lost mode
		AiEnableLostMode:     true,
		AiLostModeThreshold:  winScore / 2,
		AiLostModeMaxMoves:   6,
		AiLostModeReplyLimit: 12,
		AiLostModeMinDepth:   2,

		// Queue
		AiQueueWorkers:        1,
		AiQueueAnalyzeThreads: 0,
		AiQueueEnabled:        true,
		AiAnaliticsTopBoards:  7,

		// TT: slightly larger than 1<<18 helps a lot once you deepen regularly
		AiTtUseSetAssoc:       true,
		AiUseTtCache:          true,
		AiTtBuckets:           4,
		AiTtSize:              1 << 19, // 524288
		AiTtMaxEntries:        0,
		AiTtMaxMemoryBytes:    5 * 1024 * 1024 * 1024, // 5 GB
		AiEnableTtPersistence: true,
		AiTtPersistencePath:   "tt_cache.gob",
		AiEnableRootTranspose: true,
		AiRootTransposeSize:   1 << 16, // 65536

		// Move ordering helpers
		AiEnableKillerMoves:  true,
		AiEnableHistoryMoves: true,

		// Boosts: keep killer moderate, history moderate
		AiKillerBoost:  6000,
		AiHistoryBoost: 16,

		// Background pondering off for latency
		AiPonderingEnabled: false,

		AiGhostThrottleMs:  50,
		AiLogSearchStats:   false,
		AiMinmaxCacheLimit: 1000,

		Heuristics: HeuristicConfig{
			Open4:   131633.82492556606,
			Closed4: 23451.264466845663,
			Broken4: 16588.885030052134,

			Open3:   19124.538397343695,
			Broken3: 11377.927833097501,
			Closed3: 802.1059657246053,

			Open2:   400.7080720328319,
			Broken2: 215.2849716438038,

			ForkOpen3:    42035.40739524599,
			ForkFourPlus: 130181.77247952914,

			CaptureNow:          2200.0,
			CaptureDoubleThreat: 2600.0,
			CaptureNearWin:      12000.0,
			CaptureInTwo:        700.0,
			HangingPair:         2400.0,
			CaptureWinSoonScale: 0.95,
			CaptureInTwoLimit:   8,
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
