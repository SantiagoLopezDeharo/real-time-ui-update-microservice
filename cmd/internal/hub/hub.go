package hub

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	Conn *websocket.Conn
	Send chan []byte
	// Authenticated indicates whether this client connected via the authenticated WS endpoint
	Authenticated bool
	// Channel is the logical channel the client is subscribed to
	Channel string
}

type Hub struct {
	// Separate client collections keyed by channel name
	authChannels   map[string]map[*Client]bool
	publicChannels map[string]map[*Client]bool

	register   chan *Client
	unregister chan *Client

	// Separate broadcast channels
	broadcastAuth   chan broadcastMessage
	broadcastPublic chan broadcastMessage

	mu sync.RWMutex
}

type broadcastMessage struct {
	Channel string
	Message []byte
}

func NewHub() *Hub {
	return &Hub{
		authChannels:    make(map[string]map[*Client]bool),
		publicChannels:  make(map[string]map[*Client]bool),
		register:        make(chan *Client),
		unregister:      make(chan *Client),
		broadcastAuth:   make(chan broadcastMessage),
		broadcastPublic: make(chan broadcastMessage),
	}
}

func NewClient(conn *websocket.Conn) *Client {
	return &Client{
		Conn: conn,
		Send: make(chan []byte, 256),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if client.Authenticated {
				m, ok := h.authChannels[client.Channel]
				if !ok {
					m = make(map[*Client]bool)
					h.authChannels[client.Channel] = m
				}
				m[client] = true
			} else {
				m, ok := h.publicChannels[client.Channel]
				if !ok {
					m = make(map[*Client]bool)
					h.publicChannels[client.Channel] = m
				}
				m[client] = true
			}
			h.mu.Unlock()
			log.Printf("Client registered. Authenticated: %t, Channel: %s", client.Authenticated, client.Channel)

		case client := <-h.unregister:
			h.mu.Lock()
			if client.Authenticated {
				if m, ok := h.authChannels[client.Channel]; ok {
					if _, ok2 := m[client]; ok2 {
						delete(m, client)
						close(client.Send)
						if len(m) == 0 {
							delete(h.authChannels, client.Channel)
						}
					}
				}
			} else {
				if m, ok := h.publicChannels[client.Channel]; ok {
					if _, ok2 := m[client]; ok2 {
						delete(m, client)
						close(client.Send)
						if len(m) == 0 {
							delete(h.publicChannels, client.Channel)
						}
					}
				}
			}
			h.mu.Unlock()
			log.Printf("Client unregistered. Authenticated: %t, Channel: %s", client.Authenticated, client.Channel)

		case bm := <-h.broadcastAuth:
			h.mu.RLock()
			clientsMap := h.authChannels[bm.Channel]
			clients := make([]*Client, 0, len(clientsMap))
			for client := range clientsMap {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			for _, client := range clients {
				select {
				case client.Send <- bm.Message:
					// sent
				default:
					go func(c *Client) {
						select {
						case c.Send <- bm.Message:
							// sent
						case <-time.After(5 * time.Second):
							h.unregister <- c
							c.Conn.Close()
							log.Println("Disconnected slow client")
						}
					}(client)
				}
			}

		case bm := <-h.broadcastPublic:
			h.mu.RLock()
			clientsMap := h.publicChannels[bm.Channel]
			clients := make([]*Client, 0, len(clientsMap))
			for client := range clientsMap {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			for _, client := range clients {
				select {
				case client.Send <- bm.Message:
					// sent
				default:
					go func(c *Client) {
						select {
						case c.Send <- bm.Message:
							// sent
						case <-time.After(5 * time.Second):
							h.unregister <- c
							c.Conn.Close()
							log.Println("Disconnected slow client")
						}
					}(client)
				}
			}
		}
	}
}

func (h *Hub) RegisterClient(client *Client) {
	h.register <- client
}

func (h *Hub) UnregisterClient(client *Client) {
	h.unregister <- client
}

// BroadcastToAuthenticated sends a message to authenticated websocket clients only
// BroadcastToAuthenticated sends a message to authenticated websocket clients for a channel
func (h *Hub) BroadcastToAuthenticated(channel string, message []byte) {
	h.broadcastAuth <- broadcastMessage{Channel: channel, Message: message}
}

// BroadcastToPublic sends a message to public websocket clients for a channel
func (h *Hub) BroadcastToPublic(channel string, message []byte) {
	h.broadcastPublic <- broadcastMessage{Channel: channel, Message: message}
}

// Exported pump methods so other packages (handlers) can start them:
func (c *Client) WritePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// channel closed
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				_ = w.Close()
				return
			}

			// Add queued messages to this websocket message
			n := len(c.Send)
			for i := 0; i < n; i++ {
				if _, err := w.Write(<-c.Send); err != nil {
					_ = w.Close()
					return
				}
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) ReadPump(h *Hub) {
	defer func() {
		h.UnregisterClient(c)
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(512)
	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		log.Printf("Received message: %s", string(message))
	}
}
