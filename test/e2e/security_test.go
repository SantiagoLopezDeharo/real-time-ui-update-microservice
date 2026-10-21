//go:build e2e

package e2e

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

func TestPrivateWebSocketRejectsBadJWTs(t *testing.T) {
	s := startServer(t)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	key := []byte(s.jwtSecret)
	valid := s.userJWT(t, "u")
	parts := strings.Split(valid, ".")

	tokens := map[string]string{
		"missing":            "",
		"garbage":            "garbage",
		"wrong secret":       signJWT(t, jwt.SigningMethodHS256, []byte("guess"), jwt.MapClaims{"sub": "u"}),
		"alg none":           signJWT(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.MapClaims{"sub": "admin"}),
		"rs256":              signJWT(t, jwt.SigningMethodRS256, rsaKey, jwt.MapClaims{"sub": "u"}),
		"es256":              signJWT(t, jwt.SigningMethodES256, ecKey, jwt.MapClaims{"sub": "u"}),
		"expired":            signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"sub": "u", "exp": time.Now().Add(-time.Second).Unix()}),
		"not yet valid":      signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"sub": "u", "nbf": time.Now().Add(time.Hour).Unix()}),
		"numeric subject":    signJWT(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"sub": 1}),
		"tampered payload":   parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin"}`)) + "." + parts[2],
		"stripped signature": parts[0] + "." + parts[1] + ".",
		"time token as jwt":  s.token(),
	}
	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			conn, resp, err := s.dial(private, "secure", token, nil)
			if err == nil {
				_ = conn.Close()
				t.Fatal("handshake succeeded")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("resp = %v, want 401", resp)
			}
		})
	}

	ok, _, err := s.dial(private, "secure", valid, nil)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	_ = ok.Close()
}

func TestTokenInHeaderDoesNotAuthenticateWebSocket(t *testing.T) {
	s := startServer(t)
	header := http.Header{"Authorization": {"Bearer " + s.userJWT(t, "u")}}
	_, resp, err := websocket.DefaultDialer.Dial(s.wsURL+"/ws?channel=x", header)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v resp = %v, want 401", err, resp)
	}
}

func TestPublicEndpointCannotBeEscalatedToPrivate(t *testing.T) {
	s := startServer(t)
	query := url.Values{"channel": {"payroll"}, "token": {s.userJWT(t, "intruder")}, "scope": {"private"}}
	conn, _, err := websocket.DefaultDialer.Dial(s.wsURL+"/ws/public?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	intruder := &user{scope: public, channel: "payroll", conn: conn, notify: make(chan struct{}, 1), done: make(chan struct{})}
	go intruder.read()
	defer conn.Close()

	insider := s.connect(t, private, "payroll")
	s.awaitSubscribed(t, intruder, insider)

	s.mustPublish(t, private, "payroll", []byte(`{"salary":"secret"}`))
	if got := string(insider.next(t, deliveryTimeout)); got != `{"salary":"secret"}` {
		t.Fatalf("insider got %s", got)
	}
	intruder.expectNothing(t, 300*time.Millisecond)
}

func TestPublishEndpointsRejectBadTimeTokens(t *testing.T) {
	s := startServer(t, withEnv("ALLOWED_CLOCK_SKEW", "1"))
	sign := func(window string, secret string) string {
		h := hmac.New(sha256.New, []byte(secret))
		h.Write([]byte(window))
		return base64.RawURLEncoding.EncodeToString([]byte(window + ":" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))))
	}
	current := time.Now().Unix() / s.window
	at := func(offset int64) string { return strconv.FormatInt(current+offset, 10) }

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"garbage", "garbage", http.StatusUnauthorized},
		{"jwt instead", s.userJWT(t, "u"), http.StatusUnauthorized},
		{"wrong secret", sign(at(0), "guess"), http.StatusUnauthorized},
		{"two windows old", sign(at(-2), s.timeSecret), http.StatusUnauthorized},
		{"two windows ahead", sign(at(2), s.timeSecret), http.StatusUnauthorized},
		{"far future", sign("9223372036854775807", s.timeSecret), http.StatusUnauthorized},
		{"negative window", sign("-1", s.timeSecret), http.StatusUnauthorized},
		{"no mac", base64.RawURLEncoding.EncodeToString([]byte(at(0) + ":")), http.StatusUnauthorized},
		{"no separator", base64.RawURLEncoding.EncodeToString([]byte(at(0))), http.StatusUnauthorized},
		{"plaintext", at(0) + ":abc", http.StatusUnauthorized},
		{"previous window within skew", sign(at(-1), s.timeSecret), http.StatusAccepted},
		{"next window within skew", sign(at(1), s.timeSecret), http.StatusAccepted},
		{"current window", sign(at(0), s.timeSecret), http.StatusAccepted},
		{"standard base64 with padding", base64.StdEncoding.EncodeToString(mustDecode(t, s.token())), http.StatusAccepted},
	}
	for _, path := range []string{"/update", "/publish"} {
		for _, tt := range tests {
			header := http.Header{}
			if tt.token != "" {
				header.Set("X-API-Token", tt.token)
			}
			res := s.request(t, http.MethodPost, path+"?channel=x", header, []byte(`{}`))
			if res.status != tt.want {
				t.Errorf("%s %s: status = %d, want %d", path, tt.name, res.status, tt.want)
			}
			assertNoLeak(t, s, res.body)
		}
	}
}

func mustDecode(t *testing.T, token string) []byte {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTimeTokenExpiresAfterItsWindow(t *testing.T) {
	s := startServer(t, withEnv("TIME_WINDOW_SECONDS", "1"), withEnv("ALLOWED_CLOCK_SKEW", "0"))
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 50*time.Millisecond)))

	token := s.token()
	header := http.Header{"X-Api-Token": {token}}
	if res := s.request(t, http.MethodPost, "/publish", header, []byte(`{}`)); res.status != http.StatusAccepted {
		t.Fatalf("fresh token: status = %d", res.status)
	}
	time.Sleep(2 * time.Second)
	if res := s.request(t, http.MethodPost, "/publish", header, []byte(`{}`)); res.status != http.StatusUnauthorized {
		t.Fatalf("replayed token after window: status = %d, want 401", res.status)
	}
}

func TestTimeTokenOnlyAcceptedInHeader(t *testing.T) {
	s := startServer(t)
	for _, res := range []response{
		s.request(t, http.MethodPost, "/publish?token="+s.token(), nil, []byte(`{}`)),
		s.request(t, http.MethodPost, "/publish", http.Header{"Authorization": {"Bearer " + s.token()}}, []byte(`{}`)),
		s.request(t, http.MethodPost, "/publish", http.Header{"Cookie": {"X-API-Token=" + s.token()}}, []byte(`{}`)),
	} {
		if res.status != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", res.status)
		}
	}
}

func TestRejectedPublishesAreNeverDelivered(t *testing.T) {
	s := startServer(t, withEnv("MAX_BODY_BYTES", "1024"))
	u := s.connect(t, public, "guarded")
	s.awaitSubscribed(t, u)

	auth := http.Header{"X-Api-Token": {s.token()}}
	attempts := []struct {
		name   string
		header http.Header
		body   string
		want   int
	}{
		{"unauthenticated", nil, `{"evil":1}`, http.StatusUnauthorized},
		{"invalid json", auth, `{"evil":`, http.StatusBadRequest},
		{"two documents", auth, `{"a":1}{"evil":2}`, http.StatusBadRequest},
		{"trailing garbage", auth, `{"a":1} <script>`, http.StatusBadRequest},
		{"empty", auth, ``, http.StatusBadRequest},
		{"oversized", auth, `{"evil":"` + strings.Repeat("x", 2048) + `"}`, http.StatusRequestEntityTooLarge},
		{"oversized unauthenticated", nil, `{"evil":"` + strings.Repeat("x", 4<<20) + `"}`, http.StatusUnauthorized},
	}
	for _, a := range attempts {
		res := s.request(t, http.MethodPost, "/publish?channel=guarded", a.header, []byte(a.body))
		if res.status != a.want {
			t.Errorf("%s: status = %d, want %d", a.name, res.status, a.want)
		}
		assertNoLeak(t, s, res.body)
	}

	s.mustPublish(t, public, "guarded", []byte(`{"legit":true}`))
	if got := string(u.next(t, deliveryTimeout)); got != `{"legit":true}` {
		t.Fatalf("first delivered message = %s; a rejected publish leaked through", got)
	}
}

func TestMethodAndRouteHardening(t *testing.T) {
	s := startServer(t)
	auth := http.Header{"X-Api-Token": {s.token()}}
	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/update", http.StatusMethodNotAllowed},
		{http.MethodPut, "/update", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/publish", http.StatusMethodNotAllowed},
		{http.MethodOptions, "/publish", http.StatusMethodNotAllowed},
		{"TRACE", "/publish", http.StatusMethodNotAllowed},
		{http.MethodPost, "/ws", http.StatusMethodNotAllowed},
		{http.MethodPost, "/ws/public", http.StatusMethodNotAllowed},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodGet, "/admin", http.StatusNotFound},
		{http.MethodGet, "/debug/pprof/", http.StatusNotFound},
		{http.MethodGet, "/debug/pprof/goroutine", http.StatusNotFound},
		{http.MethodGet, "/.env", http.StatusNotFound},
		{http.MethodPost, "/update/", http.StatusNotFound},
		{http.MethodPost, "/UPDATE", http.StatusNotFound},
		{http.MethodGet, "/ws/public/../../etc/passwd", http.StatusNotFound},
	}
	for _, tt := range tests {
		res := s.request(t, tt.method, tt.path, auth, []byte(`{}`))
		if res.status != tt.want {
			t.Errorf("%s %s: status = %d, want %d", tt.method, tt.path, res.status, tt.want)
		}
		assertNoLeak(t, s, res.body)
	}
}

func TestOriginAllowlist(t *testing.T) {
	s := startServer(t, withEnv("ALLOWED_ORIGINS", "https://app.example,https://admin.example"))
	tests := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"https://app.example", true},
		{"https://admin.example", true},
		{"https://evil.example", false},
		{"https://app.example.evil.example", false},
		{"http://app.example", false},
		{"null", false},
	}
	for _, sc := range []scope{public, private} {
		for _, tt := range tests {
			header := http.Header{}
			if tt.origin != "" {
				header.Set("Origin", tt.origin)
			}
			conn, resp, err := s.dial(sc, "c", s.userJWT(t, "u"), header)
			if tt.want {
				if err != nil {
					t.Errorf("%s origin %q rejected: %v", sc, tt.origin, err)
					continue
				}
				_ = conn.Close()
				continue
			}
			if err == nil {
				_ = conn.Close()
				t.Errorf("%s origin %q accepted", sc, tt.origin)
			} else if resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s origin %q: resp = %v, want 403", sc, tt.origin, resp)
			}
		}
	}
}

func TestOversizedClientFrameClosesOnlyThatConnection(t *testing.T) {
	s := startServer(t)
	attacker := s.connect(t, public, "room")
	victim := s.connect(t, public, "room")
	s.awaitSubscribed(t, attacker, victim)

	_ = attacker.conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("A", 64<<10)))
	if err := attacker.waitClosed(t, 5*time.Second); !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Errorf("attacker close error = %v, want 1009", err)
	}

	s.mustPublish(t, public, "room", []byte(`{"ok":1}`))
	if got := string(victim.next(t, deliveryTimeout)); got != `{"ok":1}` {
		t.Fatalf("victim got %s", got)
	}
}

func TestProtocolViolationsAreRejected(t *testing.T) {
	s := startServer(t)

	t.Run("unmasked client frame", func(t *testing.T) {
		conn, reader := rawHandshake(t, s, "/ws/public?channel=raw")
		if _, err := conn.Write([]byte{0x81, 0x05, 'h', 'e', 'l', 'l', 'o'}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		data, _ := io.ReadAll(reader)
		if len(data) < 4 || data[0] != 0x88 || int(data[2])<<8|int(data[3]) != websocket.CloseProtocolError {
			t.Fatalf("expected close frame 1002, got % x", data)
		}
	})

	t.Run("invalid websocket version", func(t *testing.T) {
		status := rawRequest(t, s, "GET /ws/public HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 8\r\n\r\n")
		if !strings.Contains(status, " 400 ") && !strings.Contains(status, " 426 ") {
			t.Fatalf("status line = %q", status)
		}
	})

	t.Run("plain http on websocket path", func(t *testing.T) {
		if res := s.request(t, http.MethodGet, "/ws/public", nil, nil); res.status != http.StatusBadRequest {
			t.Fatalf("status = %d", res.status)
		}
	})

	t.Run("garbage bytes", func(t *testing.T) {
		conn, err := net.Dial("tcp", s.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("\x00\x01\x02\xff\xfe garbage \r\n\r\n"))
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		if !strings.Contains(line, "400") {
			t.Fatalf("response = %q, want 400", line)
		}
	})

	if res := s.request(t, http.MethodGet, "/healthz", nil, nil); res.status != http.StatusOK {
		t.Fatalf("server unhealthy after protocol abuse: %d", res.status)
	}
}

func TestOversizedHeadersRejected(t *testing.T) {
	s := startServer(t)
	status := rawRequest(t, s, "GET /healthz HTTP/1.1\r\nHost: x\r\nX-Big: "+strings.Repeat("a", 128<<10)+"\r\n\r\n")
	if !strings.Contains(status, " 431 ") {
		t.Fatalf("status line = %q, want 431", status)
	}
}

func TestSlowlorisConnectionsAreCut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the header read timeout")
	}
	s := startServer(t)
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\n"))

	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, _ = io.ReadAll(conn)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("incomplete request held the connection for %s", elapsed)
	}
}

func TestConnectionFloodDoesNotLeak(t *testing.T) {
	s := startServer(t)
	baseline := s.goroutines(t)

	const waves, perWave = 5, 200
	for range waves {
		users := s.connectMany(t, public, "flood", perWave)
		for i, u := range users {
			if i%2 == 0 {
				_ = u.conn.UnderlyingConn().Close()
			} else {
				_ = u.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				_ = u.conn.Close()
			}
		}
	}
	for range 200 {
		conn, err := net.Dial("tcp", s.addr)
		if err == nil {
			_, _ = conn.Write([]byte("GET /ws/public HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n"))
			_ = conn.Close()
		}
	}

	if after := s.waitGoroutinesBelow(t, baseline+5, 15*time.Second); after > baseline+5 {
		t.Fatalf("goroutines grew from %d to %d after %d abandoned connections", baseline, after, waves*perWave)
	}
	if res := s.request(t, http.MethodGet, "/healthz", nil, nil); res.status != http.StatusOK {
		t.Fatalf("server unhealthy after flood: %d", res.status)
	}
}

var leakPattern = regexp.MustCompile(`goroutine|\.go:\d+|panic|runtime\.|/Users/|/home/`)

func assertNoLeak(t *testing.T, s *server, body string) {
	t.Helper()
	for _, secret := range []string{s.jwtSecret, s.timeSecret} {
		if strings.Contains(body, secret) {
			t.Errorf("response leaks a secret: %q", body)
		}
	}
	if leakPattern.MatchString(body) {
		t.Errorf("response leaks internals: %q", body)
	}
}

func rawHandshake(t *testing.T, s *server, target string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _ = conn.Write([]byte("GET " + target + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d", resp.StatusCode)
	}
	return conn, reader
}

func rawRequest(t *testing.T, s *server, request string) string {
	t.Helper()
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(request))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, _ := bufio.NewReader(conn).ReadString('\n')
	return line
}
