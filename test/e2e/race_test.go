//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type seqMessage struct {
	P, I int
}

// assertPerPublisherOrder checks that every message arrived exactly once and
// that each publisher's sequence is in order for this user.
func assertPerPublisherOrder(t *testing.T, u *user, publishers, perPublisher int) {
	t.Helper()
	next := make([]int, publishers)
	for _, raw := range u.collect(t, publishers*perPublisher, 30*time.Second) {
		m := decode[seqMessage](t, raw)
		if m.I != next[m.P] {
			t.Fatalf("publisher %d: got message %d, expected %d", m.P, m.I, next[m.P])
		}
		next[m.P]++
	}
	u.expectNothing(t, 0)
}

func publishConcurrently(t *testing.T, s *server, sc scope, channel string, publishers, perPublisher int) {
	t.Helper()
	var wg sync.WaitGroup
	var failures atomic.Int64
	for p := range publishers {
		wg.Go(func() {
			for i := range perPublisher {
				if s.publish(t, sc, channel, message(map[string]any{"p": p, "i": i})) != http.StatusAccepted {
					failures.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if n := failures.Load(); n > 0 {
		t.Fatalf("%d publishes failed", n)
	}
}

func TestConcurrentPublishersExactlyOnceInOrder(t *testing.T) {
	s := startServer(t)
	users := s.connectMany(t, private, "hot", 30)
	s.awaitSubscribed(t, users...)

	const publishers, perPublisher = 20, 50
	publishConcurrently(t, s, private, "hot", publishers, perPublisher)
	for _, u := range users {
		assertPerPublisherOrder(t, u, publishers, perPublisher)
	}
}

func TestMixedScopesAndChannelsUnderConcurrency(t *testing.T) {
	s := startServer(t)
	type target struct {
		sc      scope
		channel string
		users   []*user
	}
	var targets []target
	var all []*user
	for _, sc := range []scope{private, public} {
		for c := range 4 {
			ch := fmt.Sprintf("c%d", c)
			us := s.connectMany(t, sc, ch, 5)
			targets = append(targets, target{sc, ch, us})
			all = append(all, us...)
		}
	}
	s.awaitSubscribed(t, all...)

	const publishers, perPublisher = 4, 40
	var wg sync.WaitGroup
	for _, tg := range targets {
		wg.Go(func() { publishConcurrently(t, s, tg.sc, tg.channel, publishers, perPublisher) })
	}
	wg.Wait()
	for _, tg := range targets {
		for _, u := range tg.users {
			assertPerPublisherOrder(t, u, publishers, perPublisher)
		}
	}
}

func TestSubscriberChurnDuringBroadcasts(t *testing.T) {
	s := startServer(t)
	stable := s.connectMany(t, public, "churn", 10)
	s.awaitSubscribed(t, stable...)

	stop := make(chan struct{})
	var churners sync.WaitGroup
	var churned atomic.Int64
	for w := range 16 {
		churners.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, _, err := s.dial(public, "churn", "", nil)
				if err != nil {
					continue
				}
				churned.Add(1)
				switch w % 3 {
				case 0:
					_ = conn.UnderlyingConn().Close()
				case 1:
					_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					_ = conn.Close()
				default:
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
					_, _, _ = conn.ReadMessage()
					_ = conn.Close()
				}
			}
		})
	}

	const publishers, perPublisher = 10, 60
	publishConcurrently(t, s, public, "churn", publishers, perPublisher)
	close(stop)
	churners.Wait()

	for _, u := range stable {
		assertPerPublisherOrder(t, u, publishers, perPublisher)
	}
	t.Logf("%d connections churned during %d broadcasts", churned.Load(), publishers*perPublisher)
}

func TestSlowConsumerDoesNotStallOthers(t *testing.T) {
	s := startServer(t, withEnv("MAX_BODY_BYTES", "262144"))
	conn, _, err := s.dial(public, "firehose", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fast := s.connectMany(t, public, "firehose", 5)
	s.awaitSubscribed(t, fast...)

	payload := `{"seq":%d,"blob":"` + strings.Repeat("x", 200<<10) + `"}`
	const messages = 400
	start := time.Now()
	for i := range messages {
		s.mustPublish(t, public, "firehose", fmt.Appendf(nil, payload, i))
	}
	publishTime := time.Since(start)

	for _, u := range fast {
		for i, raw := range u.collect(t, messages, 60*time.Second) {
			if got := decode[struct{ Seq int }](t, raw); got.Seq != i {
				t.Fatalf("fast user got seq %d, want %d", got.Seq, i)
			}
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	received := 0
	var closeErr error
	for {
		if _, _, closeErr = conn.ReadMessage(); closeErr != nil {
			break
		}
		received++
	}
	if received >= messages {
		t.Fatalf("slow consumer received all %d messages; it was never evicted", received)
	}
	if !websocket.IsCloseError(closeErr, websocket.ClosePolicyViolation) && !websocket.IsUnexpectedCloseError(closeErr) {
		t.Logf("slow consumer ended with %v", closeErr)
	}
	t.Logf("published %d×200KiB in %s; slow consumer evicted after %d messages", messages, publishTime, received)
}

func TestShutdownUnderLoad(t *testing.T) {
	s := startServer(t)
	users := s.connectMany(t, public, "load", 50)
	s.awaitSubscribed(t, users...)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				req, _ := http.NewRequest(http.MethodPost, s.baseURL+"/publish?channel=load", strings.NewReader(`{"x":1}`))
				req.Header.Set("X-API-Token", s.token())
				if resp, err := httpClient.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}
		})
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if conn, _, err := s.dial(public, "load", "", nil); err == nil {
					_ = conn.Close()
				}
			}
		})
	}

	time.Sleep(500 * time.Millisecond)
	if err := s.stop(15 * time.Second); err != nil {
		t.Errorf("exit under load: %v", err)
	}
	close(stop)
	wg.Wait()

	for _, u := range users {
		if err := u.waitClosed(t, 5*time.Second); !websocket.IsCloseError(err, websocket.CloseGoingAway) {
			t.Errorf("close error = %v, want 1001", err)
		}
	}
}

func TestConcurrentSubscribeAndPublishSameChannel(t *testing.T) {
	s := startServer(t)
	const joiners = 100
	var wg sync.WaitGroup
	users := make([]*user, joiners)
	errs := make([]error, joiners)
	stop := make(chan struct{})
	var publishes atomic.Int64

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if s.publish(t, private, "race", []byte(`{"bg":1}`)) == http.StatusAccepted {
				publishes.Add(1)
			}
		}
	})
	var joinWG sync.WaitGroup
	for i := range joiners {
		joinWG.Go(func() { users[i], errs[i] = s.tryConnect(t, private, "race") })
	}
	joinWG.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s.awaitSubscribed(t, users...)
	close(stop)
	wg.Wait()

	s.mustPublish(t, private, "race", []byte(`{"final":true}`))
	for _, u := range users {
		for {
			if string(u.next(t, deliveryTimeout)) == `{"final":true}` {
				break
			}
		}
	}
	t.Logf("%d background publishes raced with %d subscriptions", publishes.Load(), joiners)
}

func TestUserChannelIsolationUnderConcurrency(t *testing.T) {
	s := startServer(t)
	const users, tabsPerUser, perUser = 30, 2, 40
	tabs := make([][]*user, users)
	var all []*user
	for i := range users {
		for range tabsPerUser {
			tab := s.connectAs(t, fmt.Sprintf("u%d", i), fmt.Sprintf("user:u%d", i))
			tabs[i] = append(tabs[i], tab)
			all = append(all, tab)
		}
	}
	s.awaitSubscribed(t, all...)

	stop := make(chan struct{})
	var attackers sync.WaitGroup
	var attempts, breaches atomic.Int64
	for a := range 8 {
		attackers.Go(func() {
			token := s.userJWT(t, fmt.Sprintf("attacker%d", a))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				conn, resp, err := s.dial(private, fmt.Sprintf("user:u%d", i%users), token, nil)
				attempts.Add(1)
				if err == nil {
					breaches.Add(1)
					_ = conn.Close()
				} else if statusOf(resp) != http.StatusForbidden {
					breaches.Add(1)
				}
			}
		})
	}

	var publishers sync.WaitGroup
	var failures atomic.Int64
	for i := range users {
		publishers.Go(func() {
			for seq := range perUser {
				if s.publish(t, private, fmt.Sprintf("user:u%d", i), message(map[string]any{"u": i, "seq": seq})) != http.StatusAccepted {
					failures.Add(1)
				}
			}
		})
	}
	publishers.Wait()
	close(stop)
	attackers.Wait()

	if n := failures.Load(); n > 0 {
		t.Fatalf("%d publishes failed", n)
	}
	if n := breaches.Load(); n > 0 {
		t.Fatalf("%d of %d foreign subscriptions were not refused with 403", n, attempts.Load())
	}
	for i, userTabs := range tabs {
		for _, tab := range userTabs {
			for want, raw := range tab.collect(t, perUser, 30*time.Second) {
				got := decode[struct{ U, Seq int }](t, raw)
				if got.U != i || got.Seq != want {
					t.Fatalf("user u%d: got message %+v, want seq %d for itself", i, got, want)
				}
			}
			tab.expectNothing(t, 0)
		}
	}
	t.Logf("%d foreign subscription attempts refused during %d personal deliveries", attempts.Load(), users*tabsPerUser*perUser)
}
