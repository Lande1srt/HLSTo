package websocket

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type WebSocketHandler struct {
	wsManager *WebSocketManager
}

func NewWebSocketHandler(wsManager *WebSocketManager) *WebSocketHandler {
	return &WebSocketHandler{
		wsManager: wsManager,
	}
}

func (h *WebSocketHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("taskId")
	// taskId can be empty for global updates (task list page)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		conn:   conn,
		taskID: taskID,
		send:   make(chan []byte, 256),
	}

	h.wsManager.Register(client)

	go client.writePump()
	go client.readPump(h.wsManager)
}
