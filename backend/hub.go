package main

import (
	"encoding/json"
	"sync"
)

type Hub struct {
	mu                sync.Mutex
	clients           map[*Client]struct{}
	broadcastBoard    chan boardPayload
	broadcastHistory  chan historyPayload
	broadcastStatus   chan StatusResponse
	broadcastReset    chan resetPayload
	broadcastSettings chan settingsPayload
}

type Client struct {
	hub  *Hub
	send chan []byte
}

type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type boardPayload struct {
	Board      [][]int           `json:"board"`
	NextPlayer int               `json:"next_player"`
	Winner     int               `json:"winner"`
	MoveCount  int               `json:"move_count"`
	Status     string            `json:"status"`
	AiThinking bool              `json:"ai_thinking"`
	History    []historyEntryDTO `json:"history"`
}

func NewHub() *Hub {
	return &Hub{
		clients:           make(map[*Client]struct{}),
		broadcastBoard:    make(chan boardPayload, 16),
		broadcastHistory:  make(chan historyPayload, 32),
		broadcastStatus:   make(chan StatusResponse, 32),
		broadcastReset:    make(chan resetPayload, 8),
		broadcastSettings: make(chan settingsPayload, 8),
	}
}

func (h *Hub) Run(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case payload := <-h.broadcastBoard:
			h.mu.Lock()
			for client := range h.clients {
				client.sendJSON(wsMessage{Type: "board", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		case payload := <-h.broadcastHistory:
			h.mu.Lock()
			for client := range h.clients {
				client.sendJSON(wsMessage{Type: "history", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		case payload := <-h.broadcastStatus:
			h.mu.Lock()
			for client := range h.clients {
				client.sendJSON(wsMessage{Type: "status", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		case payload := <-h.broadcastReset:
			h.mu.Lock()
			for client := range h.clients {
				client.sendJSON(wsMessage{Type: "reset", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		case payload := <-h.broadcastSettings:
			h.mu.Lock()
			for client := range h.clients {
				client.sendJSON(wsMessage{Type: "settings", Payload: mustMarshal(payload)})
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

func (h *Hub) HasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

func (c *Client) sendJSON(msg wsMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}
