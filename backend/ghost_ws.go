package main

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type ghostCell struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Player int `json:"player"`
}

type ghostPayload struct {
	Mode       string      `json:"mode,omitempty"`
	Positions  []ghostCell `json:"positions,omitempty"`
	Best       *ghostCell  `json:"best,omitempty"`
	Depth      int         `json:"depth,omitempty"`
	Score      float64     `json:"score,omitempty"`
	NextPlayer int         `json:"next_player,omitempty"`
	HistoryLen int         `json:"history_len,omitempty"`
	Active     bool        `json:"active"`
	Final      bool        `json:"final,omitempty"`
}

type GhostClient struct {
	hub  *GhostHub
	conn *websocket.Conn
	send chan []byte
}

type GhostHub struct {
	mu        sync.Mutex
	clients   map[*GhostClient]struct{}
	broadcast chan ghostPayload
}

func NewGhostHub() *GhostHub {
	return &GhostHub{
		clients:   make(map[*GhostClient]struct{}),
		broadcast: make(chan ghostPayload, 32),
	}
}

func (h *GhostHub) Run(done <-chan struct{}) {
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
				client.sendJSON(wsMessage{Type: "ghost", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		}
	}
}

func (h *GhostHub) Register(c *GhostClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *GhostHub) Publish(payload ghostPayload) {
	select {
	case h.broadcast <- payload:
	default:
	}
}

func (h *GhostHub) Unregister(c *GhostClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

func (h *GhostHub) HasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

func (c *GhostClient) sendJSON(msg wsMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

func serveGhostWS(hub *GhostHub, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &GhostClient{hub: hub, conn: conn, send: make(chan []byte, 16)}
	hub.Register(client)

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

func ghostPositionsFromBoard(board Board) []ghostCell {
	positions := []ghostCell{}
	size := board.Size()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			cell := board.At(x, y)
			if cell == CellEmpty {
				continue
			}
			positions = append(positions, ghostCell{X: x, Y: y, Player: cellToInt(cell)})
		}
	}
	return positions
}
