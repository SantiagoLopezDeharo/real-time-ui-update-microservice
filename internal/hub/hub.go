package hub

import (
	"context"
	"sync"

	"github.com/gorilla/websocket"
)

type Scope uint8

const (
	Public Scope = iota
	Private
)

type topic struct {
	scope   Scope
	channel string
}

// Hub fans messages out to the clients subscribed to a (scope, channel) pair.
// Broadcasts run on the caller's goroutine under a read lock, so concurrent
// publishers proceed in parallel and never wait on slow clients.
type Hub struct {
	opts Options

	mu     sync.RWMutex
	topics map[topic]map[*Client]struct{}
	closed bool

	pumps sync.WaitGroup
}

func New(opts Options) *Hub {
	return &Hub{
		opts:   opts,
		topics: make(map[topic]map[*Client]struct{}),
	}
}

func (h *Hub) subscribe(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	subs, ok := h.topics[c.topic]
	if !ok {
		subs = make(map[*Client]struct{})
		h.topics[c.topic] = subs
	}
	subs[c] = struct{}{}
	h.pumps.Add(1)
	return true
}

func (h *Hub) unsubscribe(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs, ok := h.topics[c.topic]
	if !ok {
		return
	}
	delete(subs, c)
	if len(subs) == 0 {
		delete(h.topics, c.topic)
	}
}

// Broadcast queues payload for every subscriber of the channel and returns how
// many clients accepted it. Clients whose send buffer is full are disconnected.
// The payload must not be modified after the call.
func (h *Hub) Broadcast(scope Scope, channel string, payload []byte) (int, error) {
	h.mu.RLock()
	subs := h.topics[topic{scope: scope, channel: channel}]
	if len(subs) == 0 {
		h.mu.RUnlock()
		return 0, nil
	}

	msg, err := websocket.NewPreparedMessage(websocket.TextMessage, payload)
	if err != nil {
		h.mu.RUnlock()
		return 0, err
	}

	var slow []*Client
	for c := range subs {
		if !c.enqueue(msg, len(payload)) {
			slow = append(slow, c)
		}
	}
	delivered := len(subs) - len(slow)
	h.mu.RUnlock()

	for _, c := range slow {
		c.close(closeSlow)
	}
	return delivered, nil
}

func (h *Hub) Subscribers(scope Scope, channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.topics[topic{scope: scope, channel: channel}])
}

// Shutdown rejects new clients, asks every connected client to close and waits
// for their close frames to be written or ctx to expire.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	var clients []*Client
	for _, subs := range h.topics {
		for c := range subs {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		c.close(closeShutdown)
	}

	done := make(chan struct{})
	go func() {
		h.pumps.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
