package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"real-time-ui-update-microservice/internal/auth"
	"real-time-ui-update-microservice/internal/hub"

	"github.com/gorilla/websocket"
)

type serveCall struct {
	scope   hub.Scope
	channel string
}

type fakeServer struct {
	mu    sync.Mutex
	calls []serveCall
}

func (f *fakeServer) Serve(conn *websocket.Conn, scope hub.Scope, channel string) {
	f.mu.Lock()
	f.calls = append(f.calls, serveCall{scope, channel})
	f.mu.Unlock()
	_ = conn.Close()
}

func (f *fakeServer) snapshot() []serveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]serveCall(nil), f.calls...)
}

func TestSubscribeUpgradesAndServes(t *testing.T) {
	tests := []struct {
		query string
		scope hub.Scope
		want  string
	}{
		{"", hub.Public, "default"},
		{"?channel=news", hub.Public, "news"},
		{"?channel=orders", hub.Private, "orders"},
		{"?channel=a%20b", hub.Private, "a b"},
	}
	for _, tt := range tests {
		fake := &fakeServer{}
		srv := httptest.NewServer(Subscribe(fake, tt.scope, NewUpgrader(nil)))
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+tt.query, nil)
		if err != nil {
			t.Fatalf("dial %q: %v", tt.query, err)
		}
		_ = conn.Close()
		srv.Close()

		calls := fake.snapshot()
		if len(calls) != 1 || calls[0] != (serveCall{tt.scope, tt.want}) {
			t.Errorf("query %q: calls = %+v, want scope %d channel %q", tt.query, calls, tt.scope, tt.want)
		}
	}
}

// asUser authenticates every request as userID, standing in for the JWT
// middleware; an empty userID leaves the request anonymous.
func asUser(userID string, next http.Handler) http.Handler {
	if userID == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithUserID(r.Context(), userID)))
	})
}

func TestSubscribeUserChannelAccess(t *testing.T) {
	tests := []struct {
		name    string
		scope   hub.Scope
		userID  string
		query   string
		allowed bool
		channel string
	}{
		{"owner", hub.Private, "42", "?channel=user:42", true, "user:42"},
		{"owner, encoded colon", hub.Private, "42", "?channel=user%3A42", true, "user:42"},
		{"owner, shared channel", hub.Private, "42", "?channel=orders", true, "orders"},
		{"owner, default channel", hub.Private, "42", "", true, "default"},
		{"other user", hub.Private, "7", "?channel=user:42", false, ""},
		{"other user, encoded colon", hub.Private, "7", "?channel=user%3A42", false, ""},
		{"empty owner", hub.Private, "42", "?channel=user:", false, ""},
		{"first channel parameter wins", hub.Private, "7", "?channel=user:7&channel=user:42", true, "user:7"},
		{"first channel parameter denied", hub.Private, "7", "?channel=user:42&channel=user:7", false, ""},
		{"public, anonymous", hub.Public, "", "?channel=user:42", false, ""},
		{"public, token in query is ignored", hub.Public, "", "?channel=user:42&token=x", false, ""},
		{"public, shared channel", hub.Public, "", "?channel=orders", true, "orders"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeServer{}
			srv := httptest.NewServer(asUser(tt.userID, Subscribe(fake, tt.scope, NewUpgrader(nil))))
			defer srv.Close()

			conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+tt.query, nil)
			if tt.allowed {
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				_ = conn.Close()
			} else if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("err = %v, resp = %v; want 403", err, resp)
			}

			calls := fake.snapshot()
			if !tt.allowed {
				if len(calls) != 0 {
					t.Errorf("Serve called for a denied subscription: %+v", calls)
				}
				return
			}
			if len(calls) != 1 || calls[0] != (serveCall{tt.scope, tt.channel}) {
				t.Errorf("calls = %+v, want channel %q", calls, tt.channel)
			}
		})
	}
}

func TestSubscribeDeniesBeforeUpgrading(t *testing.T) {
	fake := &fakeServer{}
	handler := asUser("7", Subscribe(fake, hub.Private, NewUpgrader(nil)))
	req := withHeaders(httptest.NewRequest(http.MethodGet, "/ws?channel=user:42", nil),
		"Connection", "Upgrade", "Upgrade", "websocket", "Sec-WebSocket-Version", "13", "Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if rec.Header().Get("Upgrade") != "" || rec.Header().Get("Sec-WebSocket-Accept") != "" {
		t.Errorf("denied request carries upgrade headers: %v", rec.Header())
	}
	if strings.Contains(rec.Body.String(), "42") || strings.Contains(rec.Body.String(), "7") {
		t.Errorf("response body %q reveals user ids", rec.Body.String())
	}
}

func TestSubscribeRejectsNonWebSocketRequests(t *testing.T) {
	fake := &fakeServer{}
	handler := Subscribe(fake, hub.Public, NewUpgrader(nil))

	requests := map[string]*http.Request{
		"plain GET":       httptest.NewRequest(http.MethodGet, "/ws/public", nil),
		"missing version": withHeaders(httptest.NewRequest(http.MethodGet, "/ws/public", nil), "Connection", "Upgrade", "Upgrade", "websocket", "Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ=="),
		"wrong version":   withHeaders(httptest.NewRequest(http.MethodGet, "/ws/public", nil), "Connection", "Upgrade", "Upgrade", "websocket", "Sec-WebSocket-Version", "8", "Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ=="),
		"missing key":     withHeaders(httptest.NewRequest(http.MethodGet, "/ws/public", nil), "Connection", "Upgrade", "Upgrade", "websocket", "Sec-WebSocket-Version", "13"),
		"POST":            httptest.NewRequest(http.MethodPost, "/ws/public", nil),
	}
	for name, req := range requests {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("%s: status = %d, want 4xx", name, rec.Code)
		}
	}
	if calls := fake.snapshot(); len(calls) != 0 {
		t.Errorf("Serve called for rejected requests: %+v", calls)
	}
}

func withHeaders(r *http.Request, kv ...string) *http.Request {
	for i := 0; i < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	return r
}

func TestUpgraderOriginPolicy(t *testing.T) {
	allow := []string{"https://app.example", "https://admin.example"}
	tests := []struct {
		name    string
		allowed []string
		origin  string
		want    bool
	}{
		{"no allowlist, no origin", nil, "", true},
		{"no allowlist, any origin", nil, "https://evil.example", true},
		{"allowlist, no origin", allow, "", true},
		{"allowlist, allowed origin", allow, "https://app.example", true},
		{"allowlist, second allowed origin", allow, "https://admin.example", true},
		{"allowlist, foreign origin", allow, "https://evil.example", false},
		{"allowlist, scheme mismatch", allow, "http://app.example", false},
		{"allowlist, port mismatch", allow, "https://app.example:8443", false},
		{"allowlist, suffix attack", allow, "https://app.example.evil.com", false},
		{"allowlist, prefix attack", allow, "https://evilapp.example", false},
		{"allowlist, case mismatch", allow, "https://APP.example", false},
		{"allowlist, null origin", allow, "null", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if got := NewUpgrader(tt.allowed).CheckOrigin(req); got != tt.want {
				t.Errorf("CheckOrigin = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubscribeEnforcesOriginOverTheWire(t *testing.T) {
	fake := &fakeServer{}
	srv := httptest.NewServer(Subscribe(fake, hub.Public, NewUpgrader([]string{"https://app.example"})))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"https://evil.example"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: err = %v, resp = %v; want 403", err, resp)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"https://app.example"}})
	if err != nil {
		t.Fatalf("allowed origin: %v", err)
	}
	_ = conn.Close()
	if calls := fake.snapshot(); len(calls) != 1 {
		t.Errorf("Serve calls = %d, want 1", len(calls))
	}
}
