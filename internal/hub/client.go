package hub

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Options struct {
	SendBuffer int
	WriteWait  time.Duration
	PongWait   time.Duration
	PingPeriod time.Duration
	ReadLimit  int64
}

func DefaultOptions() Options {
	return Options{
		SendBuffer: 256,
		WriteWait:  10 * time.Second,
		PongWait:   60 * time.Second,
		PingPeriod: 30 * time.Second,
		ReadLimit:  512,
	}
}

var (
	closeNormal   = websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	closeSlow     = websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "slow consumer")
	closeShutdown = websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down")
)

type Client struct {
	conn  *websocket.Conn
	topic topic
	send  chan *websocket.PreparedMessage

	closeOnce  sync.Once
	closeFrame []byte
	done       chan struct{}
}

// Serve subscribes conn to the channel and pumps messages until the connection
// ends. It blocks, so it is meant to run on the HTTP handler goroutine.
func (h *Hub) Serve(conn *websocket.Conn, scope Scope, channel string) {
	c := &Client{
		conn:  conn,
		topic: topic{scope: scope, channel: channel},
		send:  make(chan *websocket.PreparedMessage, h.opts.SendBuffer),
		done:  make(chan struct{}),
	}
	if !h.subscribe(c) {
		_ = conn.WriteControl(websocket.CloseMessage, closeShutdown, time.Now().Add(h.opts.WriteWait))
		_ = conn.Close()
		return
	}

	go func() {
		defer h.pumps.Done()
		c.writePump(h.opts)
	}()
	c.readPump(h.opts)

	h.unsubscribe(c)
	c.close(closeNormal)
}

func (c *Client) enqueue(msg *websocket.PreparedMessage) bool {
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
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
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(opts.WriteWait))
			if err := c.conn.WritePreparedMessage(msg); err != nil {
				return
			}
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
