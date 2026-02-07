package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type analiticsQueueEntryDTO struct {
	ID                  string  `json:"id"`
	Board               [][]int `json:"board"`
	CurrentDepth        int     `json:"current_depth"`
	TargetDepth         int     `json:"target_depth"`
	Hits                int     `json:"hits"`
	Analyzing           bool    `json:"analyzing"`
	AnalysisStartedAtMs int64   `json:"analysis_started_at_ms"`
}

type analiticsQueueResponse struct {
	Queue        []analiticsQueueEntryDTO `json:"queue"`
	TotalInQueue int                      `json:"total_in_queue"`
}

type analiticsPayload struct {
	Event        string                    `json:"event"`
	Entry        *analiticsQueueEventEntry `json:"entry,omitempty"`
	TotalInQueue int                       `json:"total_in_queue"`
	UpdatedAt    int64                     `json:"updated_at_ms"`
}

type analiticsQueueEventEntry struct {
	ID                  string `json:"id"`
	CurrentDepth        int    `json:"current_depth"`
	TargetDepth         int    `json:"target_depth"`
	Hits                int    `json:"hits"`
	Analyzing           bool   `json:"analyzing"`
	AnalysisStartedAtMs int64  `json:"analysis_started_at_ms"`
}

type backlogAnalyticsEntry struct {
	Hash                uint64
	Board               Board
	Stones              int
	Created             time.Time
	Hits                int
	CurrentDepth        int
	TargetDepth         int
	Analyzing           bool
	AnalysisStartedAtMs int64
}

type AnaliticsClient struct {
	hub  *AnaliticsHub
	conn *websocket.Conn
	send chan []byte
}

type AnaliticsHub struct {
	mu        sync.Mutex
	clients   map[*AnaliticsClient]struct{}
	broadcast chan analiticsPayload
}

func NewAnaliticsHub() *AnaliticsHub {
	return &AnaliticsHub{
		clients:   make(map[*AnaliticsClient]struct{}),
		broadcast: make(chan analiticsPayload, 64),
	}
}

func (h *AnaliticsHub) Run(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case payload := <-h.broadcast:
			h.mu.Lock()
			if len(h.clients) == 0 {
				h.mu.Unlock()
				continue
			}
			for client := range h.clients {
				client.sendJSON(wsMessage{Type: "analitics", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		}
	}
}

func (h *AnaliticsHub) Publish(payload analiticsPayload) {
	select {
	case h.broadcast <- payload:
	default:
	}
}

func (h *AnaliticsHub) Register(c *AnaliticsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *AnaliticsHub) Unregister(c *AnaliticsClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

func (c *AnaliticsClient) sendJSON(msg wsMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

func serveAnaliticsWS(hub *AnaliticsHub, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &AnaliticsClient{hub: hub, conn: conn, send: make(chan []byte, 16)}
	hub.Register(client)

	initial := analiticsPayload{
		Event:        "snapshot",
		TotalInQueue: searchBacklogManager.TotalAnaliticsQueue(),
		UpdatedAt:    time.Now().UnixMilli(),
	}
	client.sendJSON(wsMessage{Type: "analitics", Payload: mustMarshal(initial)})

	go func() {
		defer conn.Close()
		if err := writeWSWithHeartbeat(conn, client.send); err != nil {
			return
		}
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			hub.Unregister(client)
			return
		}
	}
}

func hashToBoardID(hash uint64) string {
	return "0x" + strconv.FormatUint(hash, 16)
}

func boardToIntGrid(board Board) [][]int {
	size := board.Size()
	result := make([][]int, size)
	for y := 0; y < size; y++ {
		row := make([]int, size)
		for x := 0; x < size; x++ {
			row[x] = cellToInt(board.At(x, y))
		}
		result[y] = row
	}
	return result
}

func analiticsEntryToDTO(entry backlogAnalyticsEntry) analiticsQueueEntryDTO {
	return analiticsQueueEntryDTO{
		ID:                  hashToBoardID(entry.Hash),
		Board:               boardToIntGrid(entry.Board),
		CurrentDepth:        entry.CurrentDepth,
		TargetDepth:         entry.TargetDepth,
		Hits:                entry.Hits,
		Analyzing:           entry.Analyzing,
		AnalysisStartedAtMs: entry.AnalysisStartedAtMs,
	}
}

func analiticsEntryToEventEntry(entry backlogAnalyticsEntry) analiticsQueueEventEntry {
	return analiticsQueueEventEntry{
		ID:                  hashToBoardID(entry.Hash),
		CurrentDepth:        entry.CurrentDepth,
		TargetDepth:         entry.TargetDepth,
		Hits:                entry.Hits,
		Analyzing:           entry.Analyzing,
		AnalysisStartedAtMs: entry.AnalysisStartedAtMs,
	}
}

func sortAnaliticsQueue(entries []backlogAnalyticsEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return compareAnaliticsPriority(entries[i], entries[j]) < 0
	})
}

func compareAnaliticsPriority(a, b backlogAnalyticsEntry) int {
	if a.Hits != b.Hits {
		if a.Hits > b.Hits {
			return -1
		}
		return 1
	}
	if a.Stones != b.Stones {
		if a.Stones > b.Stones {
			return -1
		}
		return 1
	}
	remainingA := analiticsRemainingDepth(a)
	remainingB := analiticsRemainingDepth(b)
	if remainingA != remainingB {
		if remainingA > remainingB {
			return -1
		}
		return 1
	}
	if !a.Created.Equal(b.Created) {
		if a.Created.Before(b.Created) {
			return -1
		}
		return 1
	}
	if a.Hash < b.Hash {
		return -1
	}
	if a.Hash > b.Hash {
		return 1
	}
	return 0
}

func analiticsRemainingDepth(entry backlogAnalyticsEntry) int {
	remaining := entry.TargetDepth - entry.CurrentDepth
	if remaining < 0 {
		return 0
	}
	return remaining
}

func analiticsTopBoardsLimit() int {
	limit := GetConfig().AiAnaliticsTopBoards
	if limit <= 0 {
		return 10
	}
	return limit
}
