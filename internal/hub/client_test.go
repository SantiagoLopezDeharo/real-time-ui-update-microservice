package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestServeDeliversExactPayloads(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	conns := subscribeN(t, h, srv, Private, "orders", 5)

	payloads := []string{
		`{"id":"order-1","amount":10}`,
		`[1,2,3]`,
		`"plain string"`,
		`null`,
		`{"unicode":"日本語 – émoji 🚀","escaped":"\u0000\n\t"}`,
		`{"nested":{"deep":{"deeper":[{"x":true}]}}}`,
	}
	for _, p := range payloads {
		if n := h.Broadcast(Private, "orders", []byte(p)); n != len(conns) {
			t.Fatalf("Broadcast(%s) = %d", p, n)
		}
	}
	for i, conn := range conns {
		for _, want := range payloads {
			if got := readText(t, conn); got != want {
				t.Errorf("conn %d got %q, want %q", i, got, want)
			}
		}
	}
}

func TestServeIsolatesScopesAndChannels(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	privateOrders := subscribeN(t, h, srv, Private, "orders", 1)[0]
	publicOrders := subscribeN(t, h, srv, Public, "orders", 1)[0]
	privateNews := subscribeN(t, h, srv, Private, "news", 1)[0]

	mustBroadcast(t, h, Private, "orders", `{"to":"private-orders"}`)
	mustBroadcast(t, h, Public, "orders", `{"to":"public-orders"}`)
	mustBroadcast(t, h, Private, "news", `{"to":"private-news"}`)

	if got := readText(t, privateOrders); got != `{"to":"private-orders"}` {
		t.Errorf("private orders got %q", got)
	}
	if got := readText(t, publicOrders); got != `{"to":"public-orders"}` {
		t.Errorf("public orders got %q", got)
	}
	if got := readText(t, privateNews); got != `{"to":"private-news"}` {
		t.Errorf("private news got %q", got)
	}
	for _, conn := range []*websocket.Conn{privateOrders, publicOrders, privateNews} {
		expectSilence(t, conn, 100*time.Millisecond)
	}
}

func TestServeSendsEveryMessageAsItsOwnFrame(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	conn := subscribeN(t, h, srv, Public, "burst", 1)[0]

	const burst = 200
	for i := range burst {
		mustBroadcast(t, h, Public, "burst", fmt.Sprintf(`{"seq":%d}`, i))
	}
	for i := range burst {
		got := readText(t, conn)
		var msg struct{ Seq int }
		if err := json.Unmarshal([]byte(got), &msg); err != nil {
			t.Fatalf("frame %d is not a single JSON document: %q", i, got)
		}
		if msg.Seq != i {
			t.Fatalf("frame %d has seq %d: messages reordered", i, msg.Seq)
		}
	}
}

func TestServeUnsubscribesOnClientClose(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	conns := subscribeN(t, h, srv, Public, "c", 3)

	_ = conns[0].WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
	_ = conns[1].Close()
	waitFor(t, "two clients unsubscribed", func() bool { return h.Subscribers(Public, "c") == 1 })

	mustBroadcast(t, h, Public, "c", `{"still":"here"}`)
	if got := readText(t, conns[2]); got != `{"still":"here"}` {
		t.Errorf("remaining client got %q", got)
	}
}

func TestServeIgnoresInboundMessages(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	sender := subscribeN(t, h, srv, Public, "c", 1)[0]
	listener := subscribeN(t, h, srv, Public, "c", 1)[0]

	for range 20 {
		if err := sender.WriteMessage(websocket.TextMessage, []byte(`{"spoof":true}`)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = sender.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 2})
	}
	time.Sleep(100 * time.Millisecond)

	mustBroadcast(t, h, Public, "c", `{"marker":1}`)
	for name, conn := range map[string]*websocket.Conn{"sender": sender, "listener": listener} {
		if got := readText(t, conn); got != `{"marker":1}` {
			t.Errorf("%s first message = %q, want marker: inbound data was relayed", name, got)
		}
	}
}

func TestServeClosesOnOversizedInboundMessage(t *testing.T) {
	opts := fastOptions()
	opts.ReadLimit = 64
	h := New(opts)
	srv := newTestServer(t, h)
	conn := subscribeN(t, h, srv, Public, "c", 1)[0]

	_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 65)))
	expectClose(t, conn, websocket.CloseMessageTooBig)
	waitFor(t, "client unsubscribed", func() bool { return h.Subscribers(Public, "c") == 0 })
}

func TestServeAcceptsInboundMessageAtReadLimit(t *testing.T) {
	opts := fastOptions()
	opts.ReadLimit = 64
	h := New(opts)
	srv := newTestServer(t, h)
	conn := subscribeN(t, h, srv, Public, "c", 1)[0]

	_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 64)))
	mustBroadcast(t, h, Public, "c", `{}`)
	if got := readText(t, conn); got != `{}` {
		t.Errorf("got %q", got)
	}
}

func TestServeSendsPingsAndKeepsResponsiveClients(t *testing.T) {
	opts := fastOptions()
	opts.PingPeriod = 30 * time.Millisecond
	opts.PongWait = 150 * time.Millisecond
	h := New(opts)
	srv := newTestServer(t, h)
	conn := subscribeN(t, h, srv, Public, "c", 1)[0]

	var mu sync.Mutex
	pings := 0
	conn.SetPingHandler(func(data string) error {
		mu.Lock()
		pings++
		mu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})

	expectSilence(t, conn, 500*time.Millisecond)
	if h.Subscribers(Public, "c") != 1 {
		t.Fatal("responsive client was disconnected")
	}
	mu.Lock()
	defer mu.Unlock()
	if pings < 5 {
		t.Errorf("received %d pings in 500ms with 30ms period", pings)
	}
}

func TestServeDropsClientsThatStopAnsweringPings(t *testing.T) {
	opts := fastOptions()
	opts.PingPeriod = 20 * time.Millisecond
	opts.PongWait = 100 * time.Millisecond
	h := New(opts)
	srv := newTestServer(t, h)
	subscribeN(t, h, srv, Public, "c", 1)

	waitFor(t, "unresponsive client dropped", func() bool { return h.Subscribers(Public, "c") == 0 })
}

func TestServeDisconnectsSlowConsumerWithoutAffectingOthers(t *testing.T) {
	opts := fastOptions()
	opts.MaxQueuedBytes = 512 << 10
	opts.WriteWait = 200 * time.Millisecond
	h := New(opts)
	srv := newTestServer(t, h)
	slow := subscribeN(t, h, srv, Public, "c", 1)[0]
	fast := subscribeN(t, h, srv, Public, "c", 1)[0]

	received := make(chan int, 1)
	go func() {
		count := 0
		for {
			_ = fast.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := fast.ReadMessage(); err != nil {
				received <- count
				return
			}
			count++
		}
	}()

	payload := `{"blob":"` + strings.Repeat("x", 256<<10) + `"}`
	const messages = 200
	for range messages {
		h.Broadcast(Public, "c", []byte(payload))
		time.Sleep(time.Millisecond)
	}

	waitFor(t, "slow consumer dropped", func() bool { return h.Subscribers(Public, "c") == 1 })
	_ = slow.Close()
	mustBroadcast(t, h, Public, "c", `{"last":true}`)
	time.Sleep(200 * time.Millisecond)
	_ = fast.Close()
	if got := <-received; got != messages+1 {
		t.Errorf("fast client received %d messages, want %d", got, messages+1)
	}
}

func TestServeShutdownSendsGoingAway(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	conns := subscribeN(t, h, srv, Private, "c", 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, conn := range conns {
		expectClose(t, conn, websocket.CloseGoingAway)
	}
	waitFor(t, "clients unsubscribed", func() bool { return h.Subscribers(Private, "c") == 0 })

	late := dial(t, srv, Private, "c")
	expectClose(t, late, websocket.CloseGoingAway)
	if h.Subscribers(Private, "c") != 0 {
		t.Error("client subscribed after shutdown")
	}
}

func TestServeConcurrentChurnAndBroadcasts(t *testing.T) {
	h := New(fastOptions())
	srv := newTestServer(t, h)
	stable := subscribeN(t, h, srv, Public, "stable", 4)

	const publishers, perPublisher = 8, 50
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn := dial(t, srv, Public, "stable")
				_ = conn.Close()
			}
		})
	}

	var publishWG sync.WaitGroup
	for p := range publishers {
		publishWG.Go(func() {
			for i := range perPublisher {
				h.Broadcast(Public, "stable", fmt.Appendf(nil, `{"p":%d,"i":%d}`, p, i))
			}
		})
	}
	publishWG.Wait()
	close(stop)
	wg.Wait()

	for idx, conn := range stable {
		next := make([]int, publishers)
		for range publishers * perPublisher {
			var msg struct{ P, I int }
			if err := json.Unmarshal([]byte(readText(t, conn)), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.I != next[msg.P] {
				t.Fatalf("conn %d: publisher %d message %d arrived, expected %d", idx, msg.P, msg.I, next[msg.P])
			}
			next[msg.P]++
		}
	}
	waitFor(t, "churned clients unsubscribed", func() bool { return h.Subscribers(Public, "stable") == len(stable) })
}

func mustBroadcast(t *testing.T, h *Hub, scope Scope, channel, payload string) {
	t.Helper()
	if n := h.Broadcast(scope, channel, []byte(payload)); n == 0 {
		t.Fatal("Broadcast reached no subscriber")
	}
}
