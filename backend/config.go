package main

import "sync"

type Config struct {
	GhostMode                  bool            `json:"ghost_mode"`
	LogDepthScores             bool            `json:"log_depth_scores"`
	AiDepth                    int             `json:"ai_depth"`
	AiTimeoutMs                int             `json:"ai_timeout_ms"`
	AiTimeBudgetMs             int             `json:"ai_time_budget_ms"`
	AiBacklogEstimateMs        int             `json:"ai_backlog_estimate_ms"`
	AiMaxDepth                 int             `json:"ai_max_depth"`
	AiMinDepth                 int             `json:"ai_min_depth"`
	AiReturnLastComplete       bool            `json:"ai_return_last_complete_depth_only"`
	AiEnableDynamicTopK        bool            `json:"ai_enable_dynamic_top_k"`
	AiEnableHardPlyCaps        bool            `json:"ai_enable_hard_ply_caps"`
	AiMaxCandidates            int             `json:"ai_max_candidates"`
	AiMaxCandidatesPly7        int             `json:"ai_max_candidates_ply7"`
	AiMaxCandidatesPly8        int             `json:"ai_max_candidates_ply8"`
	AiMaxCandidatesPly9        int             `json:"ai_max_candidates_ply9"`
	AiEnableTacticalK          bool            `json:"ai_enable_tactical_k"`
	AiKQuietRoot               int             `json:"ai_k_quiet_root"`
	AiKQuietMid                int             `json:"ai_k_quiet_mid"`
	AiKQuietDeep               int             `json:"ai_k_quiet_deep"`
	AiKTactRoot                int             `json:"ai_k_tact_root"`
	AiKTactMid                 int             `json:"ai_k_tact_mid"`
	AiKTactDeep                int             `json:"ai_k_tact_deep"`
	AiQuickWinExit             bool            `json:"ai_quick_win_exit"`
	AiEnableAspiration         bool            `json:"ai_enable_aspiration"`
	AiAspWindow                float64         `json:"ai_asp_window"`
	AiAspWindowMax             float64         `json:"ai_asp_window_max"`
	AiAspMinDepth              int             `json:"ai_asp_min_depth"`
	AiDepthStep                int             `json:"ai_depth_step"`
	AiTtMaxEntries             int64           `json:"ai_tt_max_entries"`
	AiPonderingEnabled         bool            `json:"ai_pondering_enabled"`
	AiGhostThrottleMs          int             `json:"ai_ghost_throttle_ms"`
	AiTtSize                   int             `json:"ai_tt_size"`
	AiTtBuckets                int             `json:"ai_tt_buckets"`
	AiTtUseSetAssoc            bool            `json:"ai_tt_use_set_assoc"`
	AiUseTtCache               bool            `json:"ai_use_tt_cache"`
	AiTtMaxMemoryBytes         int64           `json:"ai_tt_max_memory_bytes"`
	AiEnableTtPersistence      bool            `json:"ai_enable_tt_persistence"`
	AiTtPersistencePath        string          `json:"ai_tt_persistence_path"`
	AiEnableRootTranspose      bool            `json:"ai_enable_root_transpose_tt"`
	AiRootTransposeSize        int             `json:"ai_root_transpose_tt_size"`
	AiLogSearchStats           bool            `json:"ai_log_search_stats"`
	AiMinmaxCacheLimit         int             `json:"ai_minmax_cache_limit"`
	AiLocalityTopAlignments    int             `json:"ai_locality_top_alignments"`
	AiRootBadMoveDepths        int             `json:"ai_root_bad_move_depths"`
	AiRootBadMoveKeepTop       int             `json:"ai_root_bad_move_keep_top"`
	AiRootBadMoveMinDepth      int             `json:"ai_root_bad_move_min_depth"`
	AiRootBadMoveMargin        float64         `json:"ai_root_bad_move_margin"`
	AiEnableKillerMoves        bool            `json:"ai_enable_killer_moves"`
	AiEnableHistoryMoves       bool            `json:"ai_enable_history_moves"`
	AiEnablePVS                bool            `json:"ai_enable_pvs"`
	AiKillerBoost              int             `json:"ai_killer_boost"`
	AiHistoryBoost             int             `json:"ai_history_boost"`
	AiLazySMPWorkers           int             `json:"ai_lazy_smp_workers"`
	AiEnableTacticalQuiescence bool            `json:"ai_enable_tactical_quiescence"`
	AiTacticalQuiescenceDepth  int             `json:"ai_tactical_quiescence_depth"`
	AiEnableEvalCache          bool            `json:"ai_enable_eval_cache"`
	AiEvalCacheSize            int             `json:"ai_eval_cache_size"`
	AiEvalCacheMinAbs          float64         `json:"ai_eval_cache_min_abs"`
	AiEnableLostMode           bool            `json:"ai_enable_lost_mode"`
	AiLostModeThreshold        float64         `json:"ai_lost_mode_threshold"`
	AiLostModeMaxMoves         int             `json:"ai_lost_mode_max_moves"`
	AiLostModeReplyLimit       int             `json:"ai_lost_mode_reply_limit"`
	AiLostModeMinDepth         int             `json:"ai_lost_mode_min_depth"`
	AiQueueWorkers             int             `json:"ai_queue_workers"`
	AiQueueAnalyzeThreads      int             `json:"ai_queue_analyze_threads"`
	AiQueueEnabled             bool            `json:"ai_enable_queue"`
	AiAnaliticsTopBoards       int             `json:"ai_analitics_top_boards"`
	Heuristics                 HeuristicConfig `json:"heuristics"`
}

type HeuristicConfig struct {
	Open4               float64 `json:"open_4"`
	Closed4             float64 `json:"closed_4"`
	Broken4             float64 `json:"broken_4"`
	Open3               float64 `json:"open_3"`
	Broken3             float64 `json:"broken_3"`
	Closed3             float64 `json:"closed_3"`
	Open2               float64 `json:"open_2"`
	Closed2             float64 `json:"closed_2"`
	Broken2             float64 `json:"broken_2"`
	ForkOpen3           float64 `json:"fork_open_3"`
	ForkFourPlus        float64 `json:"fork_four_plus"`
	CaptureNow          float64 `json:"capture_now"`
	CaptureDoubleThreat float64 `json:"capture_double_threat"`
	CaptureNearWin      float64 `json:"capture_near_win"`
	CaptureInTwo        float64 `json:"capture_in_two"`
	HangingPair         float64 `json:"hanging_pair"`
	LastMoveNeighbor    float64 `json:"last_move_neighbor"`
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
		AiTimeoutMs:          400,
		AiDepth:              10,
		AiMinDepth:           6,
		AiMaxDepth:           10,
		AiDepthStep:          1,
		AiReturnLastComplete: true,

		// Branching control
		AiEnableDynamicTopK: true,
		AiEnableTacticalK:   true,
		AiEnableHardPlyCaps: true,

		// Hard caps
		AiMaxCandidates: 32,

		// Quiet positions (dynamic K)
		AiKQuietRoot: 16,
		AiKQuietMid:  12,
		AiKQuietDeep: 6,

		// Tactical positions (don’t over-cap tactics)
		AiKTactRoot: 24,
		AiKTactMid:  18,
		AiKTactDeep: 14,

		// Tactical quiescence: extend only along tactical moves when the leaf is unstable.
		AiEnableTacticalQuiescence: false,
		AiTacticalQuiescenceDepth:  6,

		// Quick win exit
		AiQuickWinExit: true,

		// Aspiration ON (small window -> fewer nodes, usually faster)
		// Only activate at depth >= AiAspMinDepth: shallow depths have high score variance
		// and fail often, making re-searches more expensive than the pruning benefit.
		AiEnableAspiration: true,
		AiAspWindow:        1200.0,
		AiAspWindowMax:     2000000000.0,
		AiAspMinDepth:      5,

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
		AiQueueWorkers:        4,
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
		AiEnablePVS:          true,

		// Boosts: keep killer moderate, history moderate
		AiKillerBoost:  6000,
		AiHistoryBoost: 16,

		// Lazy SMP: number of parallel search threads (1 = disabled).
		// Workers share the TT, each has own killers/history.
		AiLazySMPWorkers: 4,

		// Background pondering off for latency
		AiPonderingEnabled: false,

		AiGhostThrottleMs:       50,
		AiLogSearchStats:        false,
		AiMinmaxCacheLimit:      1000,
		AiLocalityTopAlignments: 2,
		AiRootBadMoveDepths:     2,
		AiRootBadMoveKeepTop:    2,
		AiRootBadMoveMinDepth:   6,
		AiRootBadMoveMargin:     1500.0,

		Heuristics: HeuristicConfig{
			Open4:   131633.82492556606,
			Closed4: 23451.264466845663,
			Broken4: 16588.885030052134,

			Open3:   19124.538397343695,
			Broken3: 11377.927833097501,
			Closed3: 802.1059657246053,

			Open2:   400.7080720328319,
			Closed2: -600.0,
			Broken2: 215.2849716438038,

			ForkOpen3:    42035.40739524599,
			ForkFourPlus: 130181.77247952914,

			CaptureNow:          38000.0,
			CaptureDoubleThreat: 55000.0,
			CaptureNearWin:      120000.0,
			CaptureInTwo:        12000.0,
			HangingPair:         3000.0,
			LastMoveNeighbor:    24.0,
			CaptureWinSoonScale: 0.80,
			CaptureInTwoLimit:   12,
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
