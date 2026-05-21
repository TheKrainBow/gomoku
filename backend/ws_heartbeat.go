package main

import (
	"time"

	"github.com/gorilla/websocket"
)

const wsIdlePingInterval = 30 * time.Second

func writeWSWithHeartbeat(conn *websocket.Conn, send <-chan []byte) error {
	ticker := time.NewTicker(wsIdlePingInterval)
	defer ticker.Stop()
	lastWrite := time.Now()
	pingPayload := mustMarshal(wsMessage{Type: "ping"})

	for {
		select {
		case msg, ok := <-send:
			if !ok {
				return nil
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return err
			}
			lastWrite = time.Now()
		case <-ticker.C:
			if time.Since(lastWrite) < wsIdlePingInterval {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, pingPayload); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
	}
}
