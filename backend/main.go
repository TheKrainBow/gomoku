package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

type StatusResponse struct {
	Settings           GameSettingsDTO   `json:"settings"`
	Config             Config            `json:"config"`
	NextPlayer         int               `json:"next_player"`
	Winner             int               `json:"winner"`
	BoardSize          int               `json:"board_size"`
	Status             string            `json:"status"`
	History            []historyEntryDTO `json:"history"`
	WinReason          string            `json:"win_reason"`
	WinningLine        []Move            `json:"winning_line"`
	WinningCapturePair []Move            `json:"winning_capture_pair"`
	CaptureWinStones   int               `json:"capture_win_stones"`
	TurnStartedAtMs    int64             `json:"turn_started_at_ms"`
}

type GameSettingsDTO struct {
	Mode        string `json:"mode"`
	HumanPlayer int    `json:"human_player"`
}

type apiMove struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Player int `json:"player"`
}

type historyEntryDTO struct {
	X                 int          `json:"x"`
	Y                 int          `json:"y"`
	Player            int          `json:"player"`
	ElapsedMs         float64      `json:"elapsed_ms"`
	IsAi              bool         `json:"is_ai"`
	CapturedCount     int          `json:"captured_count"`
	CapturedPositions []Move       `json:"captured_positions"`
	Changes           []cellChange `json:"changes"`
	Depth             int          `json:"depth"`
}

type changesPayload struct {
	Changes []cellChange `json:"changes"`
}

type historyPayload struct {
	History []historyEntryDTO `json:"history"`
}

type resetPayload struct {
	History            []historyEntryDTO `json:"history"`
	NextPlayer         int               `json:"next_player"`
	Winner             int               `json:"winner"`
	Status             string            `json:"status"`
	BoardSize          int               `json:"board_size"`
	WinReason          string            `json:"win_reason"`
	WinningLine        []Move            `json:"winning_line"`
	WinningCapturePair []Move            `json:"winning_capture_pair"`
	CaptureWinStones   int               `json:"capture_win_stones"`
	TurnStartedAtMs    int64             `json:"turn_started_at_ms"`
}

type cellChange struct {
	X     int `json:"x"`
	Y     int `json:"y"`
	Value int `json:"value"`
}

type settingsPayload struct {
	Settings GameSettingsDTO `json:"settings"`
	Config   Config          `json:"config"`
}

type analyseMove struct {
	X     int     `json:"x"`
	Y     int     `json:"y"`
	Score float64 `json:"score"`
	Depth int     `json:"depth"`
	Valid bool    `json:"valid"`
}

type analyseResponse struct {
	BestMove        analyseMove `json:"best_move"`
	DepthUsed       int         `json:"depth_used"`
	DurationMs      int64       `json:"duration_ms"`
	Nodes           int64       `json:"nodes"`
	HeuristicCalls  int64       `json:"heuristic_calls"`
	HeuristicTimeMs int64       `json:"heuristic_time_ms"`
	AvgHeuristicMs  float64     `json:"avg_heuristic_ms"`
	BoardGenTimeMs  int64       `json:"board_gen_time_ms"`
	BoardGenOps     int64       `json:"board_gen_ops"`
	TTProbes        int64       `json:"tt_probes"`
	TTHits          int64       `json:"tt_hits"`
	TTExactHits     int64       `json:"tt_exact_hits"`
	TTLowerHits     int64       `json:"tt_lower_hits"`
	TTUpperHits     int64       `json:"tt_upper_hits"`
	TTStores        int64       `json:"tt_stores"`
	TTOverwrites    int64       `json:"tt_overwrites"`
	TTReplacements  int64       `json:"tt_replacements"`
	Cutoffs         int64       `json:"cutoffs"`
	TTCutoffs       int64       `json:"tt_cutoffs"`
	ABCutoffs       int64       `json:"ab_cutoffs"`
}

type analyseRequest struct {
	Board        [][]int `json:"board"`
	Depth        int     `json:"depth"`
	NextPlayer   int     `json:"next_player"`
	UseTempCache bool    `json:"use_temp_cache"`
}

type ttCacheStatusResponse struct {
	Count    int     `json:"count"`
	Capacity int     `json:"capacity"`
	Usage    float64 `json:"usage"`
	Full     bool    `json:"full"`
}

func main() {
	var persistOnce sync.Once
	persistOnShutdown := func(reason string) {
		persistOnce.Do(func() {
			log.Printf("[backend] persisting caches on %s", reason)
			persistCaches()
		})
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[backend] panic recovered in main: %v", recovered)
			persistOnShutdown("panic")
		}
	}()

	controller := NewGameController(DefaultGameSettings())
	loadPersistedCaches()
	defer persistOnShutdown("exit")
	hub := NewHub()
	ghostHub := NewGhostHub()
	analiticsHub := NewAnaliticsHub()
	searchBacklogManager.SetAnaliticsHub(analiticsHub)
	startSearchBacklogWorker(controller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controller.SetGhostPublisher(
		func() bool { return ghostHub.HasClients() && GetConfig().GhostMode },
		func(payload ghostPayload) {
			ghostHub.Publish(payload)
		},
	)

	go hub.Run(ctx.Done())
	go ghostHub.Run(ctx.Done())
	go analiticsHub.Run(ctx.Done())
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if controller.Tick() {
					if entry, ok := controller.LatestHistoryEntry(); ok {
						hub.broadcastHistory <- historyPayload{History: []historyEntryDTO{historyEntryToDTO(entry)}}
					}
					hub.broadcastStatus <- controllerStatus(controller)
				}
			}
		}
	}()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	r.Get("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, controllerStatus(controller))
	})

	r.Post("/api/start", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Settings GameSettingsDTO `json:"settings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
			return
		}
		settings := settingsFromDTO(payload.Settings, DefaultGameSettings())
		searchBacklogManager.RequestStop()
		controller.StartGame(settings)
		writeJSON(w, http.StatusOK, controllerStatus(controller))
		hub.broadcastReset <- resetFromController(controller)
	})

	r.Post("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		settings := controller.Settings()
		searchBacklogManager.RequestStop()
		controller.Reset(settings)
		writeJSON(w, http.StatusOK, controllerStatus(controller))
		hub.broadcastReset <- resetFromController(controller)
	})

	r.Post("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Settings *GameSettingsDTO `json:"settings"`
			Config   *Config          `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
			return
		}
		if payload.Config != nil {
			configStore.Update(*payload.Config)
			controller.ResetForConfigChange()
			FlushGlobalCaches()
		}
		if payload.Settings != nil {
			settings := settingsFromDTO(*payload.Settings, controller.Settings())
			controller.UpdateSettings(settings, false)
		}
		hub.broadcastSettings <- settingsPayload{
			Settings: controllerSettingsDTO(controller.Settings()),
			Config:   GetConfig(),
		}
		writeJSON(w, http.StatusOK, controllerStatus(controller))
	})

	r.Post("/api/move", func(w http.ResponseWriter, r *http.Request) {
		var payload apiMove
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
			return
		}
		applied, errMsg := controller.ApplyHumanMove(Move{X: payload.X, Y: payload.Y})
		if !applied {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
			return
		}
		searchBacklogManager.RequestStop()
		if entry, ok := controller.LatestHistoryEntry(); ok {
			hub.broadcastHistory <- historyPayload{History: []historyEntryDTO{historyEntryToDTO(entry)}}
		}
		hub.broadcastStatus <- controllerStatus(controller)
		writeJSON(w, http.StatusOK, controllerStatus(controller))
	})

	r.Post("/api/analyse", func(w http.ResponseWriter, r *http.Request) {
		handleAnalyseRequest(w, r)
	})
	r.Get("/api/analitics/queue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, analiticsQueueResponse{
			Queue:        searchBacklogManager.TopAnaliticsQueue(analiticsTopBoardsLimit()),
			TotalInQueue: searchBacklogManager.TotalAnaliticsQueue(),
		})
	})
	r.Get("/api/cache/tt", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ttCacheStatus())
	})

	r.Get("/ws/", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, controller, w, r)
	})
	r.Get("/ws/ghost", func(w http.ResponseWriter, r *http.Request) {
		serveGhostWS(ghostHub, w, r)
	})
	r.Get("/ws/analitics", func(w http.ResponseWriter, r *http.Request) {
		serveAnaliticsWS(analiticsHub, w, r)
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}
	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
		close(serverErrCh)
	}()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	log.Println("backend listening on :8080")
	var runErr error
	select {
	case <-sigCtx.Done():
		log.Printf("[backend] shutdown signal received: %v", sigCtx.Err())
	case err, ok := <-serverErrCh:
		if ok {
			runErr = err
			log.Printf("[backend] server error: %v", err)
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("[backend] graceful shutdown failed: %v", err)
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			log.Printf("[backend] forced close failed: %v", closeErr)
		}
	}

	cancel()
	searchBacklogManager.RequestStop()
	persistOnShutdown("shutdown")
	if runErr != nil {
		log.Printf("[backend] exiting after server error: %v", runErr)
	}
}

func serveWS(hub *Hub, controller *GameController, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &Client{hub: hub, send: make(chan []byte, 16)}
	hub.Register(client)

	status := controllerStatus(controller)
	client.sendJSON(wsMessage{Type: "status", Payload: mustMarshal(status)})

	go func() {
		defer conn.Close()
		if err := writeWSWithHeartbeat(conn, client.send); err != nil {
			return
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			hub.Unregister(client)
			return
		}
		var msg wsMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "request_status":
			status := controllerStatus(controller)
			client.sendJSON(wsMessage{Type: "status", Payload: mustMarshal(status)})
		}
	}
}

func controllerStatus(controller *GameController) StatusResponse {
	state := controller.State()
	settings := controllerSettingsDTO(controller.Settings())
	gameSettings := controller.Settings()
	return StatusResponse{
		Settings:           settings,
		Config:             GetConfig(),
		NextPlayer:         playerToInt(state.ToMove),
		Winner:             winnerFromStatus(state.Status),
		BoardSize:          state.Board.Size(),
		Status:             statusToString(state.Status),
		History:            historyToDTO(controller.History()),
		WinReason:          winReasonFromState(state),
		WinningLine:        append([]Move(nil), state.WinningLine...),
		WinningCapturePair: append([]Move(nil), state.WinningCapturePair...),
		CaptureWinStones:   gameSettings.CaptureWinStones,
		TurnStartedAtMs:    controller.CurrentTurnStartedAtMs(),
	}
}

func winReasonFromState(state GameState) string {
	if winnerFromStatus(state.Status) == 0 {
		return ""
	}
	if len(state.WinningLine) > 0 {
		return "alignment"
	}
	return "capture"
}

func settingsFromDTO(dto GameSettingsDTO, base GameSettings) GameSettings {
	settings := base
	switch dto.Mode {
	case "ai_vs_ai":
		settings.BlackType = PlayerAI
		settings.WhiteType = PlayerAI
	case "human_vs_human":
		settings.BlackType = PlayerHuman
		settings.WhiteType = PlayerHuman
	case "ai_vs_human":
		if dto.HumanPlayer == 2 {
			settings.BlackType = PlayerAI
			settings.WhiteType = PlayerHuman
		} else {
			settings.BlackType = PlayerHuman
			settings.WhiteType = PlayerAI
		}
	}
	return settings
}

func controllerSettingsDTO(settings GameSettings) GameSettingsDTO {
	mode := "ai_vs_human"
	if settings.BlackType == PlayerAI && settings.WhiteType == PlayerAI {
		mode = "ai_vs_ai"
	} else if settings.BlackType == PlayerHuman && settings.WhiteType == PlayerHuman {
		mode = "human_vs_human"
	} else if settings.BlackType != settings.WhiteType {
		mode = "ai_vs_human"
	}
	humanPlayer := 0
	if settings.BlackType == PlayerHuman && settings.WhiteType != PlayerHuman {
		humanPlayer = 1
	} else if settings.WhiteType == PlayerHuman && settings.BlackType != PlayerHuman {
		humanPlayer = 2
	} else if settings.BlackType == PlayerHuman && settings.WhiteType == PlayerHuman {
		humanPlayer = 1
	}
	return GameSettingsDTO{Mode: mode, HumanPlayer: humanPlayer}
}

func boardToSlice(board Board) [][]int {
	size := board.Size()
	rows := make([][]int, size)
	for y := 0; y < size; y++ {
		rows[y] = make([]int, size)
		for x := 0; x < size; x++ {
			cell := board.At(x, y)
			rows[y][x] = cellToInt(cell)
		}
	}
	return rows
}

func cellToInt(cell Cell) int {
	switch cell {
	case CellBlack:
		return 1
	case CellWhite:
		return 2
	default:
		return 0
	}
}

func intToCell(value int) Cell {
	switch value {
	case 1:
		return CellBlack
	case 2:
		return CellWhite
	default:
		return CellEmpty
	}
}

func playerToInt(player PlayerColor) int {
	if player == PlayerBlack {
		return 1
	}
	return 2
}

func intToPlayer(value int) PlayerColor {
	if value == 2 {
		return PlayerWhite
	}
	return PlayerBlack
}

func winnerFromStatus(status GameStatus) int {
	switch status {
	case StatusBlackWon:
		return 1
	case StatusWhiteWon:
		return 2
	default:
		return 0
	}
}

func statusToString(status GameStatus) string {
	switch status {
	case StatusNotStarted:
		return "not_started"
	case StatusBlackWon:
		return "black_won"
	case StatusWhiteWon:
		return "white_won"
	case StatusDraw:
		return "draw"
	default:
		return "running"
	}
}

func historyToDTO(history MoveHistory) []historyEntryDTO {
	entries := history.All()
	result := make([]historyEntryDTO, 0, len(entries))
	for _, entry := range entries {
		result = append(result, historyEntryToDTO(entry))
	}
	return result
}

func handleAnalyseRequest(w http.ResponseWriter, r *http.Request) {
	var payload analyseRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	boardSize := len(payload.Board)
	if boardSize == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "board required"})
		return
	}
	if boardSize < 3 || boardSize > 25 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "board size must be 3-25"})
		return
	}
	for _, row := range payload.Board {
		if len(row) != boardSize {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "board must be square"})
			return
		}
	}

	if payload.Depth <= 0 {
		payload.Depth = 1
	}
	config := GetConfig()
	maxDepth := config.AiMaxDepth
	if maxDepth <= 0 {
		maxDepth = payload.Depth
	}
	if payload.Depth > maxDepth {
		payload.Depth = maxDepth
	}

	settings := DefaultGameSettings()
	settings.BoardSize = boardSize
	state := GameState{Board: NewBoard(boardSize)}
	for y, row := range payload.Board {
		for x, value := range row {
			state.Board.Set(x, y, intToCell(value))
		}
	}
	state.ToMove = intToPlayer(payload.NextPlayer)
	state.Status = StatusRunning
	state.HasLastMove = false
	state.LastMove = Move{X: -1, Y: -1}
	state.CapturedBlack = 0
	state.CapturedWhite = 0
	state.MustCapture = false
	state.ForcedCaptureMoves = nil
	state.LastMessage = ""
	state.WinningLine = nil
	state.WinningCapturePair = nil
	state.recomputeHashes()

	rules := NewRules(settings)
	stats := &SearchStats{Start: time.Now()}
	config.AiDepth = payload.Depth
	if !payload.UseTempCache {
		unlock := lockDefaultCache()
		defer unlock()
	}
	cache := SharedSearchCache()
	var tempCache AISearchCache
	if payload.UseTempCache {
		tempCache = newAISearchCache()
		cache = &tempCache
	}
	analysisSettings := AIScoreSettings{
		Depth:           payload.Depth,
		TimeoutMs:       0,
		BoardSize:       boardSize,
		Player:          state.ToMove,
		Cache:           cache,
		Config:          config,
		Stats:           stats,
		DirectDepthOnly: true,
	}
	scores := ScoreBoard(state, rules, analysisSettings)
	bestMove, ok := bestMoveFromScores(scores, state, rules, analysisSettings.BoardSize)
	if ok {
		if lostModeMove, changed := maybeSelectLostModeMove(scores, state, rules, analysisSettings, bestMove); changed {
			bestMove = lostModeMove
		}
	}
	best := analyseMove{}
	if ok {
		score := scores[bestMove.Y*analysisSettings.BoardSize+bestMove.X]
		best = analyseMove{
			X:     bestMove.X,
			Y:     bestMove.Y,
			Score: score,
			Depth: bestMove.Depth,
			Valid: true,
		}
	}
	durationMs := time.Since(stats.Start).Milliseconds()
	avgHeuristic := 0.0
	if stats.HeuristicCalls > 0 {
		avgHeuristic = float64(stats.HeuristicTime.Milliseconds()) / float64(stats.HeuristicCalls)
	}
	response := analyseResponse{
		BestMove:        best,
		DepthUsed:       stats.CompletedDepths,
		DurationMs:      durationMs,
		Nodes:           stats.Nodes,
		HeuristicCalls:  stats.HeuristicCalls,
		HeuristicTimeMs: stats.HeuristicTime.Milliseconds(),
		AvgHeuristicMs:  avgHeuristic,
		BoardGenTimeMs:  stats.BoardGenTime.Milliseconds(),
		BoardGenOps:     stats.BoardGenOps,
		TTProbes:        stats.TTProbes,
		TTHits:          stats.TTHits,
		TTExactHits:     stats.TTExactHits,
		TTLowerHits:     stats.TTLowerHits,
		TTUpperHits:     stats.TTUpperHits,
		TTStores:        stats.TTStores,
		TTOverwrites:    stats.TTOverwrites,
		TTReplacements:  stats.TTReplacements,
		Cutoffs:         stats.Cutoffs,
		TTCutoffs:       stats.TTCutoffs,
		ABCutoffs:       stats.ABCutoffs,
	}
	writeJSON(w, http.StatusOK, response)
}

func ttCacheStatus() ttCacheStatusResponse {
	config := GetConfig()
	cache := SharedSearchCache()
	tt := ensureTT(cache, config)
	if tt == nil {
		return ttCacheStatusResponse{}
	}
	count := tt.Count()
	capacity := tt.Capacity()
	usage := 0.0
	full := false
	if capacity > 0 {
		usage = float64(count) / float64(capacity)
		full = count >= capacity
	}
	return ttCacheStatusResponse{
		Count:    count,
		Capacity: capacity,
		Usage:    usage,
		Full:     full,
	}
}

func historyEntryToDTO(entry HistoryEntry) historyEntryDTO {
	return historyEntryDTO{
		X:                 entry.Move.X,
		Y:                 entry.Move.Y,
		Player:            playerToInt(entry.Player),
		ElapsedMs:         entry.ElapsedMs,
		IsAi:              entry.IsAi,
		CapturedCount:     entry.CapturedCount,
		CapturedPositions: append([]Move(nil), entry.CapturedPositions...),
		Changes:           changesFromEntry(entry),
		Depth:             entry.Depth,
	}
}

func changesFromEntry(entry HistoryEntry) []cellChange {
	changes := []cellChange{{
		X:     entry.Move.X,
		Y:     entry.Move.Y,
		Value: playerToInt(entry.Player),
	}}
	for _, captured := range entry.CapturedPositions {
		changes = append(changes, cellChange{
			X:     captured.X,
			Y:     captured.Y,
			Value: 0,
		})
	}
	return changes
}

func resetFromController(controller *GameController) resetPayload {
	state := controller.State()
	settings := controller.Settings()
	return resetPayload{
		History:            historyToDTO(controller.History()),
		NextPlayer:         playerToInt(state.ToMove),
		Winner:             winnerFromStatus(state.Status),
		Status:             statusToString(state.Status),
		BoardSize:          state.Board.Size(),
		WinReason:          winReasonFromState(state),
		WinningLine:        append([]Move(nil), state.WinningLine...),
		WinningCapturePair: append([]Move(nil), state.WinningCapturePair...),
		CaptureWinStones:   settings.CaptureWinStones,
		TurnStartedAtMs:    controller.CurrentTurnStartedAtMs(),
	}
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
