package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

type StatusResponse struct {
	Settings   GameSettingsDTO   `json:"settings"`
	Config     Config            `json:"config"`
	NextPlayer int               `json:"next_player"`
	Winner     int               `json:"winner"`
	BoardSize  int               `json:"board_size"`
	Status     string            `json:"status"`
	History    []historyEntryDTO `json:"history"`
}

type GameSettingsDTO struct {
	Mode string `json:"mode"`
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
}

type changesPayload struct {
	Changes []cellChange `json:"changes"`
}

type historyPayload struct {
	History []historyEntryDTO `json:"history"`
}

type resetPayload struct {
	History    []historyEntryDTO `json:"history"`
	NextPlayer int               `json:"next_player"`
	Winner     int               `json:"winner"`
	Status     string            `json:"status"`
	BoardSize  int               `json:"board_size"`
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

func main() {
	controller := NewGameController(DefaultGameSettings())
	hub := NewHub()
	ghostHub := NewGhostHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controller.SetGhostPublisher(
		func() bool { return ghostHub.HasClients() && GetConfig().GhostMode },
		func(board Board) {
			positions := ghostPositionsFromBoard(board)
			ghostHub.broadcast <- ghostPayload{Positions: positions}
		},
	)

	go hub.Run(ctx.Done())
	go ghostHub.Run(ctx.Done())
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
		controller.StartGame(settings)
		writeJSON(w, http.StatusOK, controllerStatus(controller))
		hub.broadcastReset <- resetFromController(controller)
	})

	r.Post("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		settings := controller.Settings()
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
		if entry, ok := controller.LatestHistoryEntry(); ok {
			hub.broadcastHistory <- historyPayload{History: []historyEntryDTO{historyEntryToDTO(entry)}}
		}
		writeJSON(w, http.StatusOK, controllerStatus(controller))
	})

	r.Get("/ws/", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, controller, w, r)
	})
	r.Get("/ws/ghost", func(w http.ResponseWriter, r *http.Request) {
		serveGhostWS(ghostHub, w, r)
	})

	log.Println("backend listening on :8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		log.Fatal(err)
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
		for msg := range client.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
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
	return StatusResponse{
		Settings:   settings,
		Config:     GetConfig(),
		NextPlayer: playerToInt(state.ToMove),
		Winner:     winnerFromStatus(state.Status),
		BoardSize:  state.Board.Size(),
		Status:     statusToString(state.Status),
		History:    historyToDTO(controller.History()),
	}
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
		settings.BlackType = PlayerHuman
		settings.WhiteType = PlayerAI
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
	return GameSettingsDTO{Mode: mode}
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

func playerToInt(player PlayerColor) int {
	if player == PlayerBlack {
		return 1
	}
	return 2
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
	return resetPayload{
		History:    historyToDTO(controller.History()),
		NextPlayer: playerToInt(state.ToMove),
		Winner:     winnerFromStatus(state.Status),
		Status:     statusToString(state.Status),
		BoardSize:  state.Board.Size(),
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
