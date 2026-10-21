package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fastOptions() Options {
	opts := DefaultOptions()
	opts.WriteWait = time.Second
	return opts
}

func newTestServer(t *testing.T, h *Hub) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		scope := Public
		if r.URL.Query().Get("scope") == "private" {
			scope = Private
		}
		h.Serve(conn, scope, r.URL.Query().Get("channel"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func scopeName(scope Scope) string {
	if scope == Private {
		return "private"
	}
	return "public"
}

func dial(t *testing.T, srv *httptest.Server, scope Scope, channel string) *websocket.Conn {
	t.Helper()
	query := url.Values{"scope": {scopeName(scope)}, "channel": {channel}}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?" + query.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func subscribeN(t *testing.T, h *Hub, srv *httptest.Server, scope Scope, channel string, n int) []*websocket.Conn {
	t.Helper()
	before := h.Subscribers(scope, channel)
	conns := make([]*websocket.Conn, n)
	for i := range conns {
		conns[i] = dial(t, srv, scope, channel)
	}
	waitFor(t, "subscribers registered", func() bool { return h.Subscribers(scope, channel) == before+n })
	return conns
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readText(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != websocket.TextMessage {
		t.Fatalf("message type = %d, want text", kind)
	}
	return string(data)
}

func expectSilence(t *testing.T, conn *websocket.Conn, wait time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	if _, data, err := conn.ReadMessage(); err == nil {
		t.Fatalf("unexpected message %q", data)
	}
}

func expectClose(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		if !websocket.IsCloseError(err, code) {
			t.Fatalf("read error = %v, want close code %d", err, code)
		}
		return
	}
}

func fakeClient(scope Scope, channel string, maxBytes int) *Client {
	return newClient(nil, topic{scope: scope, channel: channel}, maxBytes)
}

func queued(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

func drain(c *Client) {
	c.release(c.takeQueue())
}

func isClosed(c *Client) bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
