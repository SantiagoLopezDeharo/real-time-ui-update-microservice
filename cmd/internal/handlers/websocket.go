package handlers

import (
	"log"
	"net/http"

	"real-time-ui-update-microservice/cmd/internal/hub"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func HandleWebSocket(h *hub.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("WebSocket upgrade error:", err)
			return
		}

		client := hub.NewClient(conn)

		// Determine channel from query parameter (default to "default")
		ch := r.URL.Query().Get("channel")
		if ch == "" {
			ch = "default"
		}

		// Mark as authenticated since this handler is wrapped by JWT middleware
		client.Authenticated = true
		client.Channel = ch

		h.RegisterClient(client)

		// Start goroutines for reading and writing (exported methods on hub.Client)
		go client.WritePump()
		go client.ReadPump(h)
	}
}

// HandleWebSocketPublic returns a websocket handler that registers clients as public (unauthenticated)
func HandleWebSocketPublic(h *hub.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("WebSocket upgrade error:", err)
			return
		}

		client := hub.NewClient(conn)
		// Determine channel from query parameter (default to "default")
		ch := r.URL.Query().Get("channel")
		if ch == "" {
			ch = "default"
		}
		client.Authenticated = false
		client.Channel = ch
		h.RegisterClient(client)

		go client.WritePump()
		go client.ReadPump(h)
	}
}
