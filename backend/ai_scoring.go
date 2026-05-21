package main

// ai_scoring.go — Alpha-beta minimax search engine for Gomoku.
//
// High-level algorithm (top-down reading order):
//
//	ScoreBoard()                  ← public entry point; runs iterative deepening
//	  buildRootMovePool()         ← builds the root candidate set from threats/captures/locality
//	  searchRootPoolAtDepth()     ← scores each root move at a given depth
//	    minimax()                 ← recursive alpha-beta with TT, NMP, RFP, LMR, PVS
//	      AnalyzeThreats()        ← detects wins, blocks, forced moves, capture threats
//	      chooseNodeCandidates()  ← selects and orders the moves to try at this node
//	      evaluateMoveWithCache() ← applies the move and recurses or returns leaf eval
//
// Supporting subsystems (in order of dependency):
//   - Cache systems     — TranspositionTable, EvalCache, RootTransposeCache
//   - Board geometry    — bounding box, proximity, alignment utilities
//   - Threat analysis   — pattern tiers, response urgency, ThreatContext
//   - Candidate gen     — quiet/tactical move collection, ordering heuristics
//   - Move mechanics    — incremental apply/undo with EvalState maintenance
//   - Win detection     — immediate win/capture detection + caching

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// ================================================================
// Constants and core search parameters
// Win/loss sentinels, pruning flags, priority levels.
// ================================================================

const (
	illegalScore = -1e9
	winScore     = 2000000000.0
	// Keep node-loop instrumentation cheap: sample high-cost timers and emit progress in chunks.
	searchTimingSampleMask  int64 = 0x3ff // 1/1024
	searchProgressChunkMask int64 = 0x3f  // 64
	lmrLateMoveStart              = 4
	lmrMinDepth                   = 4
	lmrReduction                  = 1
	maxSearchBoardCells           = 19 * 19
)

type AISearchCache struct {
	mu                 sync.Mutex
	TT                 *TranspositionTable
	TTSize             int
	TTBuckets          int
	EvalCache          *EvalCache
	EvalCacheSize      int
	RootTranspose      *RootTransposeCache
	RootTransposeSize  int
	RootTransposeBucks int
	ImmediateWinMoves  map[uint64][]Move
}

type AIScoreSettings struct {
	Depth                   int
	TimeoutMs               int
	BoardSize               int
	Player                  PlayerColor
	OnGhostUpdate           func(GameState)
	OnDepthComplete         func(depth int, move Move, score float64)
	OnDepthCompleteDebug    func(depth int, move Move, score float64, line *SearchDebugLine)
	OnNodeProgress          func(delta int64)
	OnSearchProgress        func(delta SearchProgressDelta)
	Cache                   *AISearchCache
	Config                  Config
	ShouldStop              func() bool
	Stats                   *SearchStats
	DirectDepthOnly         bool
	SkipQueueBacklog        bool
	DebugWideRootAtDepthOne bool
}

type SearchDebugStep struct {
	Player   PlayerColor
	Move     Move
	Captures []Move
}

type SearchDebugLine struct {
	Steps      []SearchDebugStep
	FinalBoard Board
}

type minimaxContext struct {
	rules          Rules
	settings       AIScoreSettings
	evalState      *EvalState
	start          time.Time
	killers        [][]Move
	history        []int
	footprint      *searchFootprint
	mustBlockLog   *mustBlockLogger
	deadline       time.Time
	hasDeadline    bool
	logIndent      int
	nullMoveActive bool // true during a null-move sub-search; prevents consecutive null moves
}

type mustBlockLogger struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func shouldLogTacticalNodes(ctx minimaxContext) bool {
	if !ctx.settings.Config.AiLogSearchStats {
		return false
	}
	return os.Getenv("GOMOKU_DEBUG_TACTICAL_NODES") != ""
}

func shouldLogDetailedSearchTrace(cfg Config) bool {
	if !cfg.AiLogSearchStats {
		return false
	}
	return os.Getenv("GOMOKU_DEBUG_SEARCH_TRACE") != ""
}

func shouldLogCandidateZone(cfg Config) bool {
	if !cfg.AiLogSearchStats {
		return false
	}
	return shouldLogDetailedSearchTrace(cfg) || os.Getenv("GOMOKU_DEBUG_CANDIDATE_BOARD") != ""
}

func shouldLogCandidateBoardOverlay(cfg Config) bool {
	if !cfg.AiLogSearchStats {
		return false
	}
	return os.Getenv("GOMOKU_DEBUG_CANDIDATE_BOARD") != ""
}

func formatTacticalSummaryForSearchLog(summary TacticalSummary) string {
	return fmt.Sprintf(
		"win(B:%d W:%d) capWin(B:%d W:%d) open4(B:%d W:%d) closed4(B:%d W:%d) broken4(B:%d W:%d) open3(B:%d W:%d) broken3(B:%d W:%d) double(B:%t W:%t) forcing(B:%d W:%d) criticalCap(B:%d W:%d) must(B:%t W:%t) tactical=%t",
		summary.WinNowBlue,
		summary.WinNowRed,
		summary.CaptureWinNowBlue,
		summary.CaptureWinNowRed,
		summary.Open4Blue,
		summary.Open4Red,
		summary.Closed4Blue,
		summary.Closed4Red,
		summary.Broken4Blue,
		summary.Broken4Red,
		summary.Open3Blue,
		summary.Open3Red,
		summary.Broken3Blue,
		summary.Broken3Red,
		summary.DoubleThreatBlue,
		summary.DoubleThreatRed,
		summary.ForcingThreatsBlue,
		summary.ForcingThreatsRed,
		summary.CriticalCapturesBlue,
		summary.CriticalCapturesRed,
		summary.MustAnswerForBlue,
		summary.MustAnswerForRed,
		summary.IsTactical,
	)
}

func formatBoardForSearchLog(board Board) string {
	size := board.Size()
	if size <= 0 {
		return "<empty board>"
	}
	var sb strings.Builder
	sb.WriteString("    ")
	for x := 0; x < size; x++ {
		fmt.Fprintf(&sb, "%2d", x)
		if x+1 < size {
			sb.WriteByte(' ')
		}
	}
	sb.WriteByte('\n')
	for y := 0; y < size; y++ {
		fmt.Fprintf(&sb, "%2d  ", y)
		for x := 0; x < size; x++ {
			ch := '.'
			switch board.At(x, y) {
			case CellBlue:
				ch = 'B'
			case CellRed:
				ch = 'W'
			}
			sb.WriteByte(byte(ch))
			if x+1 < size {
				sb.WriteString("  ")
			}
		}
		if y+1 < size {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func formatBoardForCandidateZone(board Board, moves []Move) string {
	size := board.Size()
	if size <= 0 {
		return ""
	}
	marks := make([]bool, size*size)
	for _, move := range moves {
		if !move.IsValid(size) {
			continue
		}
		idx := move.Y*size + move.X
		if idx >= 0 && idx < len(marks) {
			marks[idx] = true
		}
	}
	var sb strings.Builder
	sb.WriteString("    ")
	for x := 0; x < size; x++ {
		sb.WriteString(fmt.Sprintf("%2d ", x))
	}
	sb.WriteByte('\n')
	for y := 0; y < size; y++ {
		sb.WriteString(fmt.Sprintf("%2d ", y))
		for x := 0; x < size; x++ {
			cell := board.At(x, y)
			ch := '.'
			switch cell {
			case CellBlue:
				ch = 'B'
			case CellRed:
				ch = 'W'
			default:
				if marks[y*size+x] {
					ch = '?'
				}
			}
			sb.WriteString(fmt.Sprintf(" %c ", ch))
		}
		if y+1 < size {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func formatThreatArrayForSearchLog(threats []Threat, limit int) string {
	if len(threats) == 0 {
		return "[]"
	}
	if limit > 0 && len(threats) > limit {
		threats = threats[:limit]
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, threat := range threats {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%v tier=%d ext=%d def=%d", threat.Type, threat.Tier, len(threat.ExtensionSquares), len(threat.DefenseSquares))
	}
	sb.WriteByte(']')
	return sb.String()
}

func logTacticalNode(ctx minimaxContext, state GameState, depthLeft int, depthFromRoot int, move Move, player PlayerColor) {
	if !shouldLogTacticalNodes(ctx) {
		return
	}
	var eval EvalResult
	if ctx.evalState != nil {
		eval = ctx.evalState.Snapshot(&state.Board)
	} else {
		eval = EvaluateBoardWithContext(
			state.Board,
			state.ToMove,
			clampUint8(state.CapturedBlue),
			clampUint8(state.CapturedRed),
			ctx.settings.Config,
		)
	}
	if !eval.Summary.IsTactical {
		return
	}
	fmt.Printf("[ai:tactical_node] move=(%d,%d) player=%v to_move=%v depthLeft=%d depthFromRoot=%d score=%d summary=%s compact=%s\n%s\n",
		move.X,
		move.Y,
		player,
		state.ToMove,
		depthLeft,
		depthFromRoot,
		eval.Score,
		formatTacticalSummaryForSearchLog(eval.Summary),
		formatThreatArrayForSearchLog(eval.Threats[:int(eval.ThreatCount)], 8),
		formatBoardForSearchLog(state.Board),
	)
}

// ================================================================
// Search statistics
// Diagnostic counters accumulated during a search call.
// ================================================================

type SearchStats struct {
	Nodes                      int64
	RootSearchCalls            int64
	CollectCandidateCalls      int64
	QuietNodes                 int64
	SoftTacticalNodes          int64
	HardTacticalNodes          int64
	TTProbes                   int64
	TTHits                     int64
	TTExactHits                int64
	TTLowerHits                int64
	TTUpperHits                int64
	TTStores                   int64
	TTOverwrites               int64
	TTReplacements             int64
	Cutoffs                    int64
	TTCutoffs                  int64
	ABCutoffs                  int64
	CandidateCount             int64
	QuietCandidates            int64
	SoftTacticalCandidates     int64
	HardTacticalCandidates     int64
	RootCandidates             int64
	DeepCandidates             int64
	RootSamples                int64
	DeepSamples                int64
	EvalCacheProbes            int64
	EvalCacheHits              int64
	UsedCache                  bool
	DecisionSource             string
	RootPoolSnapshot           []RootMove
	Start                      time.Time
	DepthDurations             []time.Duration
	CompletedDepths            int
	ReturnedDepth              int
	HeuristicCalls             int64
	HeuristicTime              time.Duration
	BoardGenOps                int64
	BoardGenTime               time.Duration
	CollectBBoxTime            time.Duration
	CollectThreatMergeTime     time.Duration
	CollectQuietOnlyTime       time.Duration
	QuietFrontTime             time.Duration
	LastMoveScanTime           time.Duration
	LastMoveLegalTime          time.Duration
	ProximityScanTime          time.Duration
	ProximityLegalTime         time.Duration
	QuietKeepCheckTime         time.Duration
	QuietKeepNeighborhoodTime  time.Duration
	QuietKeepLineTime          time.Duration
	QuietSortTime              time.Duration
	QuietLegalCheckTime        time.Duration
	RootPrepTime               time.Duration
	AnalyzeThreatsTime         time.Duration
	AnalyzeThreatCalls         int64
	AnalyzeThreatEvalTime      time.Duration
	AnalyzeThreatCaptureTime   time.Duration
	AnalyzeThreatDetailTime    time.Duration
	AnalyzeThreatUrgencyTime   time.Duration
	AnalyzeThreatWinTime       time.Duration
	AnalyzeThreatResponseTime  time.Duration
	AnalyzeThreatFilterTime    time.Duration
	AnalyzeThreatStrongCalls   int64
	AnalyzeThreatEvalStateHits int64
	ChooseCandidatesTime       time.Duration
	BuildHardRestrictedTime    time.Duration
	HardBuildGenerateTime      time.Duration
	HardBuildThreatOrderTime   time.Duration
	HardBuildCollectTime       time.Duration
	HardBuildMergeOrderTime    time.Duration
	HardBuildRestrictedTime    time.Duration
	GenerateThreatsTime        time.Duration
	CollectCandidatesTime      time.Duration
	OrderCandidatesTime        time.Duration
	QuietCandidateTime         time.Duration
	SoftCandidateTime          time.Duration
	HardCandidateTime          time.Duration
	RootMoveEvaluations        int64
	QuietMoveEvaluations       int64
	SoftMoveEvaluations        int64
	HardMoveEvaluations        int64
	CollectThreatCandidates    int64
	CollectMergedCandidates    int64
	QuietFrontCandidates       int64
	CollectEmptyBoardReturns   int64
	CollectSingleStoneReturns  int64
	LastMoveWindowChecks       int64
	LastMoveEmptyChecks        int64
	LastMovePrioritySkips      int64
	LastMoveKeepChecks         int64
	LastMoveKeepCacheHits      int64
	LastMoveKeepCacheMisses    int64
	LastMoveKeepAccepted       int64
	LastMoveLegalChecks        int64
	LastMoveLegalRejected      int64
	LastMoveCandidatesAdded    int64
	ProximityWindowChecks      int64
	ProximityEmptyChecks       int64
	ProximityCoveredSkips      int64
	ProximityDuplicateSkips    int64
	ProximityPrioritySkips     int64
	ProximityKeepChecks        int64
	ProximityKeepCacheHits     int64
	ProximityKeepCacheMisses   int64
	ProximityKeepAccepted      int64
	ProximityLegalChecks       int64
	ProximityLegalRejected     int64
	ProximityCandidatesAdded   int64
	QuietLegalChecks           int64
	QuietLegalRejected         int64
	QuietAddedCandidates       int64
	QuietPriorityReplacements  int64
	QuietPrioritySkipped       int64
	HardCoreMoves              int64
	HardThreatCandidates       int64
	HardThreatOrderedMoves     int64
	HardGenericCandidates      int64
	HardCarryoverTarget        int64
	HardCarryoverFromThreat    int64
	HardCarryoverFromGeneric   int64
	HardGenericCollectCalls    int64
	HardGenericCollectSkipped  int64
	HardGenericFilteredOut     int64
	RootFirstMoveSamples       int64
	RootFirstMoveWins          int64
	RootTop2Samples            int64
	RootTop2Wins               int64
	RootTop3Samples            int64
	RootTop3Wins               int64
	NodeFirstLeadSamples       int64
	NodeFirstLeadWins          int64
	NodeFirstExactSamples      int64
	NodeFirstExactWins         int64
	NodeFirstCutoffSamples     int64
	NodeFirstCutoffWins        int64
	PVSProxySamples            int64
	PVSProxyWouldResearch      int64
	PVSProxyQuietSamples       int64
	PVSProxyQuietWouldResearch int64
	PVSProxySoftSamples        int64
	PVSProxySoftWouldResearch  int64
	PVSProxyHardSamples        int64
	PVSProxyHardWouldResearch  int64
	NMPAttempts                int64
	NMPCutoffs                 int64
	RFPAttempts                int64
	RFPCutoffs                 int64
	LMRReduced                 int64
	LMRResearches              int64
	TacticalQuiescenceCalls    int64
	RootFirstMoveDepthSamples  [orderingStatsDepthBuckets]int64
	RootFirstMoveDepthWins     [orderingStatsDepthBuckets]int64
	PVSProxyDepthSamples       [orderingStatsDepthBuckets]int64
	PVSProxyDepthWouldResearch [orderingStatsDepthBuckets]int64
	RootBestRankHistogram      [orderingRankBuckets]int64
	NodeBestRankHistogram      [orderingRankBuckets]int64
	RootBestRankByDepth        [orderingStatsDepthBuckets][orderingRankBuckets]int64
	NodeBestRankByDepth        [orderingStatsDepthBuckets][orderingRankBuckets]int64

	progressReportedNodes    int64
	progressReportedBoardGen int64
	progressMetricNodes      int64
	progressMetricCandidates int64
	progressMetricTTProbes   int64
	progressMetricTTHits     int64
	progressMetricTTCutoffs  int64
	progressMetricABCutoffs  int64
}

func cloneRootPool(pool []RootMove) []RootMove {
	if len(pool) == 0 {
		return nil
	}
	out := make([]RootMove, len(pool))
	copy(out, pool)
	return out
}

const orderingStatsDepthBuckets = 32
const orderingRankBuckets = 13

func orderingDepthBucket(depth int) int {
	if depth < 1 {
		return 0
	}
	if depth >= orderingStatsDepthBuckets {
		return orderingStatsDepthBuckets - 1
	}
	return depth
}

func orderingRankBucket(rank int) int {
	if rank < 0 {
		return -1
	}
	if rank >= orderingRankBuckets-1 {
		return orderingRankBuckets - 1
	}
	return rank
}

func recordOrderingRank(hist *[orderingRankBuckets]int64, rank int) {
	if hist == nil {
		return
	}
	bucket := orderingRankBucket(rank)
	if bucket < 0 {
		return
	}
	hist[bucket]++
}

type SearchProgressDelta struct {
	Nodes          int64
	CandidateCount int64
	TTProbes       int64
	TTHits         int64
	TTCutoffs      int64
	ABCutoffs      int64
}

// ================================================================
// Cache subsystems
// EvalCache, RootTransposeCache, and the global default cache.
// ================================================================

func newAISearchCache() AISearchCache {
	return AISearchCache{}
}

type EvalCacheEntry struct {
	Key         uint64
	Value       float64
	GenWritten  uint32
	GenLastUsed uint32
	Valid       bool
}

const evalCacheNumStripes = 64

type EvalCache struct {
	mu         [evalCacheNumStripes]sync.Mutex
	stripeMask uint64
	mask       uint64
	buckets    int
	entries    []EvalCacheEntry
	gen        atomic.Uint32
}

func NewEvalCache(size uint64, buckets int) *EvalCache {
	if buckets <= 0 {
		buckets = 2
	}
	if size < 1 {
		size = 1
	}
	if (size & (size - 1)) != 0 {
		size = nextPowerOfTwo(size)
	}
	stripes := uint64(evalCacheNumStripes)
	if size < stripes {
		stripes = size
	}
	ec := &EvalCache{
		stripeMask: stripes - 1,
		mask:       size - 1,
		buckets:    buckets,
		entries:    make([]EvalCacheEntry, int(size)*buckets),
	}
	ec.gen.Store(1)
	return ec
}

func (ec *EvalCache) stripeIndexFor(key uint64) int {
	return int((key & ec.mask) & ec.stripeMask)
}

func (ec *EvalCache) NextGeneration() {
	if ec == nil {
		return
	}
	gen := ec.gen.Add(1)
	if gen == 0 {
		ec.gen.CompareAndSwap(0, 1)
	}
}

func (ec *EvalCache) bucketIndex(key uint64) int {
	return int(key&ec.mask) * ec.buckets
}

func (ec *EvalCache) Get(key uint64) (float64, bool) {
	stripe := ec.stripeIndexFor(key)
	ec.mu[stripe].Lock()
	defer ec.mu[stripe].Unlock()
	gen := ec.gen.Load()
	start := ec.bucketIndex(key)
	for i := 0; i < ec.buckets; i++ {
		idx := start + i
		entry := ec.entries[idx]
		if entry.Valid && entry.Key == key {
			entry.GenLastUsed = gen
			ec.entries[idx] = entry
			return entry.Value, true
		}
	}
	return 0.0, false
}

func (ec *EvalCache) Put(key uint64, value float64) {
	stripe := ec.stripeIndexFor(key)
	ec.mu[stripe].Lock()
	defer ec.mu[stripe].Unlock()
	gen := ec.gen.Load()
	start := ec.bucketIndex(key)
	victim := -1
	oldestAge := uint32(0)
	for i := 0; i < ec.buckets; i++ {
		idx := start + i
		entry := ec.entries[idx]
		if entry.Valid && entry.Key == key {
			ec.entries[idx] = EvalCacheEntry{
				Key:         key,
				Value:       value,
				GenWritten:  gen,
				GenLastUsed: gen,
				Valid:       true,
			}
			return
		}
		if !entry.Valid {
			victim = idx
			break
		}
		age := gen - entry.GenLastUsed
		if victim == -1 || age > oldestAge {
			victim = idx
			oldestAge = age
		}
	}
	if victim >= 0 {
		ec.entries[victim] = EvalCacheEntry{
			Key:         key,
			Value:       value,
			GenWritten:  gen,
			GenLastUsed: gen,
			Valid:       true,
		}
	}
}

func (ec *EvalCache) Clear() {
	if ec == nil {
		return
	}
	for i := range ec.mu {
		ec.mu[i].Lock()
	}
	defer func() {
		for i := len(ec.mu) - 1; i >= 0; i-- {
			ec.mu[i].Unlock()
		}
	}()
	for i := range ec.entries {
		ec.entries[i] = EvalCacheEntry{}
	}
	ec.gen.Store(1)
}

type RootTransposeEntry struct {
	Key         uint64
	Depth       int
	Score       int32
	Flag        TTFlag
	BestRel     Move
	ProvenExact bool
	GrowLeft    uint8
	GrowRight   uint8
	GrowTop     uint8
	GrowBottom  uint8
	HitLeft     bool
	HitRight    bool
	HitTop      bool
	HitBottom   bool
	FrameW      uint8
	FrameH      uint8
	GenWritten  uint32
	GenLastUsed uint32
	Valid       bool
}

func (e RootTransposeEntry) ScoreFloat() float64 {
	return float64(e.Score)
}

type RootTransposeCache struct {
	mu      sync.Mutex
	mask    uint64
	buckets int
	entries []RootTransposeEntry
	gen     uint32
}

func NewRootTransposeCache(size uint64, buckets int) *RootTransposeCache {
	if buckets <= 0 {
		buckets = 2
	}
	if size < 1 {
		size = 1
	}
	if (size & (size - 1)) != 0 {
		size = nextPowerOfTwo(size)
	}
	return &RootTransposeCache{
		mask:    size - 1,
		buckets: buckets,
		entries: make([]RootTransposeEntry, int(size)*buckets),
		gen:     1,
	}
}

func (rtc *RootTransposeCache) NextGeneration() {
	if rtc == nil {
		return
	}
	rtc.mu.Lock()
	defer rtc.mu.Unlock()
	rtc.gen++
	if rtc.gen == 0 {
		rtc.gen = 1
	}
}

func (rtc *RootTransposeCache) bucketIndex(key uint64) int {
	return int(key&rtc.mask) * rtc.buckets
}

func shouldReplaceRootTransposeEntry(old RootTransposeEntry, depth int, gen uint32) bool {
	if !old.Valid {
		return true
	}
	if depth > old.Depth {
		return true
	}
	if depth < old.Depth {
		return false
	}
	age := gen - old.GenLastUsed
	return age >= ttVeryOldGenerations
}

func (rtc *RootTransposeCache) Get(key uint64, minDepth int) (RootTransposeEntry, bool) {
	if rtc == nil {
		return RootTransposeEntry{}, false
	}
	rtc.mu.Lock()
	defer rtc.mu.Unlock()
	start := rtc.bucketIndex(key)
	for i := 0; i < rtc.buckets; i++ {
		idx := start + i
		entry := rtc.entries[idx]
		if !entry.Valid || entry.Key != key {
			continue
		}
		if entry.Flag != TTExact || (!entry.ProvenExact && entry.Depth < minDepth) {
			continue
		}
		entry.GenLastUsed = rtc.gen
		rtc.entries[idx] = entry
		return entry, true
	}
	return RootTransposeEntry{}, false
}

func clampToUint8(v int) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

func (rtc *RootTransposeCache) Put(key uint64, depth int, value float64, flag TTFlag, bestRel Move, meta TTMeta) {
	if rtc == nil {
		return
	}
	rtc.mu.Lock()
	defer rtc.mu.Unlock()
	start := rtc.bucketIndex(key)
	gen := rtc.gen
	newEntry := RootTransposeEntry{
		Key:         key,
		Depth:       depth,
		Score:       scoreToTT(value),
		Flag:        flag,
		BestRel:     bestRel,
		ProvenExact: meta.ProvenExact,
		GrowLeft:    clampToUint8(meta.GrowLeft),
		GrowRight:   clampToUint8(meta.GrowRight),
		GrowTop:     clampToUint8(meta.GrowTop),
		GrowBottom:  clampToUint8(meta.GrowBottom),
		FrameW:      clampToUint8(meta.FrameW),
		FrameH:      clampToUint8(meta.FrameH),
		HitLeft:     meta.HitLeft,
		HitRight:    meta.HitRight,
		HitTop:      meta.HitTop,
		HitBottom:   meta.HitBottom,
		GenWritten:  gen,
		GenLastUsed: gen,
		Valid:       true,
	}
	for i := 0; i < rtc.buckets; i++ {
		idx := start + i
		entry := rtc.entries[idx]
		if !entry.Valid {
			rtc.entries[idx] = newEntry
			return
		}
		if entry.Key == key {
			if shouldReplaceRootTransposeEntry(entry, depth, gen) {
				rtc.entries[idx] = newEntry
			}
			return
		}
	}
	victim := start
	oldestAge := uint32(0)
	for i := 0; i < rtc.buckets; i++ {
		idx := start + i
		age := gen - rtc.entries[idx].GenLastUsed
		if i == 0 || age > oldestAge {
			victim = idx
			oldestAge = age
		}
	}
	rtc.entries[victim] = newEntry
}

func (rtc *RootTransposeCache) Clear() {
	if rtc == nil {
		return
	}
	rtc.mu.Lock()
	defer rtc.mu.Unlock()
	for i := range rtc.entries {
		rtc.entries[i] = RootTransposeEntry{}
	}
	rtc.gen = 1
}

func (rtc *RootTransposeCache) snapshotEntries() []RootTransposeEntry {
	if rtc == nil {
		return nil
	}
	rtc.mu.Lock()
	defer rtc.mu.Unlock()
	entries := make([]RootTransposeEntry, len(rtc.entries))
	copy(entries, rtc.entries)
	return entries
}

func (rtc *RootTransposeCache) loadEntries(entries []RootTransposeEntry) {
	if rtc == nil {
		return
	}
	rtc.mu.Lock()
	defer rtc.mu.Unlock()
	if len(entries) > len(rtc.entries) {
		entries = entries[:len(rtc.entries)]
	}
	copy(rtc.entries[:len(entries)], entries)
}

// ================================================================
// Search footprint
// Tracks how far the search explored beyond the initial stone cluster.
// ================================================================

type searchFootprint struct {
	mu                        sync.Mutex
	rootMinX, rootMaxX        int
	rootMinY, rootMaxY        int
	maxGrowLeft, maxGrowRight int
	maxGrowTop, maxGrowBottom int
}

func newSearchFootprint(state GameState, boardSize int) *searchFootprint {
	bbox := computeBBox(state.Board, boardSize)
	if bbox.stones == 0 {
		return nil
	}
	return &searchFootprint{
		rootMinX: bbox.minX,
		rootMaxX: bbox.maxX,
		rootMinY: bbox.minY,
		rootMaxY: bbox.maxY,
	}
}

func (sf *searchFootprint) ObserveMove(move Move) {
	if sf == nil {
		return
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if move.X < sf.rootMinX {
		grow := sf.rootMinX - move.X
		if grow > sf.maxGrowLeft {
			sf.maxGrowLeft = grow
		}
	}
	if move.X > sf.rootMaxX {
		grow := move.X - sf.rootMaxX
		if grow > sf.maxGrowRight {
			sf.maxGrowRight = grow
		}
	}
	if move.Y < sf.rootMinY {
		grow := sf.rootMinY - move.Y
		if grow > sf.maxGrowTop {
			sf.maxGrowTop = grow
		}
	}
	if move.Y > sf.rootMaxY {
		grow := move.Y - sf.rootMaxY
		if grow > sf.maxGrowBottom {
			sf.maxGrowBottom = grow
		}
	}
}

func (sf *searchFootprint) Growth() (left, right, top, bottom int) {
	if sf == nil {
		return 0, 0, 0, 0
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.maxGrowLeft, sf.maxGrowRight, sf.maxGrowTop, sf.maxGrowBottom
}

func buildTTMeta(state GameState, boardSize int, footprint *searchFootprint) TTMeta {
	_, bbox, ok := rootShapeKey(state, boardSize)
	if !ok {
		return TTMeta{}
	}
	rawLeft, rawRight, rawTop, rawBottom := 0, 0, 0, 0
	if footprint != nil {
		rawLeft, rawRight, rawTop, rawBottom = footprint.Growth()
	}
	left := rawLeft
	top := rawTop
	right := rawRight
	bottom := rawBottom
	if left > bbox.minX {
		left = bbox.minX
	}
	if top > bbox.minY {
		top = bbox.minY
	}
	maxRight := boardSize - 1 - bbox.maxX
	if right > maxRight {
		right = maxRight
	}
	maxBottom := boardSize - 1 - bbox.maxY
	if bottom > maxBottom {
		bottom = maxBottom
	}
	frameW := bbox.width + left + right
	frameH := bbox.height + top + bottom
	if frameW <= 0 || frameH <= 0 {
		return TTMeta{}
	}
	originX := bbox.minX - left
	originY := bbox.minY - top
	return TTMeta{
		GrowLeft:   left,
		GrowRight:  right,
		GrowTop:    top,
		GrowBottom: bottom,
		FrameW:     frameW,
		FrameH:     frameH,
		HitLeft:    rawLeft > left || originX == 0,
		HitRight:   rawRight > right || originX+frameW == boardSize,
		HitTop:     rawTop > top || originY == 0,
		HitBottom:  rawBottom > bottom || originY+frameH == boardSize,
	}
}

func selectCache(ctx minimaxContext) *AISearchCache {
	if ctx.settings.Cache != nil {
		return ctx.settings.Cache
	}
	return SharedSearchCache()
}

var (
	defaultCache      = newAISearchCache()
	defaultCacheMutex sync.Mutex
)

func SharedSearchCache() *AISearchCache {
	return &defaultCache
}

func lockDefaultCache() func() {
	defaultCacheMutex.Lock()
	return defaultCacheMutex.Unlock
}

func FlushGlobalCaches() {
	unlock := lockDefaultCache()
	defer unlock()
	defaultCache.mu.Lock()
	tt := defaultCache.TT
	evalCache := defaultCache.EvalCache
	rootTranspose := defaultCache.RootTranspose
	defaultCache.ImmediateWinMoves = nil
	defaultCache.EvalCacheSize = 0
	defaultCache.RootTransposeSize = 0
	defaultCache.mu.Unlock()
	if tt != nil {
		tt.Clear()
	}
	if evalCache != nil {
		evalCache.Clear()
	}
	if rootTranspose != nil {
		rootTranspose.Clear()
	}
}

func ensureTT(cache *AISearchCache, config Config) *TranspositionTable {
	if cache == nil {
		return nil
	}
	if config.AiTtSize <= 0 && config.AiTtMaxEntries <= 0 {
		return nil
	}
	if config.AiTtSize <= 0 {
		config.AiTtSize = int(config.AiTtMaxEntries)
	}
	buckets := config.AiTtBuckets
	if !config.AiTtUseSetAssoc {
		buckets = 1
	}
	if buckets <= 0 {
		buckets = 2
	}
	if config.AiTtMaxMemoryBytes > 0 {
		entryBytes := int64(unsafe.Sizeof(TTEntry{}))
		if entryBytes <= 0 {
			entryBytes = 1
		}
		maxEntriesByMemory := int(config.AiTtMaxMemoryBytes / entryBytes)
		if maxEntriesByMemory < 1 {
			maxEntriesByMemory = 1
		}
		maxSizeByMemory := maxEntriesByMemory / buckets
		if maxSizeByMemory < 1 {
			maxSizeByMemory = 1
		}
		if config.AiTtSize > maxSizeByMemory {
			config.AiTtSize = floorPowerOfTwo(maxSizeByMemory)
			if config.AiTtSize < 1 {
				config.AiTtSize = 1
			}
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.TT == nil || cache.TTSize != config.AiTtSize || cache.TTBuckets != buckets {
		cache.TT = NewTranspositionTable(uint64(config.AiTtSize), buckets)
		cache.TTSize = config.AiTtSize
		cache.TTBuckets = buckets
	}
	return cache.TT
}

func floorPowerOfTwo(value int) int {
	if value < 1 {
		return 1
	}
	pow := 1
	for pow <= value/2 {
		pow <<= 1
	}
	return pow
}

func ensureEvalCache(cache *AISearchCache, config Config) *EvalCache {
	if cache == nil {
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !config.AiEnableEvalCache {
		cache.EvalCache = nil
		cache.EvalCacheSize = 0
		return nil
	}
	size := config.AiEvalCacheSize
	if size <= 0 {
		size = 1 << 18
	}
	if cache.EvalCache == nil || cache.EvalCacheSize != size {
		cache.EvalCache = NewEvalCache(uint64(size), 2)
		cache.EvalCacheSize = size
	}
	return cache.EvalCache
}

func ensureRootTransposeCache(cache *AISearchCache, config Config) *RootTransposeCache {
	if cache == nil || !config.AiEnableRootTranspose || !config.AiUseTtCache {
		return nil
	}
	size := config.AiRootTransposeSize
	if size <= 0 {
		size = 1 << 16
	}
	buckets := 2
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.RootTranspose == nil || cache.RootTransposeSize != size || cache.RootTransposeBucks != buckets {
		cache.RootTranspose = NewRootTransposeCache(uint64(size), buckets)
		cache.RootTransposeSize = size
		cache.RootTransposeBucks = buckets
	}
	return cache.RootTranspose
}

func playerCell(player PlayerColor) Cell {
	return CellFromPlayer(player)
}

// ================================================================
// Candidate move and root move types
// ================================================================

type candidateMove struct {
	move       Move
	priority   int
	quietScore int
}

type rootLocalityThreatChoice struct {
	Owner     PlayerColor
	Threat    Threat
	Positions []Move
}

type rootLocalityMoveChoice struct {
	move                Move
	priority            int
	quietScore          int
	touchCount          int
	ownAlignmentCount   int
	oppAlignmentCount   int
	totalAlignmentCount int
	threatIndices       []int
}

type rootLocalitySelection struct {
	Threats []rootLocalityThreatChoice
	Moves   []rootLocalityMoveChoice
}

type RootMove struct {
	Move               Move
	ShallowScore       float64
	LastSearchScore    float64
	VerificationScore  float64
	ChildForcingScore  int
	LastCompletedDepth int
	VerificationDepth  int
	TacticalPriority   int
	ThreatFlags        uint32
	SourceFlags        uint32
	ThreatSeverity     int
	CaptureValue       int
	CaptureDefense     int
	ForkPotential      int
	StabilityNeed      int
	LastSearchBand     int
	SearchCount        int
	WasSearched        bool
	WasVerified        bool
	IsForced           bool
	BadDepthStreak     int
	HasShallowScore    bool
	HasLastSearch      bool
	HasVerification    bool
	LastSearchStatus   string
}

type rootSearchBands struct {
	forced       []int
	principal    []int
	speculative  []int
	verification []int
}

type ThreatContext struct {
	SideToMove           PlayerColor
	OwnThreats           []Threat
	OppThreats           []Threat
	OwnBestTier          ThreatTier
	OppBestTier          ThreatTier
	WinningMoves         []Move
	MustPlayMoves        []Move
	MustPlayDetails      []ThreatResponseMove
	MustBlockMoves       []Move
	MustBlockDetails     []ThreatResponseMove
	CounterThreatMoves   []Move
	ForkMoves            []Move
	PreventForkMoves     []Move
	CaptureMoves         []Move
	CaptureDefenseMoves  []Move
	CaptureDefenseForced bool
	StabilizationMoves   []Move
	IsSoftTactical       bool
	IsHardTactical       bool
}

type ThreatResponseKind int

const (
	ThreatResponseMustBlock ThreatResponseKind = iota
	ThreatResponseMustPlay
)

type ThreatResponseMove struct {
	Move       Move
	Pattern    PatternType
	Severity   int
	Tempo      int
	WinTempo   int
	ForceTempo int
	Tier       ThreatTier
	Kind       ThreatResponseKind
}

const (
	rootThreatOwnWin uint32 = 1 << iota
	rootThreatOppWin
	rootThreatOwnFour
	rootThreatOppFour
	rootThreatOwnThree
	rootThreatOppThree
	rootThreatForkCreate
	rootThreatForkPrevent
	rootThreatCaptureCreate
	rootThreatCapturePrevent
	rootThreatStabilize
	rootThreatChildMustAnswer
	rootThreatChildOpenFour
	rootThreatChildDoubleThreat
	rootThreatChildCriticalCapture
)

const (
	rootSourceImmediateWin uint32 = 1 << iota
	rootSourceImmediateBlock
	rootSourceCaptureWin
	rootSourceCaptureDefense
	rootSourceThreatOwn
	rootSourceThreatOpp
	rootSourceCaptureOwn
	rootSourceCaptureOpp
	rootSourceStabilize
	rootSourceLocality
)

const (
	rootBandForced = iota
	rootBandPrincipal
	rootBandSpeculative
	rootBandVerification
)

const (
	prioWin            = 0
	prioBlockWin       = 1
	prioCaptureCreate  = 2
	prioCapturePrevent = 3
	prioCreateFour     = 4
	prioBlockFour      = 5
	prioCreateOpen3    = 6
	prioBlockOpen3     = 7
	prioQuietOwn4      = 8
	prioQuietOpp4      = 9
	prioQuietOwn3      = 10
	prioQuietOpp3      = 11
	prioLastMove       = 12
	prioQuietOwn2      = 13
	prioQuietOpp2      = 14
	prioProximity      = 20
	prioDefault        = 50
	maxCandidatePrio   = 100
	proximityRadius    = 2
	lastMoveRadius     = 3
)

// ================================================================
// Board geometry utilities
// Bounding box, proximity masks, distance helpers.
// ================================================================

func captureThreatsForPlayerDetails(details ThreatDetails, player PlayerColor) []CaptureThreat {
	if details.CaptureThreatCount == 0 {
		return nil
	}
	out := make([]CaptureThreat, 0, details.CaptureThreatCount)
	for i := 0; i < int(details.CaptureThreatCount); i++ {
		if details.CaptureThreats[i].Owner == player {
			out = append(out, details.CaptureThreats[i])
		}
	}
	return out
}

type boardBBox struct {
	minX, maxX int
	minY, maxY int
	width      int
	height     int
	spread     int
	stones     int
}

func computeBBox(board Board, boardSize int) boardBBox {
	bbox := boardBBox{
		minX:   boardSize,
		maxX:   -1,
		minY:   boardSize,
		maxY:   -1,
		width:  0,
		height: 0,
		spread: 0,
		stones: 0,
	}
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			bbox.stones++
			if x < bbox.minX {
				bbox.minX = x
			}
			if x > bbox.maxX {
				bbox.maxX = x
			}
			if y < bbox.minY {
				bbox.minY = y
			}
			if y > bbox.maxY {
				bbox.maxY = y
			}
		}
	}
	if bbox.stones == 0 {
		return bbox
	}
	bbox.width = bbox.maxX - bbox.minX + 1
	bbox.height = bbox.maxY - bbox.minY + 1
	if bbox.width > bbox.height {
		bbox.spread = bbox.width
	} else {
		bbox.spread = bbox.height
	}
	return bbox
}

func computeDensity(stones, width, height int) float64 {
	if stones <= 0 || width <= 0 || height <= 0 {
		return 0.0
	}
	return float64(stones) / float64(width*height)
}

// buildStoneProximityMask returns a flat boolean array (indexed y*boardSize+x)
// where true means the cell is within radius cells (Chebyshev distance) of at
// least one occupied stone. Used to reject "UFO" candidates that score well in
// the threat LUT only because they lie on a long line extending far from the
// active cluster.
func buildStoneProximityMask(board Board, boardSize int, radius int) []bool {
	mask := make([]bool, boardSize*boardSize)
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			for dy := -radius; dy <= radius; dy++ {
				for dx := -radius; dx <= radius; dx++ {
					if chebDist(dx, dy) > radius {
						continue
					}
					nx, ny := x+dx, y+dy
					if nx >= 0 && nx < boardSize && ny >= 0 && ny < boardSize {
						mask[ny*boardSize+nx] = true
					}
				}
			}
		}
	}
	return mask
}

func logAITask(ctx minimaxContext, indent int, format string, args ...interface{}) {
	if !ctx.settings.Config.AiLogSearchStats {
		return
	}
	prefix := strings.Repeat("  ", indent)
	fmt.Printf("[ai:trace] %s%s\n", prefix, fmt.Sprintf(format, args...))
}

func logPrune(ctx minimaxContext, depth int, move Move, best, alpha, beta float64) {
	if !shouldLogDetailedSearchTrace(ctx.settings.Config) {
		return
	}
	prefix := strings.Repeat("  ", ctx.logIndent+1)
	fmt.Printf("[ai:prune] %sdepth=%d move=(%d,%d) best=%.2f alpha=%.2f beta=%.2f\n", prefix, depth, move.X, move.Y, best, alpha, beta)
}

func logSearchMove(ctx minimaxContext, depth int, depthFromRoot int, move Move, moveIndex int, totalMoves int, searchDepth int, alpha, beta float64, band string) {
	if !shouldLogDetailedSearchTrace(ctx.settings.Config) {
		return
	}
	if depthFromRoot == 0 && totalMoves > 48 {
		last := totalMoves - 1
		if moveIndex >= 12 && moveIndex != last && (moveIndex+1)%25 != 0 {
			return
		}
	}
	label := "search"
	if band != "" {
		label = band
	}
	logAITask(ctx, ctx.logIndent+1, "%s move=(%d,%d) idx=%d/%d depth=%d depthFromRoot=%d searchDepth=%d alpha=%.2f beta=%.2f",
		label,
		move.X, move.Y,
		moveIndex+1,
		totalMoves,
		depth,
		depthFromRoot,
		searchDepth,
		alpha,
		beta,
	)
}

func shouldKeepQuietCandidate(board Board, move Move, boardSize int) bool {
	return shouldKeepQuietCandidateWithStats(board, move, boardSize, nil)
}

func quietCandidateLocalAnalysis(board Board, move Move) (bool, int, int, bool, int) {
	adjacentStone := false
	adjacentCount := 0
	withinTwo := 0
	var forward [4]int
	var backward [4]int

	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx := move.X + dx
			ny := move.Y + dy
			if !board.InBounds(nx, ny) {
				continue
			}
			if board.At(nx, ny) == CellEmpty {
				continue
			}

			dist := chebDist(dx, dy)
			if dist == 1 {
				adjacentStone = true
				adjacentCount++
			}
			if dist <= 2 {
				withinTwo++
			}

			switch {
			case dy == 0:
				if dx > 0 {
					forward[0]++
				} else {
					backward[0]++
				}
			case dx == 0:
				if dy > 0 {
					forward[1]++
				} else {
					backward[1]++
				}
			case dx == dy:
				if dx > 0 {
					forward[2]++
				} else {
					backward[2]++
				}
			case dx == -dy:
				if dx > 0 {
					forward[3]++
				} else {
					backward[3]++
				}
			}
		}
	}

	lineInterest := false
	activeDirections := 0
	for i := 0; i < len(forward); i++ {
		if (forward[i] > 0 && backward[i] > 0) || (forward[i]+backward[i] >= 2) {
			activeDirections++
			lineInterest = true
		}
	}
	return adjacentStone, adjacentCount, withinTwo, lineInterest, activeDirections
}

func quietLineInterest(board Board, x, y int) (bool, int) {
	_, _, _, lineInterest, activeDirections := quietCandidateLocalAnalysis(board, Move{X: x, Y: y})
	return lineInterest, activeDirections
}

func shouldKeepQuietCandidateWithStats(board Board, move Move, boardSize int, stats *SearchStats) bool {
	adjacentStone, adjacentCount, withinTwo, lineInterest, activeDirections := quietCandidateLocalAnalysis(board, move)
	// Rules C/D: extend/bridge a useful line or touch multiple active directions.
	strongAlignmentInterest := lineInterest || activeDirections >= 2
	// A locally dense contact point is still worth trying even if the line signal is not
	// yet strong enough under C/D. This keeps moves like (11,8) in clustered positions.
	if adjacentCount >= 2 {
		return true
	}
	if !strongAlignmentInterest {
		return false
	}
	// Keep only candidates that are both locally connected (A/B)
	// and structurally meaningful (C/D).
	return adjacentStone || withinTwo >= 2
}

func countContiguous(board Board, x, y, dx, dy int, target Cell) int {
	count := 0
	nx := x + dx
	ny := y + dy
	for board.InBounds(nx, ny) && board.At(nx, ny) == target {
		count++
		nx += dx
		ny += dy
	}
	return count
}

func chebDist(dx, dy int) int {
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dy > dx {
		return dy
	}
	return dx
}

func threatFlagsForMove(board Board, move Move, target Cell) (winNow bool, createFour bool, openThree bool) {
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for _, dir := range directions {
		dx := dir[0]
		dy := dir[1]
		left := countContiguous(board, move.X, move.Y, -dx, -dy, target)
		right := countContiguous(board, move.X, move.Y, dx, dy, target)
		total := left + right + 1
		if total >= 5 {
			winNow = true
			continue
		}
		if total == 4 {
			leftX := move.X - (left+1)*dx
			leftY := move.Y - (left+1)*dy
			rightX := move.X + (right+1)*dx
			rightY := move.Y + (right+1)*dy
			openLeft := board.InBounds(leftX, leftY) && board.At(leftX, leftY) == CellEmpty
			openRight := board.InBounds(rightX, rightY) && board.At(rightX, rightY) == CellEmpty
			if openLeft || openRight {
				createFour = true
			}
			continue
		}
		if total == 3 {
			leftX := move.X - (left+1)*dx
			leftY := move.Y - (left+1)*dy
			rightX := move.X + (right+1)*dx
			rightY := move.Y + (right+1)*dy
			openLeft := board.InBounds(leftX, leftY) && board.At(leftX, leftY) == CellEmpty
			openRight := board.InBounds(rightX, rightY) && board.At(rightX, rightY) == CellEmpty
			if openLeft && openRight {
				openThree = true
			}
		}
	}
	return winNow, createFour, openThree
}

func generateThreatMoves(board Board, boardSize int, toPlay PlayerColor) ([]candidateMove, bool) {
	threats := make([]candidateMove, 0, 32)
	cellCount := boardSize * boardSize
	var seenPriorityStack [maxSearchBoardCells]int
	seenPriority := seenPriorityStack[:0]
	if cellCount <= len(seenPriorityStack) {
		seenPriority = seenPriorityStack[:cellCount]
	} else {
		seenPriority = make([]int, cellCount)
	}
	for i := range seenPriority {
		seenPriority[i] = maxCandidatePrio
	}
	toPlayCell := CellFromPlayer(toPlay)
	oppCell := CellFromPlayer(otherPlayer(toPlay))
	urgent := false
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) != CellEmpty {
				continue
			}
			move := Move{X: x, Y: y}
			bestPrio := maxCandidatePrio

			winNow, createFour, openThree := threatFlagsForMove(board, move, toPlayCell)
			if winNow {
				bestPrio = prioWin
				urgent = true
			} else if createFour {
				if prioCreateFour < bestPrio {
					bestPrio = prioCreateFour
				}
				urgent = true
			} else if openThree {
				if prioCreateOpen3 < bestPrio {
					bestPrio = prioCreateOpen3
				}
			}

			winNow, createFour, openThree = threatFlagsForMove(board, move, oppCell)
			if winNow {
				if prioBlockWin < bestPrio {
					bestPrio = prioBlockWin
				}
				urgent = true
			} else if createFour {
				if prioBlockFour < bestPrio {
					bestPrio = prioBlockFour
				}
				urgent = true
			} else if openThree {
				if prioBlockOpen3 < bestPrio {
					bestPrio = prioBlockOpen3
				}
			}

			if bestPrio == maxCandidatePrio {
				continue
			}
			idx := y*boardSize + x
			if bestPrio < seenPriority[idx] {
				seenPriority[idx] = bestPrio
				threats = append(threats, candidateMove{move: move, priority: bestPrio})
			}
		}
	}
	return threats, urgent
}

func moveFromCellIndex(boardSize, idx int) Move {
	if boardSize <= 0 || idx < 0 {
		return Move{}
	}
	return Move{X: idx % boardSize, Y: idx / boardSize}
}

// ================================================================
// Threat pattern helpers
// Severity, priority, and flags derived from pattern types.
// ================================================================

func threatSeverityForPattern(pattern PatternType) int {
	switch pattern {
	case PatternWin5:
		return 100
	case PatternOpen4:
		return 90
	case PatternClosed4, PatternBroken4:
		return 70
	case PatternOpen3:
		return 50
	case PatternBroken3, PatternClosed3:
		return 35
	case PatternOpen2:
		return 10
	case PatternClosed2:
		return 4
	case PatternBroken2:
		return 6
	default:
		return 0
	}
}

func rootPriorityForThreat(player, currentPlayer PlayerColor, threat Threat) int {
	if player == currentPlayer {
		if threat.Type == PatternWin5 {
			return prioWin
		}
		if isFourThreat(PatternType(threat.Type)) {
			return prioCreateFour
		}
		if threat.Type == PatternOpen3 || threat.Type == PatternBroken3 {
			return prioCreateOpen3
		}
		return prioDefault
	}
	if threat.Type == PatternWin5 {
		return prioBlockWin
	}
	if isFourThreat(PatternType(threat.Type)) {
		return prioBlockFour
	}
	if threat.Type == PatternOpen3 || threat.Type == PatternBroken3 {
		return prioBlockOpen3
	}
	return prioDefault
}

func rootFlagsForThreat(player, currentPlayer PlayerColor, threat Threat) uint32 {
	flags := uint32(0)
	if player == currentPlayer {
		if threat.Type == PatternWin5 {
			flags |= rootThreatOwnWin
		}
		if isFourThreat(PatternType(threat.Type)) {
			flags |= rootThreatOwnFour
		}
		if threat.Type == PatternOpen3 || threat.Type == PatternBroken3 {
			flags |= rootThreatOwnThree
		}
		if len(threat.ExtensionSquares) > 1 {
			flags |= rootThreatForkCreate
		}
	} else {
		if threat.Type == PatternWin5 {
			flags |= rootThreatOppWin
		}
		if isFourThreat(PatternType(threat.Type)) {
			flags |= rootThreatOppFour
		}
		if threat.Type == PatternOpen3 || threat.Type == PatternBroken3 {
			flags |= rootThreatOppThree
		}
		if len(threat.ExtensionSquares) > 1 {
			flags |= rootThreatForkPrevent
		}
	}
	return flags
}

// ================================================================
// Evaluation utilities
// Position scoring helpers and threat tier classification.
// ================================================================

func evaluateStateDetailedWithEvaluator(state GameState, settings AIScoreSettings, evalState *EvalState) EvalResult {
	if evalState != nil {
		return EvalResult{
			Score:           evalState.Score,
			StructuralScore: evalState.StructuralScore,
			CaptureScore:    evalState.CaptureScore,
			ComboScore:      evalState.ComboScore,
			Summary:         evalState.Summary,
		}
	}
	return EvaluateBoardWithContext(
		state.Board,
		state.ToMove,
		clampUint8(state.CapturedBlue),
		clampUint8(state.CapturedRed),
		settings.Config,
	)
}

func bestThreatTier(threats []Threat) ThreatTier {
	best := TierNone
	for _, threat := range threats {
		if threat.Tier > best {
			best = threat.Tier
		}
	}
	return best
}

func bestThreatTierFromSummary(summary TacticalSummary, player PlayerColor) ThreatTier {
	switch player {
	case PlayerBlue:
		switch {
		case summary.WinNowBlue > 0:
			return TierWinning
		case summary.Open4Blue > 0:
			return TierCritical
		case summary.Closed4Blue > 0 || summary.Broken4Blue > 0:
			return TierMustAnswer
		case summary.Open3Blue > 0 || summary.Broken3Blue > 0:
			return TierStrong
		default:
			return TierNone
		}
	case PlayerRed:
		switch {
		case summary.WinNowRed > 0:
			return TierWinning
		case summary.Open4Red > 0:
			return TierCritical
		case summary.Closed4Red > 0 || summary.Broken4Red > 0:
			return TierMustAnswer
		case summary.Open3Red > 0 || summary.Broken3Red > 0:
			return TierStrong
		default:
			return TierNone
		}
	default:
		return TierNone
	}
}

func bestThreatPatternFromSummary(summary TacticalSummary, player PlayerColor) PatternType {
	switch player {
	case PlayerBlue:
		switch {
		case summary.WinNowBlue > 0:
			return PatternWin5
		case summary.Open4Blue > 0:
			return PatternOpen4
		case summary.Closed4Blue > 0:
			return PatternClosed4
		case summary.Broken4Blue > 0:
			return PatternBroken4
		case summary.Open3Blue > 0:
			return PatternOpen3
		case summary.Broken3Blue > 0:
			return PatternBroken3
		default:
			return PatternNone
		}
	case PlayerRed:
		switch {
		case summary.WinNowRed > 0:
			return PatternWin5
		case summary.Open4Red > 0:
			return PatternOpen4
		case summary.Closed4Red > 0:
			return PatternClosed4
		case summary.Broken4Red > 0:
			return PatternBroken4
		case summary.Open3Red > 0:
			return PatternOpen3
		case summary.Broken3Red > 0:
			return PatternBroken3
		default:
			return PatternNone
		}
	default:
		return PatternNone
	}
}

func threatsForPlayerResult(result EvalResult, player PlayerColor) []Threat {
	if result.ThreatCount == 0 {
		return nil
	}
	out := make([]Threat, 0, result.ThreatCount)
	for i := 0; i < int(result.ThreatCount); i++ {
		if result.Threats[i].Owner == player {
			out = append(out, result.Threats[i])
		}
	}
	return out
}

func uniqueMoves(moves []Move, boardSize int) []Move {
	if len(moves) == 0 || boardSize <= 0 {
		return nil
	}
	seen := make([]bool, boardSize*boardSize)
	out := make([]Move, 0, len(moves))
	for _, move := range moves {
		if !move.IsValid(boardSize) {
			continue
		}
		idx := move.Y*boardSize + move.X
		if seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, move)
	}
	return out
}

func filterLegalMoves(state GameState, rules Rules, player PlayerColor, moves []Move, boardSize int) []Move {
	if len(moves) == 0 {
		return nil
	}
	filtered := make([]Move, 0, len(moves))
	for _, move := range uniqueMoves(moves, boardSize) {
		if ok, _ := rules.IsLegal(state, move, player); !ok {
			continue
		}
		filtered = append(filtered, move)
	}
	return filtered
}

func filterLegalThreatResponseDetails(state GameState, rules Rules, player PlayerColor, details []ThreatResponseMove, boardSize int) []ThreatResponseMove {
	if len(details) == 0 {
		return nil
	}
	filtered := make([]ThreatResponseMove, 0, len(details))
	for _, detail := range dedupeThreatResponseDetails(details, boardSize) {
		if ok, _ := rules.IsLegal(state, detail.Move, player); !ok {
			continue
		}
		filtered = append(filtered, detail)
	}
	return filtered
}

func movesFromThreatPositions(positions []Pos) []Move {
	out := make([]Move, 0, len(positions))
	for _, pos := range positions {
		out = append(out, Move{X: pos.X, Y: pos.Y})
	}
	return out
}

func computeThreatUrgency(state GameState, rules Rules, settings AIScoreSettings, threat Threat) Threat {
	_ = state
	_ = rules
	_ = settings
	out := threat
	out.BestFollowupTier = TierNone
	out.NumStrongExtensions = 0
	out.RealDefenseCount = len(uniqueMoves(movesFromThreatPositions(threat.DefenseSquares), state.Board.Size()))
	out.ForkPotential = out.ForkPotential || len(threat.ExtensionSquares) >= 2
	out.Tier = staticThreatTier(threat.Type)
	out.UrgencyScore = float64(threatSeverityForPattern(PatternType(threat.Type)))
	return out
}

// ================================================================
// Threat response generation
// Converting threat data into forced/blocking moves.
// ================================================================

func GenerateThreatCandidates(context ThreatContext, state GameState, rules Rules) []candidateMove {
	boardSize := state.Board.Size()
	seen := make(map[moveKey]int, 32)
	out := make([]candidateMove, 0, 32)
	addMoves := func(moves []Move, priority int) {
		for _, move := range uniqueMoves(moves, boardSize) {
			if ok, _ := rules.IsLegal(state, move, context.SideToMove); !ok {
				continue
			}
			key := moveKey{X: move.X, Y: move.Y}
			if idx, ok := seen[key]; ok {
				if priority < out[idx].priority {
					out[idx].priority = priority
				}
				continue
			}
			seen[key] = len(out)
			out = append(out, candidateMove{move: move, priority: priority})
		}
	}
	addMoves(context.WinningMoves, prioWin)
	addMoves(selectedMustResponseMoves(context, boardSize), prioCreateFour)
	addMoves(context.CaptureMoves, prioCaptureCreate)
	addMoves(context.CaptureDefenseMoves, prioCapturePrevent)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].priority != out[j].priority {
			return out[i].priority < out[j].priority
		}
		if out[i].move.Y != out[j].move.Y {
			return out[i].move.Y < out[j].move.Y
		}
		return out[i].move.X < out[j].move.X
	})
	return out
}


func appendThreatResponseDetails(dst []ThreatResponseMove, moves []Move, threat Threat, kind ThreatResponseKind, boardSize int) []ThreatResponseMove {
	moves = uniqueMoves(moves, boardSize)
	severity := threatSeverityForPattern(PatternType(threat.Type))
	for _, move := range moves {
		winTempo, forceTempo, bestTempo := responseTimingForPattern(PatternType(threat.Type))
		dst = append(dst, ThreatResponseMove{
			Move:       move,
			Pattern:    PatternType(threat.Type),
			Severity:   severity,
			Tempo:      bestTempo,
			WinTempo:   winTempo,
			ForceTempo: forceTempo,
			Tier:       threat.Tier,
			Kind:       kind,
		})
	}
	return dst
}

func responseMovesFromScores(state GameState, scores []int32, boardSize int) []Move {
	if len(scores) == 0 {
		return nil
	}
	out := make([]Move, 0, 8)
	limit := len(scores)
	if len(state.Board.cells) < limit {
		limit = len(state.Board.cells)
	}
	for idx := 0; idx < limit; idx++ {
		if scores[idx] <= 0 || state.Board.cells[idx] != CellEmpty {
			continue
		}
		out = append(out, moveFromCellIndex(boardSize, idx))
	}
	return out
}

func mustResponsePattern(score int32) PatternType {
	switch {
	case score >= 100:
		return PatternWin5
	case score >= 90:
		return PatternOpen4
	case score >= 70:
		return PatternClosed4
	case score >= 50:
		return PatternOpen3
	case score >= 35:
		return PatternBroken3
	default:
		return PatternOpen2
	}
}

func responseDetailsFromScores(state GameState, scores []int32, boardSize int, kind ThreatResponseKind) []ThreatResponseMove {
	return responseDetailsFromScoresWithPattern(state, scores, boardSize, kind, PatternNone)
}

func responseDetailsFromScoresWithPattern(state GameState, scores []int32, boardSize int, kind ThreatResponseKind, forcedPattern PatternType) []ThreatResponseMove {
	if len(scores) == 0 {
		return nil
	}
	out := make([]ThreatResponseMove, 0, 8)
	limit := len(scores)
	if len(state.Board.cells) < limit {
		limit = len(state.Board.cells)
	}
	for idx := 0; idx < limit; idx++ {
		score := scores[idx]
		if score <= 0 || state.Board.cells[idx] != CellEmpty {
			continue
		}
		pattern := forcedPattern
		if pattern == PatternNone {
			pattern = mustResponsePattern(score)
		}
		winTempo, forceTempo, tempo := responseTimingForPattern(pattern)
		out = append(out, ThreatResponseMove{
			Move:       moveFromCellIndex(boardSize, idx),
			Pattern:    pattern,
			Severity:   threatSeverityForPattern(pattern),
			Tempo:      tempo,
			WinTempo:   winTempo,
			ForceTempo: forceTempo,
			Tier:       staticThreatTier(ThreatType(pattern)),
			Kind:       kind,
		})
	}
	return out
}

func responseTimingForPattern(pattern PatternType) (winTempo, forceTempo, tempo int) {
	winTempo = -1
	forceTempo = -1
	switch pattern {
	case PatternWin5:
		winTempo = 0
		forceTempo = 0
	case PatternOpen4:
		winTempo = 1
		forceTempo = 1
	case PatternClosed4, PatternBroken4:
		forceTempo = 1
	case PatternOpen3:
		forceTempo = 2
	case PatternBroken3, PatternClosed3:
		forceTempo = 3
	case PatternOpen2:
		forceTempo = 4
	case PatternClosed2:
		forceTempo = 5
	case PatternBroken2:
		forceTempo = 6
	}
	tempo = bestResponseTempo(winTempo, forceTempo)
	return winTempo, forceTempo, tempo
}

func bestResponseTempo(winTempo, forceTempo int) int {
	best := -1
	if winTempo >= 0 {
		best = winTempo
	}
	if forceTempo >= 0 && (best < 0 || forceTempo < best) {
		best = forceTempo
	}
	return best
}

const responseUrgencyUnknown = int(^uint(0) >> 1)

func responseUrgencyPly(detail ThreatResponseMove) int {
	tempo := bestResponseTempo(detail.WinTempo, detail.ForceTempo)
	if tempo < 0 {
		return responseUrgencyUnknown
	}
	if detail.Kind == ThreatResponseMustBlock {
		ply := tempo*2 - 1
		if ply < 0 {
			return 0
		}
		return ply
	}
	return tempo * 2
}

func bestThreatPattern(threats []Threat) PatternType {
	best := PatternNone
	bestSeverity := -1
	for _, threat := range threats {
		pattern := PatternType(threat.Type)
		severity := threatSeverityForPattern(pattern)
		if severity > bestSeverity {
			best = pattern
			bestSeverity = severity
		}
	}
	return best
}

func summaryTriggersHardTactical(summary TacticalSummary, player PlayerColor) bool {
	switch player {
	case PlayerBlue:
		return summary.WinNowBlue > 0 || summary.Open4Blue > 0 || summary.Closed4Blue > 0 || summary.Broken4Blue > 0
	case PlayerRed:
		return summary.WinNowRed > 0 || summary.Open4Red > 0 || summary.Closed4Red > 0 || summary.Broken4Red > 0
	default:
		return false
	}
}

func summaryTriggersSoftTactical(summary TacticalSummary, player PlayerColor) bool {
	switch player {
	case PlayerBlue:
		return summary.Open3Blue > 0 || summary.Broken3Blue > 0
	case PlayerRed:
		return summary.Open3Red > 0 || summary.Broken3Red > 0
	default:
		return false
	}
}

func evalStateThreatResponseArrays(evalState *EvalState, player PlayerColor) (win []int32, mustPlay []int32, counter []int32, fork []int32, captureRace []int32, mustBlock []int32, preventFork []int32) {
	if evalState == nil {
		return nil, nil, nil, nil, nil, nil, nil
	}
	if player == PlayerBlue {
		return evalState.blueRespWin, evalState.blueRespMustPlay, evalState.blueRespCounter, evalState.blueRespFork, evalState.blueRespCaptureRace, evalState.blueRespMustBlock, evalState.blueRespPreventFork
	}
	return evalState.redRespWin, evalState.redRespMustPlay, evalState.redRespCounter, evalState.redRespFork, evalState.redRespCaptureRace, evalState.redRespMustBlock, evalState.redRespPreventFork
}

func dedupeThreatResponseDetails(details []ThreatResponseMove, boardSize int) []ThreatResponseMove {
	if len(details) == 0 {
		return nil
	}
	bestByIdx := make(map[int]ThreatResponseMove, len(details))
	for _, detail := range details {
		if !detail.Move.IsValid(boardSize) {
			continue
		}
		idx := detail.Move.Y*boardSize + detail.Move.X
		current, ok := bestByIdx[idx]
		if !ok || compareThreatResponseDetails(detail, current) < 0 {
			bestByIdx[idx] = detail
		}
	}
	out := make([]ThreatResponseMove, 0, len(bestByIdx))
	for _, detail := range bestByIdx {
		out = append(out, detail)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return compareThreatResponseDetails(out[i], out[j]) < 0
	})
	return out
}

func compareThreatResponseDetails(left, right ThreatResponseMove) int {
	leftUrgency := responseUrgencyPly(left)
	rightUrgency := responseUrgencyPly(right)
	if leftUrgency != rightUrgency {
		if leftUrgency < rightUrgency {
			return -1
		}
		return 1
	}
	if left.WinTempo != right.WinTempo {
		if left.WinTempo < 0 {
			return 1
		}
		if right.WinTempo < 0 {
			return -1
		}
		if left.WinTempo < right.WinTempo {
			return -1
		}
		return 1
	}
	if left.ForceTempo != right.ForceTempo {
		if left.ForceTempo < 0 {
			return 1
		}
		if right.ForceTempo < 0 {
			return -1
		}
		if left.ForceTempo < right.ForceTempo {
			return -1
		}
		return 1
	}
	if left.Tempo != right.Tempo {
		if left.Tempo < right.Tempo {
			return -1
		}
		return 1
	}
	if left.Severity != right.Severity {
		if left.Severity > right.Severity {
			return -1
		}
		return 1
	}
	if left.Kind != right.Kind {
		if left.Kind == ThreatResponseMustPlay {
			return -1
		}
		return 1
	}
	if left.Move.Y != right.Move.Y {
		if left.Move.Y < right.Move.Y {
			return -1
		}
		return 1
	}
	if left.Move.X != right.Move.X {
		if left.Move.X < right.Move.X {
			return -1
		}
		return 1
	}
	return 0
}

func selectedThreatResponseDetails(context ThreatContext, boardSize int) []ThreatResponseMove {
	candidates := make([]ThreatResponseMove, 0, len(context.MustPlayDetails)+len(context.MustBlockDetails))
	bestUrgency := bestThreatResponseUrgency(context.MustPlayDetails, context.MustBlockDetails)
	if bestUrgency < 0 {
		return nil
	}
	for _, detail := range context.MustPlayDetails {
		if responseUrgencyPly(detail) == bestUrgency {
			candidates = append(candidates, detail)
		}
	}
	for _, detail := range context.MustBlockDetails {
		if responseUrgencyPly(detail) == bestUrgency {
			candidates = append(candidates, detail)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return dedupeThreatResponseDetails(candidates, boardSize)
}

func bestThreatResponseTempo(details []ThreatResponseMove) int {
	best := -1
	for _, detail := range details {
		if best < 0 || detail.Tempo < best {
			best = detail.Tempo
		}
	}
	return best
}

func bestThreatResponseUrgency(groups ...[]ThreatResponseMove) int {
	best := -1
	for _, details := range groups {
		for _, detail := range details {
			urgency := responseUrgencyPly(detail)
			if urgency == responseUrgencyUnknown {
				continue
			}
			if best < 0 || urgency < best {
				best = urgency
			}
		}
	}
	return best
}

func selectedMustResponseMoves(context ThreatContext, boardSize int) []Move {
	details := selectedThreatResponseDetails(context, boardSize)
	moves := make([]Move, 0, len(details))
	for _, detail := range details {
		moves = append(moves, detail.Move)
	}
	return moves
}

func selectedMustPlayMoves(context ThreatContext, boardSize int) []Move {
	details := selectedThreatResponseDetails(context, boardSize)
	moves := make([]Move, 0, len(details))
	for _, detail := range details {
		if detail.Kind == ThreatResponseMustPlay {
			moves = append(moves, detail.Move)
		}
	}
	moves = uniqueMoves(moves, boardSize)
	if len(context.MustPlayMoves) == 0 {
		return moves
	}
	allowed := make(map[int]struct{}, len(context.MustPlayMoves))
	for _, move := range uniqueMoves(context.MustPlayMoves, boardSize) {
		allowed[move.Y*boardSize+move.X] = struct{}{}
	}
	filtered := make([]Move, 0, len(moves))
	for _, move := range moves {
		if _, ok := allowed[move.Y*boardSize+move.X]; ok {
			filtered = append(filtered, move)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return uniqueMoves(context.MustPlayMoves, boardSize)
}

// isDecisiveBlockPattern returns true for threats that require an immediate forced block
// (Win5, Open4, Closed4, Broken4). Open3 and below are NOT forced blocks — the AI
// should evaluate them freely against its own counter-threats.
func isDecisiveBlockPattern(pattern PatternType) bool {
	switch pattern {
	case PatternWin5, PatternOpen4, PatternClosed4, PatternBroken4:
		return true
	default:
		return false
	}
}

func selectedMustBlockMoves(context ThreatContext, boardSize int) []Move {
	details := selectedThreatResponseDetails(context, boardSize)
	moves := make([]Move, 0, len(details))
	for _, detail := range details {
		if detail.Kind == ThreatResponseMustBlock && isDecisiveBlockPattern(detail.Pattern) {
			moves = append(moves, detail.Move)
		}
	}
	moves = uniqueMoves(moves, boardSize)
	if len(context.MustBlockMoves) == 0 {
		return moves
	}
	allowed := make(map[int]struct{}, len(context.MustBlockMoves))
	for _, move := range uniqueMoves(context.MustBlockMoves, boardSize) {
		allowed[move.Y*boardSize+move.X] = struct{}{}
	}
	filtered := make([]Move, 0, len(moves))
	for _, move := range moves {
		if _, ok := allowed[move.Y*boardSize+move.X]; ok {
			filtered = append(filtered, move)
		}
	}
	return filtered
}

func summaryNeedsStrongThreatDetails(summary TacticalSummary) bool {
	return summary.IsTactical ||
		summary.MustAnswerForBlue ||
		summary.MustAnswerForRed ||
		summary.ForcingThreatsBlue > 0 ||
		summary.ForcingThreatsRed > 0 ||
		summary.Open3Blue > 0 ||
		summary.Open3Red > 0
}

func hasForcedThreatResponses(context ThreatContext, boardSize int) bool {
	return len(context.WinningMoves) > 0 ||
		len(selectedMustPlayMoves(context, boardSize)) > 0 ||
		len(selectedMustBlockMoves(context, boardSize)) > 0 ||
		context.CaptureDefenseForced
}

func tierTriggersTactical(threats []Threat) bool {
	for _, threat := range threats {
		switch threat.Type {
		case ThreatWin5, ThreatOpen4, ThreatClosed4, ThreatBroken4:
			return true
		}
	}
	return false
}

func threatsTriggerSoftTactical(threats []Threat) bool {
	for _, threat := range threats {
		switch threat.Type {
		case ThreatBroken3:
			return true
		}
	}
	return false
}

// ================================================================
// Threat analysis engine
// Builds a ThreatContext describing tactical obligations.
// ================================================================

// AnalyzeThreats inspects the current position from sideToMove's perspective and
// returns a ThreatContext describing every tactical obligation: winning moves,
// forced responses to opponent threats, capture attacks and defenses, and fork
// opportunities. The returned context is used by candidate selection to restrict
// or order moves at both root and interior nodes.
func AnalyzeThreats(state GameState, rules Rules, settings AIScoreSettings, sideToMove PlayerColor, evalState *EvalState) ThreatContext {
	stats := settings.Stats
	if stats != nil {
		stats.AnalyzeThreatCalls++
		if evalState != nil {
			stats.AnalyzeThreatEvalStateHits++
		}
	}
	evalStart := time.Now()
	eval := evaluateStateDetailedWithEvaluator(state, settings, evalState)
	if stats != nil {
		stats.AnalyzeThreatEvalTime += time.Since(evalStart)
		if summaryNeedsStrongThreatDetails(eval.Summary) {
			stats.AnalyzeThreatStrongCalls++
		}
	}
	context := ThreatContext{SideToMove: sideToMove}
	var captureThreats []CaptureThreat
	captureStart := time.Now()
	ownImmediateCaptures := uniqueMoves(findCaptureMoves(state, rules, sideToMove, evalState), settings.BoardSize)
	oppImmediateCaptures := uniqueMoves(findCaptureMoves(state, rules, otherPlayer(sideToMove), evalState), settings.BoardSize)
	if stats != nil {
		stats.AnalyzeThreatCaptureTime += time.Since(captureStart)
	}
	if evalState == nil && summaryNeedsStrongThreatDetails(eval.Summary) {
		detailStart := time.Now()
		context.OwnThreats = append([]Threat(nil), threatsForPlayerResult(eval, sideToMove)...)
		context.OppThreats = append([]Threat(nil), threatsForPlayerResult(eval, otherPlayer(sideToMove))...)
		captureThreats = append(captureThreats, captureThreatsForPlayerDetails(ThreatDetails{
			CaptureThreatCount: eval.CaptureThreatCount,
			CaptureThreats:     eval.CaptureThreats,
		}, sideToMove)...)
		captureThreats = append(captureThreats, captureThreatsForPlayerDetails(ThreatDetails{
			CaptureThreatCount: eval.CaptureThreatCount,
			CaptureThreats:     eval.CaptureThreats,
		}, otherPlayer(sideToMove))...)
		if stats != nil {
			stats.AnalyzeThreatDetailTime += time.Since(detailStart)
		}
	}
	if evalState != nil {
		context.OwnBestTier = bestThreatTierFromSummary(eval.Summary, sideToMove)
		context.OppBestTier = bestThreatTierFromSummary(eval.Summary, otherPlayer(sideToMove))
	} else {
		urgencyStart := time.Now()
		for i := range context.OwnThreats {
			context.OwnThreats[i] = computeThreatUrgency(state, rules, settings, context.OwnThreats[i])
		}
		for i := range context.OppThreats {
			context.OppThreats[i] = computeThreatUrgency(state, rules, settings, context.OppThreats[i])
		}
		context.OwnBestTier = bestThreatTier(context.OwnThreats)
		context.OppBestTier = bestThreatTier(context.OppThreats)
		if stats != nil {
			stats.AnalyzeThreatUrgencyTime += time.Since(urgencyStart)
		}
	}

	cache := selectCache(minimaxContext{settings: settings})
	winStart := time.Now()
	if evalState != nil {
		selfWin, selfMustPlay, selfCounter, selfFork, selfCaptureRace, _, _ := evalStateThreatResponseArrays(evalState, sideToMove)
		_, _, _, _, _, oppMustBlock, oppPreventFork := evalStateThreatResponseArrays(evalState, otherPlayer(sideToMove))
		bestOwnPattern := bestThreatPatternFromSummary(eval.Summary, sideToMove)
		bestOppPattern := bestThreatPatternFromSummary(eval.Summary, otherPlayer(sideToMove))
		context.WinningMoves = uniqueMoves(responseMovesFromScores(state, selfWin, settings.BoardSize), settings.BoardSize)
		context.MustPlayMoves = append(context.MustPlayMoves, responseMovesFromScores(state, selfMustPlay, settings.BoardSize)...)
		context.MustPlayDetails = append(context.MustPlayDetails, responseDetailsFromScoresWithPattern(state, selfMustPlay, settings.BoardSize, ThreatResponseMustPlay, bestOwnPattern)...)
		if capturesRemaining(state, rules, otherPlayer(sideToMove)) <= 2 {
			context.StabilizationMoves = append(context.StabilizationMoves, responseMovesFromScores(state, selfCaptureRace, settings.BoardSize)...)
		}
		context.CounterThreatMoves = append(context.CounterThreatMoves, responseMovesFromScores(state, selfCounter, settings.BoardSize)...)
		context.ForkMoves = append(context.ForkMoves, responseMovesFromScores(state, selfFork, settings.BoardSize)...)
		context.MustBlockMoves = append(context.MustBlockMoves, responseMovesFromScores(state, oppMustBlock, settings.BoardSize)...)
		// Only treat opponent moves as forced blocks for decisive threats (Win5/Open4/Closed4/Broken4).
		// Open3 and below must not restrict the search to defensive-only candidates; they belong
		// in the candidate pool where the minimax can freely compare them against counter-threats.
		if isDecisiveBlockPattern(bestOppPattern) {
			context.MustBlockDetails = append(context.MustBlockDetails, responseDetailsFromScoresWithPattern(state, oppMustBlock, settings.BoardSize, ThreatResponseMustBlock, bestOppPattern)...)
		}
		context.PreventForkMoves = append(context.PreventForkMoves, responseMovesFromScores(state, oppPreventFork, settings.BoardSize)...)
	} else {
		context.WinningMoves = uniqueMoves(findImmediateWinMovesCached(cache, state, rules, sideToMove, settings.BoardSize, settings.Config), settings.BoardSize)
	}
	if len(context.WinningMoves) == 0 {
		context.WinningMoves = uniqueMoves(findCaptureWinMoves(state, rules, sideToMove), settings.BoardSize)
	}
	if stats != nil {
		stats.AnalyzeThreatWinTime += time.Since(winStart)
	}
	responseStart := time.Now()
	if context.OppBestTier >= TierWinning && (evalState == nil || len(context.MustBlockMoves) == 0) {
		context.MustBlockMoves = uniqueMoves(findBlockingMoves(cache, state, rules, sideToMove, settings.BoardSize, settings.Config), settings.BoardSize)
		for _, move := range context.MustBlockMoves {
			winTempo, forceTempo, tempo := responseTimingForPattern(PatternWin5)
			context.MustBlockDetails = append(context.MustBlockDetails, ThreatResponseMove{
				Move:       move,
				Pattern:    PatternWin5,
				Severity:   threatSeverityForPattern(PatternWin5),
				Tempo:      tempo,
				WinTempo:   winTempo,
				ForceTempo: forceTempo,
				Tier:       TierWinning,
				Kind:       ThreatResponseMustBlock,
			})
		}
	}
	if hasDecisiveCaptureThreatWithEval(state, rules, otherPlayer(sideToMove), evalState) {
		context.CaptureDefenseForced = true
		context.CaptureDefenseMoves = uniqueMoves(findCaptureThreatResponsesWithEval(state, rules, sideToMove, otherPlayer(sideToMove), settings.BoardSize, evalState), settings.BoardSize)
	}
	if len(context.CaptureDefenseMoves) == 0 && len(oppImmediateCaptures) > 0 {
		context.CaptureDefenseMoves = uniqueMoves(findImmediateCaptureDefenseMovesWithEval(state, rules, sideToMove, otherPlayer(sideToMove), settings.BoardSize, evalState), settings.BoardSize)
		if len(context.CaptureDefenseMoves) == 0 {
			context.CaptureDefenseMoves = append(context.CaptureDefenseMoves, oppImmediateCaptures...)
		}
	}
	context.CaptureMoves = append(context.CaptureMoves, ownImmediateCaptures...)
	for _, captureThreat := range captureThreats {
		move := moveFromCellIndex(settings.BoardSize, int(captureThreat.Move))
		if captureThreat.Owner == sideToMove {
			if captureThreat.GivesImmediateWin {
				context.WinningMoves = append(context.WinningMoves, move)
				continue
			}
			if captureThreat.BreaksEnemyOpen4 || captureThreat.BreaksEnemyThreat || captureThreat.CreatesOwnThreat {
				context.CaptureMoves = append(context.CaptureMoves, move)
			}
		} else if captureThreat.GivesImmediateWin || captureThreat.BreaksEnemyOpen4 || captureThreat.BreaksEnemyThreat {
			context.CaptureDefenseMoves = append(context.CaptureDefenseMoves, move)
		}
	}

	if evalState == nil {
		for _, threat := range context.OwnThreats {
			moves := uniqueMoves(movesFromThreatPositions(threat.ExtensionSquares), settings.BoardSize)
			switch threat.Tier {
			case TierWinning:
				context.WinningMoves = append(context.WinningMoves, moves...)
			case TierCritical:
				context.MustPlayMoves = append(context.MustPlayMoves, moves...)
				context.MustPlayDetails = appendThreatResponseDetails(context.MustPlayDetails, moves, threat, ThreatResponseMustPlay, settings.BoardSize)
				context.CounterThreatMoves = append(context.CounterThreatMoves, moves...)
			case TierMustAnswer:
				context.MustPlayMoves = append(context.MustPlayMoves, moves...)
				context.MustPlayDetails = appendThreatResponseDetails(context.MustPlayDetails, moves, threat, ThreatResponseMustPlay, settings.BoardSize)
				if threat.ForkPotential {
					context.ForkMoves = append(context.ForkMoves, moves...)
				} else {
					context.CounterThreatMoves = append(context.CounterThreatMoves, moves...)
				}
			case TierStrong:
				if threat.ForkPotential {
					context.ForkMoves = append(context.ForkMoves, moves...)
				} else {
					context.CounterThreatMoves = append(context.CounterThreatMoves, moves...)
				}
			}
		}
		for _, threat := range context.OppThreats {
			defenses := uniqueMoves(movesFromThreatPositions(threat.DefenseSquares), settings.BoardSize)
			switch threat.Tier {
			case TierWinning, TierCritical, TierMustAnswer:
				context.MustBlockMoves = append(context.MustBlockMoves, defenses...)
				context.MustBlockDetails = appendThreatResponseDetails(context.MustBlockDetails, defenses, threat, ThreatResponseMustBlock, settings.BoardSize)
			case TierStrong:
				context.PreventForkMoves = append(context.PreventForkMoves, defenses...)
			}
			if threat.ForkPotential {
				context.PreventForkMoves = append(context.PreventForkMoves, defenses...)
			}
		}
	}
	if stats != nil {
		stats.AnalyzeThreatResponseTime += time.Since(responseStart)
	}
	filterStart := time.Now()
	context.WinningMoves = filterLegalMoves(state, rules, sideToMove, context.WinningMoves, settings.BoardSize)
	context.MustPlayMoves = filterLegalMoves(state, rules, sideToMove, context.MustPlayMoves, settings.BoardSize)
	context.MustBlockMoves = filterLegalMoves(state, rules, sideToMove, context.MustBlockMoves, settings.BoardSize)
	context.MustPlayDetails = filterLegalThreatResponseDetails(state, rules, sideToMove, context.MustPlayDetails, settings.BoardSize)
	context.MustBlockDetails = filterLegalThreatResponseDetails(state, rules, sideToMove, context.MustBlockDetails, settings.BoardSize)
	context.CounterThreatMoves = filterLegalMoves(state, rules, sideToMove, context.CounterThreatMoves, settings.BoardSize)
	context.ForkMoves = filterLegalMoves(state, rules, sideToMove, context.ForkMoves, settings.BoardSize)
	context.PreventForkMoves = filterLegalMoves(state, rules, sideToMove, context.PreventForkMoves, settings.BoardSize)
	context.CaptureMoves = filterLegalMoves(state, rules, sideToMove, context.CaptureMoves, settings.BoardSize)
	context.CaptureDefenseMoves = filterLegalMoves(state, rules, sideToMove, context.CaptureDefenseMoves, settings.BoardSize)
	// When the opponent has a hard threat (4-in-a-row or better), keep only
	// captures that directly break that threat. Capturing elsewhere only buys
	// one tempo while the opponent completes their forcing sequence.
	if summaryTriggersHardTactical(eval.Summary, otherPlayer(sideToMove)) || len(context.MustBlockMoves) > 0 {
		breaking := make(map[int]struct{}, len(captureThreats))
		for _, ct := range captureThreats {
			if ct.Owner == sideToMove && (ct.BreaksEnemyOpen4 || ct.BreaksEnemyThreat) {
				breaking[int(ct.Move)] = struct{}{}
			}
		}
		filtered := make([]Move, 0, len(context.CaptureMoves))
		for _, m := range context.CaptureMoves {
			if _, ok := breaking[m.Y*settings.BoardSize+m.X]; ok {
				filtered = append(filtered, m)
			}
		}
		context.CaptureMoves = filtered
	}
	context.StabilizationMoves = nil
	if evalState != nil {
		context.IsHardTactical =
			summaryTriggersHardTactical(eval.Summary, sideToMove) ||
				summaryTriggersHardTactical(eval.Summary, otherPlayer(sideToMove)) ||
				len(context.WinningMoves) > 0 ||
				len(selectedMustPlayMoves(context, settings.BoardSize)) > 0 ||
				len(selectedMustBlockMoves(context, settings.BoardSize)) > 0 ||
				context.CaptureDefenseForced
		if !context.IsHardTactical && (summaryTriggersSoftTactical(eval.Summary, sideToMove) ||
			summaryTriggersSoftTactical(eval.Summary, otherPlayer(sideToMove)) ||
			len(context.CaptureMoves) > 0) {
			context.IsSoftTactical = true
		}
	} else {
		if tierTriggersTactical(context.OwnThreats) ||
			tierTriggersTactical(context.OppThreats) ||
			len(context.WinningMoves) > 0 ||
			context.CaptureDefenseForced {
			context.IsHardTactical = true
		}
		if !context.IsHardTactical && (threatsTriggerSoftTactical(context.OwnThreats) || threatsTriggerSoftTactical(context.OppThreats) || len(context.CaptureMoves) > 0) {
			context.IsSoftTactical = true
		}
	}
	if stats != nil {
		stats.AnalyzeThreatFilterTime += time.Since(filterStart)
	}
	return context
}

// ================================================================
// Root move pool
// Builds and restricts the candidate set for the root node.
// ================================================================

type rootMoveBuilder struct {
	state         GameState
	ctx           minimaxContext
	currentPlayer PlayerColor
	boardSize     int
	moves         []RootMove
	byIndex       map[int]int
}

func newRootMoveBuilder(state GameState, ctx minimaxContext, currentPlayer PlayerColor) *rootMoveBuilder {
	boardSize := ctx.settings.BoardSize
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	return &rootMoveBuilder{
		state:         state,
		ctx:           ctx,
		currentPlayer: currentPlayer,
		boardSize:     boardSize,
		moves:         make([]RootMove, 0, 64),
		byIndex:       make(map[int]int, 64),
	}
}

func (b *rootMoveBuilder) addMove(move Move, source uint32, priority int, forced bool, update func(*RootMove)) {
	if b == nil || !move.IsValid(b.boardSize) {
		return
	}
	if !b.state.Board.IsEmpty(move.X, move.Y) {
		return
	}
	if ok, _ := b.ctx.rules.IsLegal(b.state, move, b.currentPlayer); !ok {
		return
	}
	idx := move.Y*b.boardSize + move.X
	rmIndex, ok := b.byIndex[idx]
	if !ok {
		rmIndex = len(b.moves)
		b.byIndex[idx] = rmIndex
		b.moves = append(b.moves, RootMove{
			Move:             move,
			TacticalPriority: priority,
			SourceFlags:      source,
			IsForced:         forced,
		})
	} else {
		b.moves[rmIndex].SourceFlags |= source
		if priority < b.moves[rmIndex].TacticalPriority {
			b.moves[rmIndex].TacticalPriority = priority
		}
		if forced {
			b.moves[rmIndex].IsForced = true
		}
	}
	if update != nil {
		update(&b.moves[rmIndex])
	}
}

// buildRootMovePool builds the ordered set of root candidates for the current
// position. It starts from tactical obligations (immediate wins, forced blocks,
// captures) and falls back to quiet locality moves. When the position has forcing
// constraints (must-play or must-block moves), the pool is restricted to only
// those moves to avoid wasting search budget on irrelevant alternatives.
func buildRootMovePool(state GameState, ctx minimaxContext, currentPlayer PlayerColor) []RootMove {
	builder := newRootMoveBuilder(state, ctx, currentPlayer)
	if builder.boardSize <= 0 {
		return nil
	}
	opponent := otherPlayer(currentPlayer)
	threatContext := AnalyzeThreats(state, ctx.rules, ctx.settings, currentPlayer, ctx.evalState)

	for _, move := range threatContext.WinningMoves {
		builder.addMove(move, rootSourceImmediateWin, prioWin, true, func(rm *RootMove) {
			rm.ThreatFlags |= rootThreatOwnWin
			if rm.ThreatSeverity < threatSeverityForPattern(PatternWin5) {
				rm.ThreatSeverity = threatSeverityForPattern(PatternWin5)
			}
		})
	}

	for _, move := range selectedMustPlayMoves(threatContext, builder.boardSize) {
		builder.addMove(move, rootSourceThreatOwn, prioCreateFour, true, func(rm *RootMove) {
			rm.ThreatFlags |= rootThreatOwnFour
			if rm.ThreatSeverity < 80 {
				rm.ThreatSeverity = 80
			}
		})
	}

	for _, move := range selectedMustBlockMoves(threatContext, builder.boardSize) {
		builder.addMove(move, rootSourceImmediateBlock, prioBlockWin, true, func(rm *RootMove) {
			rm.ThreatFlags |= rootThreatOppWin
			if rm.ThreatSeverity < threatSeverityForPattern(PatternWin5) {
				rm.ThreatSeverity = threatSeverityForPattern(PatternWin5)
			}
		})
	}

	for _, move := range threatContext.CaptureMoves {
		builder.addMove(move, rootSourceCaptureOwn, prioCaptureCreate, false, func(rm *RootMove) {
			captured := captureStoneCountForMove(state.Board, ctx.rules, move, currentPlayer)
			rm.ThreatFlags |= rootThreatCaptureCreate
			rm.CaptureValue += maxInt(1, captured/2)
			rm.ThreatSeverity += 35 + captured*10
		})
	}

	for _, move := range threatContext.CaptureDefenseMoves {
		builder.addMove(move, rootSourceCaptureDefense, prioCapturePrevent, threatContext.CaptureDefenseForced, func(rm *RootMove) {
			rm.ThreatFlags |= rootThreatCapturePrevent
			rm.CaptureDefense += 5
			rm.ThreatSeverity += 95
		})
	}

	for _, move := range threatContext.ForkMoves {
		builder.addMove(move, rootSourceThreatOwn, prioCreateOpen3, false, func(rm *RootMove) {
			rm.ThreatFlags |= rootThreatForkCreate
			rm.ForkPotential += 3
			if rm.ThreatSeverity < 50 {
				rm.ThreatSeverity = 50
			}
		})
	}

	for _, move := range threatContext.CounterThreatMoves {
		builder.addMove(move, rootSourceThreatOwn, prioCreateOpen3+1, false, func(rm *RootMove) {
			rm.ForkPotential++
			if rm.ThreatSeverity < 25 {
				rm.ThreatSeverity = 25
			}
		})
	}

	if shouldRestrictRootPoolToMustPlay(threatContext, builder.boardSize) {
		limitRootMovePoolToMoves(builder, uniqueMoves(append(append([]Move(nil), threatContext.WinningMoves...), selectedMustPlayMoves(threatContext, builder.boardSize)...), builder.boardSize))
		finishRootMovePool(state, builder)
		return builder.moves
	}

	if shouldRestrictRootPoolToCounterAttack(threatContext, builder.boardSize) {
		// Include PreventForkMoves so the AI can block fork creation, but not
		// waste moves defensively blocking non-fork Open3 continuations.
		ownAttackMoves := uniqueMoves(
			append(append(append(append(append([]Move(nil),
				threatContext.WinningMoves...),
				threatContext.ForkMoves...),
				threatContext.CounterThreatMoves...),
				threatContext.CaptureMoves...),
				threatContext.PreventForkMoves...),
			builder.boardSize)
		if len(ownAttackMoves) > 0 {
			limitRootMovePoolToMoves(builder, ownAttackMoves)
			finishRootMovePool(state, builder)
			return builder.moves
		}
	}

	if shouldRestrictRootPoolToForcing(threatContext, builder.moves, builder.boardSize) {
		limitRootMovePoolToForced(builder)
		finishRootMovePool(state, builder)
		return builder.moves
	}

	for _, move := range findCaptureMoves(state, ctx.rules, opponent, ctx.evalState) {
		builder.addMove(move, rootSourceCaptureOpp, prioCapturePrevent, false, func(rm *RootMove) {
			rm.ThreatFlags |= rootThreatCapturePrevent
			rm.CaptureDefense += 3
			rm.ThreatSeverity += 60
		})
	}

	if limitRootMovePoolToForced(builder) {
		finishRootMovePool(state, builder)
		return builder.moves
	}

	hasForcedResponses := len(threatContext.WinningMoves) > 0 ||
		len(selectedMustPlayMoves(threatContext, builder.boardSize)) > 0 ||
		len(selectedMustBlockMoves(threatContext, builder.boardSize)) > 0 ||
		threatContext.CaptureDefenseForced
	captureFallback := collectCandidateMovesWithEval(state, ctx.rules, currentPlayer, builder.boardSize, ctx.evalState, ctx.settings.Stats)
	if !hasForcedResponses {
		addedCaptureFallback := 0
		for _, fallback := range captureFallback {
			builder.addMove(fallback.move, rootSourceLocality, fallback.priority, false, func(rm *RootMove) {
				if fallback.priority < rm.TacticalPriority {
					rm.TacticalPriority = fallback.priority
				}
				rm.ThreatFlags |= rootThreatCaptureCreate
			})
			addedCaptureFallback++
			if addedCaptureFallback >= 10 {
				break
			}
		}
	}

	if len(builder.moves) == 0 {
		for i, fallback := range captureFallback {
			builder.addMove(fallback.move, rootSourceLocality, fallback.priority, false, func(rm *RootMove) {
				if fallback.priority < rm.TacticalPriority {
					rm.TacticalPriority = fallback.priority
				}
			})
			if i >= 9 {
				break
			}
		}
	} else if len(builder.moves) == 1 {
		for _, fallback := range captureFallback {
			if fallback.move.Equals(builder.moves[0].Move) {
				continue
			}
			builder.addMove(fallback.move, rootSourceLocality, fallback.priority, false, func(rm *RootMove) {
				if fallback.priority < rm.TacticalPriority {
					rm.TacticalPriority = fallback.priority
				}
			})
			break
		}
	}

	finishRootMovePool(state, builder)
	return builder.moves
}

func shouldRestrictRootPoolToForcing(context ThreatContext, rootMoves []RootMove, boardSize int) bool {
	if len(rootMoves) == 0 {
		return false
	}
	if len(context.WinningMoves) > 0 || len(selectedMustBlockMoves(context, boardSize)) > 0 {
		return true
	}
	return len(selectedMustPlayMoves(context, boardSize)) > 0 && context.OppBestTier <= context.OwnBestTier
}

func shouldRestrictRootPoolToMustPlay(context ThreatContext, boardSize int) bool {
	if len(context.WinningMoves) > 0 {
		return false
	}
	return len(selectedMustPlayMoves(context, boardSize)) > 0 && context.OppBestTier <= context.OwnBestTier
}

// shouldRestrictRootPoolToCounterAttack returns true when the AI has counter-threats
// (fork or Open3 creation moves) at least as strong as the opponent's threats.
// In that case, focus the root pool on own attacks rather than defensive blocking.
func shouldRestrictRootPoolToCounterAttack(context ThreatContext, boardSize int) bool {
	if len(context.WinningMoves) > 0 || len(selectedMustPlayMoves(context, boardSize)) > 0 {
		return false // already handled by other restrictions
	}
	ownHasTactical := len(context.ForkMoves) > 0 || len(context.CounterThreatMoves) > 0
	if !ownHasTactical {
		return false
	}
	return context.OwnBestTier >= TierStrong && context.OppBestTier <= context.OwnBestTier
}

func limitRootMovePoolToMoves(builder *rootMoveBuilder, moves []Move) {
	if builder == nil || len(builder.moves) == 0 || len(moves) == 0 {
		return
	}
	allowed := make(map[int]struct{}, len(moves))
	for _, move := range moves {
		if !move.IsValid(builder.boardSize) {
			continue
		}
		allowed[move.Y*builder.boardSize+move.X] = struct{}{}
	}
	filtered := make([]RootMove, 0, len(builder.moves))
	byIndex := make(map[int]int, len(allowed))
	for _, rm := range builder.moves {
		idx := rm.Move.Y*builder.boardSize + rm.Move.X
		if _, ok := allowed[idx]; !ok {
			continue
		}
		byIndex[idx] = len(filtered)
		filtered = append(filtered, rm)
	}
	builder.moves = filtered
	builder.byIndex = byIndex
}

func limitRootMovePoolToForced(builder *rootMoveBuilder) bool {
	if builder == nil || len(builder.moves) == 0 {
		return false
	}
	filtered := make([]RootMove, 0, len(builder.moves))
	byIndex := make(map[int]int, len(builder.moves))
	for _, rm := range builder.moves {
		if !rm.IsForced {
			continue
		}
		idx := rm.Move.Y*builder.boardSize + rm.Move.X
		byIndex[idx] = len(filtered)
		filtered = append(filtered, rm)
	}
	if len(filtered) == 0 {
		return false
	}
	builder.moves = filtered
	builder.byIndex = byIndex
	return true
}

func finishRootMovePool(state GameState, builder *rootMoveBuilder) {
	if builder == nil {
		return
	}
	evalSettings := builder.ctx.settings
	evalSettings.Player = builder.currentPlayer
	sharedEval := BuildEvalStateFromBoard(
		state.Board, state.ToMove,
		clampUint8(state.CapturedBlue), clampUint8(state.CapturedRed),
		evalSettings.Config,
	)
	for i := range builder.moves {
		preview := previewRootMove(state, builder.ctx.rules, evalSettings, builder.moves[i].Move, &sharedEval)
		builder.moves[i].ShallowScore = preview.score
		builder.moves[i].HasShallowScore = preview.valid
		builder.moves[i].ChildForcingScore = preview.childForcingScore
		builder.moves[i].ThreatFlags |= preview.flags
		if preview.severity > builder.moves[i].ThreatSeverity {
			builder.moves[i].ThreatSeverity = preview.severity
		}
	}
}

type rootMovePreview struct {
	score             float64
	childForcingScore int
	severity          int
	flags             uint32
	valid             bool
}

func previewRootMove(state GameState, rules Rules, settings AIScoreSettings, move Move, evalState *EvalState) rootMovePreview {
	if ok, _ := rules.IsLegal(state, move, settings.Player); !ok {
		return rootMovePreview{score: illegalScore}
	}
	next := state
	if evalState == nil {
		fresh := BuildEvalStateFromBoard(
			next.Board, next.ToMove,
			clampUint8(next.CapturedBlue), clampUint8(next.CapturedRed),
			settings.Config,
		)
		evalState = &fresh
	}
	var undo searchMoveUndo
	if !applyMoveWithUndo(&next, rules, move, settings.Player, evalState, &undo) {
		return rootMovePreview{score: illegalScore}
	}
	cache := selectCache(minimaxContext{settings: settings})
	score := evalBoardCached(next, rules, settings, cache, evalState)
	flags, forcingScore, severity := rootChildForcingFeatures(evalState.Summary, settings.Player)
	undoMoveWithUndo(&next, evalState, undo)
	return rootMovePreview{
		score:             score,
		childForcingScore: forcingScore,
		severity:          severity,
		flags:             flags,
		valid:             true,
	}
}

func rootChildForcingFeatures(summary TacticalSummary, player PlayerColor) (uint32, int, int) {
	var ownWinNow, ownCaptureWin, ownOpen4, ownForcing, ownCritical uint8
	var ownDouble bool
	var oppMustAnswer bool
	switch player {
	case PlayerRed:
		ownWinNow = summary.WinNowRed
		ownCaptureWin = summary.CaptureWinNowRed
		ownOpen4 = summary.Open4Red
		ownForcing = summary.ForcingThreatsRed
		ownCritical = summary.CriticalCapturesRed
		ownDouble = summary.DoubleThreatRed
		oppMustAnswer = summary.MustAnswerForBlue
	case PlayerBlue:
		ownWinNow = summary.WinNowBlue
		ownCaptureWin = summary.CaptureWinNowBlue
		ownOpen4 = summary.Open4Blue
		ownForcing = summary.ForcingThreatsBlue
		ownCritical = summary.CriticalCapturesBlue
		ownDouble = summary.DoubleThreatBlue
		oppMustAnswer = summary.MustAnswerForRed
	default:
		return 0, 0, 0
	}
	flags := uint32(0)
	boost := 0
	severity := 0
	if ownWinNow > 0 || ownCaptureWin > 0 {
		flags |= rootThreatOwnWin
		boost += 1200
		severity = maxInt(severity, 100)
	}
	if oppMustAnswer {
		flags |= rootThreatChildMustAnswer
		boost += 450
		severity = maxInt(severity, 80)
	}
	if ownOpen4 > 0 {
		flags |= rootThreatChildOpenFour | rootThreatOwnFour
		boost += 350
		severity = maxInt(severity, 90)
	}
	if ownDouble {
		flags |= rootThreatChildDoubleThreat | rootThreatForkCreate
		boost += 300
		severity = maxInt(severity, 75)
	}
	if ownCritical > 0 {
		flags |= rootThreatChildCriticalCapture | rootThreatCaptureCreate
		boost += 200
		severity = maxInt(severity, 60)
	}
	if ownForcing > 0 {
		boost += int(ownForcing) * 40
		severity = maxInt(severity, 50+int(ownForcing)*5)
	}
	return flags, boost, severity
}

func rootVerificationValue(move RootMove) float64 {
	value := float64(move.ThreatSeverity + move.ForkPotential*12 + move.CaptureValue*24 + move.CaptureDefense*18 + move.StabilityNeed*10)
	if move.IsForced {
		value += 500
	}
	if !move.HasLastSearch {
		value += 75
	}
	if move.HasVerification {
		value += 25
	}
	return value
}

func compareRootScores(left, right float64, maximizing bool) int {
	if left == right {
		return 0
	}
	if maximizing {
		if left > right {
			return -1
		}
		return 1
	}
	if left < right {
		return -1
	}
	return 1
}

const rootQuietScoreOrderingMargin = 256.0

func isQuietRootMove(move RootMove) bool {
	return !move.IsForced &&
		move.TacticalPriority >= prioLastMove &&
		move.ThreatSeverity == 0 &&
		move.ChildForcingScore == 0 &&
		move.CaptureValue == 0 &&
		move.CaptureDefense == 0 &&
		move.ForkPotential == 0 &&
		move.StabilityNeed == 0
}

func compareRootScoresWithMargin(left, right float64, maximizing bool, margin float64) int {
	if margin > 0 && math.Abs(left-right) <= margin {
		return 0
	}
	return compareRootScores(left, right, maximizing)
}

// ================================================================
// Root move ordering and search bands
// How root moves are ranked and bucketed.
// ================================================================

func sortRootMoveIndices(pool []RootMove, maximizing bool, pvMove *Move) []int {
	order := make([]int, len(pool))
	for i := range pool {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		left := pool[order[i]]
		right := pool[order[j]]
		quietPair := isQuietRootMove(left) && isQuietRootMove(right)
		leftCapture := left.CaptureValue + left.CaptureDefense
		rightCapture := right.CaptureValue + right.CaptureDefense
		leftHasCapture := leftCapture > 0 || (left.ThreatFlags&(rootThreatCaptureCreate|rootThreatCapturePrevent)) != 0
		rightHasCapture := rightCapture > 0 || (right.ThreatFlags&(rootThreatCaptureCreate|rootThreatCapturePrevent)) != 0
		leftForcedThreat := (left.ThreatFlags & (rootThreatOwnWin | rootThreatOppWin | rootThreatOwnFour | rootThreatOppFour)) != 0
		rightForcedThreat := (right.ThreatFlags & (rootThreatOwnWin | rootThreatOppWin | rootThreatOwnFour | rootThreatOppFour)) != 0
		if left.IsForced != right.IsForced {
			return left.IsForced
		}
		if pvMove != nil {
			leftPV := left.Move.Equals(*pvMove)
			rightPV := right.Move.Equals(*pvMove)
			if leftPV != rightPV && !quietPair {
				return leftPV
			}
		}
		if !quietPair && left.HasLastSearch && right.HasLastSearch {
			if left.LastCompletedDepth != right.LastCompletedDepth {
				return left.LastCompletedDepth > right.LastCompletedDepth
			}
			if cmp := compareRootScores(left.LastSearchScore, right.LastSearchScore, maximizing); cmp != 0 {
				return cmp < 0
			}
		} else if !quietPair && left.HasLastSearch != right.HasLastSearch {
			return left.HasLastSearch
		}
		if !leftForcedThreat && !rightForcedThreat {
			if leftHasCapture != rightHasCapture {
				return leftHasCapture
			}
			if leftCapture != rightCapture {
				return leftCapture > rightCapture
			}
		}
		if left.ChildForcingScore != right.ChildForcingScore {
			return left.ChildForcingScore > right.ChildForcingScore
		}
		if left.TacticalPriority != right.TacticalPriority {
			return left.TacticalPriority < right.TacticalPriority
		}
		if left.ThreatSeverity != right.ThreatSeverity {
			return left.ThreatSeverity > right.ThreatSeverity
		}
		if left.ForkPotential != right.ForkPotential {
			return left.ForkPotential > right.ForkPotential
		}
		if left.StabilityNeed != right.StabilityNeed {
			return left.StabilityNeed > right.StabilityNeed
		}
		if !quietPair && left.HasVerification && right.HasVerification {
			if left.VerificationDepth != right.VerificationDepth {
				return left.VerificationDepth > right.VerificationDepth
			}
			if cmp := compareRootScores(left.VerificationScore, right.VerificationScore, maximizing); cmp != 0 {
				return cmp < 0
			}
		} else if !quietPair && left.HasVerification != right.HasVerification {
			return left.HasVerification
		}
		if !quietPair && left.HasShallowScore && right.HasShallowScore {
			cmpFn := compareRootScores
			if cmp := cmpFn(left.ShallowScore, right.ShallowScore, maximizing); cmp != 0 {
				return cmp < 0
			}
		}
		if left.Move.Y != right.Move.Y {
			return left.Move.Y < right.Move.Y
		}
		return left.Move.X < right.Move.X
	})
	return order
}

func rootBasePrincipalLimit(config Config) int {
	limit := config.AiMaxCandidatesRoot
	if limit <= 0 {
		limit = 20
	}
	if config.AiEnableDynamicTopK && config.AiKQuietRoot > 0 && config.AiKQuietRoot < limit {
		limit = config.AiKQuietRoot
	}
	return limit
}

func rootPrincipalBandLimit(ctx minimaxContext, poolSize, depth int) int {
	if poolSize <= 0 {
		return 0
	}
	baseLimit := rootBasePrincipalLimit(ctx.settings.Config)
	if ctx.settings.DebugWideRootAtDepthOne {
		if depth == 1 {
			return poolSize
		}
		if baseLimit > poolSize {
			return poolSize
		}
		return baseLimit
	}
	if ctx.settings.Config.AiTimeBudgetMs == 0 && ctx.settings.TimeoutMs == 0 {
		if baseLimit > poolSize {
			return poolSize
		}
		return baseLimit
	}
	limit := baseLimit
	if depth > 1 {
		limit += minInt(2, depth-1)
	}
	if limit > poolSize {
		limit = poolSize
	}
	return limit
}

func rootForcedCarryoverLimit(ctx minimaxContext, depth int) int {
	limit := 2
	if depth >= 5 {
		limit = 3
	}
	if ctx.settings.Config.AiTimeBudgetMs == 0 && ctx.settings.TimeoutMs == 0 {
		if limit < 4 {
			limit = 4
		}
	}
	return limit
}

func isPureLocalityRootMove(move RootMove) bool {
	return move.SourceFlags == rootSourceLocality &&
		move.ThreatFlags == 0 &&
		move.ThreatSeverity == 0 &&
		move.CaptureValue == 0 &&
		move.CaptureDefense == 0 &&
		move.ForkPotential == 0 &&
		move.StabilityNeed == 0 &&
		!move.IsForced
}

func rootBadMoveKeepTop(config Config, move RootMove) int {
	keepTop := config.AiRootBadMoveKeepTop
	if keepTop <= 0 {
		keepTop = 3
	}
	if move.IsForced && keepTop > 2 {
		keepTop = 2
	}
	if keepTop < 1 {
		keepTop = 1
	}
	return keepTop
}

func isSuppressibleRootMove(move RootMove) bool {
	if isPureLocalityRootMove(move) {
		return true
	}
	if move.IsForced && move.ThreatFlags&rootThreatOwnWin == 0 {
		return true
	}
	return false
}

func shouldSuppressRootMove(config Config, move RootMove, depth int) bool {
	if !isSuppressibleRootMove(move) {
		return false
	}
	if config.AiRootBadMoveDepths <= 0 {
		return false
	}
	if config.AiRootBadMoveMinDepth > 0 && depth < config.AiRootBadMoveMinDepth {
		return false
	}
	return move.BadDepthStreak >= config.AiRootBadMoveDepths
}

func updateRootBadMoveStreaks(rootPool []RootMove, ordered []int, scores []float64, boardSize int, maximizing bool, depth int, config Config) {
	if len(rootPool) == 0 || len(ordered) == 0 || len(scores) == 0 {
		return
	}
	bestMove, bestScore, found := bestRootMoveFromScores(rootPool, scores, boardSize, maximizing)
	if !found {
		return
	}
	bestRank := make(map[int]int, len(ordered))
	scoredOrder := make([]int, 0, len(ordered))
	for _, idx := range ordered {
		if idx < 0 || idx >= len(rootPool) {
			continue
		}
		move := rootPool[idx].Move
		scoreIdx := move.Y*boardSize + move.X
		if scoreIdx < 0 || scoreIdx >= len(scores) || scores[scoreIdx] == illegalScore {
			continue
		}
		scoredOrder = append(scoredOrder, idx)
	}
	sort.SliceStable(scoredOrder, func(i, j int) bool {
		leftIdx := scoredOrder[i]
		rightIdx := scoredOrder[j]
		leftMove := rootPool[leftIdx].Move
		rightMove := rootPool[rightIdx].Move
		leftScore := scores[leftMove.Y*boardSize+leftMove.X]
		rightScore := scores[rightMove.Y*boardSize+rightMove.X]
		if cmp := compareRootScores(leftScore, rightScore, maximizing); cmp != 0 {
			return cmp < 0
		}
		return bestRankFallback(rootPool[leftIdx], rootPool[rightIdx], maximizing)
	})
	for rank, idx := range scoredOrder {
		bestRank[idx] = rank
	}
	margin := config.AiRootBadMoveMargin
	for idx := range rootPool {
		move := rootPool[idx]
		scoreIdx := move.Move.Y*boardSize + move.Move.X
		if scoreIdx < 0 || scoreIdx >= len(scores) || scores[scoreIdx] == illegalScore {
			continue
		}
		if move.Move == bestMove {
			rootPool[idx].BadDepthStreak = 0
			continue
		}
		if !isSuppressibleRootMove(move) {
			rootPool[idx].BadDepthStreak = 0
			continue
		}
		moveRank, ok := bestRank[idx]
		if !ok {
			rootPool[idx].BadDepthStreak = 0
			continue
		}
		keepTop := rootBadMoveKeepTop(config, move)
		badByRank := moveRank >= keepTop
		badByScore := false
		if maximizing {
			badByScore = scores[scoreIdx] < bestScore-margin
		} else {
			badByScore = scores[scoreIdx] > bestScore+margin
		}
		catastrophicLoss := false
		if maximizing {
			catastrophicLoss = scores[scoreIdx] <= -winScore+64
		} else {
			catastrophicLoss = scores[scoreIdx] >= winScore-64
		}
		if catastrophicLoss && badByScore {
			rootPool[idx].BadDepthStreak = maxInt(rootPool[idx].BadDepthStreak, config.AiRootBadMoveDepths)
		} else if badByRank && badByScore {
			rootPool[idx].BadDepthStreak++
		} else {
			rootPool[idx].BadDepthStreak = 0
		}
	}
}

func bestRankFallback(left, right RootMove, maximizing bool) bool {
	leftCapture := left.CaptureValue + left.CaptureDefense
	rightCapture := right.CaptureValue + right.CaptureDefense
	leftHasCapture := leftCapture > 0 || (left.ThreatFlags&(rootThreatCaptureCreate|rootThreatCapturePrevent)) != 0
	rightHasCapture := rightCapture > 0 || (right.ThreatFlags&(rootThreatCaptureCreate|rootThreatCapturePrevent)) != 0
	leftForcedThreat := (left.ThreatFlags & (rootThreatOwnWin | rootThreatOppWin | rootThreatOwnFour | rootThreatOppFour)) != 0
	rightForcedThreat := (right.ThreatFlags & (rootThreatOwnWin | rootThreatOppWin | rootThreatOwnFour | rootThreatOppFour)) != 0
	if left.IsForced != right.IsForced {
		return left.IsForced
	}
	if !leftForcedThreat && !rightForcedThreat {
		if leftHasCapture != rightHasCapture {
			return leftHasCapture
		}
		if leftCapture != rightCapture {
			return leftCapture > rightCapture
		}
	}
	if left.TacticalPriority != right.TacticalPriority {
		return left.TacticalPriority < right.TacticalPriority
	}
	if left.ThreatSeverity != right.ThreatSeverity {
		return left.ThreatSeverity > right.ThreatSeverity
	}
	if left.HasShallowScore && right.HasShallowScore {
		if cmp := compareRootScores(left.ShallowScore, right.ShallowScore, maximizing); cmp != 0 {
			return cmp < 0
		}
	}
	if left.Move.Y != right.Move.Y {
		return left.Move.Y < right.Move.Y
	}
	return left.Move.X < right.Move.X
}

func chooseRootSearchBands(ctx minimaxContext, pool []RootMove, ordered []int, depth int) rootSearchBands {
	bands := rootSearchBands{
		forced:       make([]int, 0, 8),
		principal:    make([]int, 0, 24),
		speculative:  make([]int, 0, 12),
		verification: make([]int, 0, 12),
	}
	seen := make(map[int]struct{}, len(ordered))
	principalLimit := rootPrincipalBandLimit(ctx, len(pool), depth)
	survivors := 0
	for _, idx := range ordered {
		if idx < 0 || idx >= len(pool) {
			continue
		}
		if !shouldSuppressRootMove(ctx.settings.Config, pool[idx], depth) {
			survivors++
		}
	}
	forcedSurvivors := 0
	for _, idx := range ordered {
		if idx < 0 || idx >= len(pool) || !pool[idx].IsForced {
			continue
		}
		if !shouldSuppressRootMove(ctx.settings.Config, pool[idx], depth) {
			forcedSurvivors++
		}
	}
	for _, idx := range ordered {
		move := pool[idx]
		if move.IsForced {
			keepSuppressedTop := rootBadMoveKeepTop(ctx.settings.Config, move)
			if shouldSuppressRootMove(ctx.settings.Config, move, depth) && forcedSurvivors >= keepSuppressedTop {
				continue
			}
			seen[idx] = struct{}{}
			bands.forced = append(bands.forced, idx)
		}
	}
	if len(bands.forced) > 0 {
		return bands
	}
	keepSuppressedTop := ctx.settings.Config.AiRootBadMoveKeepTop
	if keepSuppressedTop <= 0 {
		keepSuppressedTop = 3
	}
	for _, idx := range ordered {
		if len(bands.forced)+len(bands.principal) >= principalLimit {
			break
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		if shouldSuppressRootMove(ctx.settings.Config, pool[idx], depth) && len(bands.principal) >= keepSuppressedTop && survivors > keepSuppressedTop {
			continue
		}
		seen[idx] = struct{}{}
		bands.principal = append(bands.principal, idx)
	}
	return bands
}

func rootBandSearchOrder(bands rootSearchBands) []int {
	order := make([]int, 0, len(bands.forced)+len(bands.principal)+len(bands.speculative))
	order = append(order, bands.forced...)
	order = append(order, bands.principal...)
	order = append(order, bands.speculative...)
	return order
}

// ================================================================
// Node-level candidate selection utilities
// ================================================================

func nodeForcedCarryoverLimit(depthFromRoot, maxCandidates int) int {
	limit := 2
	if depthFromRoot <= 1 {
		limit = 4
	} else if depthFromRoot <= 3 {
		limit = 3
	}
	if maxCandidates > 0 && limit > maxCandidates {
		limit = maxCandidates
	}
	return limit
}

func uniqueOrderedMovesForBoard(moves []Move, boardSize int) []Move {
	if len(moves) == 0 || boardSize <= 0 {
		return nil
	}
	seen := make([]bool, boardSize*boardSize)
	out := make([]Move, 0, len(moves))
	for _, move := range moves {
		if !move.IsValid(boardSize) {
			continue
		}
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(seen) || seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, move)
	}
	return out
}

func buildHardRestrictedNodeCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, maxCandidates int, pvMove *Move, context ThreatContext) []Move {
	buildStart := time.Now()
	defer func() {
		if stats := ctx.settings.Stats; stats != nil {
			stats.BuildHardRestrictedTime += time.Since(buildStart)
		}
	}()
	boardSize := ctx.settings.BoardSize
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	mustPlay := selectedMustPlayMoves(context, boardSize)
	mustBlock := selectedMustBlockMoves(context, boardSize)
	hasMustResponse := len(context.WinningMoves) > 0 || len(mustPlay) > 0 || len(mustBlock) > 0
	coreMoves := make([]Move, 0, 16)
	coreSeen := make(map[int]struct{}, 16)
	addCore := func(moves []Move, priority int, dst *[]candidateMove) {
		for _, move := range uniqueOrderedMovesForBoard(moves, boardSize) {
			key := move.Y*boardSize + move.X
			if _, ok := coreSeen[key]; ok {
				continue
			}
			coreSeen[key] = struct{}{}
			coreMoves = append(coreMoves, move)
			*dst = append(*dst, candidateMove{move: move, priority: priority})
		}
	}
	restricted := make([]candidateMove, 0, 20)
	addCore(context.WinningMoves, prioWin, &restricted)
	addCore(mustPlay, prioBlockWin, &restricted)
	addCore(mustBlock, prioBlockFour, &restricted)
	if !hasMustResponse {
		addCore(context.CaptureDefenseMoves, prioCapturePrevent, &restricted)
	}
	if len(coreMoves) == 0 {
		return nil
	}
	if stats := ctx.settings.Stats; stats != nil {
		stats.HardCoreMoves += int64(len(coreMoves))
	}
	coreSet := make(map[int]struct{}, len(coreMoves))
	for _, move := range coreMoves {
		coreSet[move.Y*boardSize+move.X] = struct{}{}
	}
	carryover := nodeForcedCarryoverLimit(depthFromRoot, maxCandidates)
	threatStart := time.Now()
	threatCandidates := GenerateThreatCandidates(context, state, ctx.rules)
	if stats := ctx.settings.Stats; stats != nil {
		stats.HardBuildGenerateTime += time.Since(threatStart)
		stats.HardThreatCandidates += int64(len(threatCandidates))
		stats.HardCarryoverTarget += int64(maxInt(carryover, 0))
	}
	threatOrderStart := time.Now()
	orderedThreats := orderCandidateMoves(
		state,
		ctx,
		currentPlayer,
		maximizing,
		depthFromRoot,
		threatCandidates,
		0,
		pvMove,
	)
	if stats := ctx.settings.Stats; stats != nil {
		stats.HardBuildThreatOrderTime += time.Since(threatOrderStart)
		stats.HardThreatOrderedMoves += int64(len(orderedThreats))
	}
	threatCarryCount := 0
	for _, move := range orderedThreats {
		if carryover <= 0 {
			break
		}
		key := move.Y*boardSize + move.X
		if _, ok := coreSet[key]; ok {
			continue
		}
		coreSet[key] = struct{}{}
		restricted = append(restricted, candidateMove{move: move, priority: prioDefault})
		if stats := ctx.settings.Stats; stats != nil {
			stats.HardCarryoverFromThreat++
		}
		threatCarryCount++
		carryover--
	}
	genericCarryLimit := carryover
	if len(coreMoves) >= 3 {
		switch {
		case threatCarryCount >= 2:
			genericCarryLimit = minInt(genericCarryLimit, 2)
		case threatCarryCount >= 1:
			genericCarryLimit = minInt(genericCarryLimit, 3)
		}
	}
	if genericCarryLimit > 0 {
		collectStart := time.Now()
		allCandidates := collectCandidateMovesWithEval(state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.evalState, ctx.settings.Stats)
		if stats := ctx.settings.Stats; stats != nil {
			stats.HardBuildCollectTime += time.Since(collectStart)
			stats.HardGenericCandidates += int64(len(allCandidates))
			stats.HardGenericCollectCalls++
		}
		mergeOrderStart := time.Now()
		fullOrdered := orderCandidateMoves(
			state,
			ctx,
			currentPlayer,
			maximizing,
			depthFromRoot,
			mergeCandidateMoves(threatCandidates, allCandidates),
			0,
			pvMove,
		)
		if stats := ctx.settings.Stats; stats != nil {
			stats.HardBuildMergeOrderTime += time.Since(mergeOrderStart)
		}
		for _, move := range fullOrdered {
			if genericCarryLimit <= 0 {
				break
			}
			if !hardGenericCarryoverAllowed(move, coreMoves) {
				if stats := ctx.settings.Stats; stats != nil {
					stats.HardGenericFilteredOut++
				}
				continue
			}
			key := move.Y*boardSize + move.X
			if _, ok := coreSet[key]; ok {
				continue
			}
			coreSet[key] = struct{}{}
			restricted = append(restricted, candidateMove{move: move, priority: prioDefault})
			if stats := ctx.settings.Stats; stats != nil {
				stats.HardCarryoverFromGeneric++
			}
			genericCarryLimit--
		}
	} else if stats := ctx.settings.Stats; stats != nil {
		stats.HardGenericCollectSkipped++
	}
	restrictedOrderStart := time.Now()
	orderedRestricted := orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, restricted, maxCandidates, pvMove)
	if stats := ctx.settings.Stats; stats != nil {
		stats.HardBuildRestrictedTime += time.Since(restrictedOrderStart)
	}
	return orderedRestricted
}

func hardGenericCarryoverAllowed(move Move, coreMoves []Move) bool {
	if len(coreMoves) <= 2 {
		return true
	}
	for _, core := range coreMoves {
		distance := chebyshevDistance(move, core)
		if distance <= 2 {
			return true
		}
		if sharesLine(move, core) && distance <= 6 {
			return true
		}
	}
	return false
}

func sharesLine(a, b Move) bool {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return dx == 0 || dy == 0 || absInt(dx) == absInt(dy)
}

func chebyshevDistance(a, b Move) int {
	return maxInt(absInt(a.X-b.X), absInt(a.Y-b.Y))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func candidateMovesFromRootIndices(pool []RootMove, indices []int) []candidateMove {
	seen := make(map[int]struct{}, len(indices))
	out := make([]candidateMove, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(pool) {
			continue
		}
		move := pool[idx].Move
		key := move.Y*64 + move.X
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		priority := pool[idx].TacticalPriority
		if priority <= 0 {
			priority = prioDefault
		}
		out = append(out, candidateMove{move: move, priority: priority})
	}
	return out
}

func formatRootBandMoves(pool []RootMove, indices []int) string {
	moves := make([]Move, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(pool) {
			continue
		}
		moves = append(moves, pool[idx].Move)
	}
	return formatMoves(moves)
}

func formatRootBandMovesLimited(pool []RootMove, indices []int, limit int) string {
	if len(indices) == 0 {
		return "[]"
	}
	if limit <= 0 || limit > len(indices) {
		limit = len(indices)
	}
	parts := make([]string, 0, minInt(len(indices), limit)+1)
	for i, idx := range indices {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("...+%d", len(indices)-limit))
			break
		}
		if idx < 0 || idx >= len(pool) {
			continue
		}
		move := pool[idx]
		if move.BadDepthStreak > 0 {
			parts = append(parts, fmt.Sprintf("(%d,%d|s=%d)", move.Move.X, move.Move.Y, move.BadDepthStreak))
		} else {
			parts = append(parts, fmt.Sprintf("(%d,%d)", move.Move.X, move.Move.Y))
		}
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func rootMovesFromIndices(pool []RootMove, indices []int) []Move {
	moves := make([]Move, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(pool) {
			continue
		}
		moves = append(moves, pool[idx].Move)
	}
	return moves
}

func bestRootMoveFromScores(pool []RootMove, scores []float64, boardSize int, maximizing bool) (Move, float64, bool) {
	best := Move{}
	bestScore := math.Inf(1)
	if maximizing {
		bestScore = math.Inf(-1)
	}
	found := false
	for _, entry := range pool {
		idx := entry.Move.Y*boardSize + entry.Move.X
		if idx < 0 || idx >= len(scores) || scores[idx] == illegalScore {
			continue
		}
		if !found {
			best = entry.Move
			bestScore = scores[idx]
			found = true
			continue
		}
		if maximizing {
			if scores[idx] > bestScore {
				best = entry.Move
				bestScore = scores[idx]
			}
		} else if scores[idx] < bestScore {
			best = entry.Move
			bestScore = scores[idx]
		}
	}
	return best, bestScore, found
}

// ================================================================
// Candidate collection
// Gathers all candidate moves for a search node.
// ================================================================

// collectCandidateMovesWithEval gathers all candidate moves for a search node by
// combining threat-driven moves (wins, fours, threes) with quiet proximity moves.
// Threat moves are generated first; quiet moves fill in the local neighborhood.
func collectCandidateMovesWithEval(state GameState, rules Rules, currentPlayer PlayerColor, boardSize int, evalState *EvalState, stats *SearchStats) []candidateMove {
	if stats != nil {
		stats.CollectCandidateCalls++
	}
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	board := state.Board
	bboxStart := time.Now()
	bbox := computeBBox(board, boardSize)
	if stats != nil {
		stats.CollectBBoxTime += time.Since(bboxStart)
	}
	if bbox.stones == 0 {
		center := boardSize / 2
		move := Move{X: center, Y: center}
		if ok, _ := rules.IsLegal(state, move, currentPlayer); !ok {
			return nil
		}
		if stats != nil {
			stats.CollectEmptyBoardReturns++
			stats.QuietLegalChecks++
			stats.QuietAddedCandidates++
		}
		return []candidateMove{{move: move, priority: prioDefault}}
	}
	if bbox.stones == 1 {
		moves := []candidateMove{}
		cellCount := boardSize * boardSize
		var seenStack [maxSearchBoardCells]bool
		seen := seenStack[:0]
		if cellCount <= len(seenStack) {
			seen = seenStack[:cellCount]
		} else {
			seen = make([]bool, cellCount)
		}
		for y := 0; y < boardSize; y++ {
			for x := 0; x < boardSize; x++ {
				if board.At(x, y) == CellEmpty {
					continue
				}
				for dy := -proximityRadius; dy <= proximityRadius; dy++ {
					for dx := -proximityRadius; dx <= proximityRadius; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						if chebDist(dx, dy) > proximityRadius {
							continue
						}
						nx := x + dx
						ny := y + dy
						if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
							continue
						}
						idx := ny*boardSize + nx
						if !seen[idx] {
							move := Move{X: nx, Y: ny}
							if stats != nil {
								stats.QuietLegalChecks++
							}
							if ok, _ := rules.IsLegal(state, move, currentPlayer); !ok {
								if stats != nil {
									stats.QuietLegalRejected++
								}
								continue
							}
							seen[idx] = true
							if stats != nil {
								stats.QuietAddedCandidates++
							}
							moves = append(moves, candidateMove{move: move, priority: prioProximity})
						}
					}
				}
				if stats != nil {
					stats.CollectSingleStoneReturns++
				}
				return moves
			}
		}
	}

	threatMoves, urgent := generateThreatMoves(board, boardSize, currentPlayer)
	if stats != nil {
		stats.CollectThreatCandidates += int64(len(threatMoves))
	}
	quietStart := time.Now()
	quietMoves := collectQuietCandidateMovesWithEval(state, rules, currentPlayer, boardSize, evalState, urgent, stats)
	if stats != nil {
		stats.CollectQuietOnlyTime += time.Since(quietStart)
	}
	mergeStart := time.Now()
	merged := mergeCandidateMoves(threatMoves, quietMoves)
	if stats != nil {
		stats.CollectThreatMergeTime += time.Since(mergeStart)
		stats.CollectMergedCandidates += int64(len(merged))
	}
	return merged
}

func collectQuietCandidateMovesWithEval(state GameState, rules Rules, currentPlayer PlayerColor, boardSize int, evalState *EvalState, urgent bool, stats *SearchStats) []candidateMove {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	board := state.Board
	bboxStart := time.Now()
	bbox := computeBBox(board, boardSize)
	if stats != nil {
		stats.CollectBBoxTime += time.Since(bboxStart)
	}
	if bbox.stones == 0 {
		center := boardSize / 2
		move := Move{X: center, Y: center}
		if stats != nil {
			stats.QuietLegalChecks++
		}
		if ok, _ := rules.IsLegal(state, move, currentPlayer); !ok {
			if stats != nil {
				stats.QuietLegalRejected++
			}
			return nil
		}
		if stats != nil {
			stats.CollectEmptyBoardReturns++
			stats.QuietAddedCandidates++
		}
		return []candidateMove{{move: move, priority: prioDefault}}
	}
	if bbox.stones == 1 {
		moves := []candidateMove{}
		cellCount := boardSize * boardSize
		var seenStack [maxSearchBoardCells]bool
		seen := seenStack[:0]
		if cellCount <= len(seenStack) {
			seen = seenStack[:cellCount]
		} else {
			seen = make([]bool, cellCount)
		}
		for y := 0; y < boardSize; y++ {
			for x := 0; x < boardSize; x++ {
				if board.At(x, y) == CellEmpty {
					continue
				}
				for dy := -proximityRadius; dy <= proximityRadius; dy++ {
					for dx := -proximityRadius; dx <= proximityRadius; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						if chebDist(dx, dy) > proximityRadius {
							continue
						}
						nx := x + dx
						ny := y + dy
						if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
							continue
						}
						idx := ny*boardSize + nx
						if !seen[idx] {
							move := Move{X: nx, Y: ny}
							if stats != nil {
								stats.QuietLegalChecks++
							}
							if ok, _ := rules.IsLegal(state, move, currentPlayer); !ok {
								if stats != nil {
									stats.QuietLegalRejected++
								}
								continue
							}
							seen[idx] = true
							if stats != nil {
								stats.QuietAddedCandidates++
							}
							moves = append(moves, candidateMove{move: move, priority: prioProximity})
						}
					}
				}
				if stats != nil {
					stats.CollectSingleStoneReturns++
				}
				return moves
			}
		}
	}

	density := computeDensity(bbox.stones, bbox.width, bbox.height)
	margin := 2
	if density < 0.15 {
		margin++
	}
	if urgent {
		margin++
	}
	if margin > 4 {
		margin = 4
	}
	x0 := bbox.minX - margin
	y0 := bbox.minY - margin
	x1 := bbox.maxX + margin
	y1 := bbox.maxY + margin
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= boardSize {
		x1 = boardSize - 1
	}
	if y1 >= boardSize {
		y1 = boardSize - 1
	}

	cellCount := boardSize * boardSize
	var seenPriorityStack [maxSearchBoardCells]uint8
	seenPriority := seenPriorityStack[:0]
	if cellCount <= len(seenPriorityStack) {
		seenPriority = seenPriorityStack[:cellCount]
	} else {
		seenPriority = make([]uint8, cellCount)
	}
	var keepKnownStack [maxSearchBoardCells]bool
	keepKnown := keepKnownStack[:0]
	if cellCount <= len(keepKnownStack) {
		keepKnown = keepKnownStack[:cellCount]
	} else {
		keepKnown = make([]bool, cellCount)
	}
	var keepAllowedStack [maxSearchBoardCells]bool
	keepAllowed := keepAllowedStack[:0]
	if cellCount <= len(keepAllowedStack) {
		keepAllowed = keepAllowedStack[:cellCount]
	} else {
		keepAllowed = make([]bool, cellCount)
	}
	var proximitySeenStack [maxSearchBoardCells]bool
	proximitySeen := proximitySeenStack[:0]
	if cellCount <= len(proximitySeenStack) {
		proximitySeen = proximitySeenStack[:cellCount]
	} else {
		proximitySeen = make([]bool, cellCount)
	}
	var proximityMarkedStack [maxSearchBoardCells]bool
	proximityMarked := proximityMarkedStack[:0]
	if cellCount <= len(proximityMarkedStack) {
		proximityMarked = proximityMarkedStack[:cellCount]
	} else {
		proximityMarked = make([]bool, cellCount)
	}
	var proximityCoveredStack [maxSearchBoardCells]bool
	proximityCovered := proximityCoveredStack[:0]
	if cellCount <= len(proximityCoveredStack) {
		proximityCovered = proximityCoveredStack[:cellCount]
	} else {
		proximityCovered = make([]bool, cellCount)
	}
	proximityIndices := make([]int, 0, 64)
	candidates := make([]candidateMove, 0, 64)
	shouldKeepCached := func(move Move, source int) bool {
		idx := move.Y*boardSize + move.X
		if idx >= 0 && idx < len(keepKnown) && keepKnown[idx] {
			if stats != nil {
				if source == prioLastMove {
					stats.LastMoveKeepCacheHits++
				} else if source == prioProximity {
					stats.ProximityKeepCacheHits++
				}
			}
			return keepAllowed[idx]
		}
		if stats != nil {
			if source == prioLastMove {
				stats.LastMoveKeepCacheMisses++
			} else if source == prioProximity {
				stats.ProximityKeepCacheMisses++
			}
		}
		keepStart := time.Now()
		keep := shouldKeepQuietCandidateWithStats(board, move, boardSize, stats)
		if stats != nil {
			stats.QuietKeepCheckTime += time.Since(keepStart)
		}
		if idx >= 0 && idx < len(keepKnown) {
			keepKnown[idx] = true
			keepAllowed[idx] = keep
		}
		return keep
	}
	addCandidate := func(move Move, priority int, source int) {
		if stats != nil {
			stats.QuietLegalChecks++
			if source == prioLastMove {
				stats.LastMoveLegalChecks++
			} else if source == prioProximity {
				stats.ProximityLegalChecks++
			}
		}
		legalStart := time.Now()
		ok, _ := rules.IsLegal(state, move, currentPlayer)
		if stats != nil {
			elapsed := time.Since(legalStart)
			stats.QuietLegalCheckTime += elapsed
			if source == prioLastMove {
				stats.LastMoveLegalTime += elapsed
			} else if source == prioProximity {
				stats.ProximityLegalTime += elapsed
			}
		}
		if !ok {
			if stats != nil {
				stats.QuietLegalRejected++
				if source == prioLastMove {
					stats.LastMoveLegalRejected++
				} else if source == prioProximity {
					stats.ProximityLegalRejected++
				}
			}
			return
		}
		idx := move.Y*boardSize + move.X
		stored := int(seenPriority[idx])
		if stored == 0 || priority < stored-1 {
			if stats != nil {
				if stored == 0 {
					stats.QuietAddedCandidates++
					if source == prioLastMove {
						stats.LastMoveCandidatesAdded++
					} else if source == prioProximity {
						stats.ProximityCandidatesAdded++
					}
				} else {
					stats.QuietPriorityReplacements++
				}
			}
			seenPriority[idx] = uint8(priority + 1)
			candidates = append(candidates, candidateMove{move: move, priority: priority})
			return
		}
		if stats != nil {
			stats.QuietPrioritySkipped++
		}
	}

	quietFrontStart := time.Now()
	if quietFronts := buildQuietFrontCandidates(state, currentPlayer, boardSize, evalState, GetConfig()); len(quietFronts) > 0 {
		if stats != nil {
			stats.QuietFrontTime += time.Since(quietFrontStart)
			stats.QuietFrontCandidates += int64(len(quietFronts))
		}
		return mergeCandidateMoves(candidates, quietFronts)
	}
	if stats != nil {
		stats.QuietFrontTime += time.Since(quietFrontStart)
	}

	if state.HasLastMove {
		lm := state.LastMove
		lastMoveStart := time.Now()
		for dy := -lastMoveRadius; dy <= lastMoveRadius; dy++ {
			for dx := -lastMoveRadius; dx <= lastMoveRadius; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				if chebDist(dx, dy) > lastMoveRadius {
					continue
				}
				if stats != nil {
					stats.LastMoveWindowChecks++
				}
				nx := lm.X + dx
				ny := lm.Y + dy
				if nx < x0 || nx > x1 || ny < y0 || ny > y1 {
					continue
				}
				if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
					continue
				}
				if stats != nil {
					stats.LastMoveEmptyChecks++
				}
				idx := ny*boardSize + nx
				if idx < 0 || idx >= len(seenPriority) {
					continue
				}
				proximityCovered[idx] = true
				if stored := int(seenPriority[idx]); stored != 0 && stored-1 <= prioLastMove {
					if stats != nil {
						stats.LastMovePrioritySkips++
					}
					continue
				}
				move := Move{X: nx, Y: ny}
				if stats != nil {
					stats.LastMoveKeepChecks++
				}
				keep := shouldKeepCached(move, prioLastMove)
				if !keep {
					continue
				}
				if stats != nil {
					stats.LastMoveKeepAccepted++
				}
				addCandidate(move, prioLastMove, prioLastMove)
			}
		}
		if stats != nil {
			stats.LastMoveScanTime += time.Since(lastMoveStart)
		}
	}

	proximityStart := time.Now()
	for y := bbox.minY; y <= bbox.maxY; y++ {
		for x := bbox.minX; x <= bbox.maxX; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			for dy := -proximityRadius; dy <= proximityRadius; dy++ {
				for dx := -proximityRadius; dx <= proximityRadius; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					if chebDist(dx, dy) > proximityRadius {
						continue
					}
					if stats != nil {
						stats.ProximityWindowChecks++
					}
					nx := x + dx
					ny := y + dy
					if nx < x0 || nx > x1 || ny < y0 || ny > y1 {
						continue
					}
					if !board.IsEmpty(nx, ny) {
						continue
					}
					if stats != nil {
						stats.ProximityEmptyChecks++
					}
					idx := ny*boardSize + nx
					if idx < 0 || idx >= len(proximityMarked) {
						continue
					}
					if proximityCovered[idx] {
						if stats != nil {
							stats.ProximityCoveredSkips++
						}
						continue
					}
					if proximityMarked[idx] {
						if stats != nil {
							stats.ProximityDuplicateSkips++
						}
						continue
					}
					proximityMarked[idx] = true
					proximityIndices = append(proximityIndices, idx)
				}
			}
		}
	}
	for _, idx := range proximityIndices {
		if idx < 0 || idx >= len(proximitySeen) {
			continue
		}
		if proximitySeen[idx] {
			continue
		}
		proximitySeen[idx] = true
		if stored := int(seenPriority[idx]); stored != 0 && stored-1 <= prioProximity {
			if stats != nil {
				stats.ProximityPrioritySkips++
			}
			continue
		}
		move := Move{X: idx % boardSize, Y: idx / boardSize}
		if stats != nil {
			stats.ProximityKeepChecks++
		}
		keep := shouldKeepCached(move, prioProximity)
		if !keep {
			continue
		}
		if stats != nil {
			stats.ProximityKeepAccepted++
		}
		addCandidate(move, prioProximity, prioProximity)
	}
	if stats != nil {
		stats.ProximityScanTime += time.Since(proximityStart)
	}

	sortStart := time.Now()
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		if candidates[i].move.Y != candidates[j].move.Y {
			return candidates[i].move.Y < candidates[j].move.Y
		}
		return candidates[i].move.X < candidates[j].move.X
	})
	if stats != nil {
		stats.QuietSortTime += time.Since(sortStart)
	}
	return candidates
}

func collectCandidateMoves(state GameState, rules Rules, currentPlayer PlayerColor, boardSize int) []candidateMove {
	return collectCandidateMovesWithEval(state, rules, currentPlayer, boardSize, nil, nil)
}

// ================================================================
// Candidate limits
// Ply-depth caps and candidate count ceilings.
// ================================================================

func hardPlyCandidateCap(config Config, depthFromRoot int) int {
	switch {
	case depthFromRoot >= 9:
		if config.AiMaxCandidatesPly9 > 0 {
			return config.AiMaxCandidatesPly9
		}
		return 8
	case depthFromRoot == 8:
		if config.AiMaxCandidatesPly8 > 0 {
			return config.AiMaxCandidatesPly8
		}
		return 12
	case depthFromRoot == 7:
		if config.AiMaxCandidatesPly7 > 0 {
			return config.AiMaxCandidatesPly7
		}
		return 16
	default:
		if config.AiMaxCandidatesRoot > 0 {
			return config.AiMaxCandidatesRoot
		}
		return 24
	}
}

func tacticalKLimit(config Config, depthFromRoot int) int {
	if depthFromRoot == 0 && config.AiKTactRoot > 0 {
		return config.AiKTactRoot
	}
	if depthFromRoot <= 2 && config.AiKTactMid > 0 {
		return config.AiKTactMid
	}
	if config.AiKTactDeep > 0 {
		return config.AiKTactDeep
	}
	return 0
}

func candidateLimit(ctx minimaxContext, depthLeft, depthFromRoot int, tactical bool) int {
	config := ctx.settings.Config
	if config.AiEnableHardPlyCaps {
		limit := hardPlyCandidateCap(config, depthFromRoot)
		if config.AiEnableTacticalK && tactical {
			if tacticalLimit := tacticalKLimit(config, depthFromRoot); tacticalLimit > 0 && tacticalLimit < limit {
				limit = tacticalLimit
			}
		}
		return limit
	}

	limit := 0
	if config.AiEnableTacticalK && tactical {
		limit = tacticalKLimit(config, depthFromRoot)
	} else if !config.AiEnableDynamicTopK {
		// no static cap when dynamic top-k is disabled: falls through to 0 (no limit)
	} else if config.AiEnableTacticalK {
		if depthFromRoot == 0 && config.AiKQuietRoot > 0 {
			limit = config.AiKQuietRoot
		} else if depthFromRoot <= 2 && config.AiKQuietMid > 0 {
			limit = config.AiKQuietMid
		} else if config.AiKQuietDeep > 0 {
			limit = config.AiKQuietDeep
		}
	} else if depthFromRoot == 0 && config.AiMaxCandidatesRoot > 0 {
		limit = config.AiMaxCandidatesRoot
	} else if depthLeft >= 3 && config.AiMaxCandidatesDeep > 0 {
		limit = config.AiMaxCandidatesDeep
	} else if config.AiMaxCandidatesMid > 0 {
		limit = config.AiMaxCandidatesMid
	}

	if depthFromRoot >= 9 && config.AiMaxCandidatesPly9 > 0 {
		if limit <= 0 || config.AiMaxCandidatesPly9 < limit {
			limit = config.AiMaxCandidatesPly9
		}
	} else if depthFromRoot >= 8 && config.AiMaxCandidatesPly8 > 0 {
		if limit <= 0 || config.AiMaxCandidatesPly8 < limit {
			limit = config.AiMaxCandidatesPly8
		}
	} else if depthFromRoot >= 7 && config.AiMaxCandidatesPly7 > 0 {
		if limit <= 0 || config.AiMaxCandidatesPly7 < limit {
			limit = config.AiMaxCandidatesPly7
		}
	}
	return limit
}

func shouldLogMustBlock(ctx minimaxContext, depthFromRoot int, candidates []Move) bool {
	if depthFromRoot > 1 {
		return false
	}
	if ctx.mustBlockLog == nil {
		return true
	}
	key := formatMoves(candidates)
	ctx.mustBlockLog.mu.Lock()
	defer ctx.mustBlockLog.mu.Unlock()
	if _, ok := ctx.mustBlockLog.seen[key]; ok {
		return false
	}
	ctx.mustBlockLog.seen[key] = struct{}{}
	return true
}

// ================================================================
// Candidate merging and capping
// ================================================================

func mergeCandidateMoves(primary, fallback []candidateMove) []candidateMove {
	if len(primary) == 0 {
		return append([]candidateMove(nil), fallback...)
	}
	if len(fallback) == 0 {
		return append([]candidateMove(nil), primary...)
	}
	merged := make([]candidateMove, 0, len(primary)+len(fallback))
	seen := make(map[moveKey]int, len(primary)+len(fallback))
	for _, cand := range primary {
		key := moveKey{X: cand.move.X, Y: cand.move.Y}
		seen[key] = len(merged)
		merged = append(merged, cand)
	}
	for _, cand := range fallback {
		key := moveKey{X: cand.move.X, Y: cand.move.Y}
		if idx, ok := seen[key]; ok {
			if cand.priority < merged[idx].priority {
				merged[idx].priority = cand.priority
			}
			merged[idx].quietScore += cand.quietScore
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, cand)
	}
	return merged
}

func applyCandidateCap(candidates []Move, limit int) []Move {
	if limit <= 0 || len(candidates) <= limit {
		return candidates
	}
	return candidates[:limit]
}

func isKillerMove(ctx minimaxContext, depthFromRoot int, move Move) bool {
	if depthFromRoot < 0 || depthFromRoot >= len(ctx.killers) {
		return false
	}
	for _, km := range ctx.killers[depthFromRoot] {
		if km.Equals(move) {
			return true
		}
	}
	return false
}

func recordKiller(ctx minimaxContext, depthFromRoot int, move Move) {
	if depthFromRoot < 0 || depthFromRoot >= len(ctx.killers) {
		return
	}
	killers := ctx.killers[depthFromRoot]
	if len(killers) == 0 {
		ctx.killers[depthFromRoot] = []Move{move}
		return
	}
	if killers[0].Equals(move) {
		return
	}
	if len(killers) == 1 {
		ctx.killers[depthFromRoot] = []Move{killers[0], move}
		return
	}
	ctx.killers[depthFromRoot] = []Move{move, killers[0]}
}

func recordHistory(ctx minimaxContext, boardSize int, move Move, depthLeft int) {
	if len(ctx.history) == 0 || boardSize <= 0 {
		return
	}
	idx := move.Y*boardSize + move.X
	if idx < 0 || idx >= len(ctx.history) {
		return
	}
	bonus := depthLeft * depthLeft
	ctx.history[idx] += bonus
}

func expensiveOrderingPrefixLimit(depthFromRoot int, candidateCount int, maxCandidates int) int {
	limit := 8
	switch {
	case depthFromRoot == 0:
		limit = 10
	case depthFromRoot == 1:
		limit = 8
	default:
		limit = 6
	}
	if maxCandidates > 0 && maxCandidates < limit {
		limit = maxCandidates
	}
	if candidateCount < limit {
		limit = candidateCount
	}
	if limit < 1 {
		return 1
	}
	return limit
}

func orderCandidateMoves(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, candidates []candidateMove, maxCandidates int, pvMove *Move) []Move {
	evalSettings := ctx.settings
	evalSettings.Player = currentPlayer
	// Full move simulation + eval for ordering is expensive; keep it to shallow nodes.
	useExpensiveOrdering := depthFromRoot <= 2
	type scoredMove struct {
		score    float64
		priority int
		move     Move
	}
	scored := make([]scoredMove, 0, len(candidates))
	cache := selectCache(ctx)
	opponentHasImmediateWin := false
	playerCaptureMoves := make(map[int]int, 8)
	opponentCaptureMoves := make(map[int]int, 8)
	if useExpensiveOrdering {
		opponentHasImmediateWin = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)
		for _, move := range findCaptureMoves(state, ctx.rules, currentPlayer, ctx.evalState) {
			playerCaptureMoves[move.Y*ctx.settings.BoardSize+move.X]++
		}
		for _, move := range findCaptureMoves(state, ctx.rules, otherPlayer(currentPlayer), ctx.evalState) {
			opponentCaptureMoves[move.Y*ctx.settings.BoardSize+move.X]++
		}
	}
	applyCheapBoosts := func(move Move, score float64) float64 {
		if pvMove != nil && move.Equals(*pvMove) {
			boost := float64(ctx.settings.Config.AiHistoryBoost * 2)
			if maximizing {
				score += boost
			} else {
				score -= boost
			}
		}
		if ctx.settings.Config.AiEnableKillerMoves && isKillerMove(ctx, depthFromRoot, move) {
			boost := float64(ctx.settings.Config.AiKillerBoost)
			if maximizing {
				score += boost
			} else {
				score -= boost
			}
		}
		if ctx.settings.Config.AiEnableHistoryMoves && len(ctx.history) > 0 {
			idx := move.Y*ctx.settings.BoardSize + move.X
			if idx >= 0 && idx < len(ctx.history) {
				boost := float64(ctx.history[idx] * ctx.settings.Config.AiHistoryBoost)
				if maximizing {
					score += boost
				} else {
					score -= boost
				}
			}
		}
		if useExpensiveOrdering {
			idx := move.Y*ctx.settings.BoardSize + move.X
			if hits := playerCaptureMoves[idx]; hits > 0 {
				boost := float64(220 * hits)
				if maximizing {
					score += boost
				} else {
					score -= boost
				}
			}
			if covers := opponentCaptureMoves[idx]; covers > 0 {
				boost := float64(180 * covers)
				if maximizing {
					score += boost
				} else {
					score -= boost
				}
			}
		}
		return score
	}
	for _, cand := range candidates {
		move := cand.move
		scored = append(scored, scoredMove{
			score:    applyCheapBoosts(move, float64(cand.quietScore)),
			priority: cand.priority,
			move:     move,
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].priority != scored[j].priority {
			return scored[i].priority < scored[j].priority
		}
		if maximizing {
			return scored[i].score > scored[j].score
		}
		return scored[i].score < scored[j].score
	})
	if useExpensiveOrdering && len(scored) > 0 {
		prefix := expensiveOrderingPrefixLimit(depthFromRoot, len(scored), maxCandidates)
		for i := 0; i < prefix; i++ {
			move := scored[i].move
			priority := scored[i].priority
			if isImmediateWinCached(cache, state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
				if prioWin < priority {
					priority = prioWin
				}
			} else if opponentHasImmediateWin {
				blockState := state
				var undo searchMoveUndo
				if applyMoveWithUndo(&blockState, ctx.rules, move, currentPlayer, nil, &undo) {
					if !hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config) {
						if prioBlockWin < priority {
							priority = prioBlockWin
						}
					}
					undoMoveWithUndo(&blockState, nil, undo)
				}
			}
			scored[i].priority = priority
			scored[i].score = applyCheapBoosts(move, heuristicForMove(state, ctx.rules, evalSettings, move, ctx.evalState))
		}
		sort.SliceStable(scored, func(i, j int) bool {
			if scored[i].priority != scored[j].priority {
				return scored[i].priority < scored[j].priority
			}
			if maximizing {
				return scored[i].score > scored[j].score
			}
			return scored[i].score < scored[j].score
		})
	}
	if maxCandidates > 0 && len(scored) > maxCandidates {
		scored = scored[:maxCandidates]
	}
	moves := make([]Move, 0, len(scored))
	for _, entry := range scored {
		moves = append(moves, entry.move)
	}
	return moves
}

// ================================================================
// Quiet threat scoring
// Priority and secondary squares for non-forcing threats.
// ================================================================

func quietPriorityForThreat(owner, currentPlayer PlayerColor, typ PatternType) int {
	mine := owner == currentPlayer
	switch typ {
	case PatternOpen4:
		if mine {
			return prioQuietOwn4
		}
		return prioQuietOpp4
	case PatternClosed4, PatternBroken4:
		if mine {
			return prioQuietOwn4 + 1
		}
		return prioQuietOpp4 + 1
	case PatternOpen3:
		if mine {
			return prioQuietOwn3
		}
		return prioQuietOpp3
	case PatternBroken3, PatternClosed3:
		if mine {
			return prioQuietOwn3 + 1
		}
		return prioQuietOpp3 + 1
	case PatternOpen2, PatternClosed2, PatternBroken2:
		if mine {
			return prioQuietOwn2
		}
		return prioQuietOpp2
	default:
		return prioDefault
	}
}

func quietScoreForThreat(owner, currentPlayer PlayerColor, threat Threat) int {
	score := threatSeverityForPattern(PatternType(threat.Type)) * 10
	if owner == currentPlayer {
		score += 5
	}
	if len(threat.ExtensionSquares) > 1 {
		score += 8
	}
	if threat.ForkPotential {
		score += 12
	}
	return score
}

func threatDirectionDelta(direction int) (int, int, bool) {
	switch threatDirection(direction) {
	case threatDirRow:
		return 1, 0, true
	case threatDirCol:
		return 0, 1, true
	case threatDirDiagDown:
		return 1, 1, true
	case threatDirDiagUp:
		return 1, -1, true
	default:
		return 0, 0, false
	}
}

func quietSecondarySquares(board Board, threat Threat) []Pos {
	dx, dy, ok := threatDirectionDelta(threat.Direction)
	if !ok || len(threat.PatternCells) == 0 {
		return nil
	}
	out := make([]Pos, 0, len(threat.PatternCells)*2)
	seen := make(map[moveKey]struct{}, len(threat.PatternCells)*2)
	hasPrimary := make(map[moveKey]struct{}, len(threat.ExtensionSquares))
	for _, pos := range threat.ExtensionSquares {
		hasPrimary[moveKey{X: pos.X, Y: pos.Y}] = struct{}{}
	}
	for _, pos := range threat.PatternCells {
		candidates := [2]Pos{
			{X: pos.X - dx, Y: pos.Y - dy},
			{X: pos.X + dx, Y: pos.Y + dy},
		}
		for _, candidate := range candidates {
			if !board.InBounds(candidate.X, candidate.Y) || !board.IsEmpty(candidate.X, candidate.Y) {
				continue
			}
			key := moveKey{X: candidate.X, Y: candidate.Y}
			if _, blocked := hasPrimary[key]; blocked {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func addQuietThreatMoves(candidates *[]candidateMove, seen map[moveKey]int, moves []Move, priority, score int) {
	for _, move := range moves {
		key := moveKey{X: move.X, Y: move.Y}
		if idx, ok := seen[key]; ok {
			if priority < (*candidates)[idx].priority {
				(*candidates)[idx].priority = priority
			}
			(*candidates)[idx].quietScore += score
			continue
		}
		seen[key] = len(*candidates)
		*candidates = append(*candidates, candidateMove{move: move, priority: priority, quietScore: score})
	}
}

func addQuietThreatPositions(candidates *[]candidateMove, seen map[moveKey]int, board Board, boardSize int, positions []Pos, priority, score int) {
	if len(positions) == 0 {
		return
	}
	localSeen := make(map[moveKey]struct{}, len(positions))
	for _, pos := range positions {
		move := Move{X: pos.X, Y: pos.Y}
		if !move.IsValid(boardSize) || !board.IsEmpty(move.X, move.Y) {
			continue
		}
		key := moveKey{X: move.X, Y: move.Y}
		if _, ok := localSeen[key]; ok {
			continue
		}
		localSeen[key] = struct{}{}
		if idx, ok := seen[key]; ok {
			if priority < (*candidates)[idx].priority {
				(*candidates)[idx].priority = priority
			}
			(*candidates)[idx].quietScore += score
			continue
		}
		seen[key] = len(*candidates)
		*candidates = append(*candidates, candidateMove{move: move, priority: priority, quietScore: score})
	}
}

func quietThreatRank(owner, currentPlayer PlayerColor, threat Threat) int {
	score := quietScoreForThreat(owner, currentPlayer, threat)
	score += int(threat.UrgencyScore * 10.0)
	score += int(threat.BestFollowupTier) * 20
	score += threat.NumStrongExtensions * 12
	score += threat.RealDefenseCount * 6
	score += len(threat.ExtensionSquares) * 4
	score += len(threat.DefenseSquares) * 3
	score += threat.TotalStones * 2
	if threat.Stable {
		score += 8
	}
	if threat.ForkPotential {
		score += 16
	}
	return score
}

func selectTopQuietThreats(owner, currentPlayer PlayerColor, threats []Threat, limit int) []Threat {
	if len(threats) == 0 {
		return nil
	}
	selected := append([]Threat(nil), threats...)
	sort.SliceStable(selected, func(i, j int) bool {
		leftPriority := quietPriorityForThreat(owner, currentPlayer, PatternType(selected[i].Type))
		rightPriority := quietPriorityForThreat(owner, currentPlayer, PatternType(selected[j].Type))
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftRank := quietThreatRank(owner, currentPlayer, selected[i])
		rightRank := quietThreatRank(owner, currentPlayer, selected[j])
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		if selected[i].Tier != selected[j].Tier {
			return selected[i].Tier > selected[j].Tier
		}
		return selected[i].Type < selected[j].Type
	})
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}
	return selected
}

// ================================================================
// Alignment locality
// Root candidates based on stone-alignment patterns.
// ================================================================

func buildAlignmentThreatObjects(board Board, summaries []evalLineSummary, player PlayerColor) []Threat {
	out := make([]Threat, 0, 32)
	for _, line := range summaries {
		var threats []evalLUTThreat
		if player == PlayerBlue {
			threats = line.blueLUTThreats
		} else {
			threats = line.redLUTThreats
		}
		for _, threat := range threats {
			switch threat.typ {
			case PatternOpen4, PatternClosed4, PatternBroken4, PatternOpen3, PatternBroken3, PatternClosed3, PatternOpen2, PatternClosed2, PatternBroken2:
			default:
				continue
			}
			out = append(out, buildThreatObjectFromLUT(board, player, threat))
		}
	}
	return out
}

func rootLocalityMovesForThreat(state GameState, boardSize int, owner, currentPlayer PlayerColor, threat Threat) []Move {
	var positions []Pos
	if owner == currentPlayer {
		positions = threat.ExtensionSquares
	} else {
		positions = threat.DefenseSquares
	}
	if len(positions) == 0 {
		return nil
	}
	moves := make([]Move, 0, len(positions))
	seen := make(map[moveKey]struct{}, len(positions))
	for _, pos := range positions {
		if !state.Board.InBounds(pos.X, pos.Y) || !state.Board.IsEmpty(pos.X, pos.Y) {
			continue
		}
		move := Move{X: pos.X, Y: pos.Y}
		key := moveKey{X: move.X, Y: move.Y}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if move.IsValid(boardSize) {
			moves = append(moves, move)
		}
	}
	return moves
}

func buildRootAlignmentLocalitySelection(state GameState, currentPlayer PlayerColor, boardSize int, evalState *EvalState, config Config) *rootLocalitySelection {
	if evalState == nil {
		return nil
	}
	allThreats := make([]rootLocalityThreatChoice, 0, 32)
	appendThreats := func(owner PlayerColor, threats []Threat) {
		for _, threat := range threats {
			moves := rootLocalityMovesForThreat(state, boardSize, owner, currentPlayer, threat)
			if len(moves) == 0 {
				continue
			}
			allThreats = append(allThreats, rootLocalityThreatChoice{
				Owner:     owner,
				Threat:    threat,
				Positions: moves,
			})
		}
	}
	appendThreats(currentPlayer, buildAlignmentThreatObjects(state.Board, evalState.lineSummaries, currentPlayer))
	appendThreats(otherPlayer(currentPlayer), buildAlignmentThreatObjects(state.Board, evalState.lineSummaries, otherPlayer(currentPlayer)))
	if len(allThreats) == 0 {
		return nil
	}
	type agg struct {
		move          Move
		priority      int
		quietScore    int
		touchCount    int
		threatIndices []int
	}
	byMove := make(map[moveKey]*agg, 32)
	for threatIdx, choice := range allThreats {
		priority := quietPriorityForThreat(choice.Owner, currentPlayer, PatternType(choice.Threat.Type))
		score := quietScoreForThreat(choice.Owner, currentPlayer, choice.Threat)
		for _, move := range choice.Positions {
			key := moveKey{X: move.X, Y: move.Y}
			entry, ok := byMove[key]
			if !ok {
				entry = &agg{
					move:          move,
					priority:      priority,
					quietScore:    score,
					touchCount:    1,
					threatIndices: []int{threatIdx},
				}
				byMove[key] = entry
				continue
			}
			if priority < entry.priority {
				entry.priority = priority
			}
			entry.quietScore += score
			entry.touchCount++
			entry.threatIndices = append(entry.threatIndices, threatIdx)
		}
	}
	if len(byMove) == 0 {
		return nil
	}
	debugMoves := make([]rootLocalityMoveChoice, 0, len(byMove))
	for _, entry := range byMove {
		sort.Ints(entry.threatIndices)
		blueCount, redCount, totalCount := evalState.AlignmentUseCounts(entry.move)
		ownCount := redCount
		oppCount := blueCount
		if currentPlayer == PlayerBlue {
			ownCount = blueCount
			oppCount = redCount
		}
		debugMoves = append(debugMoves, rootLocalityMoveChoice{
			move:                entry.move,
			priority:            entry.priority,
			quietScore:          entry.quietScore,
			touchCount:          entry.touchCount,
			ownAlignmentCount:   ownCount,
			oppAlignmentCount:   oppCount,
			totalAlignmentCount: totalCount,
			threatIndices:       append([]int(nil), entry.threatIndices...),
		})
	}
	sort.SliceStable(debugMoves, func(i, j int) bool {
		if debugMoves[i].totalAlignmentCount != debugMoves[j].totalAlignmentCount {
			return debugMoves[i].totalAlignmentCount > debugMoves[j].totalAlignmentCount
		}
		if debugMoves[i].ownAlignmentCount != debugMoves[j].ownAlignmentCount {
			return debugMoves[i].ownAlignmentCount > debugMoves[j].ownAlignmentCount
		}
		if debugMoves[i].oppAlignmentCount != debugMoves[j].oppAlignmentCount {
			return debugMoves[i].oppAlignmentCount > debugMoves[j].oppAlignmentCount
		}
		if debugMoves[i].touchCount != debugMoves[j].touchCount {
			return debugMoves[i].touchCount > debugMoves[j].touchCount
		}
		if debugMoves[i].priority != debugMoves[j].priority {
			return debugMoves[i].priority < debugMoves[j].priority
		}
		if debugMoves[i].quietScore != debugMoves[j].quietScore {
			return debugMoves[i].quietScore > debugMoves[j].quietScore
		}
		if debugMoves[i].move.Y != debugMoves[j].move.Y {
			return debugMoves[i].move.Y < debugMoves[j].move.Y
		}
		return debugMoves[i].move.X < debugMoves[j].move.X
	})
	return &rootLocalitySelection{
		Threats: allThreats,
		Moves:   debugMoves,
	}
}

func buildRootAlignmentLocalityCandidates(state GameState, currentPlayer PlayerColor, boardSize int, evalState *EvalState, config Config) []candidateMove {
	_ = config
	if lutCandidates := buildThreatLUTCandidates(state, currentPlayer, boardSize, evalState, config); len(lutCandidates) > 0 {
		return lutCandidates
	}
	selection := buildRootAlignmentLocalitySelection(state, currentPlayer, boardSize, evalState, config)
	if selection == nil {
		return nil
	}
	out := make([]candidateMove, 0, len(selection.Moves))
	for _, choice := range selection.Moves {
		out = append(out, candidateMove{
			move:       choice.move,
			priority:   choice.priority,
			quietScore: choice.quietScore,
		})
	}
	return out
}

// ================================================================
// Quiet front candidates
// LUT-based move selection for non-tactical positions.
// ================================================================

func buildQuietFrontCandidates(state GameState, currentPlayer PlayerColor, boardSize int, evalState *EvalState, config Config) []candidateMove {
	if evalState != nil && !evalState.Summary.IsTactical {
		if lutCandidates := buildThreatLUTCandidates(state, currentPlayer, boardSize, evalState, config); len(lutCandidates) > 0 {
			return lutCandidates
		}
	}
	if evalState == nil || evalState.Summary.IsTactical {
		return nil
	}
	ownThreats := evalState.BuildQuietThreats(&state.Board, currentPlayer)
	oppThreats := evalState.BuildQuietThreats(&state.Board, otherPlayer(currentPlayer))
	if len(ownThreats) == 0 && len(oppThreats) == 0 {
		return nil
	}
	alignLimit := config.AiLocalityTopAlignments
	if alignLimit <= 0 {
		alignLimit = 2
	}
	ownThreats = selectTopQuietThreats(currentPlayer, currentPlayer, ownThreats, alignLimit)
	oppThreats = selectTopQuietThreats(otherPlayer(currentPlayer), currentPlayer, oppThreats, alignLimit)
	// Precompute a per-cell proximity mask: only cells within 3 Chebyshev steps
	// of any existing stone are eligible. This prevents threat extension/defense
	// squares that lie far from the active cluster from becoming candidates.
	const quietFrontProximityRadius = 3
	qfProxMask := buildStoneProximityMask(state.Board, boardSize, quietFrontProximityRadius)
	filterPos := func(positions []Pos) []Pos {
		filtered := make([]Pos, 0, len(positions))
		for _, p := range positions {
			idx := p.Y*boardSize + p.X
			if idx >= 0 && idx < len(qfProxMask) && qfProxMask[idx] {
				filtered = append(filtered, p)
			}
		}
		return filtered
	}
	seen := make(map[moveKey]int, 32)
	out := make([]candidateMove, 0, 32)
	addThreat := func(owner PlayerColor, threat Threat, positions []Pos, priorityAdjust int, scoreAdjust int) {
		if len(positions) == 0 {
			return
		}
		priority := quietPriorityForThreat(owner, currentPlayer, PatternType(threat.Type)) + priorityAdjust
		score := quietScoreForThreat(owner, currentPlayer, threat) + scoreAdjust
		addQuietThreatPositions(&out, seen, state.Board, boardSize, positions, priority, score)
	}
	for _, threat := range ownThreats {
		addThreat(currentPlayer, threat, filterPos(threat.ExtensionSquares), 0, 0)
		addThreat(currentPlayer, threat, filterPos(quietSecondarySquares(state.Board, threat)), 1, -15)
	}
	for _, threat := range oppThreats {
		addThreat(otherPlayer(currentPlayer), threat, filterPos(threat.DefenseSquares), 0, 0)
		addThreat(otherPlayer(currentPlayer), threat, filterPos(quietSecondarySquares(state.Board, threat)), 1, -15)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].priority != out[j].priority {
			return out[i].priority < out[j].priority
		}
		if out[i].quietScore != out[j].quietScore {
			return out[i].quietScore > out[j].quietScore
		}
		if out[i].move.Y != out[j].move.Y {
			return out[i].move.Y < out[j].move.Y
		}
		return out[i].move.X < out[j].move.X
	})
	return out
}

// ================================================================
// Node candidate dispatch
// Chooses the final ordered candidate list for a node.
// ================================================================

func orderCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, maxCandidates int, pvMove *Move) []Move {
	context := AnalyzeThreats(state, ctx.rules, ctx.settings, currentPlayer, ctx.evalState)
	threatCandidates := GenerateThreatCandidates(context, state, ctx.rules)
	candidates := collectCandidateMovesWithEval(state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.evalState, ctx.settings.Stats)
	if len(threatCandidates) > 0 {
		candidates = mergeCandidateMoves(threatCandidates, candidates)
	}
	return orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, candidates, maxCandidates, pvMove)
}

func orderMovesFromList(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, moves []Move, pvMove *Move, priority int) []Move {
	candidates := make([]candidateMove, 0, len(moves))
	for _, move := range moves {
		candidates = append(candidates, candidateMove{move: move, priority: priority})
	}
	return orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, candidates, 0, pvMove)
}

func isTacticalPosition(state GameState, ctx minimaxContext, currentPlayer PlayerColor) bool {
	context := AnalyzeThreats(state, ctx.rules, ctx.settings, currentPlayer, ctx.evalState)
	return context.IsSoftTactical || context.IsHardTactical
}

func tacticalCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor) []candidateMove {
	context := AnalyzeThreats(state, ctx.rules, ctx.settings, currentPlayer, ctx.evalState)
	return GenerateThreatCandidates(context, state, ctx.rules)
}

func chooseNodeCandidatesFromThreatContext(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, maxCandidates int, pvMove *Move, context ThreatContext) ([]Move, bool, bool) {
	if hasForcedThreatResponses(context, ctx.settings.BoardSize) {
		restricted := buildHardRestrictedNodeCandidates(state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove, context)
		if len(restricted) > 0 {
			return restricted, len(context.WinningMoves) > 0, true
		}
	}
	threatStart := time.Now()
	threatCandidates := GenerateThreatCandidates(context, state, ctx.rules)
	if stats := ctx.settings.Stats; stats != nil {
		stats.GenerateThreatsTime += time.Since(threatStart)
	}
	if len(threatCandidates) > 0 {
		orderStart := time.Now()
		ordered := orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, threatCandidates, maxCandidates, pvMove)
		if stats := ctx.settings.Stats; stats != nil {
			stats.OrderCandidatesTime += time.Since(orderStart)
		}
		return ordered, len(context.WinningMoves) > 0, false
	}
	collectStart := time.Now()
	quietCandidates := collectCandidateMovesWithEval(state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.evalState, ctx.settings.Stats)
	if stats := ctx.settings.Stats; stats != nil {
		stats.CollectCandidatesTime += time.Since(collectStart)
	}
	orderStart := time.Now()
	ordered := orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, quietCandidates, maxCandidates, pvMove)
	if stats := ctx.settings.Stats; stats != nil {
		stats.OrderCandidatesTime += time.Since(orderStart)
	}
	return ordered, len(context.WinningMoves) > 0, false
}

// ================================================================
// Board evaluation
// Heuristic scoring and eval cache.
// ================================================================

func hasStoneWithin(board Board, boardSize int) bool {
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) != CellEmpty {
				return true
			}
		}
	}
	return false
}

func evalKey(stateHash uint64, boardSize int, player PlayerColor) uint64 {
	return stateHash ^ mixKey(uint64(boardSize)<<32|uint64(player))
}

func evaluateBoardIncrementalOrFull(state GameState, config Config, evalState *EvalState) float64 {
	if evalState != nil {
		return float64(evalState.Score)
	}
	fresh := BuildEvalStateFromBoard(
		state.Board,
		state.ToMove,
		clampUint8(state.CapturedBlue),
		clampUint8(state.CapturedRed),
		config,
	)
	return float64(fresh.Score)
}

func evalBoardCached(state GameState, rules Rules, settings AIScoreSettings, cache *AISearchCache, evalState *EvalState) float64 {
	_ = rules
	if settings.SkipQueueBacklog || !settings.Config.AiEnableEvalCache {
		return evaluateBoardIncrementalOrFull(state, settings.Config, evalState)
	}
	evalCache := ensureEvalCache(cache, settings.Config)
	stateHash := state.Hash
	if evalCache != nil {
		if settings.Stats != nil {
			settings.Stats.EvalCacheProbes++
		}
		if stateHash != 0 {
			if value, ok := evalCache.Get(evalKey(stateHash, settings.BoardSize, state.ToMove)); ok {
				if settings.Stats != nil {
					settings.Stats.EvalCacheHits++
				}
				return value
			}
		}
	}
	sampleEvalTiming := false
	if stats := settings.Stats; stats != nil {
		nextCall := stats.HeuristicCalls + 1
		sampleEvalTiming = (nextCall & searchTimingSampleMask) == 0
	}
	var evalStart time.Time
	if sampleEvalTiming {
		evalStart = time.Now()
	}
	value := evaluateBoardIncrementalOrFull(state, settings.Config, evalState)
	if stats := settings.Stats; stats != nil {
		stats.HeuristicCalls++
		if sampleEvalTiming {
			stats.HeuristicTime += time.Since(evalStart)
		}
	}
	if evalCache != nil && stateHash != 0 {
		if math.Abs(value) >= settings.Config.AiEvalCacheMinAbs {
			evalCache.Put(evalKey(stateHash, settings.BoardSize, state.ToMove), value)
		}
	}
	return value
}

func heuristicForMove(state GameState, rules Rules, settings AIScoreSettings, move Move, evalState *EvalState) float64 {
	if ok, _ := rules.IsLegal(state, move, settings.Player); !ok {
		return illegalScore
	}
	next := state
	if evalState == nil {
		fresh := BuildEvalStateFromBoard(
			next.Board, next.ToMove,
			clampUint8(next.CapturedBlue), clampUint8(next.CapturedRed),
			settings.Config,
		)
		evalState = &fresh
	}
	var undo searchMoveUndo
	if !applyMoveWithUndo(&next, rules, move, settings.Player, evalState, &undo) {
		return illegalScore
	}
	cache := selectCache(minimaxContext{settings: settings})
	score := evalBoardCached(next, rules, settings, cache, evalState)
	undoMoveWithUndo(&next, evalState, undo)
	return score
}

func evaluateStateHeuristic(state GameState, rules Rules, settings AIScoreSettings) float64 {
	return evaluateStateHeuristicWithEvaluator(state, rules, settings, nil)
}

// winDistanceScore adjusts a flat ±winScore by the ply depth from the root so
// that faster wins sort ahead of slower ones in the minimax tree:
//   - For a BLUE win (score < 0): returns -(winScore - plyFromRoot)
//     → more negative = reached sooner = preferred by the minimizing player
//   - For a RED win (score > 0): returns  (winScore - plyFromRoot)
//     → more positive = reached sooner = preferred by the maximizing player
//
// Non-win scores are returned unchanged.
func winDistanceScore(score float64, plyFromRoot int) float64 {
	if score >= winScore/2 {
		return winScore - float64(plyFromRoot)
	}
	if score <= -winScore/2 {
		return -winScore + float64(plyFromRoot)
	}
	return score
}

func evaluateStateHeuristicWithEvaluator(state GameState, rules Rules, settings AIScoreSettings, evalState *EvalState) float64 {
	switch state.Status {
	case StatusDraw:
		return 0.0
	case StatusBlueWon:
		return -winScore
	case StatusRedWon:
		return winScore
	}
	cache := selectCache(minimaxContext{settings: settings})
	return evalBoardCached(state, rules, settings, cache, evalState)
}

// ================================================================
// Tactical quiescence search
// Extends search in forcing positions.
// ================================================================

func tacticalQuiescenceMoveLimit(ctx minimaxContext, depthFromRoot int) int {
	config := ctx.settings.Config
	if depthFromRoot <= 1 {
		if config.AiKTactRoot > 0 {
			return minInt(config.AiKTactRoot, 12)
		}
		return 12
	}
	if depthFromRoot >= 7 {
		if config.AiKTactDeep > 0 {
			return minInt(config.AiKTactDeep, 8)
		}
		return 8
	}
	if config.AiKTactMid > 0 {
		return minInt(config.AiKTactMid, 10)
	}
	return 10
}

func buildTacticalQuiescenceMoves(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, pvMove *Move, context ThreatContext) []Move {
	threatCandidates := GenerateThreatCandidates(context, state, ctx.rules)
	if len(threatCandidates) == 0 {
		return nil
	}
	limit := tacticalQuiescenceMoveLimit(ctx, depthFromRoot)
	return orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, threatCandidates, limit, pvMove)
}

func shouldRunTacticalQuiescence(summary TacticalSummary) bool {
	return summary.WinNowBlue > 0 ||
		summary.WinNowRed > 0 ||
		summary.CaptureWinNowBlue > 0 ||
		summary.CaptureWinNowRed > 0 ||
		summary.Open4Blue > 0 ||
		summary.Open4Red > 0 ||
		summary.DoubleThreatBlue ||
		summary.DoubleThreatRed ||
		summary.CriticalCapturesBlue > 0 ||
		summary.CriticalCapturesRed > 0
}

func tacticalQuiescence(state *GameState, ctx minimaxContext, currentPlayer PlayerColor, depthFromRoot int, qDepth int, alpha, beta float64) float64 {
	standPat := evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
	if state.Status != StatusRunning || timedOut(ctx) || qDepth <= 0 {
		return standPat
	}
	eval := evaluateStateDetailedWithEvaluator(*state, ctx.settings, ctx.evalState)
	if !shouldRunTacticalQuiescence(eval.Summary) {
		return standPat
	}
	threatContext := AnalyzeThreats(*state, ctx.rules, ctx.settings, currentPlayer, ctx.evalState)
	if !threatContext.IsSoftTactical && !threatContext.IsHardTactical {
		return standPat
	}
	maximizing := currentPlayer == PlayerRed
	if maximizing {
		if standPat >= beta {
			return standPat
		}
		if standPat > alpha {
			alpha = standPat
		}
	} else {
		if standPat <= alpha {
			return standPat
		}
		if standPat < beta {
			beta = standPat
		}
	}
	candidates := buildTacticalQuiescenceMoves(*state, ctx, currentPlayer, maximizing, depthFromRoot, nil, threatContext)
	if len(candidates) == 0 {
		return standPat
	}
	best := standPat
	for _, move := range candidates {
		if timedOut(ctx) {
			break
		}
		var undo searchMoveUndo
		if !applyMoveWithUndo(state, ctx.rules, move, currentPlayer, ctx.evalState, &undo) {
			continue
		}
		value := tacticalQuiescence(state, ctx, otherPlayer(currentPlayer), depthFromRoot+1, qDepth-1, alpha, beta)
		undoMoveWithUndo(state, ctx.evalState, undo)
		if maximizing {
			if value > best {
				best = value
			}
			if best > alpha {
				alpha = best
			}
		} else {
			if value < best {
				best = value
			}
			if best < beta {
				beta = best
			}
		}
		if beta <= alpha {
			break
		}
	}
	return best
}

// ================================================================
// Search context setup and timeout management
// ================================================================

func timedOut(ctx minimaxContext) bool {
	if ctx.settings.ShouldStop != nil && ctx.settings.ShouldStop() {
		return true
	}
	if ctx.hasDeadline && !ctx.deadline.IsZero() && time.Now().After(ctx.deadline) {
		return true
	}
	if ctx.settings.TimeoutMs <= 0 {
		return false
	}
	elapsed := time.Since(ctx.start).Milliseconds()
	return int(elapsed) >= ctx.settings.TimeoutMs
}

func initOrderingTables(settings AIScoreSettings) ([][]Move, []int) {
	var killers [][]Move
	var history []int
	if settings.Config.AiEnableKillerMoves {
		killers = make([][]Move, settings.Depth+2)
	}
	if settings.Config.AiEnableHistoryMoves {
		history = make([]int, settings.BoardSize*settings.BoardSize)
	}
	return killers, history
}

func newMinimaxContext(rules Rules, settings AIScoreSettings, start time.Time) minimaxContext {
	killers, history := initOrderingTables(settings)
	ctx := minimaxContext{
		rules:        rules,
		settings:     settings,
		start:        start,
		killers:      killers,
		history:      history,
		mustBlockLog: &mustBlockLogger{seen: make(map[string]struct{}, 32)},
		logIndent:    0,
	}
	if settings.Config.AiTimeBudgetMs > 0 {
		ctx.deadline = start.Add(time.Duration(settings.Config.AiTimeBudgetMs-100) * time.Millisecond)
		ctx.hasDeadline = true
	}
	return ctx
}

func attachEvalState(ctx *minimaxContext, state GameState) {
	if ctx == nil {
		return
	}
	evalState := BuildEvalStateFromBoard(
		state.Board,
		state.ToMove,
		clampUint8(state.CapturedBlue),
		clampUint8(state.CapturedRed),
		ctx.settings.Config,
	)
	ctx.evalState = &evalState
}

// ================================================================
// Move application
// Apply and undo moves with incremental EvalState.
// ================================================================

type searchMoveUndo struct {
	move             Move
	player           PlayerColor
	captures         [8]Move
	captureCount     int
	evalUndo         EvalUndo
	hasEvalUndo      bool
	prevStatus       GameStatus
	prevToMove       PlayerColor
	prevHasLastMove  bool
	prevLastMove     Move
	prevLastMessage  string
	prevCapturedBlue int
	prevCapturedRed  int
	prevHash         uint64
	prevHashSym      [8]uint64
	prevCanonHash    uint64
}

func applyMove(state *GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return false
	}
	prevCapturedBlue := state.CapturedBlue
	prevCapturedRed := state.CapturedRed
	prevToMove := state.ToMove
	cell := playerCell(player)
	state.Board.Set(move.X, move.Y, cell)
	state.LastMove = move
	state.HasLastMove = true
	state.LastMessage = ""

	var captureBuf [8]Move
	captures := rules.FindCapturesInto(state.Board, move, cell, captureBuf[:0])
	for _, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
	}
	if len(captures) > 0 {
		capturedCount := len(captures)
		if player == PlayerBlue {
			state.CapturedBlue += capturedCount
		} else {
			state.CapturedRed += capturedCount
		}
	}

	totalCaptured := state.CapturedBlue
	if player == PlayerRed {
		totalCaptured = state.CapturedRed
	}
	if totalCaptured >= rules.CaptureWinStones() {
		if player == PlayerBlue {
			state.Status = StatusBlueWon
		} else {
			state.Status = StatusRedWon
		}
	} else if rules.IsWin(state.Board, move) {
		if player == PlayerBlue {
			state.Status = StatusBlueWon
		} else {
			state.Status = StatusRedWon
		}
	} else if rules.IsDraw(state.Board) {
		state.Status = StatusDraw
	} else {
		state.Status = StatusRunning
	}

	state.ToMove = otherPlayer(player)
	UpdateHashAfterMove(state, move, player, captures, prevToMove, prevCapturedBlue, prevCapturedRed)
	return true
}

func applyMoveWithUndo(state *GameState, rules Rules, move Move, player PlayerColor, evalState *EvalState, undo *searchMoveUndo) bool {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return false
	}
	prevCapturedBlue := state.CapturedBlue
	prevCapturedRed := state.CapturedRed
	prevToMove := state.ToMove
	if undo != nil {
		undo.move = move
		undo.player = player
		undo.captureCount = 0
		undo.hasEvalUndo = false
		undo.prevStatus = state.Status
		undo.prevToMove = state.ToMove
		undo.prevHasLastMove = state.HasLastMove
		undo.prevLastMove = state.LastMove
		undo.prevLastMessage = state.LastMessage
		undo.prevCapturedBlue = state.CapturedBlue
		undo.prevCapturedRed = state.CapturedRed
		undo.prevHash = state.Hash
		undo.prevHashSym = state.HashSym
		undo.prevCanonHash = state.CanonHash
	}
	cell := playerCell(player)
	state.Board.Set(move.X, move.Y, cell)
	state.LastMove = move
	state.HasLastMove = true
	state.LastMessage = ""

	var captureBuf []Move
	if undo != nil {
		captureBuf = undo.captures[:0]
	}
	captures := rules.FindCapturesInto(state.Board, move, cell, captureBuf)
	for i, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
		if undo != nil && i < len(undo.captures) {
			undo.captures[i] = captured
		}
	}
	if undo != nil {
		undo.captureCount = len(captures)
	}
	if len(captures) > 0 {
		capturedCount := len(captures)
		if player == PlayerBlue {
			state.CapturedBlue += capturedCount
		} else {
			state.CapturedRed += capturedCount
		}
	}

	totalCaptured := state.CapturedBlue
	if player == PlayerRed {
		totalCaptured = state.CapturedRed
	}
	if totalCaptured >= rules.CaptureWinStones() {
		if player == PlayerBlue {
			state.Status = StatusBlueWon
		} else {
			state.Status = StatusRedWon
		}
	} else if rules.IsWin(state.Board, move) {
		if player == PlayerBlue {
			state.Status = StatusBlueWon
		} else {
			state.Status = StatusRedWon
		}
	} else if rules.IsDraw(state.Board) {
		state.Status = StatusDraw
	} else {
		state.Status = StatusRunning
	}

	state.ToMove = otherPlayer(player)
	UpdateHashAfterMove(state, move, player, captures, prevToMove, prevCapturedBlue, prevCapturedRed)
	if evalState != nil {
		delta := MoveDelta{
			Move:               move,
			Player:             player,
			CapturedCount:      uint8(len(captures)),
			CapturePairsGained: uint8(len(captures) / 2),
		}
		for i, captured := range captures {
			delta.CapturedCells[i] = CellIndex(captured.Y*state.Board.Size() + captured.X)
		}
		evalUndo := evalState.ApplyMove(&state.Board, delta)
		if undo != nil {
			undo.evalUndo = evalUndo
			undo.hasEvalUndo = true
		}
	}
	return true
}

func undoMoveWithUndo(state *GameState, evalState *EvalState, undo searchMoveUndo) {
	if evalState != nil && undo.hasEvalUndo {
		evalState.UndoMove(undo.evalUndo)
	}
	state.Board.Remove(undo.move.X, undo.move.Y)
	capturedCell := playerCell(otherPlayer(undo.player))
	for i := 0; i < undo.captureCount; i++ {
		captured := undo.captures[i]
		state.Board.Set(captured.X, captured.Y, capturedCell)
	}
	state.Status = undo.prevStatus
	state.ToMove = undo.prevToMove
	state.HasLastMove = undo.prevHasLastMove
	state.LastMove = undo.prevLastMove
	state.LastMessage = undo.prevLastMessage
	state.CapturedBlue = undo.prevCapturedBlue
	state.CapturedRed = undo.prevCapturedRed
	state.Hash = undo.prevHash
	state.HashSym = undo.prevHashSym
	state.CanonHash = undo.prevCanonHash
}

// ================================================================
// Late Move Reduction (LMR)
// ================================================================

func shouldApplyLMR(depth int, moveIndex int, quietNode bool) bool {
	if !quietNode {
		return false
	}
	if depth < lmrMinDepth {
		return false
	}
	return moveIndex >= lmrLateMoveStart
}

// ================================================================
// Immediate win and capture-win detection
// ================================================================

func isImmediateWin(state GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(state, move, player); !ok {
		return false
	}
	board := state.Board
	cell := playerCell(player)
	board.Set(move.X, move.Y, cell)
	defer board.Remove(move.X, move.Y)
	var captureBuf [8]Move
	captures := rules.FindCapturesInto(board, move, cell, captureBuf[:0])
	capturedCount := len(captures)
	totalCaptured := state.CapturedBlue
	if player == PlayerRed {
		totalCaptured = state.CapturedRed
	}
	totalCaptured += capturedCount
	if totalCaptured >= rules.CaptureWinStones() {
		return true
	}
	return rules.IsWin(board, move)
}

func isImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, move Move, player PlayerColor, boardSize int) bool {
	_ = cache
	_ = boardSize
	return isImmediateWin(state, rules, move, player)
}

func findAlignmentWinMoves(board Board, player PlayerColor, winLen int) []Move {
	if winLen <= 0 {
		winLen = 5
	}
	size := board.Size()
	cellCount := size * size
	var seenStack [maxSearchBoardCells]bool
	seen := seenStack[:0]
	if cellCount <= len(seenStack) {
		seen = seenStack[:cellCount]
	} else {
		seen = make([]bool, cellCount)
	}
	moves := make([]Move, 0, 8)
	cell := CellFromPlayer(player)
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !board.IsEmpty(x, y) {
				continue
			}
			for _, dir := range directions {
				left := countContiguous(board, x, y, -dir[0], -dir[1], cell)
				right := countContiguous(board, x, y, dir[0], dir[1], cell)
				if left+right+1 >= winLen {
					idx := y*size + x
					if !seen[idx] {
						seen[idx] = true
						moves = append(moves, Move{X: x, Y: y})
					}
					break
				}
			}
		}
	}
	return moves
}

func wouldCapture(board Board, move Move, playerCell, opponentCell Cell) bool {
	directions := [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}}
	for i := 0; i < 8; i++ {
		dx := directions[i][0]
		dy := directions[i][1]
		x1 := move.X + dx
		y1 := move.Y + dy
		x2 := move.X + 2*dx
		y2 := move.Y + 2*dy
		x3 := move.X + 3*dx
		y3 := move.Y + 3*dy
		if !board.InBounds(x3, y3) || !board.InBounds(x2, y2) || !board.InBounds(x1, y1) {
			continue
		}
		if board.At(x1, y1) == opponentCell && board.At(x2, y2) == opponentCell && board.At(x3, y3) == playerCell {
			return true
		}
	}
	return false
}

// ================================================================
// Capture mechanics
// Finding and responding to capture threats.
// ================================================================

func captureStoneCountForMove(board Board, rules Rules, move Move, player PlayerColor) int {
	return len(rules.FindCaptures(board, move, CellFromPlayer(player)))
}

func findCaptureMoves(state GameState, rules Rules, player PlayerColor, evalState *EvalState) []Move {
	if evalState != nil {
		return evalState.captureMoves(state, rules, player)
	}
	return findCaptureMovesByScan(state, rules, player)
}

func findCaptureMovesByScan(state GameState, rules Rules, player PlayerColor) []Move {
	board := state.Board
	size := board.Size()
	cellCount := size * size
	var seenStack [maxSearchBoardCells]bool
	seen := seenStack[:0]
	if cellCount <= len(seenStack) {
		seen = seenStack[:cellCount]
	} else {
		seen = make([]bool, cellCount)
	}
	moves := make([]Move, 0, 8)
	playerCell := CellFromPlayer(player)
	opponentCell := CellFromPlayer(otherPlayer(player))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					if chebDist(dx, dy) > 2 {
						continue
					}
					nx := x + dx
					ny := y + dy
					if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
						continue
					}
					idx := ny*size + nx
					if seen[idx] {
						continue
					}
					seen[idx] = true
					move := Move{X: nx, Y: ny}
					if ok, _ := rules.IsLegal(state, move, player); !ok {
						continue
					}
					if wouldCapture(board, move, playerCell, opponentCell) {
						moves = append(moves, move)
					}
				}
			}
		}
	}
	return moves
}

func countCapturablePairs(board Board, player PlayerColor) int {
	playerCell := CellFromPlayer(player)
	opponentCell := CellFromPlayer(otherPlayer(player))
	size := board.Size()
	directions := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	count := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if board.At(x, y) != playerCell {
				continue
			}
			for _, dir := range directions {
				dx := dir[0]
				dy := dir[1]
				x2 := x + dx
				y2 := y + dy
				if !board.InBounds(x2, y2) || board.At(x2, y2) != playerCell {
					continue
				}
				leftX := x - dx
				leftY := y - dy
				rightX := x + 2*dx
				rightY := y + 2*dy
				leftIsOpp := board.InBounds(leftX, leftY) && board.At(leftX, leftY) == opponentCell
				leftIsEmpty := board.InBounds(leftX, leftY) && board.At(leftX, leftY) == CellEmpty
				rightIsOpp := board.InBounds(rightX, rightY) && board.At(rightX, rightY) == opponentCell
				rightIsEmpty := board.InBounds(rightX, rightY) && board.At(rightX, rightY) == CellEmpty
				if (leftIsOpp && rightIsEmpty) || (rightIsOpp && leftIsEmpty) {
					count++
				}
			}
		}
	}
	return count
}

func findCaptureWinMoves(state GameState, rules Rules, player PlayerColor) []Move {
	remaining := rules.CaptureWinStones()
	if player == PlayerBlue {
		remaining -= state.CapturedBlue
	} else {
		remaining -= state.CapturedRed
	}
	if remaining > 2 {
		return nil
	}
	return findCaptureMoves(state, rules, player, nil)
}

func capturesRemaining(state GameState, rules Rules, player PlayerColor) int {
	remaining := rules.CaptureWinStones()
	if player == PlayerBlue {
		remaining -= state.CapturedBlue
	} else {
		remaining -= state.CapturedRed
	}
	return remaining
}

func hasDecisiveCaptureThreat(state GameState, rules Rules, player PlayerColor) bool {
	return hasDecisiveCaptureThreatWithEval(state, rules, player, nil)
}

func hasDecisiveCaptureThreatWithEval(state GameState, rules Rules, player PlayerColor, evalState *EvalState) bool {
	remaining := capturesRemaining(state, rules, player)
	if remaining <= 0 {
		return true
	}
	if remaining > 4 {
		return false
	}
	captureMoves := findCaptureMoves(state, rules, player, evalState)
	if len(captureMoves) == 0 {
		return false
	}
	// Keep precise immediate-win detection only when it matters most.
	if remaining <= 2 {
		attackerCaptured := state.CapturedBlue
		if player == PlayerRed {
			attackerCaptured = state.CapturedRed
		}
		if _, _, ok := rules.FindImmediateCaptureWinMove(state, player, attackerCaptured); ok {
			return true
		}
		return false
	}
	return true
}

func findImmediateCaptureDefenseMovesWithEval(state GameState, rules Rules, defender PlayerColor, attacker PlayerColor, boardSize int, evalState *EvalState) []Move {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	initial := uniqueMoves(findCaptureMoves(state, rules, attacker, evalState), boardSize)
	if len(initial) == 0 {
		return nil
	}

	candidates := buildCaptureThreatResponseCandidates(state, rules, defender, attacker, boardSize, evalState)
	candidates = append(candidates, initial...)
	candidates = uniqueMoves(candidates, boardSize)

	probeState := state
	localEval := evalState
	blockAll := make([]Move, 0, len(candidates))
	reducing := make([]Move, 0, len(candidates))
	initialCount := len(initial)
	for _, move := range candidates {
		var undo searchMoveUndo
		if !applyMoveWithUndo(&probeState, rules, move, defender, localEval, &undo) {
			continue
		}
		after := len(findCaptureMoves(probeState, rules, attacker, localEval))
		undoMoveWithUndo(&probeState, localEval, undo)
		if after == 0 {
			blockAll = append(blockAll, move)
			continue
		}
		if after < initialCount {
			reducing = append(reducing, move)
		}
	}
	if len(blockAll) > 0 {
		return uniqueMoves(blockAll, boardSize)
	}
	if len(reducing) > 0 {
		return uniqueMoves(reducing, boardSize)
	}
	return nil
}

func findCaptureThreatResponses(state GameState, rules Rules, defender PlayerColor, attacker PlayerColor, boardSize int) []Move {
	return findCaptureThreatResponsesWithEval(state, rules, defender, attacker, boardSize, nil)
}

func findCaptureThreatResponsesWithEval(state GameState, rules Rules, defender PlayerColor, attacker PlayerColor, boardSize int, evalState *EvalState) []Move {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	candidates := buildCaptureThreatResponseCandidates(state, rules, defender, attacker, boardSize, evalState)
	if len(candidates) == 0 {
		if evalState != nil {
			return findCaptureThreatResponsesByScan(state, rules, defender, attacker, boardSize)
		}
		return nil
	}
	probeState := state
	localEval := evalState
	moves := make([]Move, 0, len(candidates))
	for _, move := range candidates {
		var undo searchMoveUndo
		if !applyMoveWithUndo(&probeState, rules, move, defender, localEval, &undo) {
			continue
		}
		if !hasDecisiveCaptureThreatWithEval(probeState, rules, attacker, localEval) {
			moves = append(moves, move)
		}
		undoMoveWithUndo(&probeState, localEval, undo)
	}
	if len(moves) > 0 || evalState == nil {
		return moves
	}
	return findCaptureThreatResponsesByScan(state, rules, defender, attacker, boardSize)
}

func buildCaptureThreatResponseCandidates(state GameState, rules Rules, defender PlayerColor, attacker PlayerColor, boardSize int, evalState *EvalState) []Move {
	seen := make(map[moveKey]struct{}, 16)
	moves := make([]Move, 0, 8)
	addMoves := func(list []Move) {
		for _, move := range list {
			if !move.IsValid(boardSize) {
				continue
			}
			if !state.Board.IsEmpty(move.X, move.Y) {
				continue
			}
			if ok, _ := rules.IsLegal(state, move, defender); !ok {
				continue
			}
			key := moveKey{X: move.X, Y: move.Y}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			moves = append(moves, move)
		}
	}
	addMoves(findCaptureMoves(state, rules, attacker, evalState))
	addMoves(findCaptureMoves(state, rules, defender, evalState))
	return moves
}

func findCaptureThreatResponsesByScan(state GameState, rules Rules, defender PlayerColor, attacker PlayerColor, boardSize int) []Move {
	board := state.Board
	moves := make([]Move, 0, 8)
	probeState := state
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if !board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(probeState, move, defender); !ok {
				continue
			}
			var undo searchMoveUndo
			if !applyMoveWithUndo(&probeState, rules, move, defender, nil, &undo) {
				continue
			}
			if !hasDecisiveCaptureThreatWithEval(probeState, rules, attacker, nil) {
				moves = append(moves, move)
			}
			undoMoveWithUndo(&probeState, nil, undo)
		}
	}
	return moves
}

// ================================================================
// Immediate-win move cache
// Avoids re-computing known wins.
// ================================================================

func immediateWinCacheKey(state GameState, boardSize int, player PlayerColor) (uint64, bool) {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize <= 0 {
		return 0, false
	}
	hash := state.Hash
	if hash == 0 {
		hash = ComputeHash(state)
	}
	key := hash ^ mixKey(uint64(boardSize)<<32|uint64(player)<<16|uint64(state.Status))
	if state.MustCapture {
		key ^= mixKey(0x6d5e3c4b2a190817)
	}
	key ^= mixKey(uint64(len(state.ForcedCaptureMoves)) << 8)
	for _, move := range state.ForcedCaptureMoves {
		if !move.IsValid(boardSize) {
			continue
		}
		key ^= mixKey(uint64(move.Y*boardSize+move.X+1) << 1)
	}
	return key, true
}

func getImmediateWinMovesCache(cache *AISearchCache, key uint64) ([]Move, bool) {
	if cache == nil || key == 0 {
		return nil, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.ImmediateWinMoves == nil {
		return nil, false
	}
	moves, ok := cache.ImmediateWinMoves[key]
	return moves, ok
}

func putImmediateWinMovesCache(cache *AISearchCache, key uint64, moves []Move) {
	if cache == nil || key == 0 {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.ImmediateWinMoves == nil {
		cache.ImmediateWinMoves = make(map[uint64][]Move)
	}
	stored := append([]Move(nil), moves...)
	cache.ImmediateWinMoves[key] = stored
}

func findImmediateWinMovesCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) []Move {
	cacheKey, haveCacheKey := immediateWinCacheKey(state, boardSize, player)
	if haveCacheKey {
		if moves, ok := getImmediateWinMovesCache(cache, cacheKey); ok {
			return moves
		}
	}

	alignment := findAlignmentWinMoves(state.Board, player, rules.WinLength())
	capture := findCaptureWinMoves(state, rules, player)
	cellCount := boardSize * boardSize
	var seenStack [maxSearchBoardCells]bool
	seen := seenStack[:0]
	if cellCount <= len(seenStack) {
		seen = seenStack[:cellCount]
	} else {
		seen = make([]bool, cellCount)
	}
	candidates := make([]Move, 0, len(alignment)+len(capture))
	for _, move := range alignment {
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(seen) || seen[idx] {
			continue
		}
		seen[idx] = true
		candidates = append(candidates, move)
	}
	for _, move := range capture {
		idx := move.Y*boardSize + move.X
		if idx < 0 || idx >= len(seen) || seen[idx] {
			continue
		}
		seen[idx] = true
		candidates = append(candidates, move)
	}
	moves := make([]Move, 0, len(candidates))
	for _, move := range candidates {
		if ok, _ := rules.IsLegal(state, move, player); !ok {
			continue
		}
		if isImmediateWinCached(cache, state, rules, move, player, boardSize) {
			moves = append(moves, move)
		}
	}
	if haveCacheKey {
		putImmediateWinMovesCache(cache, cacheKey, moves)
	}
	return moves
}

func findBlockingMoves(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) []Move {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	board := state.Board
	moves := make([]Move, 0, 8)
	probeState := state
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if !board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(probeState, move, player); !ok {
				continue
			}
			var undo searchMoveUndo
			if !applyMoveWithUndo(&probeState, rules, move, player, nil, &undo) {
				continue
			}
			if !hasImmediateWinCached(cache, probeState, rules, otherPlayer(player), boardSize, config) {
				moves = append(moves, move)
			}
			undoMoveWithUndo(&probeState, nil, undo)
		}
	}
	return moves
}

func hasImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) bool {
	_ = cache
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	cacheKey, haveCacheKey := immediateWinCacheKey(state, boardSize, player)
	if haveCacheKey {
		if moves, ok := getImmediateWinMovesCache(cache, cacheKey); ok {
			return len(moves) > 0
		}
	}
	return len(findImmediateWinMovesCached(cache, state, rules, player, boardSize, config)) > 0
}

// ================================================================
// Move and board formatting utilities (debug output)
// ================================================================

func formatMoves(moves []Move) string {
	if len(moves) == 0 {
		return "[]"
	}
	out := make([]byte, 0, len(moves)*8)
	out = append(out, '[')
	for i, m := range moves {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, '(')
		out = append(out, []byte(fmt.Sprintf("%d,%d", m.X, m.Y))...)
		out = append(out, ')')
	}
	out = append(out, ']')
	return string(out)
}

func formatMovesLimited(moves []Move, limit int) string {
	if len(moves) == 0 {
		return "[]"
	}
	if limit <= 0 || len(moves) <= limit {
		return formatMoves(moves)
	}
	out := make([]byte, 0, limit*8+24)
	out = append(out, '[')
	for i := 0; i < limit; i++ {
		if i > 0 {
			out = append(out, ' ')
		}
		m := moves[i]
		out = append(out, '(')
		out = append(out, []byte(fmt.Sprintf("%d,%d", m.X, m.Y))...)
		out = append(out, ')')
	}
	out = append(out, []byte(fmt.Sprintf(" ... +%d]", len(moves)-limit))...)
	return string(out)
}

func formatScoredMoves(scores []float64, moves []Move, boardSize int) string {
	if len(moves) == 0 {
		return "[]"
	}
	out := make([]byte, 0, len(moves)*18)
	out = append(out, '[')
	for i, move := range moves {
		if i > 0 {
			out = append(out, ' ')
		}
		idx := move.Y*boardSize + move.X
		scoreText := "illegal"
		if idx >= 0 && idx < len(scores) {
			score := scores[idx]
			if score != illegalScore {
				scoreText = fmt.Sprintf("%.2f", score)
			}
		}
		out = append(out, []byte(fmt.Sprintf("(%d,%d)=%s", move.X, move.Y, scoreText))...)
	}
	out = append(out, ']')
	return string(out)
}

func formatScoredMovesLimited(scores []float64, moves []Move, boardSize int, limit int) string {
	if limit <= 0 || len(moves) <= limit {
		return formatScoredMoves(scores, moves, boardSize)
	}
	base := formatScoredMoves(scores, moves[:limit], boardSize)
	return base[:len(base)-1] + fmt.Sprintf(" ... +%d]", len(moves)-limit)
}

func cloneSearchDebugLine(line *SearchDebugLine) *SearchDebugLine {
	if line == nil {
		return nil
	}
	clone := &SearchDebugLine{FinalBoard: line.FinalBoard.Clone()}
	if len(line.Steps) == 0 {
		return clone
	}
	clone.Steps = make([]SearchDebugStep, len(line.Steps))
	for i, step := range line.Steps {
		clone.Steps[i].Player = step.Player
		clone.Steps[i].Move = step.Move
		if len(step.Captures) > 0 {
			clone.Steps[i].Captures = append([]Move(nil), step.Captures...)
		}
	}
	return clone
}

func leafSearchDebugLine(state GameState) *SearchDebugLine {
	return &SearchDebugLine{FinalBoard: state.Board.Clone()}
}

func debugBoardFromLine(line *SearchDebugLine) *Board {
	if line == nil || line.FinalBoard.Size() <= 0 {
		return nil
	}
	board := line.FinalBoard.Clone()
	return &board
}

func buildSearchDebugStep(player PlayerColor, move Move, undo searchMoveUndo) SearchDebugStep {
	step := SearchDebugStep{Player: player, Move: move}
	if undo.captureCount > 0 {
		step.Captures = append([]Move(nil), undo.captures[:undo.captureCount]...)
	}
	return step
}

func prependSearchDebugLine(step SearchDebugStep, child *SearchDebugLine, board Board) *SearchDebugLine {
	line := &SearchDebugLine{}
	line.Steps = append(line.Steps, step)
	if child != nil {
		line.Steps = append(line.Steps, cloneSearchDebugLine(child).Steps...)
		line.FinalBoard = child.FinalBoard.Clone()
		return line
	}
	line.FinalBoard = board.Clone()
	return line
}

func reconstructSearchDebugLineFromTT(state GameState, ctx minimaxContext, currentPlayer PlayerColor, depth int, bestMove Move) *SearchDebugLine {
	_ = currentPlayer
	_ = depth
	_ = bestMove
	cache := selectCache(ctx)
	tt := ensureTT(cache, ctx.settings.Config)
	if tt == nil {
		return leafSearchDebugLine(state)
	}
	heuristicHash := heuristicHashFromConfig(ctx.settings.Config)
	key := ttKeyFor(state, ctx.settings.BoardSize)
	if line, ok := tt.ProbeDebugLine(key, heuristicHash); ok {
		return line
	}
	if board, ok := tt.ProbeDebugBoard(key, heuristicHash); ok {
		return &SearchDebugLine{FinalBoard: board}
	}
	return leafSearchDebugLine(state)
}

// ================================================================
// Minimax search
// Recursive alpha-beta with TT, NMP, RFP, LMR, and PVS.
// ================================================================

// minimax is the recursive alpha-beta search engine. It returns the best score
// reachable from state for currentPlayer, searching up to depth plies.
//
// Enhancements applied (in order):
//  1. Transposition Table (TT) — exact/lower/upper bound re-use from prior searches.
//  2. Reverse Futility Pruning (RFP) — prunes shallow quiet nodes far from the window.
//  3. Null Move Pruning (NMP) — passes our turn at reduced depth to detect cutoffs.
//  4. Late Move Reduction (LMR) — reduces depth for late non-forcing moves.
//  5. Principal Variation Search (PVS) — uses a null window for non-first moves.
func minimax(state *GameState, ctx minimaxContext, depth int, currentPlayer PlayerColor, depthFromRoot int, alpha, beta float64, outLine **SearchDebugLine) float64 {
	if shouldLogDetailedSearchTrace(ctx.settings.Config) {
		logAITask(ctx, ctx.logIndent, "minimax enter depth=%d depthFromRoot=%d", depth, depthFromRoot)
	}
	if state.Status != StatusRunning {
		if outLine != nil {
			*outLine = leafSearchDebugLine(*state)
		}
		return winDistanceScore(evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState), depthFromRoot)
	}
	if timedOut(ctx) {
		if outLine != nil {
			*outLine = leafSearchDebugLine(*state)
		}
		return evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
	}
	if depth <= 0 {
		if outLine != nil {
			*outLine = leafSearchDebugLine(*state)
		}
		if ctx.settings.Config.AiEnableTacticalQuiescence {
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.TacticalQuiescenceCalls++
			}
			return tacticalQuiescence(state, ctx, currentPlayer, depthFromRoot, ctx.settings.Config.AiTacticalQuiescenceDepth, alpha, beta)
		}
		return evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
	}

	if ctx.settings.Stats != nil {
		ctx.settings.Stats.Nodes++
		if ctx.settings.Stats.Nodes == 1 || (ctx.settings.Stats.Nodes&searchProgressChunkMask) == 0 {
			reportSearchProgress(ctx.settings.Stats, ctx.settings)
		}
	}
	cache := selectCache(ctx)
	tt := ensureTT(cache, ctx.settings.Config)
	boardSize := ctx.settings.BoardSize
	boardHash := ttKeyFor(*state, boardSize)
	heuristicHash := heuristicHashFromConfig(ctx.settings.Config)
	alphaOrig := alpha
	betaOrig := beta
	var pvMove *Move
	if ctx.settings.Stats != nil {
		ctx.settings.Stats.TTProbes++
	}
	trace := ctx.settings.Config.AiLogSearchStats
	var ttStart time.Time
	if trace {
		ttStart = time.Now()
	}
	// Null move searches must not probe the TT: the TT key does not encode whose turn
	// it is (state.ToMove is not swapped), so regular-search entries at the same board
	// position would be returned, giving scores from the wrong player's perspective
	// and corrupting the null-move cutoff decision.
	if tt != nil && !ctx.nullMoveActive {
		if entry, ok := tt.Probe(boardHash, heuristicHash); ok {
			if trace {
				ttDuration := time.Since(ttStart).Milliseconds()
				if shouldLogDetailedSearchTrace(ctx.settings.Config) {
					logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms hit=true", depth, ttDuration)
				}
			}
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.TTHits++
				switch entry.Flag {
				case TTExact:
					ctx.settings.Stats.TTExactHits++
				case TTLower:
					ctx.settings.Stats.TTLowerHits++
				case TTUpper:
					ctx.settings.Stats.TTUpperHits++
				}
			}
			if entry.BestMove.IsValid(ctx.settings.BoardSize) {
				pv := entry.BestMove
				pvMove = &pv
			}
			if entry.Depth >= depth {
				if shouldLogDetailedSearchTrace(ctx.settings.Config) {
					logAITask(ctx, ctx.logIndent+1, "TT exact entry depth=%d flag=%d value=%.2f", entry.Depth, entry.Flag, entry.ScoreFloat())
				}
				if _, ret, value := applyTTEntry(entry, depth, &alpha, &beta, ctx.settings.Stats); ret {
					if outLine != nil {
						*outLine = reconstructSearchDebugLineFromTT(*state, ctx, currentPlayer, depth, entry.BestMove)
					}
					if shouldLogDetailedSearchTrace(ctx.settings.Config) {
						logAITask(ctx, ctx.logIndent+1, "TT exact returning value=%.2f", value)
					}
					return value
				}
			}
		} else {
			if trace && shouldLogDetailedSearchTrace(ctx.settings.Config) {
				ttDuration := time.Since(ttStart).Milliseconds()
				logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms hit=false", depth, ttDuration)
			}
		}
	} else {
		if trace && shouldLogDetailedSearchTrace(ctx.settings.Config) {
			ttDuration := time.Since(ttStart).Milliseconds()
			logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms table=nil", depth, ttDuration)
		}
	}
	if shouldLogDetailedSearchTrace(ctx.settings.Config) {
		logAITask(ctx, ctx.logIndent, "No TT hit; continuing search")
	}

	maximizing := currentPlayer == PlayerRed
	best := math.Inf(-1)
	if !maximizing {
		best = math.Inf(1)
	}
	secondBest := math.Inf(-1)
	secondBestMove := Move{}
	cache = selectCache(ctx)
	analyzeStart := time.Now()
	threatContext := AnalyzeThreats(*state, ctx.rules, ctx.settings, currentPlayer, ctx.evalState)
	analyzeDuration := time.Since(analyzeStart)
	tactical := threatContext.IsSoftTactical || threatContext.IsHardTactical
	maxCandidates := candidateLimit(ctx, depth, depthFromRoot, tactical)
	chooseStart := time.Now()
	candidates, forcedWinLine, hardRestricted := chooseNodeCandidatesFromThreatContext(*state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove, threatContext)
	candidates = applyCandidateCap(candidates, maxCandidates)
	chooseDuration := time.Since(chooseStart)
	candidatePhaseDuration := analyzeDuration + chooseDuration
	if stats := ctx.settings.Stats; stats != nil {
		stats.AnalyzeThreatsTime += analyzeDuration
		stats.ChooseCandidatesTime += chooseDuration
		switch {
		case hardRestricted || threatContext.IsHardTactical:
			stats.HardTacticalNodes++
			stats.HardTacticalCandidates += int64(len(candidates))
			stats.HardCandidateTime += candidatePhaseDuration
			stats.HardMoveEvaluations += int64(len(candidates))
		case threatContext.IsSoftTactical:
			stats.SoftTacticalNodes++
			stats.SoftTacticalCandidates += int64(len(candidates))
			stats.SoftCandidateTime += candidatePhaseDuration
			stats.SoftMoveEvaluations += int64(len(candidates))
		default:
			stats.QuietNodes++
			stats.QuietCandidates += int64(len(candidates))
			stats.QuietCandidateTime += candidatePhaseDuration
			stats.QuietMoveEvaluations += int64(len(candidates))
		}
	}
	if ctx.settings.Config.AiLogSearchStats {
		if shouldLogDetailedSearchTrace(ctx.settings.Config) && hardRestricted && len(threatContext.MustBlockMoves) > 0 && shouldLogMustBlock(ctx, depthFromRoot, candidates) {
			fmt.Printf("[ai:must_block] allowed=%s ordered=%s\n", formatMoves(candidates), formatMoves(candidates))
		}
		if shouldLogCandidateZone(ctx.settings.Config) && len(candidates) > 0 {
			fmt.Printf("[ai:candidate_zone] depth=%d depthFromRoot=%d tactical=%t hard=%t count=%d moves=%s\n",
				depth,
				depthFromRoot,
				tactical,
				hardRestricted,
				len(candidates),
				formatMovesLimited(candidates, 12),
			)
			if shouldLogCandidateBoardOverlay(ctx.settings.Config) {
				fmt.Printf("%s\n", formatBoardForCandidateZone(state.Board, candidates))
			}
		}
	}
	if ctx.settings.Stats != nil {
		ctx.settings.Stats.CandidateCount += int64(len(candidates))
		if depthFromRoot == 0 {
			ctx.settings.Stats.RootCandidates += int64(len(candidates))
			ctx.settings.Stats.RootSamples++
		} else {
			ctx.settings.Stats.DeepCandidates += int64(len(candidates))
			ctx.settings.Stats.DeepSamples++
		}
	}
	bestMove := Move{}
	bestRank := -1
	quietNode := !forcedWinLine && !hardRestricted && !tactical

	cfg := ctx.settings.Config

	// winScoreHalf separates forced-win/loss sentinel values from heuristic scores.
	// NMP and RFP must not fire when the window sits in sentinel territory:
	// e.g. beta = -winScore at a MAX node makes any null search result >= beta
	// (since -winScore is the score floor), so the prune fires unconditionally and
	// incorrectly marks non-losing moves as forced losses.
	const winScoreHalf = winScore / 2
	normalWindow := (!maximizing || beta > -winScoreHalf) && (maximizing || alpha < winScoreHalf)

	// Reverse Futility Pruning: at shallow quiet nodes compute one static eval.
	// If staticEval - margin >= beta (maximizing) or staticEval + margin <= alpha
	// (minimizing), the subtree almost certainly won't change the outcome — prune.
	if cfg.AiEnableRFP && quietNode && normalWindow && depthFromRoot > 0 {
		rfpMaxDepth := cfg.AiRFPMaxDepth
		if rfpMaxDepth <= 0 {
			rfpMaxDepth = 3
		}
		if depth <= rfpMaxDepth {
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.RFPAttempts++
			}
			rfpMargin := cfg.AiRFPMargin
			if rfpMargin <= 0 {
				rfpMargin = 200.0
			}
			staticEval := evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
			margin := rfpMargin * float64(depth)
			if maximizing && staticEval-margin >= beta {
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.RFPCutoffs++
				}
				return staticEval
			}
			if !maximizing && staticEval+margin <= alpha {
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.RFPCutoffs++
				}
				return staticEval
			}
		}
	}

	// Null Move Pruning: at quiet nodes with enough depth, try passing our turn.
	// If even the opponent, getting a free extra move at reduced depth, can't beat beta,
	// then our real search will also beat beta — return beta immediately.
	// Guard: never consecutive null moves; only quiet positions; skip at root;
	// skip when the window is in forced-win/loss territory (normalWindow).
	if cfg.AiEnableNMP && !ctx.nullMoveActive && quietNode && normalWindow && depthFromRoot > 0 {
		nmpMinDepth := cfg.AiNMPMinDepth
		if nmpMinDepth <= 0 {
			nmpMinDepth = 3
		}
		if depth >= nmpMinDepth {
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.NMPAttempts++
			}
			R := cfg.AiNMPReduction
			if R < 1 {
				R = 2
			}
			nullDepth := depth - R - 1
			if nullDepth < 1 {
				nullDepth = 1
			}
			nullOpponent := PlayerBlue
			if currentPlayer == PlayerBlue {
				nullOpponent = PlayerRed
			}
			ctx.nullMoveActive = true
			var nullPrune bool
			if maximizing {
				nv := minimax(state, ctx, nullDepth, nullOpponent, depthFromRoot+1, beta-1, beta, nil)
				nullPrune = nv >= beta
			} else {
				nv := minimax(state, ctx, nullDepth, nullOpponent, depthFromRoot+1, alpha, alpha+1, nil)
				nullPrune = nv <= alpha
			}
			ctx.nullMoveActive = false
			if nullPrune {
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.NMPCutoffs++
				}
				// MAX fail-high: even opponent with free move can't prevent us beating beta.
				// MIN fail-low: even opponent with free move can't raise score above alpha.
				if maximizing {
					return beta
				}
				return alpha
			}
		}
	}

	firstMove := Move{}
	if len(candidates) > 0 {
		firstMove = candidates[0]
	}
	var bestLine *SearchDebugLine
	searchAborted := false
	cutoffHappened := false
	cutoffOnFirst := false
	for idx, move := range candidates {
		if timedOut(ctx) {
			searchAborted = true
			break
		}
		if ctx.settings.Config.AiQuickWinExit && isImmediateWinCached(cache, *state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
			// Win happens one ply below the current node (after applying the move).
			win := winDistanceScore(-winScore, depthFromRoot+1)
			if currentPlayer == PlayerRed {
				win = winDistanceScore(winScore, depthFromRoot+1)
			}
			// Do not store to TT during null-move sub-searches: the TT key does not
			// encode whose turn it is, so an entry stored with the null opponent as
			// currentPlayer would be read back by the regular search with the real
			// player, returning an incorrect win/loss score for that side.
			if tt != nil && !ctx.nullMoveActive {
				meta := buildTTMeta(*state, ctx.settings.BoardSize, ctx.footprint)
				if outLine != nil {
					next := state.Clone()
					if applyMove(&next, ctx.rules, move, currentPlayer) {
						meta.DebugBoard = &next.Board
						meta.DebugLine = &SearchDebugLine{
							Steps:      []SearchDebugStep{{Player: currentPlayer, Move: move}},
							FinalBoard: next.Board.Clone(),
						}
					}
				}
				replaced, overwrote := tt.Store(boardHash, heuristicHash, depth, win, TTExact, move, meta)
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.TTStores++
					if replaced || overwrote {
						ctx.settings.Stats.TTOverwrites++
						ctx.settings.Stats.TTReplacements++
					}
				}
			}
			if stats := ctx.settings.Stats; stats != nil && len(candidates) > 1 {
				recordOrderingRank(&stats.NodeBestRankHistogram, idx)
				depthBucket := orderingDepthBucket(depthFromRoot)
				recordOrderingRank(&stats.NodeBestRankByDepth[depthBucket], idx)
			}
			return win
		}
		searchDepth := depth
		reducedSearch := false
		if shouldApplyLMR(depth, idx, quietNode) {
			searchDepth = depth - lmrReduction
			if searchDepth < 1 {
				searchDepth = 1
			}
			reducedSearch = searchDepth < depth
			if reducedSearch && ctx.settings.Stats != nil {
				ctx.settings.Stats.LMRReduced++
			}
		}
		// PVS: non-first moves use null window to confirm they won't exceed alpha/beta.
		// If the null window fails high, we re-search with the full window.
		isPVSMove := ctx.settings.Config.AiEnablePVS && idx > 0 && searchDepth > 1
		searchAlpha, searchBeta := alpha, beta
		if isPVSMove {
			if maximizing {
				searchBeta = alpha + 1
			} else {
				searchAlpha = beta - 1
			}
		}
		logSearchMove(ctx, depth, depthFromRoot, move, idx, len(candidates), searchDepth, searchAlpha, searchBeta, "")
		alphaBeforeMove := alpha
		betaBeforeMove := beta
		var line *SearchDebugLine
		lineOut := (**SearchDebugLine)(nil)
		if outLine != nil {
			lineOut = &line
		}
		value := evaluateMoveWithCache(state, ctx, currentPlayer, move, searchDepth, depthFromRoot, boardHash, lineOut, nil, nil, searchAlpha, searchBeta)
		// LMR re-search: reduced depth failed high → retry at full depth (still null window if PVS)
		if reducedSearch {
			needsResearch := false
			if maximizing {
				needsResearch = value > alpha
			} else {
				needsResearch = value < beta
			}
			if needsResearch {
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.LMRResearches++
				}
				value = evaluateMoveWithCache(state, ctx, currentPlayer, move, depth, depthFromRoot, boardHash, lineOut, nil, nil, searchAlpha, searchBeta)
			}
		}
		// PVS re-search: null window failed high → retry at full depth with full window
		if isPVSMove {
			needsResearch := false
			if maximizing {
				needsResearch = value > alpha
			} else {
				needsResearch = value < beta
			}
			if needsResearch {
				value = evaluateMoveWithCache(state, ctx, currentPlayer, move, depth, depthFromRoot, boardHash, lineOut, nil, nil, alpha, beta)
			}
		}
		if stats := ctx.settings.Stats; stats != nil && depthFromRoot > 0 && idx > 0 {
			wouldResearch := false
			if maximizing {
				wouldResearch = value > alphaBeforeMove
			} else {
				wouldResearch = value < betaBeforeMove
			}
			stats.PVSProxySamples++
			if wouldResearch {
				stats.PVSProxyWouldResearch++
			}
			switch {
			case threatContext.IsHardTactical:
				stats.PVSProxyHardSamples++
				if wouldResearch {
					stats.PVSProxyHardWouldResearch++
				}
			case threatContext.IsSoftTactical:
				stats.PVSProxySoftSamples++
				if wouldResearch {
					stats.PVSProxySoftWouldResearch++
				}
			default:
				stats.PVSProxyQuietSamples++
				if wouldResearch {
					stats.PVSProxyQuietWouldResearch++
				}
			}
			bucket := orderingDepthBucket(depth)
			stats.PVSProxyDepthSamples[bucket]++
			if wouldResearch {
				stats.PVSProxyDepthWouldResearch[bucket]++
			}
		}
		if maximizing {
			if value > best {
				secondBest = best
				secondBestMove = bestMove
				best = value
				bestMove = move
				bestRank = idx
				bestLine = cloneSearchDebugLine(line)
			} else if value > secondBest {
				secondBest = value
				secondBestMove = move
			}
			if best > alpha {
				alpha = best
			}
		} else {
			if value < best {
				best = value
				bestMove = move
				bestRank = idx
				bestLine = cloneSearchDebugLine(line)
			}
			if best < beta {
				beta = best
			}
		}
		if beta <= alpha {
			cutoffHappened = true
			cutoffOnFirst = idx == 0
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.Cutoffs++
				ctx.settings.Stats.ABCutoffs++
			}
			logPrune(ctx, depth, move, best, alpha, beta)
			if ctx.settings.Config.AiEnableKillerMoves {
				recordKiller(ctx, depthFromRoot, move)
			}
			if ctx.settings.Config.AiEnableHistoryMoves {
				recordHistory(ctx, ctx.settings.BoardSize, move, depth)
			}
			break
		}
		if timedOut(ctx) {
			searchAborted = true
			break
		}
	}
	if stats := ctx.settings.Stats; stats != nil && len(candidates) > 1 && !searchAborted {
		recordOrderingRank(&stats.NodeBestRankHistogram, bestRank)
		depthBucket := orderingDepthBucket(depthFromRoot)
		recordOrderingRank(&stats.NodeBestRankByDepth[depthBucket], bestRank)
		stats.NodeFirstLeadSamples++
		if firstMove == bestMove {
			stats.NodeFirstLeadWins++
		}
		if cutoffHappened {
			stats.NodeFirstCutoffSamples++
			if cutoffOnFirst {
				stats.NodeFirstCutoffWins++
			}
		} else {
			stats.NodeFirstExactSamples++
			if firstMove == bestMove {
				stats.NodeFirstExactWins++
			}
		}
	}

	if math.IsInf(best, 1) || math.IsInf(best, -1) {
		if outLine != nil {
			*outLine = leafSearchDebugLine(*state)
		}
		return 0.0
	}
	if depthFromRoot == 0 && maximizing {
		if best <= -winScore/2 && secondBest > math.Inf(-1) {
			best = secondBest
			bestMove = secondBestMove
			bestLine = nil
		}
	}
	flag := TTExact
	if best <= alphaOrig {
		flag = TTUpper
	} else if best >= betaOrig {
		flag = TTLower
	}
	if tt != nil && !ctx.nullMoveActive {
		meta := buildTTMeta(*state, ctx.settings.BoardSize, ctx.footprint)
		if outLine != nil {
			meta.DebugBoard = debugBoardFromLine(bestLine)
			meta.DebugLine = cloneSearchDebugLine(bestLine)
		}
		replaced, overwrote := tt.Store(boardHash, heuristicHash, depth, best, flag, bestMove, meta)
		if ctx.settings.Stats != nil {
			ctx.settings.Stats.TTStores++
			if replaced || overwrote {
				ctx.settings.Stats.TTOverwrites++
				ctx.settings.Stats.TTReplacements++
			}
		}
	}
	if outLine != nil {
		if bestLine != nil {
			*outLine = bestLine
		} else if bestMove.IsValid(ctx.settings.BoardSize) {
			*outLine = reconstructSearchDebugLineFromTT(*state, ctx, currentPlayer, depth, bestMove)
		} else {
			*outLine = leafSearchDebugLine(*state)
		}
	}
	return best
}

func applyTTEntry(entry TTEntry, depth int, alpha *float64, beta *float64, stats *SearchStats) (used bool, ret bool, value float64) {
	if entry.Depth < depth {
		return false, false, 0.0
	}
	switch entry.Flag {
	case TTExact:
		return true, true, entry.ScoreFloat()
	case TTLower:
		value := entry.ScoreFloat()
		if value > *alpha {
			*alpha = value
		}
	case TTUpper:
		value := entry.ScoreFloat()
		if value < *beta {
			*beta = value
		}
	}
	if *alpha >= *beta {
		if stats != nil {
			stats.Cutoffs++
			stats.TTCutoffs++
		}
		return true, true, entry.ScoreFloat()
	}
	return true, false, entry.ScoreFloat()
}

func evaluateMoveWithCache(state *GameState, ctx minimaxContext, currentPlayer PlayerColor, move Move, depthLeft int, depthFromRoot int, boardHash uint64, outLine **SearchDebugLine, outCached *bool, outStatus *string, alpha, beta float64) float64 {
	if timedOut(ctx) {
		if outLine != nil {
			*outLine = leafSearchDebugLine(*state)
		}
		if outStatus != nil {
			*outStatus = "TIMED_OUT"
		}
		return evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
	}
	_ = boardHash

	score := illegalScore
	if ok, _ := ctx.rules.IsLegal(*state, move, currentPlayer); ok {
		sampleBoardTiming := false
		if stats := ctx.settings.Stats; stats != nil {
			nextOp := stats.BoardGenOps + 1
			sampleBoardTiming = (nextOp & searchTimingSampleMask) == 0
		}
		var boardGenStart time.Time
		if sampleBoardTiming {
			boardGenStart = time.Now()
		}
		var undo searchMoveUndo
		applied := applyMoveWithUndo(state, ctx.rules, move, currentPlayer, ctx.evalState, &undo)
		if stats := ctx.settings.Stats; stats != nil {
			stats.BoardGenOps++
			if sampleBoardTiming {
				stats.BoardGenTime += time.Since(boardGenStart)
			}
			if stats.BoardGenOps == 1 || (stats.BoardGenOps&searchProgressChunkMask) == 0 {
				reportSearchProgress(stats, ctx.settings)
			}
		}
		if applied {
			if ctx.footprint != nil {
				ctx.footprint.ObserveMove(move)
			}
			if ctx.settings.OnGhostUpdate != nil {
				ctx.settings.OnGhostUpdate(state.Clone())
			}
			logTacticalNode(ctx, *state, depthLeft, depthFromRoot+1, move, currentPlayer)
			step := buildSearchDebugStep(currentPlayer, move, undo)
			if depthLeft <= 1 || timedOut(ctx) {
				score = evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
				if outLine != nil {
					*outLine = prependSearchDebugLine(step, nil, state.Board)
				}
			} else {
				nextCtx := ctx
				nextCtx.logIndent = ctx.logIndent + 1
				var childLine *SearchDebugLine
				childOut := (**SearchDebugLine)(nil)
				if outLine != nil {
					childOut = &childLine
				}
				score = minimax(state, nextCtx, depthLeft-1, otherPlayer(currentPlayer), depthFromRoot+1, alpha, beta, childOut)
				if score == illegalScore {
					score = evaluateStateHeuristicWithEvaluator(*state, ctx.rules, ctx.settings, ctx.evalState)
					if outStatus != nil {
						*outStatus = "ROOT_CHILD_RETURNED_ILLEGAL_SENTINEL_FALLBACK"
					}
				}
				if outLine != nil {
					*outLine = prependSearchDebugLine(step, childLine, state.Board)
				}
			}
			undoMoveWithUndo(state, ctx.evalState, undo)
			if outStatus != nil {
				if *outStatus == "ROOT_CHILD_RETURNED_ILLEGAL_SENTINEL_FALLBACK" {
					// Keep the more precise fallback status set above.
				} else if score == illegalScore {
					*outStatus = "ROOT_CHILD_RETURNED_ILLEGAL_SENTINEL"
				} else {
					*outStatus = "OK"
				}
			}
		} else {
			if outStatus != nil {
				*outStatus = "ROOT_APPLY_FAILED"
			}
		}
	} else {
		if outStatus != nil {
			*outStatus = "ROOT_IS_LEGAL_FALSE"
		}
	}
	if outCached != nil {
		*outCached = false
	}
	return score
}

func updateRootMoveAfterSearch(entry *RootMove, score float64, depth int, band int, verification bool) {
	if entry == nil {
		return
	}
	if verification {
		entry.VerificationScore = score
		entry.VerificationDepth = depth
		entry.HasVerification = true
		entry.WasVerified = true
		return
	}
	entry.LastSearchScore = score
	entry.LastCompletedDepth = depth
	entry.LastSearchBand = band
	entry.HasLastSearch = true
	entry.WasSearched = true
	entry.SearchCount++
}

// ================================================================
// Root search
// Scores each root move using iterative deepening.
// ================================================================

// searchRootPoolAtDepth evaluates every move in rootPool at the given depth using
// a full alpha-beta search. It divides moves into forced, principal, and speculative
// bands, processes them in order, and returns a score array indexed by board cell.
// Aspiration windows narrow the search when a good estimate is already available.
func searchRootPoolAtDepth(state GameState, settings AIScoreSettings, ctx minimaxContext, depth int, alpha, beta float64, rootPool []RootMove, pvMove *Move, outUsedCache *bool, outBestLine **SearchDebugLine) ([]float64, bool) {
	if timedOut(ctx) {
		return nil, false
	}
	if stats := settings.Stats; stats != nil {
		stats.RootSearchCalls++
	}
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	if len(rootPool) == 0 {
		return scores, true
	}
	rootMaximizing := settings.Player == PlayerRed
	rootPrepStart := time.Now()
	ordered := sortRootMoveIndices(rootPool, rootMaximizing, pvMove)
	bands := chooseRootSearchBands(ctx, rootPool, ordered, depth)
	mainOrder := rootBandSearchOrder(bands)
	if stats := settings.Stats; stats != nil {
		stats.RootPrepTime += time.Since(rootPrepStart)
		stats.RootMoveEvaluations += int64(len(mainOrder))
	}
	if settings.Config.LogDepthScores {
		fmt.Printf("[ai:root_bands depth=%d] forced=%d %s principal=%d %s speculative=%d %s verification=%d %s\n",
			depth,
			len(bands.forced),
			formatRootBandMovesLimited(rootPool, bands.forced, 8),
			len(bands.principal),
			formatRootBandMovesLimited(rootPool, bands.principal, 12),
			len(bands.speculative),
			formatRootBandMovesLimited(rootPool, bands.speculative, 8),
			len(bands.verification),
			formatRootBandMovesLimited(rootPool, bands.verification, 8),
		)
		fmt.Printf("[ai:root_order depth=%d] total=%d %s\n", depth, len(mainOrder), formatRootBandMovesLimited(rootPool, mainOrder, 16))
	}
	if settings.Stats != nil {
		settings.Stats.RootCandidates += int64(len(mainOrder))
		settings.Stats.RootSamples++
	}
	cache := selectCache(ctx)
	boardHash := ttKeyFor(state, settings.BoardSize)
	usedCache := false
	var debugLines map[Move]*SearchDebugLine
	if outBestLine != nil {
		debugLines = make(map[Move]*SearchDebugLine, len(rootPool))
	}
	aspirationAlpha := alpha
	aspirationBeta := beta
	rootAlpha := alpha
	rootBeta := beta
	searchMove := func(idx int, band int, searchDepth int, verification bool, useBounds bool, moveIndex int, totalMoves int) (float64, bool) {
		if timedOut(ctx) {
			return 0, false
		}
		move := rootPool[idx].Move
		bandName := "root"
		switch band {
		case rootBandForced:
			bandName = "root-forced"
		case rootBandPrincipal:
			bandName = "root-principal"
		case rootBandSpeculative:
			bandName = "root-speculative"
		case rootBandVerification:
			bandName = "root-verify"
		}
		if settings.Config.AiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, settings.Player, settings.BoardSize) {
			// Root immediate wins: one ply from root (depthFromRoot = 1).
			win := winDistanceScore(-winScore, 1)
			if settings.Player == PlayerRed {
				win = winDistanceScore(winScore, 1)
			}
			updateRootMoveAfterSearch(&rootPool[idx], win, searchDepth, band, verification)
			if !verification || searchDepth == depth {
				scores[move.Y*settings.BoardSize+move.X] = win
			}
			if debugLines != nil {
				next := state.Clone()
				var undo searchMoveUndo
				if applyMoveWithUndo(&next, ctx.rules, move, settings.Player, nil, &undo) {
					debugLines[move] = prependSearchDebugLine(buildSearchDebugStep(settings.Player, move, undo), nil, next.Board)
				}
			}
			return win, true
		}
		cached := false
		var line *SearchDebugLine
		lineOut := (**SearchDebugLine)(nil)
		if debugLines != nil {
			lineOut = &line
		}
		localAlpha := math.Inf(-1)
		localBeta := math.Inf(1)
		if useBounds {
			localAlpha = rootAlpha
			localBeta = rootBeta
		}
		logSearchMove(ctx, depth, 0, move, moveIndex, totalMoves, searchDepth, localAlpha, localBeta, bandName)
		var moveStatus string
		score := evaluateMoveWithCache(&state, ctx, settings.Player, move, searchDepth, searchDepth, boardHash, lineOut, &cached, &moveStatus, localAlpha, localBeta)
		if settings.Config.AiEnableAspiration && !verification && useBounds && (score <= aspirationAlpha || score >= aspirationBeta) {
			if timedOut(ctx) {
				return 0, false
			}
			// Asymmetric one-sided re-search: only widen in the direction of failure.
			// This is cheaper than a full-window re-search when the score is just outside
			// one side of the window (the other bound still prunes).
			if score <= aspirationAlpha {
				score = evaluateMoveWithCache(&state, ctx, settings.Player, move, searchDepth, searchDepth, boardHash, lineOut, &cached, &moveStatus, math.Inf(-1), aspirationBeta)
				// If the score jumped all the way past the other side, fall back to full window.
				if !timedOut(ctx) && score >= aspirationBeta {
					score = evaluateMoveWithCache(&state, ctx, settings.Player, move, searchDepth, searchDepth, boardHash, lineOut, &cached, &moveStatus, math.Inf(-1), math.Inf(1))
				}
			} else {
				score = evaluateMoveWithCache(&state, ctx, settings.Player, move, searchDepth, searchDepth, boardHash, lineOut, &cached, &moveStatus, aspirationAlpha, math.Inf(1))
				if !timedOut(ctx) && score <= aspirationAlpha {
					score = evaluateMoveWithCache(&state, ctx, settings.Player, move, searchDepth, searchDepth, boardHash, lineOut, &cached, &moveStatus, math.Inf(-1), math.Inf(1))
				}
			}
		}
		if cached {
			usedCache = true
		}
		updateRootMoveAfterSearch(&rootPool[idx], score, searchDepth, band, verification)
		rootPool[idx].LastSearchStatus = moveStatus
		if !verification || searchDepth == depth || scores[move.Y*settings.BoardSize+move.X] == illegalScore {
			scores[move.Y*settings.BoardSize+move.X] = score
		}
		if debugLines != nil && line != nil {
			debugLines[move] = cloneSearchDebugLine(line)
		}
		return score, true
	}

	rootOrder := rootBandSearchOrder(bands)
	rootOrderPos := make(map[int]int, len(rootOrder))
	for pos, idx := range rootOrder {
		rootOrderPos[idx] = pos
	}
	totalMainMoves := len(rootOrder)
	for _, idx := range bands.forced {
		score, ok := searchMove(idx, rootBandForced, depth, false, true, rootOrderPos[idx], totalMainMoves)
		if !ok {
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return nil, false
		}
		if rootMaximizing {
			if score > rootAlpha {
				rootAlpha = score
			}
		} else if score < rootBeta {
			rootBeta = score
		}
	}
	for _, idx := range bands.principal {
		score, ok := searchMove(idx, rootBandPrincipal, depth, false, true, rootOrderPos[idx], totalMainMoves)
		if !ok {
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return nil, false
		}
		if rootMaximizing {
			if score > rootAlpha {
				rootAlpha = score
			}
		} else if score < rootBeta {
			rootBeta = score
		}
	}
	for _, idx := range bands.speculative {
		score, ok := searchMove(idx, rootBandSpeculative, depth, false, true, rootOrderPos[idx], totalMainMoves)
		if !ok {
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return nil, false
		}
		if rootMaximizing {
			if score > rootAlpha {
				rootAlpha = score
			}
		} else if score < rootBeta {
			rootBeta = score
		}
	}

	if outUsedCache != nil {
		*outUsedCache = usedCache
	}
	if settings.Config.LogDepthScores {
		fmt.Printf("[ai:root_scores depth=%d] total=%d %s\n", depth, len(mainOrder), formatScoredMovesLimited(scores, rootMovesFromIndices(rootPool, mainOrder), settings.BoardSize, 16))
	}
	updateRootBadMoveStreaks(rootPool, ordered, scores, settings.BoardSize, rootMaximizing, depth, settings.Config)
	if stats := settings.Stats; stats != nil && len(mainOrder) > 1 {
		if bestMove, _, foundBest := bestRootMoveFromScores(rootPool, scores, settings.BoardSize, rootMaximizing); foundBest {
			firstMove := rootPool[mainOrder[0]].Move
			stats.RootFirstMoveSamples++
			bucket := orderingDepthBucket(depth)
			stats.RootFirstMoveDepthSamples[bucket]++
			bestRank := -1
			for rank, idx := range mainOrder {
				if rootPool[idx].Move == bestMove {
					bestRank = rank
					break
				}
			}
			recordOrderingRank(&stats.RootBestRankHistogram, bestRank)
			recordOrderingRank(&stats.RootBestRankByDepth[bucket], bestRank)
			if firstMove == bestMove {
				stats.RootFirstMoveWins++
				stats.RootFirstMoveDepthWins[bucket]++
			}
			stats.RootTop2Samples++
			stats.RootTop3Samples++
			if bestRank >= 0 && bestRank < 2 {
				stats.RootTop2Wins++
			}
			if bestRank >= 0 && bestRank < 3 {
				stats.RootTop3Wins++
			}
		}
	}
	if outBestLine != nil {
		if bestMove, _, foundBest := bestRootMoveFromScores(rootPool, scores, settings.BoardSize, rootMaximizing); foundBest {
			*outBestLine = cloneSearchDebugLine(debugLines[bestMove])
		}
	}
	return scores, true
}

// ================================================================
// Search statistics merging
// Combines parallel workers' stats.
// ================================================================

func mergeSearchStats(dst, src *SearchStats) {
	if dst == nil || src == nil {
		return
	}
	dst.Nodes += src.Nodes
	dst.RootSearchCalls += src.RootSearchCalls
	dst.CollectCandidateCalls += src.CollectCandidateCalls
	dst.QuietNodes += src.QuietNodes
	dst.SoftTacticalNodes += src.SoftTacticalNodes
	dst.HardTacticalNodes += src.HardTacticalNodes
	dst.TTProbes += src.TTProbes
	dst.TTHits += src.TTHits
	dst.TTExactHits += src.TTExactHits
	dst.TTLowerHits += src.TTLowerHits
	dst.TTUpperHits += src.TTUpperHits
	dst.TTStores += src.TTStores
	dst.TTOverwrites += src.TTOverwrites
	dst.TTReplacements += src.TTReplacements
	dst.Cutoffs += src.Cutoffs
	dst.TTCutoffs += src.TTCutoffs
	dst.ABCutoffs += src.ABCutoffs
	dst.CandidateCount += src.CandidateCount
	dst.QuietCandidates += src.QuietCandidates
	dst.SoftTacticalCandidates += src.SoftTacticalCandidates
	dst.HardTacticalCandidates += src.HardTacticalCandidates
	dst.RootCandidates += src.RootCandidates
	dst.DeepCandidates += src.DeepCandidates
	dst.RootSamples += src.RootSamples
	dst.DeepSamples += src.DeepSamples
	dst.EvalCacheProbes += src.EvalCacheProbes
	dst.EvalCacheHits += src.EvalCacheHits
	dst.HeuristicCalls += src.HeuristicCalls
	dst.HeuristicTime += src.HeuristicTime
	dst.BoardGenOps += src.BoardGenOps
	dst.BoardGenTime += src.BoardGenTime
	dst.CollectBBoxTime += src.CollectBBoxTime
	dst.CollectThreatMergeTime += src.CollectThreatMergeTime
	dst.CollectQuietOnlyTime += src.CollectQuietOnlyTime
	dst.QuietFrontTime += src.QuietFrontTime
	dst.LastMoveScanTime += src.LastMoveScanTime
	dst.LastMoveLegalTime += src.LastMoveLegalTime
	dst.ProximityScanTime += src.ProximityScanTime
	dst.ProximityLegalTime += src.ProximityLegalTime
	dst.QuietKeepCheckTime += src.QuietKeepCheckTime
	dst.QuietKeepNeighborhoodTime += src.QuietKeepNeighborhoodTime
	dst.QuietKeepLineTime += src.QuietKeepLineTime
	dst.QuietSortTime += src.QuietSortTime
	dst.QuietLegalCheckTime += src.QuietLegalCheckTime
	dst.RootPrepTime += src.RootPrepTime
	dst.AnalyzeThreatsTime += src.AnalyzeThreatsTime
	dst.AnalyzeThreatCalls += src.AnalyzeThreatCalls
	dst.AnalyzeThreatEvalTime += src.AnalyzeThreatEvalTime
	dst.AnalyzeThreatCaptureTime += src.AnalyzeThreatCaptureTime
	dst.AnalyzeThreatDetailTime += src.AnalyzeThreatDetailTime
	dst.AnalyzeThreatUrgencyTime += src.AnalyzeThreatUrgencyTime
	dst.AnalyzeThreatWinTime += src.AnalyzeThreatWinTime
	dst.AnalyzeThreatResponseTime += src.AnalyzeThreatResponseTime
	dst.AnalyzeThreatFilterTime += src.AnalyzeThreatFilterTime
	dst.AnalyzeThreatStrongCalls += src.AnalyzeThreatStrongCalls
	dst.AnalyzeThreatEvalStateHits += src.AnalyzeThreatEvalStateHits
	dst.ChooseCandidatesTime += src.ChooseCandidatesTime
	dst.BuildHardRestrictedTime += src.BuildHardRestrictedTime
	dst.HardBuildGenerateTime += src.HardBuildGenerateTime
	dst.HardBuildCollectTime += src.HardBuildCollectTime
	dst.HardBuildMergeOrderTime += src.HardBuildMergeOrderTime
	dst.HardBuildRestrictedTime += src.HardBuildRestrictedTime
	dst.GenerateThreatsTime += src.GenerateThreatsTime
	dst.CollectCandidatesTime += src.CollectCandidatesTime
	dst.OrderCandidatesTime += src.OrderCandidatesTime
	dst.QuietCandidateTime += src.QuietCandidateTime
	dst.SoftCandidateTime += src.SoftCandidateTime
	dst.HardCandidateTime += src.HardCandidateTime
	dst.RootMoveEvaluations += src.RootMoveEvaluations
	dst.QuietMoveEvaluations += src.QuietMoveEvaluations
	dst.SoftMoveEvaluations += src.SoftMoveEvaluations
	dst.HardMoveEvaluations += src.HardMoveEvaluations
	dst.CollectThreatCandidates += src.CollectThreatCandidates
	dst.CollectMergedCandidates += src.CollectMergedCandidates
	dst.QuietFrontCandidates += src.QuietFrontCandidates
	dst.CollectEmptyBoardReturns += src.CollectEmptyBoardReturns
	dst.CollectSingleStoneReturns += src.CollectSingleStoneReturns
	dst.LastMoveWindowChecks += src.LastMoveWindowChecks
	dst.LastMoveEmptyChecks += src.LastMoveEmptyChecks
	dst.LastMovePrioritySkips += src.LastMovePrioritySkips
	dst.LastMoveKeepChecks += src.LastMoveKeepChecks
	dst.LastMoveKeepCacheHits += src.LastMoveKeepCacheHits
	dst.LastMoveKeepCacheMisses += src.LastMoveKeepCacheMisses
	dst.LastMoveKeepAccepted += src.LastMoveKeepAccepted
	dst.LastMoveLegalChecks += src.LastMoveLegalChecks
	dst.LastMoveLegalRejected += src.LastMoveLegalRejected
	dst.LastMoveCandidatesAdded += src.LastMoveCandidatesAdded
	dst.ProximityWindowChecks += src.ProximityWindowChecks
	dst.ProximityEmptyChecks += src.ProximityEmptyChecks
	dst.ProximityCoveredSkips += src.ProximityCoveredSkips
	dst.ProximityDuplicateSkips += src.ProximityDuplicateSkips
	dst.ProximityPrioritySkips += src.ProximityPrioritySkips
	dst.ProximityKeepChecks += src.ProximityKeepChecks
	dst.ProximityKeepCacheHits += src.ProximityKeepCacheHits
	dst.ProximityKeepCacheMisses += src.ProximityKeepCacheMisses
	dst.ProximityKeepAccepted += src.ProximityKeepAccepted
	dst.ProximityLegalChecks += src.ProximityLegalChecks
	dst.ProximityLegalRejected += src.ProximityLegalRejected
	dst.ProximityCandidatesAdded += src.ProximityCandidatesAdded
	dst.QuietLegalChecks += src.QuietLegalChecks
	dst.QuietLegalRejected += src.QuietLegalRejected
	dst.QuietAddedCandidates += src.QuietAddedCandidates
	dst.QuietPriorityReplacements += src.QuietPriorityReplacements
	dst.QuietPrioritySkipped += src.QuietPrioritySkipped
	dst.RootFirstMoveSamples += src.RootFirstMoveSamples
	dst.RootFirstMoveWins += src.RootFirstMoveWins
	dst.RootTop2Samples += src.RootTop2Samples
	dst.RootTop2Wins += src.RootTop2Wins
	dst.RootTop3Samples += src.RootTop3Samples
	dst.RootTop3Wins += src.RootTop3Wins
	dst.NodeFirstLeadSamples += src.NodeFirstLeadSamples
	dst.NodeFirstLeadWins += src.NodeFirstLeadWins
	dst.NodeFirstExactSamples += src.NodeFirstExactSamples
	dst.NodeFirstExactWins += src.NodeFirstExactWins
	dst.NodeFirstCutoffSamples += src.NodeFirstCutoffSamples
	dst.NodeFirstCutoffWins += src.NodeFirstCutoffWins
	dst.PVSProxySamples += src.PVSProxySamples
	dst.PVSProxyWouldResearch += src.PVSProxyWouldResearch
	dst.PVSProxyQuietSamples += src.PVSProxyQuietSamples
	dst.PVSProxyQuietWouldResearch += src.PVSProxyQuietWouldResearch
	dst.PVSProxySoftSamples += src.PVSProxySoftSamples
	dst.PVSProxySoftWouldResearch += src.PVSProxySoftWouldResearch
	dst.PVSProxyHardSamples += src.PVSProxyHardSamples
	dst.PVSProxyHardWouldResearch += src.PVSProxyHardWouldResearch
	dst.NMPAttempts += src.NMPAttempts
	dst.NMPCutoffs += src.NMPCutoffs
	dst.RFPAttempts += src.RFPAttempts
	dst.RFPCutoffs += src.RFPCutoffs
	dst.LMRReduced += src.LMRReduced
	dst.LMRResearches += src.LMRResearches
	dst.TacticalQuiescenceCalls += src.TacticalQuiescenceCalls
	for i := 0; i < orderingStatsDepthBuckets; i++ {
		dst.RootFirstMoveDepthSamples[i] += src.RootFirstMoveDepthSamples[i]
		dst.RootFirstMoveDepthWins[i] += src.RootFirstMoveDepthWins[i]
		dst.PVSProxyDepthSamples[i] += src.PVSProxyDepthSamples[i]
		dst.PVSProxyDepthWouldResearch[i] += src.PVSProxyDepthWouldResearch[i]
		for j := 0; j < orderingRankBuckets; j++ {
			dst.RootBestRankByDepth[i][j] += src.RootBestRankByDepth[i][j]
			dst.NodeBestRankByDepth[i][j] += src.NodeBestRankByDepth[i][j]
		}
	}
	for i := 0; i < orderingRankBuckets; i++ {
		dst.RootBestRankHistogram[i] += src.RootBestRankHistogram[i]
		dst.NodeBestRankHistogram[i] += src.NodeBestRankHistogram[i]
	}
}

// ================================================================
// Root transposition
// Position-invariant result caching across board shifts.
// ================================================================

func rootShapeKey(state GameState, boardSize int) (uint64, boardBBox, bool) {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	bbox := computeBBox(state.Board, boardSize)
	if bbox.stones == 0 || bbox.width <= 0 || bbox.height <= 0 {
		return 0, bbox, false
	}
	key := mixKey(uint64(bbox.width)<<32 | uint64(bbox.height))
	meta := uint64(state.ToMove&0xff)<<56 | uint64(state.Status&0xff)<<48
	meta ^= uint64(state.CapturedBlue&0xffff) << 24
	meta ^= uint64(state.CapturedRed & 0xffff)
	key ^= mixKey(meta)
	if state.MustCapture {
		key ^= mixKey(0xc31f5d9f2c5a4b17)
	}
	for _, forced := range state.ForcedCaptureMoves {
		relX := forced.X - bbox.minX
		relY := forced.Y - bbox.minY
		if relX < 0 || relY < 0 || relX >= bbox.width || relY >= bbox.height {
			continue
		}
		rel := relY*bbox.width + relX
		key ^= mixKey(uint64(rel)<<2 | 3)
	}
	for y := bbox.minY; y <= bbox.maxY; y++ {
		for x := bbox.minX; x <= bbox.maxX; x++ {
			cell := state.Board.At(x, y)
			if cell == CellEmpty {
				continue
			}
			rel := (y-bbox.minY)*bbox.width + (x - bbox.minX)
			token := uint64(rel) << 2
			if cell == CellBlue {
				token |= 1
			} else if cell == CellRed {
				token |= 2
			}
			key ^= mixKey(token)
		}
	}
	return key, bbox, true
}

func storeRootTransposeExact(state GameState, settings AIScoreSettings, cache *AISearchCache, depth int, score float64, bestMove Move, meta TTMeta) {
	if cache == nil || !settings.Config.AiEnableRootTranspose || !bestMove.IsValid(settings.BoardSize) {
		return
	}
	rootTranspose := ensureRootTransposeCache(cache, settings.Config)
	if rootTranspose == nil {
		return
	}
	key, bbox, ok := rootShapeKey(state, settings.BoardSize)
	if !ok {
		return
	}
	if meta.FrameW <= 0 || meta.FrameH <= 0 {
		return
	}
	originX := bbox.minX - meta.GrowLeft
	originY := bbox.minY - meta.GrowTop
	bestRel := Move{X: bestMove.X - originX, Y: bestMove.Y - originY}
	if bestRel.X < 0 || bestRel.Y < 0 || bestRel.X >= meta.FrameW || bestRel.Y >= meta.FrameH {
		return
	}
	rootTranspose.Put(key, depth, score, TTExact, bestRel, meta)
}

func isForcedTerminalScore(score float64) bool {
	return math.Abs(score) >= winScore/2
}

func scoreBoardFromRootTranspose(state GameState, rules Rules, settings AIScoreSettings, cache *AISearchCache) ([]float64, bool) {
	if cache == nil || !settings.Config.AiEnableRootTranspose {
		return nil, false
	}
	rootTranspose := ensureRootTransposeCache(cache, settings.Config)
	if rootTranspose == nil {
		return nil, false
	}
	key, bbox, ok := rootShapeKey(state, settings.BoardSize)
	if !ok {
		return nil, false
	}
	entry, ok := rootTranspose.Get(key, settings.Depth)
	if !ok {
		return nil, false
	}
	growLeft := int(entry.GrowLeft)
	growRight := int(entry.GrowRight)
	growTop := int(entry.GrowTop)
	growBottom := int(entry.GrowBottom)
	if bbox.minX < growLeft || bbox.minY < growTop {
		return nil, false
	}
	if settings.BoardSize-1-bbox.maxX < growRight || settings.BoardSize-1-bbox.maxY < growBottom {
		return nil, false
	}
	frameW := bbox.width + growLeft + growRight
	frameH := bbox.height + growTop + growBottom
	originX := bbox.minX - growLeft
	originY := bbox.minY - growTop
	if entry.HitLeft && originX != 0 {
		return nil, false
	}
	if entry.HitTop && originY != 0 {
		return nil, false
	}
	if entry.HitRight && originX+frameW != settings.BoardSize {
		return nil, false
	}
	if entry.HitBottom && originY+frameH != settings.BoardSize {
		return nil, false
	}
	if frameW != int(entry.FrameW) || frameH != int(entry.FrameH) {
		return nil, false
	}
	move := Move{X: originX + entry.BestRel.X, Y: originY + entry.BestRel.Y}
	if !move.IsValid(settings.BoardSize) {
		return nil, false
	}
	if legal, _ := rules.IsLegal(state, move, settings.Player); !legal {
		return nil, false
	}
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	scores[move.Y*settings.BoardSize+move.X] = entry.ScoreFloat()
	if settings.Stats != nil {
		settings.Stats.TTProbes++
		settings.Stats.TTHits++
		settings.Stats.TTExactHits++
		settings.Stats.CompletedDepths = entry.Depth
		if entry.ProvenExact {
			settings.Stats.DecisionSource = "ROOT_TRANSPOSE_PROVEN_SHORTCUT"
		} else {
			settings.Stats.DecisionSource = "ROOT_TRANSPOSE_SHORTCUT"
		}
	}
	return scores, true
}

func scoreBoardFromRootTT(state GameState, rules Rules, settings AIScoreSettings, cache *AISearchCache, tt *TranspositionTable, rootHash uint64) ([]float64, bool) {
	heuristicHash := heuristicHashFromConfig(settings.Config)
	if tt != nil {
		entry, ok := tt.Probe(rootHash, heuristicHash)
		if ok && entry.Flag == TTExact && (entry.ProvenExact || entry.Depth >= settings.Depth) && entry.BestMove.IsValid(settings.BoardSize) {
			if legal, _ := rules.IsLegal(state, entry.BestMove, settings.Player); legal {
				scores := make([]float64, settings.BoardSize*settings.BoardSize)
				for i := range scores {
					scores[i] = illegalScore
				}
				scores[entry.BestMove.Y*settings.BoardSize+entry.BestMove.X] = entry.ScoreFloat()
				if settings.Stats != nil {
					settings.Stats.TTProbes++
					settings.Stats.TTHits++
					settings.Stats.TTExactHits++
					settings.Stats.CompletedDepths = entry.Depth
					if entry.ProvenExact {
						settings.Stats.DecisionSource = "ROOT_TT_PROVEN_SHORTCUT"
					} else {
						settings.Stats.DecisionSource = "ROOT_TT_SHORTCUT"
					}
				}
				return scores, true
			}
		}
	}
	if scores, ok := scoreBoardFromRootTranspose(state, rules, settings, cache); ok {
		return scores, true
	}
	if settings.Stats != nil {
		settings.Stats.TTProbes++
	}
	return nil, false
}

// ================================================================
// Search progress reporting
// Incremental callbacks during long searches.
// ================================================================

const progressChunk = int64(64)

func reportSearchProgress(stats *SearchStats, settings AIScoreSettings) {
	if stats == nil {
		return
	}
	if settings.OnNodeProgress != nil {
		nodeDelta := stats.Nodes - stats.progressReportedNodes
		if nodeDelta > 0 && stats.progressReportedNodes == 0 {
			settings.OnNodeProgress(1)
			stats.progressReportedNodes = 1
			nodeDelta = stats.Nodes - stats.progressReportedNodes
		}
		if nodeDelta >= progressChunk {
			emit := nodeDelta - (nodeDelta % progressChunk)
			settings.OnNodeProgress(emit)
			stats.progressReportedNodes += emit
		}
		if stats.Nodes == 0 {
			boardDelta := stats.BoardGenOps - stats.progressReportedBoardGen
			if boardDelta > 0 && stats.progressReportedBoardGen == 0 {
				settings.OnNodeProgress(1)
				stats.progressReportedBoardGen = 1
				boardDelta = stats.BoardGenOps - stats.progressReportedBoardGen
			}
			if boardDelta >= progressChunk {
				emit := boardDelta - (boardDelta % progressChunk)
				settings.OnNodeProgress(emit)
				stats.progressReportedBoardGen += emit
			}
		}
	}
	reportSearchMetrics(stats, settings)
}

func flushSearchProgress(stats *SearchStats, settings AIScoreSettings) {
	if stats == nil {
		return
	}
	if settings.OnNodeProgress != nil {
		nodeDelta := stats.Nodes - stats.progressReportedNodes
		if nodeDelta > 0 {
			settings.OnNodeProgress(nodeDelta)
			stats.progressReportedNodes += nodeDelta
		}
		if stats.Nodes == 0 {
			boardDelta := stats.BoardGenOps - stats.progressReportedBoardGen
			if boardDelta > 0 {
				settings.OnNodeProgress(boardDelta)
				stats.progressReportedBoardGen += boardDelta
			}
		}
	}
	reportSearchMetrics(stats, settings)
}

func reportSearchMetrics(stats *SearchStats, settings AIScoreSettings) {
	if stats == nil || settings.OnSearchProgress == nil {
		return
	}
	delta := SearchProgressDelta{
		Nodes:          stats.Nodes - stats.progressMetricNodes,
		CandidateCount: stats.CandidateCount - stats.progressMetricCandidates,
		TTProbes:       stats.TTProbes - stats.progressMetricTTProbes,
		TTHits:         stats.TTHits - stats.progressMetricTTHits,
		TTCutoffs:      stats.TTCutoffs - stats.progressMetricTTCutoffs,
		ABCutoffs:      stats.ABCutoffs - stats.progressMetricABCutoffs,
	}
	if delta.Nodes == 0 && delta.CandidateCount == 0 && delta.TTProbes == 0 && delta.TTHits == 0 && delta.TTCutoffs == 0 && delta.ABCutoffs == 0 {
		return
	}
	settings.OnSearchProgress(delta)
	stats.progressMetricNodes += delta.Nodes
	stats.progressMetricCandidates += delta.CandidateCount
	stats.progressMetricTTProbes += delta.TTProbes
	stats.progressMetricTTHits += delta.TTHits
	stats.progressMetricTTCutoffs += delta.TTCutoffs
	stats.progressMetricABCutoffs += delta.ABCutoffs
}

func recordRootOrderingStats(stats *SearchStats, rootPool []RootMove, mainOrder []int, scores []float64, boardSize int, rootMaximizing bool, depth int) {
	if stats == nil || len(mainOrder) <= 1 {
		return
	}
	bestMove, _, foundBest := bestRootMoveFromScores(rootPool, scores, boardSize, rootMaximizing)
	if !foundBest {
		return
	}
	firstMove := rootPool[mainOrder[0]].Move
	stats.RootFirstMoveSamples++
	bucket := orderingDepthBucket(depth)
	stats.RootFirstMoveDepthSamples[bucket]++
	bestRank := -1
	for rank, idx := range mainOrder {
		if rootPool[idx].Move == bestMove {
			bestRank = rank
			break
		}
	}
	recordOrderingRank(&stats.RootBestRankHistogram, bestRank)
	recordOrderingRank(&stats.RootBestRankByDepth[bucket], bestRank)
	if firstMove == bestMove {
		stats.RootFirstMoveWins++
		stats.RootFirstMoveDepthWins[bucket]++
	}
	stats.RootTop2Samples++
	stats.RootTop3Samples++
	if bestRank >= 0 && bestRank < 2 {
		stats.RootTop2Wins++
	}
	if bestRank >= 0 && bestRank < 3 {
		stats.RootTop3Wins++
	}
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func rootMainOrderFullyScored(scores []float64, rootPool []RootMove, mainOrder []int, boardSize int) bool {
	for _, idx := range mainOrder {
		if idx < 0 || idx >= len(rootPool) {
			return false
		}
		move := rootPool[idx].Move
		if !move.IsValid(boardSize) {
			return false
		}
		scoreIdx := move.Y*boardSize + move.X
		if scoreIdx < 0 || scoreIdx >= len(scores) {
			return false
		}
		if scores[scoreIdx] == illegalScore {
			return false
		}
	}
	return true
}

// ================================================================
// Parallel search
// Lazy SMP worker coordination.
// ================================================================

type sharedRootBounds struct {
	mu    sync.Mutex
	alpha float64
	beta  float64
	max   bool
}

func newSharedRootBounds(alpha, beta float64, maximizing bool) *sharedRootBounds {
	return &sharedRootBounds{
		alpha: alpha,
		beta:  beta,
		max:   maximizing,
	}
}

func (b *sharedRootBounds) snapshot() (float64, float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.alpha, b.beta
}

func (b *sharedRootBounds) update(score float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max {
		if score > b.alpha {
			b.alpha = score
		}
	} else if score < b.beta {
		b.beta = score
	}
}

func searchRootPoolAtDepthParallel(state GameState, rules Rules, settings AIScoreSettings, ctx minimaxContext, depth int, alpha, beta float64, rootPool []RootMove, pvMove *Move, workers int, outUsedCache *bool, outBestLine **SearchDebugLine) ([]float64, bool) {
	if workers <= 1 {
		return searchRootPoolAtDepth(state, settings, ctx, depth, alpha, beta, rootPool, pvMove, outUsedCache, outBestLine)
	}
	if timedOut(ctx) {
		return nil, false
	}
	if stats := settings.Stats; stats != nil {
		stats.RootSearchCalls++
	}
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	if len(rootPool) == 0 {
		return scores, true
	}
	rootMaximizing := settings.Player == PlayerRed
	rootPrepStart := time.Now()
	ordered := sortRootMoveIndices(rootPool, rootMaximizing, pvMove)
	bands := chooseRootSearchBands(ctx, rootPool, ordered, depth)
	mainOrder := rootBandSearchOrder(bands)
	if stats := settings.Stats; stats != nil {
		stats.RootPrepTime += time.Since(rootPrepStart)
		stats.RootMoveEvaluations += int64(len(mainOrder))
		stats.RootCandidates += int64(len(mainOrder))
		stats.RootSamples++
	}
	if len(mainOrder) == 0 {
		return scores, true
	}
	if settings.Config.LogDepthScores {
		fmt.Printf("[ai:root_bands depth=%d] forced=%d %s principal=%d %s speculative=%d %s verification=%d %s\n",
			depth,
			len(bands.forced),
			formatRootBandMovesLimited(rootPool, bands.forced, 8),
			len(bands.principal),
			formatRootBandMovesLimited(rootPool, bands.principal, 12),
			len(bands.speculative),
			formatRootBandMovesLimited(rootPool, bands.speculative, 8),
			len(bands.verification),
			formatRootBandMovesLimited(rootPool, bands.verification, 8),
		)
		fmt.Printf("[ai:root_order depth=%d] total=%d %s\n", depth, len(mainOrder), formatRootBandMovesLimited(rootPool, mainOrder, 16))
	}
	if workers > len(mainOrder) {
		workers = len(mainOrder)
	}
	boardHash := ttKeyFor(state, settings.BoardSize)
	sharedBounds := newSharedRootBounds(alpha, beta, rootMaximizing)
	var done atomic.Bool
	origStop := settings.ShouldStop

	evaluateOne := func(localState *GameState, wctx minimaxContext, idx int, localAlpha, localBeta float64) (float64, bool, bool, string, *SearchDebugLine) {
		if timedOut(wctx) {
			return 0, false, false, "", nil
		}
		move := rootPool[idx].Move
		cached := false
		moveStatus := ""
		var line *SearchDebugLine
		lineOut := (**SearchDebugLine)(nil)
		if outBestLine != nil {
			lineOut = &line
		}
		score := evaluateMoveWithCache(localState, wctx, settings.Player, move, depth, depth, boardHash, lineOut, &cached, &moveStatus, localAlpha, localBeta)
		return score, true, cached, moveStatus, cloneSearchDebugLine(line)
	}

	evaluateAssigned := func(workerID int, startPos int, sharedStats *SearchStats) bool {
		wsettings := settings
		wsettings.Stats = sharedStats
		wsettings.OnDepthComplete = nil
		wsettings.OnDepthCompleteDebug = nil
		wsettings.OnNodeProgress = nil
		wsettings.OnSearchProgress = nil
		wsettings.OnGhostUpdate = nil
		wsettings.ShouldStop = func() bool {
			return done.Load() || (origStop != nil && origStop())
		}
		localState := state.Clone()
		wctx := newMinimaxContext(rules, wsettings, ctx.start)
		attachEvalState(&wctx, localState)
		for pos := startPos + workerID; pos < len(mainOrder); pos += workers {
			if timedOut(wctx) {
				return false
			}
			idx := mainOrder[pos]
			band := rootBandSpeculative
			switch {
			case containsInt(bands.forced, idx):
				band = rootBandForced
			case containsInt(bands.principal, idx):
				band = rootBandPrincipal
			case containsInt(bands.verification, idx):
				band = rootBandVerification
			}
			localAlpha, localBeta := sharedBounds.snapshot()
			score, ok, cached, moveStatus, _ := evaluateOne(&localState, wctx, idx, localAlpha, localBeta)
			if !ok {
				return false
			}
			move := rootPool[idx].Move
			updateRootMoveAfterSearch(&rootPool[idx], score, depth, band, false)
			rootPool[idx].LastSearchStatus = moveStatus
			scores[move.Y*settings.BoardSize+move.X] = score
			sharedBounds.update(score)
			if sharedStats != nil && cached {
				sharedStats.UsedCache = true
			}
		}
		return true
	}

	sequentialStats := settings.Stats
	localState := state.Clone()
	localCtx := newMinimaxContext(rules, settings, ctx.start)
	attachEvalState(&localCtx, localState)
	firstIdx := mainOrder[0]
	firstBand := rootBandSpeculative
	switch {
	case containsInt(bands.forced, firstIdx):
		firstBand = rootBandForced
	case containsInt(bands.principal, firstIdx):
		firstBand = rootBandPrincipal
	case containsInt(bands.verification, firstIdx):
		firstBand = rootBandVerification
	}
	firstScore, ok, cached, moveStatus, _ := evaluateOne(&localState, localCtx, firstIdx, alpha, beta)
	if !ok {
		if outUsedCache != nil {
			*outUsedCache = sequentialStats != nil && sequentialStats.UsedCache
		}
		return nil, false
	}
	firstMove := rootPool[firstIdx].Move
	updateRootMoveAfterSearch(&rootPool[firstIdx], firstScore, depth, firstBand, false)
	rootPool[firstIdx].LastSearchStatus = moveStatus
	scores[firstMove.Y*settings.BoardSize+firstMove.X] = firstScore
	sharedBounds.update(firstScore)
	if sequentialStats != nil && cached {
		sequentialStats.UsedCache = true
	}
	startPos := 1
	if len(mainOrder) == 1 {
		startPos = len(mainOrder)
	}

	var wg sync.WaitGroup
	helperStats := make([]*SearchStats, 0, maxInt(0, workers-1))
	var helperFailed atomic.Bool
	for workerID := 1; workerID < workers; workerID++ {
		wg.Add(1)
		ws := &SearchStats{}
		helperStats = append(helperStats, ws)
		go func(id int, helper *SearchStats) {
			defer wg.Done()
			if !evaluateAssigned(id, startPos, helper) {
				helperFailed.Store(true)
			}
		}(workerID, ws)
	}
	ok = evaluateAssigned(0, startPos, settings.Stats)
	wg.Wait()
	done.Store(true)
	if !ok || helperFailed.Load() {
		fallbackRootPool := buildRootMovePool(state, ctx, settings.Player)
		if len(fallbackRootPool) == 0 {
			fallbackRootPool = rootPool
		}
		fallbackUsedCache := false
		fallbackScores, fallbackCompleted := searchRootPoolAtDepth(state, settings, ctx, depth, alpha, beta, fallbackRootPool, pvMove, &fallbackUsedCache, outBestLine)
		if outUsedCache != nil {
			*outUsedCache = (settings.Stats != nil && settings.Stats.UsedCache) || fallbackUsedCache
		}
		return fallbackScores, fallbackCompleted
	}
	if !rootMainOrderFullyScored(scores, rootPool, mainOrder, settings.BoardSize) {
		fmt.Printf("[ai:root-split] depth=%d incomplete root scoring after worker join\n", depth)
		fallbackRootPool := buildRootMovePool(state, ctx, settings.Player)
		if len(fallbackRootPool) == 0 {
			fallbackRootPool = rootPool
		}
		fallbackUsedCache := false
		fallbackScores, fallbackCompleted := searchRootPoolAtDepth(state, settings, ctx, depth, alpha, beta, fallbackRootPool, pvMove, &fallbackUsedCache, outBestLine)
		if outUsedCache != nil {
			*outUsedCache = (settings.Stats != nil && settings.Stats.UsedCache) || fallbackUsedCache
		}
		return fallbackScores, fallbackCompleted
	}
	for _, helper := range helperStats {
		mergeSearchStats(settings.Stats, helper)
	}
	confirmUsedCache := false
	confirmedScores, confirmed := searchRootPoolAtDepth(state, settings, ctx, depth, alpha, beta, rootPool, pvMove, &confirmUsedCache, outBestLine)
	if !confirmed || confirmedScores == nil {
		if outUsedCache != nil {
			*outUsedCache = (settings.Stats != nil && settings.Stats.UsedCache) || confirmUsedCache
		}
		return nil, false
	}
	scores = confirmedScores
	if outUsedCache != nil {
		*outUsedCache = (settings.Stats != nil && settings.Stats.UsedCache) || confirmUsedCache
	}
	return scores, true
}

func scoreBoardDirectDepthRootSplitParallel(state GameState, rules Rules, settings AIScoreSettings, workers int, localStats *SearchStats) ([]float64, bool) {
	if settings.BoardSize <= 0 {
		settings.BoardSize = state.Board.Size()
	}
	if settings.BoardSize > state.Board.Size() {
		settings.BoardSize = state.Board.Size()
	}
	if settings.Depth < 1 {
		settings.Depth = 1
	}
	if settings.Config == (Config{}) {
		settings.Config = GetConfig()
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	ctx := newMinimaxContext(rules, settings, time.Now())
	attachEvalState(&ctx, state)
	if localStats != nil && localStats.Start.IsZero() {
		localStats.Start = ctx.start
	}
	if !hasStoneWithin(state.Board, settings.BoardSize) {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		if localStats != nil {
			localStats.CompletedDepths = settings.Depth
			localStats.ReturnedDepth = settings.Depth
			localStats.DecisionSource = "ROOT_SPLIT_PARALLEL"
		}
		return scores, true
	}
	rootPool := buildRootMovePool(state, ctx, settings.Player)
	if len(rootPool) == 0 {
		return nil, false
	}
	rootMaximizing := settings.Player == PlayerRed
	rootPrepStart := time.Now()
	ordered := sortRootMoveIndices(rootPool, rootMaximizing, nil)
	bands := chooseRootSearchBands(ctx, rootPool, ordered, settings.Depth)
	mainOrder := rootBandSearchOrder(bands)
	if localStats != nil {
		localStats.RootSearchCalls++
		localStats.RootPrepTime += time.Since(rootPrepStart)
		localStats.RootMoveEvaluations += int64(len(mainOrder))
		localStats.RootCandidates += int64(len(mainOrder))
		localStats.RootSamples++
	}
	if len(mainOrder) == 0 {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		if localStats != nil {
			localStats.CompletedDepths = settings.Depth
			localStats.ReturnedDepth = settings.Depth
			localStats.DecisionSource = "ROOT_SPLIT_PARALLEL"
		}
		return scores, true
	}
	if workers > len(mainOrder) {
		workers = len(mainOrder)
	}
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	boardHash := ttKeyFor(state, settings.BoardSize)
	sharedBounds := newSharedRootBounds(math.Inf(-1), math.Inf(1), rootMaximizing)
	var done atomic.Bool
	origStop := settings.ShouldStop

	evaluateOne := func(localState *GameState, wctx minimaxContext, idx int, localAlpha, localBeta float64) (float64, bool, string) {
		if timedOut(wctx) {
			return 0, false, ""
		}
		move := rootPool[idx].Move
		cached := false
		moveStatus := ""
		score := evaluateMoveWithCache(localState, wctx, settings.Player, move, settings.Depth, settings.Depth, boardHash, nil, &cached, &moveStatus, localAlpha, localBeta)
		return score, cached, moveStatus
	}

	evaluateAssigned := func(workerID int, startPos int, sharedStats *SearchStats) {
		wsettings := settings
		wsettings.Stats = sharedStats
		wsettings.OnDepthComplete = nil
		wsettings.OnDepthCompleteDebug = nil
		wsettings.OnSearchProgress = nil
		wsettings.OnGhostUpdate = nil
		if workerID != 0 {
			wsettings.OnNodeProgress = nil
		}
		wsettings.ShouldStop = func() bool {
			return done.Load() || (origStop != nil && origStop())
		}
		localState := state.Clone()
		wctx := newMinimaxContext(rules, wsettings, ctx.start)
		attachEvalState(&wctx, localState)
		for pos := startPos + workerID; pos < len(mainOrder); pos += workers {
			if timedOut(wctx) {
				return
			}
			idx := mainOrder[pos]
			move := rootPool[idx].Move
			localAlpha, localBeta := sharedBounds.snapshot()
			score, cached, moveStatus := evaluateOne(&localState, wctx, idx, localAlpha, localBeta)
			scores[move.Y*settings.BoardSize+move.X] = score
			rootPool[idx].LastSearchStatus = moveStatus
			sharedBounds.update(score)
			if sharedStats != nil && cached {
				sharedStats.UsedCache = true
			}
		}
	}

	firstIdx := mainOrder[0]
	localState := state.Clone()
	localCtx := newMinimaxContext(rules, settings, ctx.start)
	attachEvalState(&localCtx, localState)
	firstMove := rootPool[firstIdx].Move
	firstScore, cached, moveStatus := evaluateOne(&localState, localCtx, firstIdx, math.Inf(-1), math.Inf(1))
	scores[firstMove.Y*settings.BoardSize+firstMove.X] = firstScore
	rootPool[firstIdx].LastSearchStatus = moveStatus
	sharedBounds.update(firstScore)
	if localStats != nil && cached {
		localStats.UsedCache = true
	}
	startPos := 1
	if len(mainOrder) == 1 {
		startPos = len(mainOrder)
	}

	var wg sync.WaitGroup
	helperStats := make([]*SearchStats, 0, maxInt(0, workers-1))
	for workerID := 1; workerID < workers; workerID++ {
		wg.Add(1)
		ws := &SearchStats{}
		helperStats = append(helperStats, ws)
		go func(id int, helper *SearchStats) {
			defer wg.Done()
			evaluateAssigned(id, startPos, helper)
		}(workerID, ws)
	}
	evaluateAssigned(0, startPos, localStats)
	wg.Wait()
	done.Store(true)
	if !rootMainOrderFullyScored(scores, rootPool, mainOrder, settings.BoardSize) {
		fmt.Printf("[ai:root-split-direct] depth=%d incomplete root scoring after worker join\n", settings.Depth)
		confirmSettings := settings
		confirmSettings.Stats = localStats
		fallbackRootPool := buildRootMovePool(state, ctx, settings.Player)
		if len(fallbackRootPool) == 0 {
			fallbackRootPool = rootPool
		}
		fallbackScores, fallbackCompleted := searchRootPoolAtDepth(state, confirmSettings, ctx, settings.Depth, math.Inf(-1), math.Inf(1), fallbackRootPool, nil, nil, nil)
		if fallbackCompleted && localStats != nil {
			localStats.CompletedDepths = settings.Depth
			localStats.ReturnedDepth = settings.Depth
			localStats.DecisionSource = "ROOT_SPLIT_PARALLEL_FALLBACK"
		}
		flushSearchProgress(localStats, settings)
		return fallbackScores, fallbackCompleted
	}
	for _, helper := range helperStats {
		mergeSearchStats(localStats, helper)
	}
	confirmSettings := settings
	confirmSettings.Stats = localStats
	confirmedScores, confirmed := searchRootPoolAtDepth(state, confirmSettings, ctx, settings.Depth, math.Inf(-1), math.Inf(1), rootPool, nil, nil, nil)
	if !confirmed || confirmedScores == nil {
		return nil, false
	}
	scores = confirmedScores
	if localStats != nil {
		localStats.CompletedDepths = settings.Depth
		localStats.ReturnedDepth = settings.Depth
		localStats.DecisionSource = "ROOT_SPLIT_PARALLEL"
	}
	flushSearchProgress(localStats, settings)
	return scores, true
}

// ================================================================
// Public search entry points
// ================================================================

func ScoreBoardDirectDepthParallel(state GameState, rules Rules, settings AIScoreSettings, workers int) ([]float64, bool) {
	// Respect config override.
	if settings.Config != (Config{}) && settings.Config.AiLazySMPWorkers > workers {
		workers = settings.Config.AiLazySMPWorkers
	}

	localStats := settings.Stats
	if localStats == nil {
		localStats = &SearchStats{}
		settings.Stats = localStats
	}

	if workers <= 1 {
		scores := ScoreBoard(state, rules, settings)
		return scores, localStats.CompletedDepths >= settings.Depth
	}

	if settings.DirectDepthOnly {
		if scores, completed := scoreBoardDirectDepthRootSplitParallel(state, rules, settings, workers, localStats); completed && scores != nil {
			return scores, true
		}
		fallbackSettings := settings
		fallbackSettings.Cache = nil
		fallbackSettings.Stats = localStats
		scores := ScoreBoard(state, rules, fallbackSettings)
		return scores, localStats.CompletedDepths >= settings.Depth
	}

	// Lazy SMP: spawn workers-1 helper goroutines that independently search the same
	// position and depth, sharing the TT (stripe-locked, already thread-safe).
	// Helpers populate the TT with ordering information that the primary search uses.
	// Each helper has its own killers and history to avoid data races.
	var done atomic.Bool
	origStop := settings.ShouldStop

	var wg sync.WaitGroup
	for w := 1; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws := settings
			ws.Stats = &SearchStats{} // own stats, don't race with primary
			ws.OnDepthComplete = nil  // no callbacks from helpers
			ws.OnDepthCompleteDebug = nil
			ws.OnNodeProgress = nil
			ws.OnSearchProgress = nil
			ws.OnGhostUpdate = nil
			ws.ShouldStop = func() bool {
				return done.Load() || (origStop != nil && origStop())
			}
			ScoreBoard(state.Clone(), rules, ws)
		}()
	}

	scores := ScoreBoard(state, rules, settings)
	done.Store(true)
	wg.Wait()

	return scores, localStats.CompletedDepths >= settings.Depth
}

// ScoreBoard is the main entry point for the AI. It runs iterative deepening from
// minDepth to settings.Depth, returning a score array indexed by board cell. The
// caller picks the move with the best score for settings.Player. Time budget and
// ShouldStop govern early termination; the last fully-completed depth is returned.
func ScoreBoard(state GameState, rules Rules, settings AIScoreSettings) []float64 {
	if settings.BoardSize <= 0 {
		settings.BoardSize = state.Board.Size()
	}
	if settings.BoardSize > state.Board.Size() {
		settings.BoardSize = state.Board.Size()
	}
	if settings.Depth < 1 {
		settings.Depth = 1
	}
	if settings.Config == (Config{}) {
		settings.Config = GetConfig()
	}
	if state.Hash == 0 {
		state.recomputeHashes()
	}
	queueState := GameState{}
	queueStateReady := false
	if settings.Config.AiQueueEnabled && !settings.SkipQueueBacklog && !settings.DirectDepthOnly {
		// Keep an immutable pre-search snapshot so queue learning always targets
		// the exact board before this AI decision.
		queueState = state.Clone()
		queueStateReady = true
	}
	if settings.Config.AiMaxDepth > 0 {
		settings.Depth = settings.Config.AiMaxDepth
	}
	minDepth := 1
	if settings.Config.AiMinDepth > 0 {
		minDepth = settings.Config.AiMinDepth
	}
	ctx := newMinimaxContext(rules, settings, time.Now())
	attachEvalState(&ctx, state)
	if settings.Stats != nil && settings.Stats.Start.IsZero() {
		settings.Stats.Start = ctx.start
	}
	logAITask(ctx, 0, "ScoreBoard start depth=%d board=%d budget=%dms", settings.Depth, settings.BoardSize, settings.Config.AiTimeBudgetMs)
	if !hasStoneWithin(state.Board, settings.BoardSize) {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	initialCandidates := collectCandidateMovesWithEval(state, rules, settings.Player, settings.BoardSize, nil, settings.Stats)
	if len(initialCandidates) == 0 {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	ctx.footprint = newSearchFootprint(state, settings.BoardSize)
	logAITask(ctx, 1, "Candidate generation complete count=%d", len(initialCandidates))
	rootPool := buildRootMovePool(state, ctx, settings.Player)
	if len(rootPool) == 0 {
		return rootOrderedFallbackScores(state, settings, ctx, nil)
	}
	logAITask(ctx, 1, "Root pool built count=%d", len(rootPool))
	singleRoot := len(rootPool) == 1
	startTime := ctx.start
	lastDepthCompleted := 0
	cache := selectCache(ctx)
	tt := ensureTT(cache, settings.Config)
	if tt != nil {
		tt.NextGeneration()
	}
	if settings.Config.AiEnableEvalCache {
		if evalCache := ensureEvalCache(cache, settings.Config); evalCache != nil {
			evalCache.NextGeneration()
		}
	}
	if settings.Config.AiEnableRootTranspose {
		if rootTranspose := ensureRootTransposeCache(cache, settings.Config); rootTranspose != nil {
			rootTranspose.NextGeneration()
		}
	}
	rootHash := ttKeyFor(state, settings.BoardSize)
	ttHeuristicHash := heuristicHashFromConfig(settings.Config)
	var pvMove *Move
	if tt != nil {
		if entry, ok := tt.Probe(rootHash, ttHeuristicHash); ok && entry.BestMove.IsValid(settings.BoardSize) {
			pv := entry.BestMove
			pvMove = &pv
		}
	}
	if scores, ok := scoreBoardFromRootTT(state, rules, settings, cache, tt, rootHash); ok {
		logAITask(ctx, 1, "Root TT shortcut hit depth=%d", settings.Depth)
		if settings.Stats != nil {
			settings.Stats.ReturnedDepth = settings.Stats.CompletedDepths
		}
		return scores
	}
	var scores []float64
	var lastScores []float64
	var lastBestScore float64
	rootMaximizing := settings.Player == PlayerRed
	haveBest := false
	startDepth := minDepth
	if settings.DirectDepthOnly {
		startDepth = settings.Depth
		if startDepth < minDepth {
			startDepth = minDepth
		}
	}
	if startDepth < minDepth {
		startDepth = minDepth
	}
	if startDepth > settings.Depth {
		return rootOrderedFallbackScores(state, settings, ctx, rootPool)
	}
	depthStep := settings.Config.AiDepthStep
	if depthStep < 1 {
		depthStep = 1
	}
	rootWorkers := settings.Config.AiLazySMPWorkers
	if rootWorkers < 1 {
		rootWorkers = 1
	}
	for depth := startDepth; depth <= settings.Depth; {
		if timedOut(ctx) && depth > minDepth {
			break
		}
		logAITask(ctx, 1, "Depth %d start", depth)
		depthStart := time.Now()
		if settings.Config.AiQuickWinExit {
			for _, rootMove := range rootPool {
				move := rootMove.Move
				if isImmediateWinCached(cache, state, rules, move, settings.Player, settings.BoardSize) {
					logAITask(ctx, 2, "Immediate win cached move=%v depth=%d", move, depth)
					winScores := make([]float64, settings.BoardSize*settings.BoardSize)
					for i := range winScores {
						winScores[i] = illegalScore
					}
					win := -winScore
					if settings.Player == PlayerRed {
						win = winScore
					}
					winScores[move.Y*settings.BoardSize+move.X] = win
					if tt != nil {
						meta := buildTTMeta(state, settings.BoardSize, ctx.footprint)
						if settings.OnDepthCompleteDebug != nil {
							next := state.Clone()
							var undo searchMoveUndo
							if applyMoveWithUndo(&next, rules, move, settings.Player, nil, &undo) {
								meta.DebugBoard = &next.Board
								meta.DebugLine = &SearchDebugLine{
									Steps:      []SearchDebugStep{buildSearchDebugStep(settings.Player, move, undo)},
									FinalBoard: next.Board.Clone(),
								}
							}
						}
						replaced, overwrote := tt.Store(rootHash, ttHeuristicHash, depth, win, TTExact, move, meta)
						if settings.Stats != nil {
							settings.Stats.TTStores++
							if replaced || overwrote {
								settings.Stats.TTOverwrites++
								settings.Stats.TTReplacements++
							}
						}
						storeRootTransposeExact(state, settings, cache, depth, win, move, meta)
					}
					quickDepth := 1
					duration := time.Since(depthStart)
					logAITask(ctx, 1, "Depth %d completed in %dms cached=%v quick_win=true requested_depth=%d", quickDepth, duration.Milliseconds(), false, depth)
					if settings.Stats != nil {
						settings.Stats.DepthDurations = append(settings.Stats.DepthDurations, duration)
						settings.Stats.CompletedDepths = quickDepth
						settings.Stats.ReturnedDepth = quickDepth
						settings.Stats.DecisionSource = "QUICK_WIN_EXIT"
					}
					if settings.OnDepthComplete != nil {
						settings.OnDepthComplete(quickDepth, move, win)
					}
					if settings.OnDepthCompleteDebug != nil {
						next := state.Clone()
						var undo searchMoveUndo
						var line *SearchDebugLine
						if applyMoveWithUndo(&next, rules, move, settings.Player, nil, &undo) {
							line = prependSearchDebugLine(buildSearchDebugStep(settings.Player, move, undo), nil, next.Board)
						}
						settings.OnDepthCompleteDebug(quickDepth, move, win, line)
					}
					return winScores
				}
			}
		}
		alpha := math.Inf(-1)
		beta := math.Inf(1)
		aspMinDepth := settings.Config.AiAspMinDepth
		if aspMinDepth <= 0 {
			aspMinDepth = 1
		}
		if settings.Config.AiEnableAspiration && haveBest && depth >= aspMinDepth {
			window := settings.Config.AiAspWindow
			if window > 0 {
				if settings.Config.AiAspWindowMax > 0 && window > settings.Config.AiAspWindowMax {
					window = settings.Config.AiAspWindowMax
				}
				alpha = lastBestScore - window
				beta = lastBestScore + window
			}
		}
		usedCache := false
		var completed bool
		var bestLine *SearchDebugLine
		lineOut := (**SearchDebugLine)(nil)
		if settings.OnDepthCompleteDebug != nil {
			lineOut = &bestLine
		}
		if rootWorkers > 1 && !singleRoot {
			scores, completed = searchRootPoolAtDepthParallel(state, rules, settings, ctx, depth, alpha, beta, rootPool, pvMove, rootWorkers, &usedCache, lineOut)
		} else {
			scores, completed = searchRootPoolAtDepth(state, settings, ctx, depth, alpha, beta, rootPool, pvMove, &usedCache, lineOut)
		}
		if !completed {
			if settings.Config.AiReturnLastComplete && lastScores != nil {
				break
			}
			break
		}
		duration := time.Since(depthStart)
		logAITask(ctx, 1, "Depth %d completed in %dms cached=%v", depth, duration.Milliseconds(), usedCache)
		if settings.Stats != nil {
			settings.Stats.UsedCache = settings.Stats.UsedCache || usedCache
			settings.Stats.DepthDurations = append(settings.Stats.DepthDurations, time.Since(depthStart))
			settings.Stats.CompletedDepths = depth
			settings.Stats.DecisionSource = "FULL_SEARCH"
			settings.Stats.RootPoolSnapshot = cloneRootPool(rootPool)
		}
		if settings.Config.LogDepthScores {
			for _, rootMove := range rootPool {
				move := rootMove.Move
				score := scores[move.Y*settings.BoardSize+move.X]
				_ = score
			}
		}
		bestMove, bestScore, foundBest := bestRootMoveFromScores(rootPool, scores, settings.BoardSize, rootMaximizing)
		meta := buildTTMeta(state, settings.BoardSize, ctx.footprint)
		if singleRoot && foundBest && isForcedTerminalScore(bestScore) {
			meta.ProvenExact = true
		}
		if settings.OnDepthCompleteDebug != nil {
			meta.DebugBoard = debugBoardFromLine(bestLine)
			meta.DebugLine = cloneSearchDebugLine(bestLine)
		}
		if tt != nil && foundBest {
			replaced, overwrote := tt.Store(rootHash, ttHeuristicHash, depth, bestScore, TTExact, bestMove, meta)
			if settings.Stats != nil {
				settings.Stats.TTStores++
				if replaced || overwrote {
					settings.Stats.TTOverwrites++
					settings.Stats.TTReplacements++
				}
			}
		}
		if foundBest {
			storeRootTransposeExact(state, settings, cache, depth, bestScore, bestMove, meta)
			if settings.OnDepthComplete != nil {
				settings.OnDepthComplete(depth, bestMove, bestScore)
			}
			if settings.OnDepthCompleteDebug != nil {
				settings.OnDepthCompleteDebug(depth, bestMove, bestScore, cloneSearchDebugLine(bestLine))
			}
			pv := bestMove
			pvMove = &pv
		}
		lastDepthCompleted = depth
		lastScores = scores
		lastBestScore = bestScore
		haveBest = true
		if singleRoot && foundBest && isForcedTerminalScore(bestScore) {
			if settings.Stats != nil {
				settings.Stats.ReturnedDepth = depth
				settings.Stats.DecisionSource = "ROOT_UNIQUE_PROVEN_DEADEND"
			}
			break
		}
		// Advance depth by step, clamping to settings.Depth so we always search the
		// target depth exactly (avoids skipping it when step > 1).
		if depth >= settings.Depth {
			break
		}
		depth += depthStep
		if depth > settings.Depth {
			depth = settings.Depth
		}
	}
	totalDuration := time.Since(startTime)
	logAITask(ctx, 0, "ScoreBoard finished depth=%d total=%dms", lastDepthCompleted, totalDuration.Milliseconds())
	if !settings.DirectDepthOnly && lastDepthCompleted < settings.Depth {
		if timedOut(ctx) || (ctx.settings.ShouldStop != nil && ctx.settings.ShouldStop()) {
			if queueStateReady {
				enqueueSearchBacklogTask(queueState, rules)
			}
		}
	}
	if lastScores != nil {
		if settings.Stats != nil {
			settings.Stats.ReturnedDepth = lastDepthCompleted
			if settings.Stats.DecisionSource == "" {
				settings.Stats.DecisionSource = "FULL_SEARCH"
			}
		}
		return lastScores
	}
	if settings.Stats != nil {
		settings.Stats.ReturnedDepth = 0
	}
	return rootOrderedFallbackScores(state, settings, ctx, rootPool)
}

// ================================================================
// Fallback scoring and small utility functions
// ================================================================

func rootOrderedFallbackScores(state GameState, settings AIScoreSettings, ctx minimaxContext, rootPool []RootMove) []float64 {
	expectedScores := settings.BoardSize * settings.BoardSize
	if expectedScores <= 0 {
		return nil
	}
	result := make([]float64, expectedScores)
	for i := range result {
		result[i] = illegalScore
	}
	rootMaximizing := settings.Player == PlayerRed
	if len(rootPool) == 0 {
		rootPool = buildRootMovePool(state, ctx, settings.Player)
	}
	if len(rootPool) == 0 {
		return result
	}
	ordered := sortRootMoveIndices(rootPool, rootMaximizing, nil)
	if len(ordered) == 0 {
		return result
	}
	move := rootPool[ordered[0]].Move
	if move.IsValid(settings.BoardSize) {
		result[move.Y*settings.BoardSize+move.X] = 0.0
	}
	return result
}

func TranspositionSize(cache *AISearchCache) int {
	if cache == nil {
		return 0
	}
	cache.mu.Lock()
	tt := cache.TT
	cache.mu.Unlock()
	if tt == nil {
		return 0
	}
	return tt.Count()
}

func ttKeyFor(state GameState, boardSize int) uint64 {
	key := state.CanonHash
	key ^= mixKey(uint64(boardSize)<<32 | uint64(state.Status))
	return key
}

func mixKey(v uint64) uint64 {
	v += 0x9e3779b97f4a7c15
	v = (v ^ (v >> 30)) * 0xbf58476d1ce4e5b9
	v = (v ^ (v >> 27)) * 0x94d049bb133111eb
	return v ^ (v >> 31)
}

func minInt(values ...int) int {
	min := values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

func maxInt(values ...int) int {
	max := values[0]
	for _, v := range values[1:] {
		if v > max {
			max = v
		}
	}
	return max
}
