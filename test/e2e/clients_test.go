//go:build e2e

package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type scope int

const (
	public scope = iota
	private
)

func (s scope) publishPath() string {
	if s == private {
		return "/update"
	}
	return "/publish"
}

// timeToken mirrors the Node.js backend simulator in demo-tools.
func timeToken(secret string, windowSeconds, offset int64) string {
	window := strconv.FormatInt(time.Now().Unix()/windowSeconds+offset, 10)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(window))
	mac := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(window + ":" + mac))
}

func (s *server) token() string {
	return timeToken(s.timeSecret, s.window, 0)
}

func signJWT(t testing.TB, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return token
}

func (s *server) userJWT(t testing.TB, userID string) string {
	return signJWT(t, jwt.SigningMethodHS256, []byte(s.jwtSecret), jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
}

type response struct {
	status int
	body   string
}

func (s *server) request(t testing.TB, method, path string, header http.Header, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(method, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(data)}
}

func (s *server) publish(t testing.TB, sc scope, channel string, payload []byte) int {
	t.Helper()
	path := sc.publishPath()
	if channel != "" {
		path += "?channel=" + url.QueryEscape(channel)
	}
	return s.request(t, http.MethodPost, path, http.Header{
		"X-Api-Token":  {s.token()},
		"Content-Type": {"application/json"},
	}, payload).status
}

func (s *server) mustPublish(t testing.TB, sc scope, channel string, payload []byte) {
	t.Helper()
	if status := s.publish(t, sc, channel, payload); status != http.StatusAccepted {
		t.Fatalf("publish to %q: status %d", channel, status)
	}
}

const probePrefix = `{"__probe":`

// user is a simulated browser tab. Its reader splits every frame into JSON
// values so servers that batch several documents per frame are still counted.
type user struct {
	scope   scope
	channel string
	conn    *websocket.Conn
	frames  atomic.Int64
	probed  atomic.Bool

	mu       sync.Mutex
	inbox    []delivery
	notify   chan struct{}
	done     chan struct{}
	closeErr error
}

type delivery struct {
	data []byte
	at   time.Time
}

func (s *server) dialURL(sc scope, channel, jwtToken string) string {
	query := url.Values{}
	if channel != "" {
		query.Set("channel", channel)
	}
	path := "/ws/public"
	if sc == private {
		path = "/ws"
		query.Set("token", jwtToken)
	}
	return s.wsURL + path + "?" + query.Encode()
}

func (s *server) dial(sc scope, channel, jwtToken string, header http.Header) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	return dialer.Dial(s.dialURL(sc, channel, jwtToken), header)
}

func (s *server) tryConnect(t testing.TB, sc scope, channel string) (*user, error) {
	jwtToken := ""
	if sc == private {
		jwtToken = s.userJWT(t, "user-"+randomSecret()[:8])
	}
	conn, resp, err := s.dial(sc, channel, jwtToken, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, fmt.Errorf("connect %v %q: %w (status %d)", sc, channel, err, status)
	}
	u := &user{
		scope:   sc,
		channel: channel,
		conn:    conn,
		notify:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go u.read()
	t.Cleanup(func() { _ = u.conn.Close() })
	return u, nil
}

func (s *server) connect(t testing.TB, sc scope, channel string) *user {
	t.Helper()
	u, err := s.tryConnect(t, sc, channel)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (s *server) connectMany(t testing.TB, sc scope, channel string, n int) []*user {
	t.Helper()
	users := make([]*user, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	for i := range users {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			users[i], errs[i] = s.tryConnect(t, sc, channel)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	return users
}

func (u *user) read() {
	defer close(u.done)
	for {
		_, frame, err := u.conn.ReadMessage()
		if err != nil {
			u.mu.Lock()
			u.closeErr = err
			u.mu.Unlock()
			return
		}
		at := time.Now()
		if !bytes.HasPrefix(frame, []byte(probePrefix)) {
			u.frames.Add(1)
		}
		dec := json.NewDecoder(bytes.NewReader(frame))
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				break
			}
			if bytes.HasPrefix(raw, []byte(probePrefix)) {
				u.probed.Store(true)
				continue
			}
			u.mu.Lock()
			u.inbox = append(u.inbox, delivery{data: raw, at: at})
			u.mu.Unlock()
			select {
			case u.notify <- struct{}{}:
			default:
			}
		}
	}
}

func (u *user) take(max int) []delivery {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := min(max, len(u.inbox))
	out := u.inbox[:n:n]
	u.inbox = u.inbox[n:]
	return out
}

func (u *user) pending() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.inbox)
}

func (u *user) err() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.closeErr
}

// awaitSubscribed publishes probe messages until every user has seen one, which
// proves its subscription is live without peeking into the server.
func (s *server) awaitSubscribed(t testing.TB, users ...*user) {
	t.Helper()
	type key struct {
		scope   scope
		channel string
	}
	pending := map[key]bool{}
	for _, u := range users {
		pending[key{u.scope, u.channel}] = true
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		ready := true
		for _, u := range users {
			if !u.probed.Load() {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("users never observed a probe message")
		}
		for k := range pending {
			s.mustPublish(t, k.scope, k.channel, []byte(probePrefix+`1}`))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deliveries waits for n messages and returns them with their arrival times.
func (u *user) deliveries(t testing.TB, n int, timeout time.Duration) []delivery {
	t.Helper()
	out := make([]delivery, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		out = append(out, u.take(n-len(out))...)
		if len(out) == n {
			break
		}
		select {
		case <-u.notify:
		case <-u.done:
			out = append(out, u.take(n-len(out))...)
			if len(out) < n {
				t.Fatalf("connection on %q closed after %d/%d messages: %v", u.channel, len(out), n, u.err())
			}
		case <-deadline:
			t.Fatalf("received %d/%d messages on %q within %s", len(out), n, u.channel, timeout)
		}
	}
	return out
}

func (u *user) collect(t testing.TB, n int, timeout time.Duration) [][]byte {
	t.Helper()
	ds := u.deliveries(t, n, timeout)
	out := make([][]byte, len(ds))
	for i, d := range ds {
		out[i] = d.data
	}
	return out
}

func (u *user) next(t testing.TB, timeout time.Duration) []byte {
	t.Helper()
	return u.collect(t, 1, timeout)[0]
}

func (u *user) expectNothing(t testing.TB, wait time.Duration) {
	t.Helper()
	time.Sleep(wait)
	if got := u.take(1); len(got) > 0 {
		t.Fatalf("unexpected message on %q: %s", u.channel, got[0].data)
	}
}

func (u *user) waitClosed(t testing.TB, timeout time.Duration) error {
	t.Helper()
	select {
	case <-u.done:
		return u.err()
	case <-time.After(timeout):
		t.Fatalf("connection on %q still open after %s", u.channel, timeout)
	}
	return nil
}

func message(fields map[string]any) []byte {
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return data
}

func decode[T any](t testing.TB, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return v
}

func (sc scope) String() string {
	if sc == private {
		return "private"
	}
	return "public"
}
