package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBroadcastWithoutSubscribers(t *testing.T) {
	h := New(DefaultOptions())
	n, err := h.Broadcast(Public, "nobody", []byte(`{}`))
	if err != nil || n != 0 {
		t.Fatalf("Broadcast = %d, %v; want 0, nil", n, err)
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

	n, err := h.Broadcast(Private, "orders", []byte(`{"id":1}`))
	if err != nil || n != len(target) {
		t.Fatalf("Broadcast = %d, %v; want %d, nil", n, err, len(target))
	}
	for i, c := range target {
		if len(c.send) != 1 {
			t.Errorf("target %d queued %d messages, want 1", i, len(c.send))
		}
	}
	for i, c := range others {
		if len(c.send) != 0 {
			t.Errorf("non-target %d (%+v) received a message", i, c.topic)
		}
	}
}

func TestBroadcastDisconnectsSlowClients(t *testing.T) {
	h := New(DefaultOptions())
	slow, fast := fakeClient(Public, "c", 1), fakeClient(Public, "c", 8)
	h.subscribe(slow)
	h.subscribe(fast)
	defer h.pumps.Add(-2)

	if n, _ := h.Broadcast(Public, "c", []byte(`1`)); n != 2 {
		t.Fatalf("first Broadcast delivered %d, want 2", n)
	}
	if n, _ := h.Broadcast(Public, "c", []byte(`2`)); n != 1 {
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
	if len(fast.send) != 2 {
		t.Errorf("fast client queued %d, want 2", len(fast.send))
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
				_, _ = h.Broadcast(Scope(w%2), channel, []byte(`{}`))
				_ = h.Subscribers(Scope(w%2), channel)
				h.unsubscribe(c)
				h.pumps.Done()
			}
		})
	}
	for w := range workers {
		wg.Go(func() {
			for range rounds {
				_, _ = h.Broadcast(Scope(w%2), fmt.Sprintf("ch-%d", w%4), []byte(`[]`))
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
				_, _ = h.Broadcast(Public, "bench", payload)
				for _, c := range clients {
					<-c.send
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
			c := fakeClient(Public, fmt.Sprint(ch), 256)
			h.subscribe(c)
			go func() {
				for {
					select {
					case <-c.send:
					case <-stop:
						return
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
			_, _ = h.Broadcast(Public, fmt.Sprint(i%channels), payload)
			i++
		}
	})
}
