package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testWindow = 30

func fixedTokens(secret string, skew int, now time.Time) *TimeTokens {
	tokens := NewTimeTokens(secret, testWindow, skew)
	tokens.now = func() time.Time { return now }
	return tokens
}

func rawMAC(secret, window string) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(window))
	return h.Sum(nil)
}

func buildToken(window string, mac []byte, outer, inner *base64.Encoding) string {
	return outer.EncodeToString([]byte(window + ":" + inner.EncodeToString(mac)))
}

func windowAt(now time.Time, offset int64) string {
	return strconv.FormatInt(now.Unix()/testWindow+offset, 10)
}

var now = time.Unix(1_760_000_000, 0)

func TestGenerateRoundTrip(t *testing.T) {
	tokens := fixedTokens("secret", 0, now)
	token := tokens.Generate()
	if !tokens.Valid(token) {
		t.Fatalf("generated token %q is not valid", token)
	}
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("generated token %q is not raw URL-safe base64", token)
	}
}

func TestValidMatchesNodeReferenceImplementation(t *testing.T) {
	vectors := []struct {
		secret string
		window int64
		token  string
	}{
		{"your-time-token-secret", 483120, "NDgzMTIwOmVKS21McDlqUzNWWVkxWWtJNGdMX2tQYWFvV0wwNVFDeTVRYjlwWlRkNWc"},
		{"s3cr3t", 1, "MTo0dGFiTUdNOXZESV95dGZ6Mm5yc21RTmFudWtibV8wd0VldEs5NlUxYWM0"},
	}
	for _, v := range vectors {
		tokens := fixedTokens(v.secret, 0, time.Unix(v.window*testWindow, 0))
		if !tokens.Valid(v.token) {
			t.Errorf("node token for window %d rejected", v.window)
		}
		if got := tokens.Generate(); got != v.token {
			t.Errorf("Generate() = %q, want node token %q", got, v.token)
		}
	}
}

func TestValidAcceptsEveryBase64Flavour(t *testing.T) {
	tokens := fixedTokens("secret", 0, now)
	window := windowAt(now, 0)
	mac := rawMAC("secret", window)
	encodings := map[string]*base64.Encoding{
		"raw-url": base64.RawURLEncoding,
		"url":     base64.URLEncoding,
		"raw-std": base64.RawStdEncoding,
		"std":     base64.StdEncoding,
	}
	for outerName, outer := range encodings {
		for innerName, inner := range encodings {
			if token := buildToken(window, mac, outer, inner); !tokens.Valid(token) {
				t.Errorf("outer=%s inner=%s token %q rejected", outerName, innerName, token)
			}
		}
	}
}

func TestValidClockSkew(t *testing.T) {
	tests := []struct {
		skew   int
		offset int64
		want   bool
	}{
		{0, 0, true},
		{0, -1, false},
		{0, 1, false},
		{1, -1, true},
		{1, 1, true},
		{1, -2, false},
		{1, 2, false},
		{3, -3, true},
		{3, 3, true},
		{3, -4, false},
		{3, 4, false},
	}
	for _, tt := range tests {
		tokens := fixedTokens("secret", tt.skew, now)
		window := windowAt(now, tt.offset)
		token := buildToken(window, rawMAC("secret", window), base64.RawURLEncoding, base64.RawURLEncoding)
		if got := tokens.Valid(token); got != tt.want {
			t.Errorf("skew=%d offset=%d: Valid = %v, want %v", tt.skew, tt.offset, got, tt.want)
		}
	}
}

func TestValidWindowBoundaries(t *testing.T) {
	windowStart := time.Unix(now.Unix()/testWindow*testWindow, 0)
	issuer := fixedTokens("secret", 0, windowStart.Add(-time.Second))
	token := issuer.Generate()

	if !fixedTokens("secret", 0, windowStart.Add(-time.Second)).Valid(token) {
		t.Error("token rejected in its own window")
	}
	if fixedTokens("secret", 0, windowStart).Valid(token) {
		t.Error("token accepted after its window ended with zero skew")
	}
	if !fixedTokens("secret", 1, windowStart.Add(testWindow*time.Second-time.Second)).Valid(token) {
		t.Error("token rejected within one window of skew")
	}
	if fixedTokens("secret", 1, windowStart.Add(testWindow*time.Second)).Valid(token) {
		t.Error("token accepted beyond allowed skew")
	}
}

func TestValidRejectsMalformedTokens(t *testing.T) {
	tokens := fixedTokens("secret", 1, now)
	window := windowAt(now, 0)
	mac := rawMAC("secret", window)
	macStr := base64.RawURLEncoding.EncodeToString(mac)
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	tamperedMAC := append([]byte(nil), mac...)
	tamperedMAC[0] ^= 0x01

	tests := map[string]string{
		"empty":                "",
		"not base64":           "!!!not-base64!!!",
		"only padding":         "====",
		"no separator":         enc(window + macStr),
		"empty window":         enc(":" + macStr),
		"empty mac":            enc(window + ":"),
		"non numeric window":   enc("abc:" + macStr),
		"float window":         enc(window + ".5:" + macStr),
		"window overflow":      enc("99999999999999999999999:" + macStr),
		"extra segment":        enc(window + ":" + macStr + ":extra"),
		"mac not base64":       enc(window + ":***"),
		"wrong secret":         buildToken(window, rawMAC("other", window), base64.RawURLEncoding, base64.RawURLEncoding),
		"tampered mac":         buildToken(window, tamperedMAC, base64.RawURLEncoding, base64.RawURLEncoding),
		"truncated mac":        buildToken(window, mac[:16], base64.RawURLEncoding, base64.RawURLEncoding),
		"mac for other window": buildToken(window, rawMAC("secret", windowAt(now, 1)), base64.RawURLEncoding, base64.RawURLEncoding),
		"raw mac bytes":        enc(window + ":" + string(mac)),
		"plain text token":     window + ":" + macStr,
		"whitespace":           " " + enc(window+":"+macStr),
		"negative window":      buildToken("-1", rawMAC("secret", "-1"), base64.RawURLEncoding, base64.RawURLEncoding),
		"far future window":    buildToken("9223372036854775807", rawMAC("secret", "9223372036854775807"), base64.RawURLEncoding, base64.RawURLEncoding),
	}
	for name, token := range tests {
		if tokens.Valid(token) {
			t.Errorf("%s: token %q accepted", name, token)
		}
	}
}

func TestValidRejectsTokenFromEmptySecretIssuer(t *testing.T) {
	window := windowAt(now, 0)
	token := buildToken(window, rawMAC("", window), base64.RawURLEncoding, base64.RawURLEncoding)
	if fixedTokens("secret", 1, now).Valid(token) {
		t.Error("token signed with empty secret accepted")
	}
}

func TestMiddleware(t *testing.T) {
	tokens := fixedTokens("secret", 0, now)
	expired := fixedTokens("secret", 0, now.Add(-time.Hour)).Generate()

	tests := []struct {
		name       string
		token      string
		setHeader  bool
		wantStatus int
		wantBody   string
		wantNext   bool
	}{
		{"missing header", "", false, http.StatusUnauthorized, "Token required", false},
		{"empty header", "", true, http.StatusUnauthorized, "Token required", false},
		{"invalid token", "garbage", true, http.StatusUnauthorized, "Invalid or expired token", false},
		{"expired token", expired, true, http.StatusUnauthorized, "Invalid or expired token", false},
		{"valid token", tokens.Generate(), true, http.StatusTeapot, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusTeapot)
			})
			req := httptest.NewRequest(http.MethodPost, "/update", nil)
			if tt.setHeader {
				req.Header.Set("X-API-Token", tt.token)
			}
			rec := httptest.NewRecorder()
			tokens.Middleware(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if called != tt.wantNext {
				t.Errorf("next called = %v, want %v", called, tt.wantNext)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("response leaks secret: %q", rec.Body.String())
			}
		})
	}
}

func TestMiddlewareIgnoresTokenOutsideHeader(t *testing.T) {
	tokens := fixedTokens("secret", 0, now)
	req := httptest.NewRequest(http.MethodPost, "/update?token="+tokens.Generate(), nil)
	req.Header.Set("Authorization", "Bearer "+tokens.Generate())
	rec := httptest.NewRecorder()
	tokens.Middleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestNewTimeTokensUsesWallClock(t *testing.T) {
	tokens := NewTimeTokens("secret", testWindow, 0)
	if !tokens.Valid(tokens.Generate()) {
		t.Error("token generated with the wall clock rejected")
	}
}

func FuzzValid(f *testing.F) {
	tokens := fixedTokens("secret", 1, now)
	f.Add(tokens.Generate())
	f.Add("")
	f.Add("MTo0dGFiTUdNOXZESV95dGZ6Mm5yc21RTmFudWtibV8wd0VldEs5NlUxYWM0")
	f.Add("====")
	f.Fuzz(func(t *testing.T, token string) {
		tokens.Valid(token)
	})
}

func BenchmarkValid(b *testing.B) {
	tokens := fixedTokens("secret", 1, now)
	token := tokens.Generate()
	b.ReportAllocs()
	for b.Loop() {
		if !tokens.Valid(token) {
			b.Fatal("token rejected")
		}
	}
}
