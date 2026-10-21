package hub

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestBroadcastWithoutSubscribers(t *testing.T) {
	h := New(DefaultOptions())
	if n := h.Broadcast(Public, "nobody", []byte(`{}`)); n != 0 {
		t.Fatalf("Broadcast = %d, want 0", n)
	}
}

func TestSubscribeAndUnsubscribeBookkeeping(t *testing.T) {
	h := New(DefaultOptions())
	a, b := fakeClient(Public, "c", 1), fakeClient(Public, "c", 1)

	if !h.subscribe(a) || !h.subscribe(b) {
		t.Fatal("subscribe rejected on open hub")
	}
	if got := h.Subscribers(Public, "c"); got != 2 {
		t.Fatalf("Subscribers = %d, want 2", got)
	}
	if got := h.Subscribers(Private, "c"); got != 0 {
		t.Fatalf("private Subscribers = %d, want 0", got)
	}

	h.unsubscribe(a)
	h.unsubscribe(a)
	if got := h.Subscribers(Public, "c"); got != 1 {
		t.Fatalf("Subscribers after unsubscribe = %d, want 1", got)
	}

	h.unsubscribe(b)
	if _, ok := h.topics[b.topic]; ok {
		t.Fatal("empty topic not removed")
	}
	h.unsubscribe(fakeClient(Public, "never-subscribed", 1))
	h.pumps.Add(-2)
}

func TestBroadcastFakeClientsDeliveryAndIsolation(t *testing.T) {
	h := New(DefaultOptions())
	target := []*Client{fakeClient(Private, "orders", 4), fakeClient(Private, "orders", 4)}
	others := []*Client{
		fakeClient(Public, "orders", 4),
		fakeClient(Private, "orders-2", 4),
		fakeClient(Private, "Orders", 4),
		fakeClient(Private, "", 4),
	}
	for _, c := range append(append([]*Client{}, target...), others...) {
		h.subscribe(c)
	}
	defer h.pumps.Add(-len(target) - len(others))

	if n := h.Broadcast(Private, "orders", []byte(`{"id":1}`)); n != len(target) {
		t.Fatalf("Broadcast = %d, want %d", n, len(target))
	}
	for i, c := range target {
		if queued(c) != 1 {
			t.Errorf("target %d queued %d messages, want 1", i, queued(c))
		}
	}
	for i, c := range others {
		if queued(c) != 0 {
			t.Errorf("non-target %d (%+v) received a message", i, c.topic)
		}
	}
}

func TestBroadcastDisconnectsSlowClients(t *testing.T) {
	h := New(DefaultOptions())
	slow, fast := fakeClient(Public, "c", 1), fakeClient(Public, "c", 2)
	h.subscribe(slow)
	h.subscribe(fast)
	defer h.pumps.Add(-2)

	if n := h.Broadcast(Public, "c", []byte(`1`)); n != 2 {
		t.Fatalf("first Broadcast delivered %d, want 2", n)
	}
	if n := h.Broadcast(Public, "c", []byte(`2`)); n != 1 {
		t.Fatalf("second Broadcast delivered %d, want 1", n)
	}
	if !isClosed(slow) {
		t.Error("slow client not closed")
	}
	if string(slow.closeFrame) != string(closeSlow) {
		t.Errorf("slow client close frame = %q, want %q", slow.closeFrame, closeSlow)
	}
	if isClosed(fast) {
		t.Error("fast client closed")
	}
	if queued(fast) != 2 {
		t.Errorf("fast client queued %d, want 2", queued(fast))
	}
}

func TestQueueIsBoundedByBytesNotMessageCount(t *testing.T) {
	h := New(DefaultOptions())
	c := fakeClient(Public, "burst", 64<<10)
	h.subscribe(c)
	defer h.pumps.Done()

	payload := []byte(`{"seq":1234,"status":"ok"}`)
	burst := (64 << 10) / len(payload)
	for i := range burst {
		if n := h.Broadcast(Public, "burst", payload); n != 1 {
			t.Fatalf("message %d of a %d-message burst rejected", i, burst)
		}
	}
	if isClosed(c) {
		t.Fatal("client evicted while within its byte budget")
	}
	if n := h.Broadcast(Public, "burst", payload); n != 0 || !isClosed(c) {
		t.Fatal("client not evicted after exceeding its byte budget")
	}
}

func TestQueueAcceptsOversizedMessageWhenEmpty(t *testing.T) {
	c := fakeClient(Public, "c", 10)
	big := make([]byte, 100)
	if !c.enqueue(big) {
		t.Fatal("oversized message rejected by empty queue")
	}
	if c.enqueue([]byte{1}) {
		t.Fatal("message accepted on top of an over-budget queue")
	}
	drain(c)
	if !c.enqueue(big) {
		t.Fatal("queue budget not released after draining")
	}
}

func TestReleaseTracksQueuedBytes(t *testing.T) {
	c := fakeClient(Public, "c", 100)
	for range 4 {
		c.enqueue(make([]byte, 25))
	}
	batch := takeQueue(c)
	if len(batch) != 4 {
		t.Fatalf("batch = %d, want 4", len(batch))
	}
	if !c.enqueue(nil) || c.enqueue([]byte{1}) {
		t.Fatal("bytes released before the batch was written")
	}
	c.release(batch)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queuedBytes != 0 {
		t.Fatalf("queuedBytes = %d after release, want 0", c.queuedBytes)
	}
}

func TestQueueBuffersNeverAlias(t *testing.T) {
	c := fakeClient(Public, "c", 1<<20)
	c.enqueue([]byte("x"))
	drain(c)
	takeQueue(c)

	c.enqueue([]byte("y"))
	batch := takeQueue(c)
	c.enqueue([]byte("z"))
	if len(batch) != 1 || string(batch[0].payload) != "y" {
		t.Fatalf("in-flight batch overwritten by a later enqueue: %q", batch[0].payload)
	}
}

func TestWriterIsClaimedOnce(t *testing.T) {
	c := newClient(nil, topic{}, DefaultOptions(), func() {})
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.claimWriter() {
		t.Fatal("first claim refused")
	}
	if c.claimWriter() {
		t.Fatal("second claim granted while a writer is running")
	}
}

func TestClosedClientRejectsMessagesAndPings(t *testing.T) {
	c := fakeClient(Public, "c", 1<<20)
	c.close(closeSlow)
	if c.enqueue([]byte(`{}`)) {
		t.Fatal("closed client accepted a message")
	}
	c.ping()
	if queued(c) != 0 {
		t.Fatal("closed client queued a ping")
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	c := fakeClient(Public, "c", 1)
	c.close(closeShutdown)
	c.close(closeSlow)
	c.close(closeNormal)
	if string(c.closeFrame) != string(closeShutdown) {
		t.Errorf("close frame = %q, want first frame %q", c.closeFrame, closeShutdown)
	}
}

func TestShutdownClosesClientsAndRejectsNewOnes(t *testing.T) {
	h := New(DefaultOptions())
	clients := []*Client{fakeClient(Public, "a", 1), fakeClient(Private, "b", 1)}
	for _, c := range clients {
		h.subscribe(c)
		h.pumps.Done()
	}

	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, c := range clients {
		if !isClosed(c) || string(c.closeFrame) != string(closeShutdown) {
			t.Errorf("client %+v not closed with shutdown frame", c.topic)
		}
	}
	if h.subscribe(fakeClient(Public, "a", 1)) {
		t.Error("subscribe accepted after shutdown")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

func TestShutdownHonoursContext(t *testing.T) {
	h := New(DefaultOptions())
	h.subscribe(fakeClient(Public, "stuck", 1))
	defer h.pumps.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
	}
}

func TestConcurrentSubscribeBroadcastUnsubscribe(t *testing.T) {
	h := New(DefaultOptions())
	const workers, rounds = 16, 200
	var wg sync.WaitGroup

	for w := range workers {
		wg.Go(func() {
			channel := fmt.Sprintf("ch-%d", w%4)
			for range rounds {
				c := fakeClient(Scope(w%2), channel, 2)
				h.subscribe(c)
				h.Broadcast(Scope(w%2), channel, []byte(`{}`))
				_ = h.Subscribers(Scope(w%2), channel)
				h.unsubscribe(c)
				h.pumps.Done()
			}
		})
	}
	for w := range workers {
		wg.Go(func() {
			for range rounds {
				h.Broadcast(Scope(w%2), fmt.Sprintf("ch-%d", w%4), []byte(`[]`))
			}
		})
	}
	wg.Wait()

	if len(h.topics) != 0 {
		t.Fatalf("topics leaked: %d", len(h.topics))
	}
}

func BenchmarkBroadcast(b *testing.B) {
	for _, subscribers := range []int{1, 100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			h := New(DefaultOptions())
			clients := make([]*Client, subscribers)
			for i := range clients {
				clients[i] = fakeClient(Public, "bench", 1)
				h.subscribe(clients[i])
			}
			payload := []byte(`{"id":"order-123","status":"shipped"}`)
			b.ReportAllocs()
			for b.Loop() {
				h.Broadcast(Public, "bench", payload)
				for _, c := range clients {
					drain(c)
				}
			}
		})
	}
}

func BenchmarkBroadcastParallelChannels(b *testing.B) {
	h := New(DefaultOptions())
	const channels = 64
	stop := make(chan struct{})
	defer close(stop)
	for ch := range channels {
		for range 16 {
			c := fakeClient(Public, fmt.Sprint(ch), 1<<20)
			h.subscribe(c)
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
						drain(c)
						runtime.Gosched()
					}
				}
			}()
		}
	}
	payload := []byte(`{"id":"order-123"}`)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			h.Broadcast(Public, fmt.Sprint(i%channels), payload)
			i++
		}
	})
}
