package websocket

import (
	"encoding/json"
	"log"
	"m3u8-downloader-web/model"
	"sync"

	"github.com/gorilla/websocket"
)

type Client struct {
	conn   *websocket.Conn
	taskID string
	send   chan []byte
}

type WebSocketManager struct {
	clients map[*Client]bool
	mu      sync.RWMutex
}

func NewWebSocketManager() *WebSocketManager {
	return &WebSocketManager{
		clients: make(map[*Client]bool),
	}
}

func (wm *WebSocketManager) Register(client *Client) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.clients[client] = true
}

func (wm *WebSocketManager) Unregister(client *Client) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if _, ok := wm.clients[client]; ok {
		delete(wm.clients, client)
		close(client.send)
	}
}

func (wm *WebSocketManager) BroadcastToTask(taskID string, message model.WebSocketMessage) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return
	}

	for client := range wm.clients {
		if client.taskID == taskID || client.taskID == "" {
			select {
			case client.send <- data:
			default:
				go wm.closeClient(client)
			}
		}
	}
}

func (wm *WebSocketManager) closeClient(client *Client) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	delete(wm.clients, client)
	close(client.send)
	client.conn.Close()
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()

	for {
		message, ok := <-c.send
		if !ok {
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}

		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}
}

func (c *Client) readPump(wsManager *WebSocketManager) {
	defer func() {
		wsManager.Unregister(c)
		c.conn.Close()
	}()

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
