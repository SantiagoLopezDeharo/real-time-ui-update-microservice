//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Performance scenarios run against an optimized (non-race) build and write a
// report with latency, throughput, memory and CPU/alloc hotspots, plus a list
// of optimization opportunities inferred from them. Scale with E2E_PERF_SCALE.

func perfScale() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("E2E_PERF_SCALE"), 64); err == nil && v > 0 {
		return v
	}
	return 1
}

func scaled(n int) int {
	return max(1, int(math.Round(float64(n)*perfScale())))
}

func TestPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("performance suite skipped in short mode")
	}
	s := startBinary(t, perfBinary)
	r := newReport(t, s)
	defer r.write()

	idle := r.scenarioIdleFootprint(scaled(2000))
	r.scenarioFanOut(idle, scaled(200), 50)
	r.scenarioMultiChannelThroughput(scaled(200), 10, 32, 5*time.Second)
	r.scenarioLargePayloads(scaled(100), scaled(40), 256<<10)
}

func (r *report) scenarioIdleFootprint(n int) []*user {
	t, s := r.t, r.s
	time.Sleep(200 * time.Millisecond)
	before := r.sample()

	start := time.Now()
	users := s.connectMany(t, public, "fanout", n)
	elapsed := time.Since(start)
	s.awaitSubscribed(t, users...)
	time.Sleep(500 * time.Millisecond)
	after := r.sample()
	r.heapProfile("idle-footprint")

	sc := r.scenario("idle-footprint", fmt.Sprintf("%d idle public connections on one channel", n))
	sc.metric("connections", float64(n), "")
	sc.metric("connect rate", float64(n)/elapsed.Seconds(), "conn/s")
	sc.metric("rss per connection", float64(after.rssKB-before.rssKB)*1024/float64(n), "B")
	if after.heapInuse > 0 {
		sc.metric("heap in use per connection", float64(int64(after.heapInuse)-int64(before.heapInuse))/float64(n), "B")
		sc.metric("goroutines per connection", float64(after.goroutines-before.goroutines)/float64(n), "")
	}
	return users
}

func (r *report) scenarioFanOut(users []*user, messages, ratePerSecond int) {
	t, s := r.t, r.s
	name := "fanout-latency"
	interval := time.Second / time.Duration(ratePerSecond)
	duration := time.Duration(messages) * interval
	stopProfile := r.profile(name, duration)

	start := time.Now()
	for i := range messages {
		s.mustPublish(t, public, "fanout", stamped(i, 64))
		time.Sleep(time.Until(start.Add(time.Duration(i+1) * interval)))
	}
	latencies := collectLatencies(t, users, messages)
	stopProfile()

	sc := r.scenario(name, fmt.Sprintf("%d messages at %d/s to %d users on one channel", messages, ratePerSecond, len(users)))
	sc.latency(latencies)
	sc.metric("deliveries", float64(len(latencies)), "")
}

func (r *report) scenarioMultiChannelThroughput(channels, perChannel, publishers int, duration time.Duration) {
	t, s := r.t, r.s
	name := "multichannel-throughput"
	var users []*user
	byChannel := make([][]*user, channels)
	for c := range channels {
		byChannel[c] = s.connectMany(t, private, fmt.Sprintf("tenant-%d", c), perChannel)
		users = append(users, byChannel[c]...)
	}
	s.awaitSubscribed(t, users...)
	r.allocBaseline(name)
	stopProfile := r.profile(name, duration)

	counts := make([]atomic.Int64, channels)
	var publishLatencies []time.Duration
	var mu sync.Mutex
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	start := time.Now()
	for p := range publishers {
		wg.Go(func() {
			var local []time.Duration
			for i := 0; time.Now().Before(deadline); i++ {
				c := (p*7919 + i) % channels
				seq := int(counts[c].Add(1)) - 1
				begin := time.Now()
				if s.publish(t, private, fmt.Sprintf("tenant-%d", c), stamped(seq, 64)) != http.StatusAccepted {
					t.Errorf("publish failed")
					return
				}
				local = append(local, time.Since(begin))
			}
			mu.Lock()
			publishLatencies = append(publishLatencies, local...)
			mu.Unlock()
		})
	}
	wg.Wait()
	publishElapsed := time.Since(start)

	var latencies []time.Duration
	var lastDelivery time.Time
	for c, group := range byChannel {
		want := int(counts[c].Load())
		for _, u := range group {
			for _, d := range u.deliveries(t, want, 60*time.Second) {
				latencies = append(latencies, d.at.Sub(sentAt(t, d.data)))
				if d.at.After(lastDelivery) {
					lastDelivery = d.at
				}
			}
		}
	}
	stopProfile()
	r.allocProfile(name)

	sc := r.scenario(name, fmt.Sprintf("%d publishers flat out for %s across %d private channels × %d users", publishers, duration, channels, perChannel))
	sc.metric("publishes per second", float64(len(publishLatencies))/publishElapsed.Seconds(), "req/s")
	sc.metric("deliveries per second", float64(len(latencies))/lastDelivery.Sub(start).Seconds(), "msg/s")
	sc.distribution("publish request latency", publishLatencies)
	sc.latency(latencies)
	for _, u := range users {
		_ = u.conn.Close()
	}
}

func (r *report) scenarioLargePayloads(n, messages, size int) {
	t, s := r.t, r.s
	name := "large-payloads"
	users := s.connectMany(t, public, "large", n)
	s.awaitSubscribed(t, users...)
	stopProfile := r.profile(name, 3*time.Second)

	start := time.Now()
	for i := range messages {
		s.mustPublish(t, public, "large", stamped(i, size))
	}
	latencies := collectLatencies(t, users, messages)
	elapsed := time.Since(start)
	stopProfile()

	sc := r.scenario(name, fmt.Sprintf("%d × %d KiB messages to %d users", messages, size>>10, n))
	sc.metric("delivered bandwidth", float64(n*messages*size)/elapsed.Seconds()/(1<<20), "MiB/s")
	sc.latency(latencies)
	for _, u := range users {
		_ = u.conn.Close()
	}
}

func stamped(seq, size int) []byte {
	head := fmt.Sprintf(`{"seq":%d,"t":%d,"pad":"`, seq, time.Now().UnixNano())
	return []byte(head + strings.Repeat("x", max(0, size-len(head)-2)) + `"}`)
}

// sentAt reads the publish timestamp from the payload prefix written by
// stamped, avoiding a full JSON decode of large payloads.
func sentAt(t testing.TB, data []byte) time.Time {
	_, rest, ok := bytes.Cut(data, []byte(`"t":`))
	end := bytes.IndexByte(rest, ',')
	if !ok || end < 0 {
		t.Fatalf("payload without timestamp: %.80s", data)
	}
	nanos, err := strconv.ParseInt(string(rest[:end]), 10, 64)
	if err != nil {
		t.Fatalf("bad timestamp in %.80s", data)
	}
	return time.Unix(0, nanos)
}

func collectLatencies(t *testing.T, users []*user, messages int) []time.Duration {
	t.Helper()
	latencies := make([]time.Duration, 0, len(users)*messages)
	for _, u := range users {
		for _, d := range u.deliveries(t, messages, 60*time.Second) {
			latencies = append(latencies, d.at.Sub(sentAt(t, d.data)))
		}
	}
	return latencies
}
