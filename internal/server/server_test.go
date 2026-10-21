package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"real-time-ui-update-microservice/internal/auth"
	"real-time-ui-update-microservice/internal/config"
	"real-time-ui-update-microservice/internal/hub"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

func testConfig() *config.Config {
	return &config.Config{
		Port:            "0",
		JWTSecret:       "jwt-secret",
		TimeTokenSecret: "time-secret",
		TimeWindow:      30,
		AllowedSkew:     1,
		MaxBodyBytes:    1024,
	}
}

type fixture struct {
	cfg    *config.Config
	hub    *hub.Hub
	srv    *httptest.Server
	tokens *auth.TimeTokens
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testConfig()
	h := hub.New(hub.DefaultOptions())
	srv := httptest.NewServer(Routes(cfg, h))
	t.Cleanup(srv.Close)
	return &fixture{cfg, h, srv, auth.NewTimeTokens(cfg.TimeTokenSecret, cfg.TimeWindow, cfg.AllowedSkew)}
}

func (f *fixture) jwt(t *testing.T) string {
	return f.jwtFor(t, "user")
}

func (f *fixture) jwtFor(t *testing.T, userID string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": userID}).SignedString([]byte(f.cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *fixture) dial(t *testing.T, path string, query url.Values) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.srv.URL, "http")+path+"?"+query.Encode(), nil)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (f *fixture) post(t *testing.T, path, channel, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path+"?channel="+url.QueryEscape(channel), strings.NewReader(body))
	req.Header.Set("X-API-Token", f.tokens.Generate())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func waitSubscribers(t *testing.T, h *hub.Hub, scope hub.Scope, channel string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Subscribers(scope, channel) != n {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers on %q = %d, want %d", channel, h.Subscribers(scope, channel), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func read(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestEndToEndPrivateAndPublicFlows(t *testing.T) {
	f := newFixture(t)
	private := f.dial(t, "/ws", url.Values{"token": {f.jwt(t)}, "channel": {"orders"}})
	public := f.dial(t, "/ws/public", url.Values{"channel": {"orders"}})
	waitSubscribers(t, f.hub, hub.Private, "orders", 1)
	waitSubscribers(t, f.hub, hub.Public, "orders", 1)

	if code := f.post(t, "/update", "orders", `{"scope":"private"}`); code != http.StatusAccepted {
		t.Fatalf("/update status = %d", code)
	}
	if code := f.post(t, "/publish", "orders", `{"scope":"public"}`); code != http.StatusAccepted {
		t.Fatalf("/publish status = %d", code)
	}
	if got := read(t, private); got != `{"scope":"private"}` {
		t.Errorf("private client got %q", got)
	}
	if got := read(t, public); got != `{"scope":"public"}` {
		t.Errorf("public client got %q", got)
	}
}

func TestDefaultChannel(t *testing.T) {
	f := newFixture(t)
	conn := f.dial(t, "/ws/public", nil)
	waitSubscribers(t, f.hub, hub.Public, "default", 1)
	if code := f.post(t, "/publish", "", `{"d":1}`); code != http.StatusAccepted {
		t.Fatalf("status = %d", code)
	}
	if got := read(t, conn); got != `{"d":1}` {
		t.Errorf("got %q", got)
	}
}

func TestRouteMatrix(t *testing.T) {
	f := newFixture(t)
	validToken := f.tokens.Generate()

	tests := []struct {
		method, path string
		header       map[string]string
		body         string
		want         int
	}{
		{http.MethodGet, "/healthz", nil, "", http.StatusOK},
		{http.MethodHead, "/healthz", nil, "", http.StatusOK},
		{http.MethodPost, "/healthz", nil, "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/update", nil, "", http.StatusMethodNotAllowed},
		{http.MethodPut, "/update", nil, "", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/publish", nil, "", http.StatusMethodNotAllowed},
		{http.MethodPatch, "/publish", nil, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/ws", nil, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/ws/public", nil, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/update", nil, "{}", http.StatusUnauthorized},
		{http.MethodPost, "/publish", nil, "{}", http.StatusUnauthorized},
		{http.MethodPost, "/update", map[string]string{"X-API-Token": "bad"}, "{}", http.StatusUnauthorized},
		{http.MethodPost, "/update", map[string]string{"X-API-Token": validToken}, "{}", http.StatusAccepted},
		{http.MethodPost, "/publish", map[string]string{"X-API-Token": validToken}, "{}", http.StatusAccepted},
		{http.MethodPost, "/update", map[string]string{"X-API-Token": validToken}, "nope", http.StatusBadRequest},
		{http.MethodPost, "/publish", map[string]string{"X-API-Token": validToken}, `{"k":"` + strings.Repeat("x", 2000) + `"}`, http.StatusRequestEntityTooLarge},
		{http.MethodPost, "/publish", nil, `{"k":"` + strings.Repeat("x", 2000) + `"}`, http.StatusUnauthorized},
		{http.MethodGet, "/ws", nil, "", http.StatusUnauthorized},
		{http.MethodGet, "/ws?token=bad", nil, "", http.StatusUnauthorized},
		{http.MethodGet, "/ws/public", nil, "", http.StatusBadRequest},
		{http.MethodGet, "/", nil, "", http.StatusNotFound},
		{http.MethodGet, "/ws/", nil, "", http.StatusNotFound},
		{http.MethodGet, "/ws/private", nil, "", http.StatusNotFound},
		{http.MethodPost, "/update/", map[string]string{"X-API-Token": validToken}, "{}", http.StatusNotFound},
		{http.MethodGet, "/debug/pprof/", nil, "", http.StatusNotFound},
	}
	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, f.srv.URL+tt.path, strings.NewReader(tt.body))
		for k, v := range tt.header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tt.method, tt.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s %s: status = %d, want %d (body %q)", tt.method, tt.path, resp.StatusCode, tt.want, body)
		}
		for _, secret := range []string{f.cfg.JWTSecret, f.cfg.TimeTokenSecret} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s %s leaks a secret", tt.method, tt.path)
			}
		}
	}
}

func TestPrivateEndpointRequiresJWTBeforeUpgrade(t *testing.T) {
	f := newFixture(t)
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.srv.URL, "http")+"/ws?channel=orders", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, resp = %v; want 401", err, resp)
	}
	if n := f.hub.Subscribers(hub.Private, "orders"); n != 0 {
		t.Errorf("unauthenticated client subscribed: %d", n)
	}
}

func TestNewSetsHardenedTimeouts(t *testing.T) {
	srv := New(":1234", http.NotFoundHandler())
	if srv.Addr != ":1234" {
		t.Errorf("Addr = %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("timeouts not set: %+v", srv)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 {
		t.Errorf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}

func TestPprofServer(t *testing.T) {
	srv := Pprof("127.0.0.1:0")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	for path, want := range map[string]int{
		"/debug/pprof/":                  http.StatusOK,
		"/debug/pprof/goroutine?debug=1": http.StatusOK,
		"/debug/pprof/heap?debug=1":      http.StatusOK,
		"/debug/pprof/cmdline":           http.StatusOK,
		"/debug/pprof/profile?seconds=1": http.StatusOK,
		"/healthz":                       http.StatusNotFound,
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func (f *fixture) dialStatus(t *testing.T, path string, query url.Values) int {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.srv.URL, "http")+path+"?"+query.Encode(), nil)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("dial %s %v succeeded", path, query)
	}
	if resp == nil {
		t.Fatalf("dial %s %v: %v", path, query, err)
	}
	return resp.StatusCode
}

func TestUserChannelsReachOnlyTheirOwner(t *testing.T) {
	f := newFixture(t)
	alice := f.dial(t, "/ws", url.Values{"token": {f.jwtFor(t, "alice")}, "channel": {"user:alice"}})
	bob := f.dial(t, "/ws", url.Values{"token": {f.jwtFor(t, "bob")}, "channel": {"user:bob"}})
	waitSubscribers(t, f.hub, hub.Private, "user:alice", 1)
	waitSubscribers(t, f.hub, hub.Private, "user:bob", 1)

	if code := f.post(t, "/update", "user:alice", `{"to":"alice"}`); code != http.StatusAccepted {
		t.Fatalf("/update status = %d", code)
	}
	if code := f.post(t, "/update", "user:bob", `{"to":"bob"}`); code != http.StatusAccepted {
		t.Fatalf("/update status = %d", code)
	}
	if got := read(t, alice); got != `{"to":"alice"}` {
		t.Errorf("alice got %q", got)
	}
	if got := read(t, bob); got != `{"to":"bob"}` {
		t.Errorf("bob got %q", got)
	}
}

func TestUserChannelsRejectEveryoneElse(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name  string
		path  string
		query url.Values
	}{
		{"other user", "/ws", url.Values{"token": {f.jwtFor(t, "bob")}, "channel": {"user:alice"}}},
		{"token without subject", "/ws", url.Values{"token": {f.jwtFor(t, "")}, "channel": {"user:"}}},
		{"public endpoint", "/ws/public", url.Values{"channel": {"user:alice"}}},
		{"public endpoint with owner token", "/ws/public", url.Values{"token": {f.jwtFor(t, "alice")}, "channel": {"user:alice"}}},
	}
	for _, tt := range tests {
		if got := f.dialStatus(t, tt.path, tt.query); got != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", tt.name, got)
		}
	}
	for _, sc := range []hub.Scope{hub.Private, hub.Public} {
		for _, ch := range []string{"user:alice", "user:"} {
			if n := f.hub.Subscribers(sc, ch); n != 0 {
				t.Errorf("scope %d channel %q has %d subscribers after denials", sc, ch, n)
			}
		}
	}

	if got := f.dialStatus(t, "/ws", url.Values{"channel": {"user:alice"}}); got != http.StatusUnauthorized {
		t.Errorf("missing JWT: status = %d, want 401 before the access check", got)
	}
	if code := f.post(t, "/publish", "user:alice", `{}`); code != http.StatusBadRequest {
		t.Errorf("/publish to a user channel: status = %d, want 400", code)
	}
}
