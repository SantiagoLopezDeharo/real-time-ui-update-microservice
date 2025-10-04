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
}

type Hub struct {
	// Separate client collections for authenticated and public connections
	authClients   map[*Client]bool
	publicClients map[*Client]bool

	register   chan *Client
	unregister chan *Client

	// Separate broadcast channels
	broadcastAuth   chan []byte
	broadcastPublic chan []byte

	mu sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		authClients:     make(map[*Client]bool),
		publicClients:   make(map[*Client]bool),
		register:        make(chan *Client),
		unregister:      make(chan *Client),
		broadcastAuth:   make(chan []byte),
		broadcastPublic: make(chan []byte),
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
				h.authClients[client] = true
			} else {
				h.publicClients[client] = true
			}
			h.mu.Unlock()
			log.Printf("Client registered. Authenticated: %t, Totals - auth: %d public: %d", client.Authenticated, len(h.authClients), len(h.publicClients))

		case client := <-h.unregister:
			h.mu.Lock()
			if client.Authenticated {
				if _, ok := h.authClients[client]; ok {
					delete(h.authClients, client)
					close(client.Send)
				}
			} else {
				if _, ok := h.publicClients[client]; ok {
					delete(h.publicClients, client)
					close(client.Send)
				}
			}
			h.mu.Unlock()
			log.Printf("Client unregistered. Authenticated: %t, Totals - auth: %d public: %d", client.Authenticated, len(h.authClients), len(h.publicClients))

		case message := <-h.broadcastAuth:
			h.mu.RLock()
			clients := make([]*Client, 0, len(h.authClients))
			for client := range h.authClients {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			for _, client := range clients {
				select {
				case client.Send <- message:
					// sent
				default:
					go func(c *Client) {
						select {
						case c.Send <- message:
							// sent
						case <-time.After(5 * time.Second):
							h.unregister <- c
							c.Conn.Close()
							log.Println("Disconnected slow client")
						}
					}(client)
				}
			}

		case message := <-h.broadcastPublic:
			h.mu.RLock()
			clients := make([]*Client, 0, len(h.publicClients))
			for client := range h.publicClients {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			for _, client := range clients {
				select {
				case client.Send <- message:
					// sent
				default:
					go func(c *Client) {
						select {
						case c.Send <- message:
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
func (h *Hub) BroadcastToAuthenticated(message []byte) {
	h.broadcastAuth <- message
}

// BroadcastToPublic sends a message to public websocket clients only
func (h *Hub) BroadcastToPublic(message []byte) {
	h.broadcastPublic <- message
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
