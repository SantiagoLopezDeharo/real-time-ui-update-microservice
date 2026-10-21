package hub

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Options struct {
	MaxQueuedBytes int
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	ReadLimit      int64
}

func DefaultOptions() Options {
	return Options{
		MaxQueuedBytes: 4 << 20,
		WriteWait:      10 * time.Second,
		PongWait:       60 * time.Second,
		PingPeriod:     30 * time.Second,
		ReadLimit:      512,
	}
}

var (
	closeNormal   = websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	closeSlow     = websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "slow consumer")
	closeShutdown = websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down")
)

type outbound struct {
	payload []byte
}

// Client buffers outbound messages in a queue bounded by payload bytes, so a
// burst never evicts a healthy reader while a stalled one cannot pin unbounded
// memory. Idle clients hold no buffer at all.
type Client struct {
	conn     *websocket.Conn
	topic    topic
	maxBytes int

	mu          sync.Mutex
	queue       []outbound
	spare       []outbound
	queuedBytes int
	wake        chan struct{}

	closeOnce  sync.Once
	closeFrame []byte
	done       chan struct{}
}

func newClient(conn *websocket.Conn, t topic, maxBytes int) *Client {
	return &Client{
		conn:     conn,
		topic:    t,
		maxBytes: maxBytes,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// Serve subscribes conn to the channel and starts its pumps. It returns
// immediately so the HTTP handler can exit and release the request state.
func (h *Hub) Serve(conn *websocket.Conn, scope Scope, channel string) {
	c := newClient(conn, topic{scope: scope, channel: channel}, h.opts.MaxQueuedBytes)
	if !h.subscribe(c) {
		_ = conn.WriteControl(websocket.CloseMessage, closeShutdown, time.Now().Add(h.opts.WriteWait))
		_ = conn.Close()
		return
	}

	go func() {
		defer h.pumps.Done()
		c.writePump(h.opts)
	}()
	go func() {
		c.readPump(h.opts)
		h.unsubscribe(c)
		c.close(closeNormal)
	}()
}

// enqueue reports false when the payload would exceed the queue budget. A
// payload larger than the budget is still accepted into an empty queue.
func (c *Client) enqueue(payload []byte) bool {
	c.mu.Lock()
	if len(c.queue) > 0 && c.queuedBytes+len(payload) > c.maxBytes {
		c.mu.Unlock()
		return false
	}
	c.queue = append(c.queue, outbound{payload: payload})
	c.queuedBytes += len(payload)
	c.mu.Unlock()

	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

func (c *Client) takeQueue() []outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	batch := c.queue
	c.queue = c.spare[:0]
	return batch
}

func (c *Client) release(batch []outbound) {
	written := 0
	for _, out := range batch {
		written += len(out.payload)
	}
	clear(batch)
	c.mu.Lock()
	c.queuedBytes -= written
	c.spare = batch[:0]
	c.mu.Unlock()
}

func (c *Client) close(frame []byte) {
	c.closeOnce.Do(func() {
		c.closeFrame = frame
		close(c.done)
	})
}

func (c *Client) writePump(opts Options) {
	ticker := time.NewTicker(opts.PingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case <-c.wake:
			batch := c.takeQueue()
			for _, out := range batch {
				_ = c.conn.SetWriteDeadline(time.Now().Add(opts.WriteWait))
				if err := c.conn.WriteMessage(websocket.TextMessage, out.payload); err != nil {
					return
				}
			}
			c.release(batch)
		case <-ticker.C:
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(opts.WriteWait)); err != nil {
				return
			}
		case <-c.done:
			_ = c.conn.WriteControl(websocket.CloseMessage, c.closeFrame, time.Now().Add(opts.WriteWait))
			return
		}
	}
}

// readPump discards inbound data; it exists to process control frames and to
// detect dead peers through the pong deadline.
func (c *Client) readPump(opts Options) {
	c.conn.SetReadLimit(opts.ReadLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(opts.PongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(opts.PongWait))
	})

	for {
		if _, _, err := c.conn.NextReader(); err != nil {
			return
		}
	}
}
