package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	illegalScore = -1e9
	winScore     = 2000000000.0
)

const (
	boardCacheLogPath  = "/cache_logs/cached_boards.log"
	minmaxCacheLogPath = "/cache_logs/cached_minmax.log"
)

type boardArea struct {
	minX int
	minY int
	maxX int
	maxY int
}

func (a boardArea) width() int {
	if a.maxX < a.minX {
		return 0
	}
	return a.maxX - a.minX + 1
}

func (a boardArea) height() int {
	if a.maxY < a.minY {
		return 0
	}
	return a.maxY - a.minY + 1
}

func computePlayableArea(board Board, boardSize, margin int) boardArea {
	bbox := computeBBox(board, boardSize)
	if bbox.stones == 0 {
		return boardArea{
			minX: 0,
			minY: 0,
			maxX: boardSize - 1,
			maxY: boardSize - 1,
		}
	}
	minX := bbox.minX - margin
	minY := bbox.minY - margin
	maxX := bbox.maxX + margin
	maxY := bbox.maxY + margin
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX >= boardSize {
		maxX = boardSize - 1
	}
	if maxY >= boardSize {
		maxY = boardSize - 1
	}
	return boardArea{
		minX: minX,
		minY: minY,
		maxX: maxX,
		maxY: maxY,
	}
}

var (
	boardCache        = newBoardScoreCache()
	boardLogMu        sync.Mutex
	minmaxLogMu       sync.Mutex
	depthCacheWarned  bool
	loggedBoardHashes = make(map[uint64]struct{})
	loggedBoardMu     sync.Mutex
)

func init() {
	loadLoggedBoardHashes()
}

type AISearchCache struct {
	TT                  *TranspositionTable
	TranslatedTT        *TranspositionTable
	MoveCache           map[MoveCacheKey]float64
	ImmediateWinMove    map[ImmediateWinKey]bool
	ImmediateWinState   map[ImmediateWinStateKey]bool
	Edges               map[StateKey][]StateKey
	Root                StateKey
	HasRoot             bool
	TTSize              int
	TTBuckets           int
	TranslatedTTSize    int
	TranslatedTTBuckets int
	EvalCache           *EvalCache
	EvalCacheSize       int
}

type StateKey struct {
	Hash          uint64
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	CurrentPlayer PlayerColor
}

type MoveCacheKey struct {
	Hash          uint64
	DepthLeft     int
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	CurrentPlayer PlayerColor
	X             int
	Y             int
}

type ImmediateWinKey struct {
	Hash          uint64
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	Player        PlayerColor
	X             int
	Y             int
}

type ImmediateWinStateKey struct {
	Hash          uint64
	BoardSize     int
	CapturedBlack int
	CapturedWhite int
	Status        GameStatus
	Player        PlayerColor
}

type AIScoreSettings struct {
	Depth            int
	TimeoutMs        int
	BoardSize        int
	Player           PlayerColor
	OnGhostUpdate    func(GameState)
	Cache            *AISearchCache
	Config           Config
	ShouldStop       func() bool
	Stats            *SearchStats
	DirectDepthOnly  bool
	SkipQueueBacklog bool
}

type minimaxContext struct {
	rules       Rules
	settings    AIScoreSettings
	start       time.Time
	killers     [][]Move
	history     []int
	deadline    time.Time
	hasDeadline bool
	logIndent   int
}

type cacheKey struct {
	Hash      uint64
	Depth     int
	BoardSize int
	Player    PlayerColor
}

type cacheSignature struct {
	Hash      uint64
	BoardSize int
	Player    PlayerColor
}

var depthCache = map[cacheKey][]float64{}
var depthCacheBest = map[cacheSignature]int{}
var defaultCache = newAISearchCache()

func highestCachedDepthScores(boardHash uint64, boardSize int, player PlayerColor, minDepth, maxDepth int) ([]float64, int) {
	if minDepth < 1 {
		minDepth = 1
	}
	sig := cacheSignature{Hash: boardHash, BoardSize: boardSize, Player: player}
	best := depthCacheBest[sig]
	if best == 0 || best < minDepth {
		return nil, 0
	}
	key := cacheKey{Hash: boardHash, Depth: best, BoardSize: boardSize, Player: player}
	if scores, ok := depthCache[key]; ok {
		return scores, best
	}
	return nil, 0
}

func formatBoardArea(board Board, area boardArea) string {
	if area.width() == 0 || area.height() == 0 {
		return ""
	}
	var sb strings.Builder
	for y := area.minY; y <= area.maxY; y++ {
		for x := area.minX; x <= area.maxX; x++ {
			switch board.At(x, y) {
			case CellBlack:
				sb.WriteByte('X')
			case CellWhite:
				sb.WriteByte('O')
			default:
				sb.WriteByte('.')
			}
		}
		if y < area.maxY {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func maxScore(scores []float64) float64 {
	if len(scores) == 0 {
		return math.Inf(-1)
	}
	best := math.Inf(-1)
	for _, v := range scores {
		if v > best {
			best = v
		}
	}
	return best
}

func playerColorName(p PlayerColor) string {
	switch p {
	case PlayerBlack:
		return "Black"
	case PlayerWhite:
		return "White"
	default:
		return "None"
	}
}

func boardAreaHash(board Board, area boardArea, toMove PlayerColor, nextCaptureWin bool, player PlayerColor, boardSize int) uint64 {
	hash := uint64(1469598103934665603)
	hash = mixKey(hash ^ uint64(area.width())<<8 ^ uint64(area.height()))
	hash = mixKey(hash ^ uint64(boardSize))
	hash = mixKey(hash ^ uint64(toMove))
	hash = mixKey(hash ^ uint64(player)<<1)
	if nextCaptureWin {
		hash = mixKey(hash ^ 0xf00df00df00d)
	}
	for dy := 0; dy < area.height(); dy++ {
		for dx := 0; dx < area.width(); dx++ {
			cell := board.At(area.minX+dx, area.minY+dy)
			hash ^= uint64(cell) + 1
			hash *= 1099511628211
		}
		hash ^= 0xff
		hash *= 1099511628211
	}
	return hash
}

func nextCaptureWouldWin(state GameState, rules Rules) bool {
	threshold := rules.CaptureWinStones() - 2
	if threshold < 0 {
		threshold = 0
	}
	var captured int
	if state.ToMove == PlayerBlack {
		captured = state.CapturedBlack
	} else {
		captured = state.CapturedWhite
	}
	return captured >= threshold
}

type SearchStats struct {
	Nodes              int64
	TTProbes           int64
	TTHits             int64
	TranslatedTTProbes int64
	TranslatedTTHits   int64
	TTStores           int64
	TTOverwrites       int64
	Cutoffs            int64
	CandidateCount     int64
	RootCandidates     int64
	DeepCandidates     int64
	RootSamples        int64
	DeepSamples        int64
	EvalCacheProbes    int64
	EvalCacheHits      int64
	Start              time.Time
	DepthDurations     []time.Duration
	CompletedDepths    int
}

type boardScoreCache struct {
	mu             sync.RWMutex
	entries        map[uint64]float64
	patternEntries map[patternKey]float64
	warned         bool
}

func newBoardScoreCache() *boardScoreCache {
	return &boardScoreCache{
		entries:        make(map[uint64]float64),
		patternEntries: make(map[patternKey]float64),
	}
}

type patternKey struct {
	signature     string
	capturedBlack int
	capturedWhite int
	toMove        PlayerColor
	nextCapture   bool
}

type boardPattern struct {
	width  int
	height int
	cells  []Cell
}

func (p boardPattern) rotate90() boardPattern {
	if len(p.cells) == 0 {
		return p
	}
	newWidth := p.height
	newHeight := p.width
	cells := make([]Cell, newWidth*newHeight)
	for y := 0; y < p.height; y++ {
		for x := 0; x < p.width; x++ {
			destX := p.height - 1 - y
			destY := x
			cells[destY*newWidth+destX] = p.cells[y*p.width+x]
		}
	}
	return boardPattern{width: newWidth, height: newHeight, cells: cells}
}

func (p boardPattern) flipHorizontal() boardPattern {
	if len(p.cells) == 0 {
		return p
	}
	cells := make([]Cell, len(p.cells))
	for y := 0; y < p.height; y++ {
		for x := 0; x < p.width; x++ {
			destX := p.width - 1 - x
			cells[y*p.width+destX] = p.cells[y*p.width+x]
		}
	}
	return boardPattern{width: p.width, height: p.height, cells: cells}
}

func (p boardPattern) rotate(rot int) boardPattern {
	result := p
	rot = ((rot % 4) + 4) % 4
	for i := 0; i < rot; i++ {
		result = result.rotate90()
	}
	return result
}

func (p boardPattern) signature() string {
	if len(p.cells) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%dx%d;", p.width, p.height))
	for _, cell := range p.cells {
		switch cell {
		case CellBlack:
			sb.WriteByte('B')
		case CellWhite:
			sb.WriteByte('W')
		default:
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

func canonicalPatternSignature(pattern boardPattern) string {
	best := ""
	for rot := 0; rot < 4; rot++ {
		transformed := pattern.rotate(rot)
		for flip := 0; flip < 2; flip++ {
			current := transformed
			if flip == 1 {
				current = current.flipHorizontal()
			}
			sig := current.signature()
			if sig == "" {
				continue
			}
			if best == "" || sig < best {
				best = sig
			}
		}
	}
	return best
}

func buildPatternKey(state GameState, area boardArea, nextCapture bool) patternKey {
	pattern := extractBoardPattern(state.Board, area)
	sig := canonicalPatternSignature(pattern)
	return patternKey{
		signature:     sig,
		capturedBlack: state.CapturedBlack,
		capturedWhite: state.CapturedWhite,
		toMove:        state.ToMove,
		nextCapture:   nextCapture,
	}
}

func extractBoardPattern(board Board, area boardArea) boardPattern {
	width := area.width()
	height := area.height()
	if width <= 0 || height <= 0 {
		return boardPattern{}
	}
	cells := make([]Cell, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cells[y*width+x] = board.At(area.minX+x, area.minY+y)
		}
	}
	return boardPattern{width: width, height: height, cells: cells}
}

func (c *boardScoreCache) get(hash uint64, pattern patternKey) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if value, ok := c.entries[hash]; ok {
		return value, true
	}
	if pattern.signature != "" {
		if value, ok := c.patternEntries[pattern]; ok {
			return value, true
		}
	}
	return 0, false
}

func (c *boardScoreCache) set(state GameState, area boardArea, hash uint64, score float64, scorePlayer PlayerColor, nextCaptureWin bool, pattern patternKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[hash]; ok {
		return
	}
	c.entries[hash] = score
	if pattern.signature != "" {
		if _, ok := c.patternEntries[pattern]; !ok {
			c.patternEntries[pattern] = score
		}
	}
	limit := GetConfig().AiBoardCacheLimit
	if limit > 0 && len(c.entries) > limit && !c.warned {
		fmt.Printf("[ai:cachewarn] board cache size %d exceeded limit %d\n", len(c.entries), limit)
		c.warned = true
	}
	logBoardEvaluation(state, area, hash, score, scorePlayer, nextCaptureWin)
}

func logBoardEvaluation(state GameState, area boardArea, hash uint64, score float64, scorePlayer PlayerColor, nextCaptureWin bool) {
	if !GetConfig().AiCacheLogToFile {
		return
	}
	if !markBoardAsLogged(hash) {
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("hash=0x%x score=%.2f scoreFor=%s toMove=%s nextCaptureWin=%t area=%dx%d\n",
		hash, score, playerColorName(scorePlayer), playerColorName(state.ToMove), nextCaptureWin, area.width(), area.height()))
	sb.WriteString(formatBoardArea(state.Board, area))
	sb.WriteString("\n---\n")
	appendCacheLog(boardCacheLogPath, sb.String(), &boardLogMu)
}

func markBoardAsLogged(hash uint64) bool {
	loggedBoardMu.Lock()
	defer loggedBoardMu.Unlock()
	if _, ok := loggedBoardHashes[hash]; ok {
		return false
	}
	loggedBoardHashes[hash] = struct{}{}
	return true
}

func loadLoggedBoardHashes() {
	f, err := os.Open(boardCacheLogPath)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if h, ok := parseHashFromLog(line); ok {
			loggedBoardMu.Lock()
			loggedBoardHashes[h] = struct{}{}
			loggedBoardMu.Unlock()
		}
	}
}

func parseHashFromLog(line string) (uint64, bool) {
	const marker = "hash=0x"
	idx := strings.Index(line, marker)
	if idx == -1 {
		return 0, false
	}
	start := idx + len(marker)
	end := start
	for end < len(line) && isHexDigit(line[end]) {
		end++
	}
	if start == end {
		return 0, false
	}
	v, err := strconv.ParseUint(line[start:end], 16, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func appendCacheLog(path, data string, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Printf("[ai:cachelog] failed to open %s: %v\n", path, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		fmt.Printf("[ai:cachelog] failed to write %s: %v\n", path, err)
	}
}

func logMinmaxCacheEntry(boardHash uint64, depth int, move Move, score float64, boardSize int) {
	if !GetConfig().AiCacheLogToFile {
		return
	}
	moveDesc := "nil"
	if move.IsValid(boardSize) {
		moveDesc = fmt.Sprintf("%d,%d", move.X, move.Y)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("boardHash=0x%x depth=%d move=[%s] score=%.2f\n", boardHash, depth, moveDesc, score))
	appendCacheLog(minmaxCacheLogPath, sb.String(), &minmaxLogMu)
}
func newAISearchCache() AISearchCache {
	return AISearchCache{
		MoveCache:         make(map[MoveCacheKey]float64),
		ImmediateWinMove:  make(map[ImmediateWinKey]bool),
		ImmediateWinState: make(map[ImmediateWinStateKey]bool),
		Edges:             make(map[StateKey][]StateKey),
	}
}

type EvalCacheEntry struct {
	Key   uint64
	Value float64
	Gen   uint32
	Valid bool
}

type EvalCache struct {
	mask    uint64
	buckets int
	entries []EvalCacheEntry
	gen     uint32
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
	return &EvalCache{
		mask:    size - 1,
		buckets: buckets,
		entries: make([]EvalCacheEntry, int(size)*buckets),
		gen:     1,
	}
}

func (ec *EvalCache) NextGeneration() {
	if ec == nil {
		return
	}
	ec.gen++
	if ec.gen == 0 {
		ec.gen = 1
	}
}

func (ec *EvalCache) bucketIndex(key uint64) int {
	return int(key&ec.mask) * ec.buckets
}

func (ec *EvalCache) Get(key uint64) (float64, bool) {
	start := ec.bucketIndex(key)
	for i := 0; i < ec.buckets; i++ {
		entry := ec.entries[start+i]
		if entry.Valid && entry.Key == key {
			return entry.Value, true
		}
	}
	return 0.0, false
}

func (ec *EvalCache) Put(key uint64, value float64) {
	start := ec.bucketIndex(key)
	victim := -1
	oldest := uint32(0)
	for i := 0; i < ec.buckets; i++ {
		idx := start + i
		entry := ec.entries[idx]
		if entry.Valid && entry.Key == key {
			ec.entries[idx] = EvalCacheEntry{Key: key, Value: value, Gen: ec.gen, Valid: true}
			return
		}
		if !entry.Valid {
			victim = idx
			break
		}
		age := ec.gen - entry.Gen
		if victim == -1 || age > oldest {
			victim = idx
			oldest = age
		}
	}
	if victim >= 0 {
		ec.entries[victim] = EvalCacheEntry{Key: key, Value: value, Gen: ec.gen, Valid: true}
	}
}

func hashBoard(board Board, boardSize int) uint64 {
	var hash uint64 = 1469598103934665603
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			v := uint64(board.At(x, y))
			hash ^= (v + 1)
			hash *= 1099511628211
		}
	}
	return hash
}

func selectCache(ctx minimaxContext) *AISearchCache {
	if ctx.settings.Cache != nil {
		return ctx.settings.Cache
	}
	return &defaultCache
}

func makeStateKey(state GameState, boardSize int, player PlayerColor) StateKey {
	return StateKey{
		Hash:          state.Hash,
		BoardSize:     boardSize,
		CapturedBlack: state.CapturedBlack,
		CapturedWhite: state.CapturedWhite,
		Status:        state.Status,
		CurrentPlayer: player,
	}
}

func ensureTT(cache *AISearchCache, config Config) {
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
	if cache.TT == nil || cache.TTSize != config.AiTtSize || cache.TTBuckets != buckets {
		cache.TT = NewTranspositionTable(uint64(config.AiTtSize), buckets)
		cache.TTSize = config.AiTtSize
		cache.TTBuckets = buckets
	}
	if cache.TranslatedTT == nil || cache.TranslatedTTSize != config.AiTtSize || cache.TranslatedTTBuckets != buckets {
		cache.TranslatedTT = NewTranspositionTable(uint64(config.AiTtSize), buckets)
		cache.TranslatedTTSize = config.AiTtSize
		cache.TranslatedTTBuckets = buckets
	}
}

func ensureEvalCache(cache *AISearchCache, config Config) {
	if !config.AiEnableEvalCache {
		cache.EvalCache = nil
		cache.EvalCacheSize = 0
		return
	}
	size := config.AiEvalCacheSize
	if size <= 0 {
		size = 1 << 18
	}
	if cache.EvalCache == nil || cache.EvalCacheSize != size {
		cache.EvalCache = NewEvalCache(uint64(size), 2)
		cache.EvalCacheSize = size
	}
}

func addEdge(cache *AISearchCache, parent, child StateKey) {
	children := cache.Edges[parent]
	for _, existing := range children {
		if existing == child {
			return
		}
	}
	cache.Edges[parent] = append(children, child)
}

func playerCell(player PlayerColor) Cell {
	return CellFromPlayer(player)
}

type candidateMove struct {
	move     Move
	priority int
}

const (
	prioWin               = 0
	prioBlockWin          = 1
	prioCreateFour        = 2
	prioBlockFour         = 3
	prioCreateOpen3       = 4
	prioBlockOpen3        = 5
	prioLastMove          = 10
	prioProximity         = 20
	prioDefault           = 50
	maxCandidatePrio      = 100
	proximityRadius       = 2
	lastMoveRadius        = 3
	translationMoveRadius = 4
	translationEvalMargin = 2
)

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

type translationMargins struct {
	left   int
	right  int
	top    int
	bottom int
}

func translationMarginsFromBBox(bbox boardBBox, boardSize int) translationMargins {
	if boardSize <= 0 {
		boardSize = bbox.width
		if boardSize <= 0 {
			boardSize = 1
		}
	}
	if bbox.stones == 0 {
		return translationMargins{
			left:   boardSize,
			right:  boardSize,
			top:    boardSize,
			bottom: boardSize,
		}
	}
	return translationMargins{
		left:   bbox.minX,
		right:  boardSize - 1 - bbox.maxX,
		top:    bbox.minY,
		bottom: boardSize - 1 - bbox.maxY,
	}
}

func translationGuardForDepth(depth, boardSize int) translationGuard {
	if depth < 0 {
		depth = 0
	}
	expansion := translationMoveRadius*depth + translationEvalMargin
	if boardSize > 0 && expansion > boardSize {
		expansion = boardSize
	}
	return translationGuard{
		Left:   expansion,
		Right:  expansion,
		Top:    expansion,
		Bottom: expansion,
	}
}

func translationMarginsSatisfy(m translationMargins, guard translationGuard) bool {
	if m.left < guard.Left {
		return false
	}
	if m.right < guard.Right {
		return false
	}
	if m.top < guard.Top {
		return false
	}
	return m.bottom >= guard.Bottom
}

func logAITask(ctx minimaxContext, indent int, format string, args ...interface{}) {
	if !ctx.settings.Config.AiLogSearchStats {
		return
	}
	prefix := strings.Repeat("  ", indent)
	fmt.Printf("[ai:trace] %s%s\n", prefix, fmt.Sprintf(format, args...))
}

func logPrune(ctx minimaxContext, depth int, move Move, best, alpha, beta float64) {
	if !ctx.settings.Config.AiLogSearchStats {
		return
	}
	prefix := strings.Repeat("  ", ctx.logIndent+1)
	fmt.Printf("[ai:prune] %sdepth=%d move=(%d,%d) best=%.2f alpha=%.2f beta=%.2f\n", prefix, depth, move.X, move.Y, best, alpha, beta)
}

func computeTranslationKey(state GameState, boardSize int) (uint64, boardBBox) {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	bbox := computeBBox(state.Board, boardSize)
	var hash uint64
	z := GetZobrist(boardSize)
	for y := bbox.minY; y <= bbox.maxY; y++ {
		if y < 0 || y >= boardSize {
			continue
		}
		for x := bbox.minX; x <= bbox.maxX; x++ {
			if x < 0 || x >= boardSize {
				continue
			}
			cell := state.Board.At(x, y)
			if cell == CellEmpty {
				continue
			}
			player := PlayerBlack
			if cell == CellWhite {
				player = PlayerWhite
			}
			relX := x
			relY := y
			if bbox.stones > 0 {
				relX = x - bbox.minX
				relY = y - bbox.minY
			}
			hash ^= z.stone(relX, relY, player)
		}
	}
	if state.ToMove == PlayerWhite {
		hash ^= z.side
	}
	hash ^= captureHash(PlayerBlack, state.CapturedBlack)
	hash ^= captureHash(PlayerWhite, state.CapturedWhite)
	key := mixKey(hash ^ mixKey(uint64(boardSize)<<32|uint64(state.Status)))
	return key, bbox
}

func translationGuardSatisfied(entry TTEntry, margins translationMargins) bool {
	if margins.left < entry.RequiredLeft {
		return false
	}
	if margins.right < entry.RequiredRight {
		return false
	}
	if margins.top < entry.RequiredTop {
		return false
	}
	return margins.bottom >= entry.RequiredBottom
}

func storeTranslatedEntry(cache *AISearchCache, key uint64, depth int, value float64, flag TTFlag, best Move, guard translationGuard, margins translationMargins) {
	if cache == nil || cache.TranslatedTT == nil {
		return
	}
	if !translationMarginsSatisfy(margins, guard) {
		return
	}
	cache.TranslatedTT.StoreWithGuard(key, depth, value, flag, best, guard)
}

func computeAvgDistToCenter(board Board, boardSize int) float64 {
	bbox := computeBBox(board, boardSize)
	if bbox.stones == 0 {
		return 0.0
	}
	center := boardSize / 2
	total := 0
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if board.At(x, y) == CellEmpty {
				continue
			}
			dx := x - center
			if dx < 0 {
				dx = -dx
			}
			dy := y - center
			if dy < 0 {
				dy = -dy
			}
			if dx > dy {
				total += dx
			} else {
				total += dy
			}
		}
	}
	return float64(total) / float64(bbox.stones)
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
			createFour = true
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
	threats := []candidateMove{}
	seenPriority := make([]int, boardSize*boardSize)
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

func hasUrgentThreat(board Board, boardSize int, toPlay PlayerColor) bool {
	_, urgent := generateThreatMoves(board, boardSize, toPlay)
	return urgent
}

func collectCandidateMoves(state GameState, currentPlayer PlayerColor, boardSize int) []candidateMove {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	board := state.Board
	bbox := computeBBox(board, boardSize)
	if bbox.stones == 0 {
		center := boardSize / 2
		return []candidateMove{{move: Move{X: center, Y: center}, priority: prioDefault}}
	}
	if bbox.stones == 1 {
		moves := []candidateMove{}
		seen := make([]bool, boardSize*boardSize)
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
							seen[idx] = true
							moves = append(moves, candidateMove{move: Move{X: nx, Y: ny}, priority: prioProximity})
						}
					}
				}
				return moves
			}
		}
	}

	threatMoves, urgent := generateThreatMoves(board, boardSize, currentPlayer)
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

	seenPriority := make([]int, boardSize*boardSize)
	for i := range seenPriority {
		seenPriority[i] = maxCandidatePrio
	}
	candidates := make([]candidateMove, 0, 64)
	addCandidate := func(move Move, priority int) {
		idx := move.Y*boardSize + move.X
		if priority < seenPriority[idx] {
			seenPriority[idx] = priority
			candidates = append(candidates, candidateMove{move: move, priority: priority})
		}
	}

	for _, threat := range threatMoves {
		addCandidate(threat.move, threat.priority)
	}

	if state.HasLastMove {
		lm := state.LastMove
		for dy := -lastMoveRadius; dy <= lastMoveRadius; dy++ {
			for dx := -lastMoveRadius; dx <= lastMoveRadius; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				if chebDist(dx, dy) > lastMoveRadius {
					continue
				}
				nx := lm.X + dx
				ny := lm.Y + dy
				if nx < x0 || nx > x1 || ny < y0 || ny > y1 {
					continue
				}
				if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
					continue
				}
				addCandidate(Move{X: nx, Y: ny}, prioLastMove)
			}
		}
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
					if nx < x0 || nx > x1 || ny < y0 || ny > y1 {
						continue
					}
					if !board.InBounds(nx, ny) || !board.IsEmpty(nx, ny) {
						continue
					}
					addCandidate(Move{X: nx, Y: ny}, prioProximity)
				}
			}
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		if candidates[i].move.Y != candidates[j].move.Y {
			return candidates[i].move.Y < candidates[j].move.Y
		}
		return candidates[i].move.X < candidates[j].move.X
	})
	return candidates
}

func candidateLimit(ctx minimaxContext, depthLeft, depthFromRoot int, tactical bool) int {
	config := ctx.settings.Config
	if config.AiEnableTacticalK && tactical {
		if depthFromRoot == 0 && config.AiKTactRoot > 0 {
			return config.AiKTactRoot
		}
		if depthFromRoot <= 2 && config.AiKTactMid > 0 {
			return config.AiKTactMid
		}
		if config.AiKTactDeep > 0 {
			return config.AiKTactDeep
		}
	}
	if !config.AiEnableDynamicTopK {
		return config.AiTopCandidates
	}
	if config.AiEnableTacticalK {
		if depthFromRoot == 0 && config.AiKQuietRoot > 0 {
			return config.AiKQuietRoot
		}
		if depthFromRoot <= 2 && config.AiKQuietMid > 0 {
			return config.AiKQuietMid
		}
		if config.AiKQuietDeep > 0 {
			return config.AiKQuietDeep
		}
	}
	if depthFromRoot == 0 && config.AiMaxCandidatesRoot > 0 {
		return config.AiMaxCandidatesRoot
	}
	if depthLeft >= 3 && config.AiMaxCandidatesDeep > 0 {
		return config.AiMaxCandidatesDeep
	}
	if config.AiMaxCandidatesMid > 0 {
		return config.AiMaxCandidatesMid
	}
	if config.AiTopCandidates > 0 {
		return config.AiTopCandidates
	}
	return 0
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

func orderCandidateMoves(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, candidates []candidateMove, maxCandidates int, pvMove *Move) []Move {
	evalSettings := ctx.settings
	evalSettings.Player = currentPlayer
	type scoredMove struct {
		score    float64
		priority int
		move     Move
	}
	scored := make([]scoredMove, 0, len(candidates))
	cache := selectCache(ctx)
	opponentHasImmediateWin := hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)
	for _, cand := range candidates {
		move := cand.move
		priority := cand.priority
		if isImmediateWinCached(cache, state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
			if prioWin < priority {
				priority = prioWin
			}
		} else if opponentHasImmediateWin {
			blockState := state.Clone()
			if applyMove(&blockState, ctx.rules, move, currentPlayer) {
				if !hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config) {
					if prioBlockWin < priority {
						priority = prioBlockWin
					}
				}
			}
		}
		score := heuristicForMove(state, ctx.rules, evalSettings, move)
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
		scored = append(scored, scoredMove{score: score, priority: priority, move: move})
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
	if pvMove != nil {
		for i := range scored {
			if scored[i].move.Equals(*pvMove) {
				pvEntry := scored[i]
				copy(scored[i:], scored[i+1:])
				scored = scored[:len(scored)-1]
				scored = append([]scoredMove{pvEntry}, scored...)
				break
			}
		}
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

func orderCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor, maximizing bool, depthFromRoot int, maxCandidates int, pvMove *Move) []Move {
	candidates := collectCandidateMoves(state, currentPlayer, ctx.settings.BoardSize)
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
	cache := selectCache(ctx)
	if len(findImmediateWinMovesCached(cache, state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)) > 0 {
		return true
	}
	if len(findImmediateWinMovesCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)) > 0 {
		return true
	}
	_, urgent := generateThreatMoves(state.Board, ctx.settings.BoardSize, currentPlayer)
	return urgent
}

func tacticalCandidates(state GameState, ctx minimaxContext, currentPlayer PlayerColor) []candidateMove {
	cache := selectCache(ctx)
	boardSize := ctx.settings.BoardSize
	seen := make(map[Move]int, 16)

	addMove := func(move Move, prio int) {
		if best, ok := seen[move]; !ok || prio < best {
			seen[move] = prio
		}
	}

	for _, move := range findImmediateWinMovesCached(cache, state, ctx.rules, currentPlayer, boardSize, ctx.settings.Config) {
		addMove(move, prioWin)
	}
	for _, move := range findImmediateWinMovesCached(cache, state, ctx.rules, otherPlayer(currentPlayer), boardSize, ctx.settings.Config) {
		addMove(move, prioBlockWin)
	}

	threatMoves, _ := generateThreatMoves(state.Board, boardSize, currentPlayer)
	for _, cand := range threatMoves {
		switch cand.priority {
		case prioCreateFour, prioBlockFour:
			addMove(cand.move, cand.priority)
		}
	}
	if len(seen) == 0 {
		for _, cand := range threatMoves {
			switch cand.priority {
			case prioCreateOpen3, prioBlockOpen3:
				addMove(cand.move, cand.priority)
			}
		}
	}

	moves := make([]candidateMove, 0, len(seen))
	for move, prio := range seen {
		if ok, _ := ctx.rules.IsLegal(state, move, currentPlayer); ok {
			moves = append(moves, candidateMove{move: move, priority: prio})
		}
	}
	return moves
}

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

func evalBoardCached(state GameState, rules Rules, settings AIScoreSettings, cache *AISearchCache) float64 {
	board := state.Board
	if !settings.Config.AiEnableEvalCache {
		return EvaluateBoard(board, settings.Player, settings.Config)
	}
	area := computePlayableArea(board, settings.BoardSize, settings.Config.AiBoardCacheMargin)
	nextCaptureWin := nextCaptureWouldWin(state, rules)
	areaHash := boardAreaHash(board, area, state.ToMove, nextCaptureWin, settings.Player, settings.BoardSize)
	pattern := buildPatternKey(state, area, nextCaptureWin)
	if value, ok := boardCache.get(areaHash, pattern); ok {
		return value
	}
	ensureEvalCache(cache, settings.Config)
	stateHash := state.Hash
	if cache.EvalCache != nil {
		if settings.Stats != nil {
			settings.Stats.EvalCacheProbes++
		}
		if stateHash != 0 {
			if value, ok := cache.EvalCache.Get(evalKey(stateHash, settings.BoardSize, settings.Player)); ok {
				if settings.Stats != nil {
					settings.Stats.EvalCacheHits++
				}
				return value
			}
		}
	}
	value := EvaluateBoard(board, settings.Player, settings.Config)
	boardCache.set(state, area, areaHash, value, settings.Player, nextCaptureWin, pattern)
	if cache.EvalCache != nil && stateHash != 0 {
		cache.EvalCache.Put(evalKey(stateHash, settings.BoardSize, settings.Player), value)
	}
	return value
}

func heuristicForMove(state GameState, rules Rules, settings AIScoreSettings, move Move) float64 {
	if ok, _ := rules.IsLegal(state, move, settings.Player); !ok {
		return illegalScore
	}
	next := state.Clone()
	if !applyMove(&next, rules, move, settings.Player) {
		return illegalScore
	}
	cache := selectCache(minimaxContext{settings: settings})
	return evalBoardCached(next, rules, settings, cache)
}

func evaluateStateHeuristic(state GameState, rules Rules, settings AIScoreSettings) float64 {
	switch state.Status {
	case StatusDraw:
		return 0.0
	case StatusBlackWon:
		if settings.Player == PlayerBlack {
			return winScore
		}
		return -winScore
	case StatusWhiteWon:
		if settings.Player == PlayerWhite {
			return winScore
		}
		return -winScore
	}
	cache := selectCache(minimaxContext{settings: settings})
	return evalBoardCached(state, rules, settings, cache)
}

func tacticalExtensionScore(state GameState, ctx minimaxContext, currentPlayer PlayerColor, depthFromRoot int) float64 {
	candidates := tacticalCandidates(state, ctx, currentPlayer)
	if len(candidates) == 0 {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	maximizing := currentPlayer == ctx.settings.Player
	best := math.Inf(-1)
	if !maximizing {
		best = math.Inf(1)
	}
	for _, cand := range candidates {
		move := cand.move
		if timedOut(ctx) {
			break
		}
		next := state.Clone()
		if !applyMove(&next, ctx.rules, move, currentPlayer) {
			continue
		}
		score := evaluateStateHeuristic(next, ctx.rules, ctx.settings)
		if maximizing {
			if score > best {
				best = score
			}
		} else {
			if score < best {
				best = score
			}
		}
	}
	if math.IsInf(best, 1) || math.IsInf(best, -1) {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	return best
}

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

func applyMove(state *GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(*state, move, player); !ok {
		return false
	}
	prevCapturedBlack := state.CapturedBlack
	prevCapturedWhite := state.CapturedWhite
	prevToMove := state.ToMove
	cell := playerCell(player)
	state.Board.Set(move.X, move.Y, cell)
	state.LastMove = move
	state.HasLastMove = true
	state.LastMessage = ""

	captures := rules.FindCaptures(state.Board, move, cell)
	for _, captured := range captures {
		state.Board.Remove(captured.X, captured.Y)
	}
	if len(captures) > 0 {
		capturedCount := len(captures)
		if player == PlayerBlack {
			state.CapturedBlack += capturedCount
		} else {
			state.CapturedWhite += capturedCount
		}
	}

	totalCaptured := state.CapturedBlack
	if player == PlayerWhite {
		totalCaptured = state.CapturedWhite
	}
	if totalCaptured >= rules.CaptureWinStones() {
		if player == PlayerBlack {
			state.Status = StatusBlackWon
		} else {
			state.Status = StatusWhiteWon
		}
	} else if rules.IsWin(state.Board, move) {
		if player == PlayerBlack {
			state.Status = StatusBlackWon
		} else {
			state.Status = StatusWhiteWon
		}
	} else if rules.IsDraw(state.Board) {
		state.Status = StatusDraw
	} else {
		state.Status = StatusRunning
	}

	state.ToMove = otherPlayer(player)
	UpdateHashAfterMove(state, move, player, captures, prevToMove, prevCapturedBlack, prevCapturedWhite)
	return true
}

func isImmediateWin(state GameState, rules Rules, move Move, player PlayerColor) bool {
	if ok, _ := rules.IsLegal(state, move, player); !ok {
		return false
	}
	probe := state.Clone()
	cell := playerCell(player)
	probe.Board.Set(move.X, move.Y, cell)
	captures := rules.FindCaptures(probe.Board, move, cell)
	capturedCount := len(captures)
	totalCaptured := state.CapturedBlack
	if player == PlayerWhite {
		totalCaptured = state.CapturedWhite
	}
	totalCaptured += capturedCount
	if totalCaptured >= rules.CaptureWinStones() {
		return true
	}
	return rules.IsWin(probe.Board, move)
}

func isImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, move Move, player PlayerColor, boardSize int) bool {
	key := ImmediateWinKey{Hash: state.Hash, BoardSize: boardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, Player: player, X: move.X, Y: move.Y}
	if value, ok := cache.ImmediateWinMove[key]; ok {
		return value
	}
	result := isImmediateWin(state, rules, move, player)
	cache.ImmediateWinMove[key] = result
	return result
}

func findAlignmentWinMoves(board Board, player PlayerColor, winLen int) []Move {
	if winLen <= 0 {
		winLen = 5
	}
	size := board.Size()
	seen := make([]bool, size*size)
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

func findCaptureWinMoves(state GameState, rules Rules, player PlayerColor) []Move {
	remaining := rules.CaptureWinStones()
	if player == PlayerBlack {
		remaining -= state.CapturedBlack
	} else {
		remaining -= state.CapturedWhite
	}
	if remaining > 2 {
		return nil
	}
	board := state.Board
	size := board.Size()
	seen := make([]bool, size*size)
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

func findImmediateWinMovesCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) []Move {
	if !config.AiUseScanWinIn1 {
		moves := make([]Move, 0, 4)
		board := state.Board
		for y := 0; y < boardSize; y++ {
			for x := 0; x < boardSize; x++ {
				if !board.IsEmpty(x, y) {
					continue
				}
				move := Move{X: x, Y: y}
				if ok, _ := rules.IsLegal(state, move, player); !ok {
					continue
				}
				if isImmediateWinCached(cache, state, rules, move, player, boardSize) {
					moves = append(moves, move)
				}
			}
		}
		return moves
	}
	alignment := findAlignmentWinMoves(state.Board, player, rules.WinLength())
	capture := findCaptureWinMoves(state, rules, player)
	seen := make(map[Move]struct{}, len(alignment)+len(capture))
	candidates := make([]Move, 0, len(alignment)+len(capture))
	for _, move := range alignment {
		seen[move] = struct{}{}
		candidates = append(candidates, move)
	}
	for _, move := range capture {
		if _, ok := seen[move]; ok {
			continue
		}
		seen[move] = struct{}{}
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
	for y := 0; y < boardSize; y++ {
		for x := 0; x < boardSize; x++ {
			if !board.IsEmpty(x, y) {
				continue
			}
			move := Move{X: x, Y: y}
			if ok, _ := rules.IsLegal(state, move, player); !ok {
				continue
			}
			blockState := state.Clone()
			if !applyMove(&blockState, rules, move, player) {
				continue
			}
			if !hasImmediateWinCached(cache, blockState, rules, otherPlayer(player), boardSize, config) {
				moves = append(moves, move)
			}
		}
	}
	return moves
}

func hasImmediateWinCached(cache *AISearchCache, state GameState, rules Rules, player PlayerColor, boardSize int, config Config) bool {
	if boardSize <= 0 {
		boardSize = state.Board.Size()
	}
	if boardSize > state.Board.Size() {
		boardSize = state.Board.Size()
	}
	key := ImmediateWinStateKey{Hash: state.Hash, BoardSize: boardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, Player: player}
	if value, ok := cache.ImmediateWinState[key]; ok {
		return value
	}
	if len(findImmediateWinMovesCached(cache, state, rules, player, boardSize, config)) > 0 {
		cache.ImmediateWinState[key] = true
		return true
	}
	cache.ImmediateWinState[key] = false
	return false
}

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

func minimax(state GameState, ctx minimaxContext, depth int, currentPlayer PlayerColor, depthFromRoot int, alpha, beta float64) float64 {
	logAITask(ctx, ctx.logIndent, "minimax enter depth=%d depthFromRoot=%d", depth, depthFromRoot)
	if timedOut(ctx) || state.Status != StatusRunning {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	if depth <= 0 {
		if ctx.settings.Config.AiEnableTacticalExt && ctx.settings.Config.AiTacticalExtDepth > 0 {
			if isTacticalPosition(state, ctx, currentPlayer) {
				return tacticalExtensionScore(state, ctx, currentPlayer, depthFromRoot)
			}
		}
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}

	if ctx.settings.Stats != nil {
		ctx.settings.Stats.Nodes++
	}
	cache := selectCache(ctx)
	ensureTT(cache, ctx.settings.Config)
	boardSize := ctx.settings.BoardSize
	boardHash := ttKeyFor(state, boardSize)
	transKey, transBBox := computeTranslationKey(state, boardSize)
	transMargins := translationMarginsFromBBox(transBBox, boardSize)
	alphaOrig := alpha
	betaOrig := beta
	var pvMove *Move
	if ctx.settings.Stats != nil {
		ctx.settings.Stats.TTProbes++
	}
	ttStart := time.Now()
	if cache.TT != nil {
		if entry, ok := cache.TT.Probe(boardHash); ok {
			ttDuration := time.Since(ttStart).Milliseconds()
			logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms hit=true", depth, ttDuration)
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.TTHits++
			}
			if entry.BestMove.IsValid(ctx.settings.BoardSize) {
				pv := entry.BestMove
				pvMove = &pv
			}
			if entry.Depth >= depth {
				logAITask(ctx, ctx.logIndent+1, "TT exact entry depth=%d flag=%d value=%.2f", entry.Depth, entry.Flag, entry.Value)
				if _, ret, value := applyTTEntry(entry, depth, &alpha, &beta, ctx.settings.Stats); ret {
					logAITask(ctx, ctx.logIndent+1, "TT exact returning value=%.2f", value)
					return value
				}
			}
		} else {
			ttDuration := time.Since(ttStart).Milliseconds()
			logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms hit=false", depth, ttDuration)
		}
	} else {
		ttDuration := time.Since(ttStart).Milliseconds()
		logAITask(ctx, ctx.logIndent+1, "TT exact probe depth=%d took=%dms table=nil", depth, ttDuration)
	}

	if cache.TranslatedTT != nil {
		if ctx.settings.Stats != nil {
			ctx.settings.Stats.TranslatedTTProbes++
		}
		transStart := time.Now()
		if entry, ok := cache.TranslatedTT.Probe(transKey); ok {
			guardOk := translationGuardSatisfied(entry, transMargins)
			logAITask(ctx, ctx.logIndent+1, "Translated TT probe depth=%d took=%dms guardOk=%v required=(%d,%d,%d,%d)", depth, time.Since(transStart).Milliseconds(), guardOk, entry.RequiredLeft, entry.RequiredRight, entry.RequiredTop, entry.RequiredBottom)
			if guardOk {
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.TranslatedTTHits++
				}
				if entry.BestMove.IsValid(boardSize) {
					pv := entry.BestMove
					if pvMove == nil {
						pvMove = &pv
					}
				}
				if _, ret, value := applyTTEntry(entry, depth, &alpha, &beta, ctx.settings.Stats); ret {
					logAITask(ctx, ctx.logIndent+1, "Translated TT returning value=%.2f", value)
					return value
				}
			} else {
				logAITask(ctx, ctx.logIndent+1, "Translated TT guard failed margins=(%d,%d,%d,%d)", transMargins.left, transMargins.right, transMargins.top, transMargins.bottom)
			}
		} else {
			logAITask(ctx, ctx.logIndent+1, "Translated TT probe depth=%d took=%dms hit=false", depth, time.Since(transStart).Milliseconds())
		}
	}
	logAITask(ctx, ctx.logIndent, "No TT hit; continuing search")

	maximizing := currentPlayer == ctx.settings.Player
	best := math.Inf(-1)
	if !maximizing {
		best = math.Inf(1)
	}
	secondBest := math.Inf(-1)
	secondBestMove := Move{}
	cache = selectCache(ctx)
	immediateWins := findImmediateWinMovesCached(cache, state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)
	mustBlock := false
	if len(immediateWins) == 0 {
		mustBlock = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.BoardSize, ctx.settings.Config)
	}
	tactical := false
	if ctx.settings.Config.AiEnableTacticalK || ctx.settings.Config.AiEnableTacticalMode || ctx.settings.Config.AiEnableTacticalExt {
		tactical = isTacticalPosition(state, ctx, currentPlayer)
	}
	maxCandidates := candidateLimit(ctx, depth, depthFromRoot, tactical)
	var truncatedCandidates []Move
	var candidates []Move
	if len(immediateWins) > 0 {
		candidates = orderMovesFromList(state, ctx, currentPlayer, maximizing, depthFromRoot, immediateWins, pvMove, prioWin)
	} else if mustBlock {
		blockMoves := findBlockingMoves(cache, state, ctx.rules, currentPlayer, ctx.settings.BoardSize, ctx.settings.Config)
		candidates = orderMovesFromList(state, ctx, currentPlayer, maximizing, depthFromRoot, blockMoves, pvMove, prioBlockWin)
	} else if ctx.settings.Config.AiEnableTacticalMode && tactical {
		tacticalMoves := tacticalCandidates(state, ctx, currentPlayer)
		if len(tacticalMoves) > 0 {
			candidates = orderCandidateMoves(state, ctx, currentPlayer, maximizing, depthFromRoot, tacticalMoves, 0, pvMove)
		} else {
			candidates = orderCandidates(state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove)
		}
	} else {
		candidates = orderCandidates(state, ctx, currentPlayer, maximizing, depthFromRoot, maxCandidates, pvMove)
	}
	if ctx.settings.Config.AiLogSearchStats {
		if mustBlock {
			if ctx.settings.Config.AiTopCandidates > 0 {
				truncatedCandidates = orderCandidates(state, ctx, currentPlayer, maximizing, depthFromRoot, ctx.settings.Config.AiTopCandidates, pvMove)
			}
			fmt.Printf("[ai:must_block] allowed=%s ordered=%s truncated=%s\n", formatMoves(candidates), formatMoves(candidates), formatMoves(truncatedCandidates))
		} else {
			fmt.Printf("[ai:must_block] opponent_win_in_1_wide=%v\n", mustBlock)
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
	for _, move := range candidates {
		if timedOut(ctx) {
			break
		}
		if ctx.settings.Config.AiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, currentPlayer, ctx.settings.BoardSize) {
			win := winScore
			if currentPlayer != ctx.settings.Player {
				win = -winScore
			}
			if cache.TT != nil {
				replaced, overwrote := cache.TT.Store(boardHash, depth, win, TTExact, move)
				if ctx.settings.Stats != nil {
					ctx.settings.Stats.TTStores++
					if replaced || overwrote {
						ctx.settings.Stats.TTOverwrites++
					}
				}
				if replaced || overwrote {
					logMinmaxCacheEntry(boardHash, depth, move, win, boardSize)
				}
			}
			storeTranslatedEntry(cache, transKey, depth, win, TTExact, move, translationGuardForDepth(depth, boardSize), transMargins)
			return win
		}
		value := evaluateMoveWithCache(state, ctx, currentPlayer, move, depth, depthFromRoot, boardHash, nil, alpha, beta)
		if maximizing {
			if value > best {
				secondBest = best
				secondBestMove = bestMove
				best = value
				bestMove = move
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
			}
			if best < beta {
				beta = best
			}
		}
		if beta <= alpha {
			if ctx.settings.Stats != nil {
				ctx.settings.Stats.Cutoffs++
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
			break
		}
	}

	if math.IsInf(best, 1) || math.IsInf(best, -1) {
		return 0.0
	}
	if depthFromRoot == 0 && maximizing {
		if best <= -winScore/2 && secondBest > math.Inf(-1) {
			best = secondBest
			bestMove = secondBestMove
		}
	}
	flag := TTExact
	if best <= alphaOrig {
		flag = TTUpper
	} else if best >= betaOrig {
		flag = TTLower
	}
	if cache.TT != nil {
		replaced, overwrote := cache.TT.Store(boardHash, depth, best, flag, bestMove)
		if ctx.settings.Stats != nil {
			ctx.settings.Stats.TTStores++
			if replaced || overwrote {
				ctx.settings.Stats.TTOverwrites++
			}
		}
		if replaced || overwrote {
			logMinmaxCacheEntry(boardHash, depth, bestMove, best, boardSize)
		}
	}
	storeTranslatedEntry(cache, transKey, depth, best, flag, bestMove, translationGuardForDepth(depth, boardSize), transMargins)
	return best
}

func applyTTEntry(entry TTEntry, depth int, alpha *float64, beta *float64, stats *SearchStats) (used bool, ret bool, value float64) {
	if entry.Depth < depth {
		return false, false, 0.0
	}
	switch entry.Flag {
	case TTExact:
		return true, true, entry.Value
	case TTLower:
		if entry.Value > *alpha {
			*alpha = entry.Value
		}
	case TTUpper:
		if entry.Value < *beta {
			*beta = entry.Value
		}
	}
	if *alpha >= *beta {
		if stats != nil {
			stats.Cutoffs++
		}
		return true, true, entry.Value
	}
	return true, false, entry.Value
}

func evaluateMoveWithCache(state GameState, ctx minimaxContext, currentPlayer PlayerColor, move Move, depthLeft int, depthFromRoot int, boardHash uint64, outCached *bool, alpha, beta float64) float64 {
	if timedOut(ctx) {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings)
	}
	cache := selectCache(ctx)
	key := MoveCacheKey{Hash: boardHash, DepthLeft: depthLeft, BoardSize: ctx.settings.BoardSize, CapturedBlack: state.CapturedBlack, CapturedWhite: state.CapturedWhite, Status: state.Status, CurrentPlayer: currentPlayer, X: move.X, Y: move.Y}
	if value, ok := cache.MoveCache[key]; ok {
		if outCached != nil {
			*outCached = true
		}
		return value
	}

	score := illegalScore
	if ok, _ := ctx.rules.IsLegal(state, move, currentPlayer); ok {
		next := state.Clone()
		if applyMove(&next, ctx.rules, move, currentPlayer) {
			parentKey := makeStateKey(state, ctx.settings.BoardSize, currentPlayer)
			childKey := makeStateKey(next, ctx.settings.BoardSize, next.ToMove)
			addEdge(cache, parentKey, childKey)
			if ctx.settings.OnGhostUpdate != nil {
				ctx.settings.OnGhostUpdate(next)
			}
			if depthLeft <= 1 || timedOut(ctx) {
				score = evaluateStateHeuristic(next, ctx.rules, ctx.settings)
			} else {
				nextCtx := ctx
				nextCtx.logIndent = ctx.logIndent + 1
				score = minimax(next, nextCtx, depthLeft-1, otherPlayer(currentPlayer), depthFromRoot+1, alpha, beta)
			}
		}
	}
	cache.MoveCache[key] = score
	if outCached != nil {
		*outCached = false
	}
	return score
}

func scoreBoardAtDepth(state GameState, settings AIScoreSettings, ctx minimaxContext, depth int, alpha, beta float64, outUsedCache *bool) ([]float64, bool) {
	if timedOut(ctx) {
		return nil, false
	}
	usedCache := false
	scores := make([]float64, settings.BoardSize*settings.BoardSize)
	for i := range scores {
		scores[i] = illegalScore
	}
	boardHash := ttKeyFor(state, settings.BoardSize)
	cache := selectCache(ctx)
	ensureTT(cache, settings.Config)
	var pvMove *Move
	if cache.TT != nil {
		if entry, ok := cache.TT.Probe(boardHash); ok {
			if entry.BestMove.IsValid(settings.BoardSize) {
				pv := entry.BestMove
				pvMove = &pv
			}
		}
	}
	immediateWins := findImmediateWinMovesCached(cache, state, ctx.rules, settings.Player, settings.BoardSize, settings.Config)
	mustBlock := false
	if len(immediateWins) == 0 {
		mustBlock = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(settings.Player), settings.BoardSize, settings.Config)
	}
	tactical := false
	if settings.Config.AiEnableTacticalK || settings.Config.AiEnableTacticalMode || settings.Config.AiEnableTacticalExt {
		tactical = isTacticalPosition(state, ctx, settings.Player)
	}
	maxCandidates := candidateLimit(ctx, depth, 0, tactical)
	var truncatedCandidates []Move
	var candidates []Move
	if len(immediateWins) > 0 {
		candidates = orderMovesFromList(state, ctx, settings.Player, true, 0, immediateWins, pvMove, prioWin)
	} else if mustBlock {
		blockMoves := findBlockingMoves(cache, state, ctx.rules, settings.Player, settings.BoardSize, settings.Config)
		candidates = orderMovesFromList(state, ctx, settings.Player, true, 0, blockMoves, pvMove, prioBlockWin)
	} else if settings.Config.AiEnableTacticalMode && tactical {
		tacticalMoves := tacticalCandidates(state, ctx, settings.Player)
		if len(tacticalMoves) > 0 {
			candidates = orderCandidateMoves(state, ctx, settings.Player, true, 0, tacticalMoves, 0, pvMove)
		} else {
			candidates = orderCandidates(state, ctx, settings.Player, true, 0, maxCandidates, pvMove)
		}
	} else {
		candidates = orderCandidates(state, ctx, settings.Player, true, 0, maxCandidates, pvMove)
	}
	if settings.Config.AiLogSearchStats {
		if mustBlock {
			if settings.Config.AiTopCandidates > 0 {
				truncatedCandidates = orderCandidates(state, ctx, settings.Player, true, 0, settings.Config.AiTopCandidates, pvMove)
			}
			fmt.Printf("[ai:must_block depth=%d] allowed=%s ordered=%s truncated=%s\n", depth, formatMoves(candidates), formatMoves(candidates), formatMoves(truncatedCandidates))
		} else {
			fmt.Printf("[ai:must_block depth=%d] opponent_win_in_1_wide=%v\n", depth, mustBlock)
		}
	}
	if settings.Stats != nil {
		settings.Stats.RootCandidates += int64(len(candidates))
		settings.Stats.RootSamples++
	}
	for _, move := range candidates {
		if timedOut(ctx) {
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return nil, false
		}
		if settings.Config.AiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, settings.Player, settings.BoardSize) {
			win := winScore
			if settings.Player != ctx.settings.Player {
				win = -winScore
			}
			scores[move.Y*settings.BoardSize+move.X] = win
			if outUsedCache != nil {
				*outUsedCache = usedCache
			}
			return scores, true
		}
		idx := move.Y*settings.BoardSize + move.X
		cached := false
		score := evaluateMoveWithCache(state, ctx, settings.Player, move, depth, depth, boardHash, &cached, alpha, beta)
		if settings.Config.AiEnableAspiration && (score <= alpha || score >= beta) {
			if timedOut(ctx) {
				if outUsedCache != nil {
					*outUsedCache = usedCache
				}
				return nil, false
			}
			score = evaluateMoveWithCache(state, ctx, settings.Player, move, depth, depth, boardHash, &cached, math.Inf(-1), math.Inf(1))
		}
		if cached {
			usedCache = true
		}
		scores[idx] = score
	}
	if outUsedCache != nil {
		*outUsedCache = usedCache
	}
	return scores, true
}

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
	if settings.Config.AiMaxDepth > 0 {
		settings.Depth = settings.Config.AiMaxDepth
	}
	minDepth := 1
	if settings.Config.AiMinDepth > 0 {
		minDepth = settings.Config.AiMinDepth
	}
	var killers [][]Move
	var history []int
	if settings.Config.AiEnableKillerMoves {
		killers = make([][]Move, settings.Depth+2)
	}
	if settings.Config.AiEnableHistoryMoves {
		history = make([]int, settings.BoardSize*settings.BoardSize)
	}
	ctx := minimaxContext{rules: rules, settings: settings, start: time.Now(), killers: killers, history: history, logIndent: 0}
	if settings.Config.AiTimeBudgetMs > 0 {
		ctx.deadline = ctx.start.Add(time.Duration(settings.Config.AiTimeBudgetMs) * time.Millisecond)
		ctx.hasDeadline = true
	}
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
	initialCandidates := collectCandidateMoves(state, settings.Player, settings.BoardSize)
	if len(initialCandidates) == 0 {
		scores := make([]float64, settings.BoardSize*settings.BoardSize)
		for i := range scores {
			scores[i] = illegalScore
		}
		center := settings.BoardSize / 2
		scores[center*settings.BoardSize+center] = 0.0
		return scores
	}
	logAITask(ctx, 1, "Candidate generation complete count=%d", len(initialCandidates))
	startTime := ctx.start
	lastDepthCompleted := 0
	cache := selectCache(ctx)
	ensureTT(cache, settings.Config)
	boardHash := ttKeyFor(state, settings.BoardSize)
	var cachedScores []float64
	var scores []float64
	var lastScores []float64
	var lastBestScore float64
	var fallbackScores []float64
	fallbackBestScore := math.Inf(-1)
	haveBest := false
	startDepth := minDepth
	cachedDepth := 0
	if settings.DirectDepthOnly {
		startDepth = settings.Depth
		if startDepth < minDepth {
			startDepth = minDepth
		}
	} else if scoresFromCache, depth := highestCachedDepthScores(boardHash, settings.BoardSize, settings.Player, minDepth, settings.Depth); depth > 0 {
		logAITask(ctx, 1, "Depth cache hit depth=%d, continuing search at %d", depth, minInt(depth+1, settings.Depth))
		cachedDepth = depth
		cachedScores = scoresFromCache
		fallbackScores = append([]float64(nil), scoresFromCache...)
		fallbackBestScore = maxScore(scoresFromCache)
		lastBestScore = fallbackBestScore
		haveBest = true
		startDepth = depth + 1
	}
	if startDepth < minDepth {
		startDepth = minDepth
	}
	if startDepth > settings.Depth {
		if settings.Stats != nil && cachedDepth > 0 {
			settings.Stats.CompletedDepths = cachedDepth
		}
		if len(fallbackScores) > 0 {
			return fallbackScores
		}
		return cachedScores
	}
	for depth := startDepth; depth <= settings.Depth; depth++ {
		if timedOut(ctx) && depth > minDepth {
			break
		}
		logAITask(ctx, 1, "Depth %d start", depth)
		depthStart := time.Now()
		if settings.Config.AiQuickWinExit {
			for _, cand := range initialCandidates {
				move := cand.move
				if isImmediateWinCached(cache, state, rules, move, settings.Player, settings.BoardSize) {
					logAITask(ctx, 2, "Immediate win cached move=%v depth=%d", move, depth)
					winScores := make([]float64, settings.BoardSize*settings.BoardSize)
					for i := range winScores {
						winScores[i] = illegalScore
					}
					winScores[move.Y*settings.BoardSize+move.X] = winScore
					return winScores
				}
			}
		}
		key := cacheKey{Hash: boardHash, Depth: depth, BoardSize: settings.BoardSize, Player: settings.Player}
		cachedScores, ok := depthCache[key]
		cached := ok
		if cached && settings.Config.AiLogSearchStats {
			fmt.Printf("[ai:cache] board=0x%x depth=%d reused cached scores\n", boardHash, depth)
		}
		if !cached {
			usedCache := false
			alpha := math.Inf(-1)
			beta := math.Inf(1)
			if settings.Config.AiEnableAspiration && haveBest {
				window := settings.Config.AiAspWindow
				if window > 0 {
					if settings.Config.AiAspWindowMax > 0 && window > settings.Config.AiAspWindowMax {
						window = settings.Config.AiAspWindowMax
					}
					alpha = lastBestScore - window
					beta = lastBestScore + window
				}
			}
			var completed bool
			scores, completed = scoreBoardAtDepth(state, settings, ctx, depth, alpha, beta, &usedCache)
			if !completed {
				if settings.Config.AiReturnLastComplete && lastScores != nil {
					break
				}
				if scores != nil {
					lastScores = scores
				}
				break
			}
			depthCache[key] = scores
			sig := cacheSignature{Hash: boardHash, BoardSize: settings.BoardSize, Player: settings.Player}
			if depth > depthCacheBest[sig] {
				depthCacheBest[sig] = depth
			}
			cached = usedCache
		} else {
			scores = cachedScores
		}
		if len(scores) > 0 {
			depthBest := maxScore(scores)
			if depthBest > fallbackBestScore {
				fallbackBestScore = depthBest
				fallbackScores = append([]float64(nil), scores...)
			}
		}
		duration := time.Since(depthStart)
		logAITask(ctx, 1, "Depth %d completed in %dms cached=%v", depth, duration.Milliseconds(), cached)
		if settings.Stats != nil {
			settings.Stats.DepthDurations = append(settings.Stats.DepthDurations, time.Since(depthStart))
			settings.Stats.CompletedDepths = depth
		}
		if settings.Config.LogDepthScores {
			for _, cand := range initialCandidates {
				move := cand.move
				score := scores[move.Y*settings.BoardSize+move.X]
				_ = score
			}
		}
		bestScore := math.Inf(-1)
		bestX, bestY := -1, -1
		for y := 0; y < settings.BoardSize; y++ {
			for x := 0; x < settings.BoardSize; x++ {
				score := scores[y*settings.BoardSize+x]
				if score > bestScore {
					bestScore = score
					bestX = x
					bestY = y
				}
			}
		}
		_ = bestX
		_ = bestY
		_ = cached
		lastDepthCompleted = depth
		lastScores = scores
		lastBestScore = bestScore
		haveBest = true
	}
	totalDuration := time.Since(startTime)
	logAITask(ctx, 0, "ScoreBoard finished depth=%d total=%dms", lastDepthCompleted, totalDuration.Milliseconds())
	if !settings.DirectDepthOnly && lastDepthCompleted < settings.Depth {
		if timedOut(ctx) || (ctx.settings.ShouldStop != nil && ctx.settings.ShouldStop()) {
			enqueueSearchBacklogTask(state, rules)
		}
	}
	if lastScores != nil {
		if lastBestScore <= fallbackBestScore && len(fallbackScores) > 0 {
			return fallbackScores
		}
		return lastScores
	}
	if len(fallbackScores) > 0 && lastBestScore <= fallbackBestScore {
		return fallbackScores
	}
	return scores
}

func TranspositionSize(cache AISearchCache) int {
	if cache.TT == nil {
		return 0
	}
	return cache.TT.Count()
}

func RerootCache(cache *AISearchCache, state GameState) {
	boardSize := state.Board.Size()
	cache.Root = makeStateKey(state, boardSize, state.ToMove)
	cache.HasRoot = true

	reachable := make(map[StateKey]struct{})
	stack := []StateKey{cache.Root}
	for len(stack) > 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := reachable[key]; ok {
			continue
		}
		reachable[key] = struct{}{}
		children, ok := cache.Edges[key]
		if !ok {
			continue
		}
		for _, child := range children {
			stack = append(stack, child)
		}
	}

	stateFromMove := func(key MoveCacheKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.CurrentPlayer}
	}
	stateFromImmediateMove := func(key ImmediateWinKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.Player}
	}
	stateFromImmediateState := func(key ImmediateWinStateKey) StateKey {
		return StateKey{Hash: key.Hash, BoardSize: key.BoardSize, CapturedBlack: key.CapturedBlack, CapturedWhite: key.CapturedWhite, Status: key.Status, CurrentPlayer: key.Player}
	}

	for key := range cache.MoveCache {
		if _, ok := reachable[stateFromMove(key)]; !ok {
			delete(cache.MoveCache, key)
		}
	}
	for key := range cache.ImmediateWinMove {
		if _, ok := reachable[stateFromImmediateMove(key)]; !ok {
			delete(cache.ImmediateWinMove, key)
		}
	}
	for key := range cache.ImmediateWinState {
		if _, ok := reachable[stateFromImmediateState(key)]; !ok {
			delete(cache.ImmediateWinState, key)
		}
	}
	for key, children := range cache.Edges {
		if _, ok := reachable[key]; !ok {
			delete(cache.Edges, key)
			continue
		}
		filtered := children[:0]
		for _, child := range children {
			if _, ok := reachable[child]; ok {
				filtered = append(filtered, child)
			}
		}
		cache.Edges[key] = filtered
	}
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
