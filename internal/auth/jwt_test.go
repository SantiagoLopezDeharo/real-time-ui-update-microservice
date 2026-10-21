package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const jwtSecret = "jwt-secret"

func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

type jwtResult struct {
	status int
	userID string
	hasID  bool
	called bool
}

func callJWT(rawQuery string) jwtResult {
	var res jwtResult
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res.called = true
		res.userID, res.hasID = UserID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/ws?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	JWTMiddleware(jwtSecret, next).ServeHTTP(rec, req)
	res.status = rec.Code
	return res
}

func tokenQuery(token string) string {
	return url.Values{"token": {token}}.Encode()
}

func TestJWTMiddlewareAcceptsHMACTokens(t *testing.T) {
	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS256, jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		t.Run(method.Alg(), func(t *testing.T) {
			token := sign(t, method, []byte(jwtSecret), jwt.MapClaims{
				"sub": "user-42",
				"exp": time.Now().Add(time.Hour).Unix(),
			})
			res := callJWT(tokenQuery(token))
			if res.status != http.StatusOK || !res.called {
				t.Fatalf("status = %d, called = %v", res.status, res.called)
			}
			if !res.hasID || res.userID != "user-42" {
				t.Errorf("UserID = %q, %v; want user-42, true", res.userID, res.hasID)
			}
		})
	}
}

func TestJWTMiddlewareWithoutSubject(t *testing.T) {
	res := callJWT(tokenQuery(sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{})))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.status)
	}
	if !res.hasID || res.userID != "" {
		t.Errorf("UserID = %q, %v; want empty, true", res.userID, res.hasID)
	}
}

func TestJWTMiddlewareRejects(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{"sub": "u"})
	parts := strings.Split(valid, ".")

	tests := map[string]string{
		"missing token":       "",
		"empty token":         tokenQuery(""),
		"garbage":             tokenQuery("not-a-jwt"),
		"two segments":        tokenQuery(parts[0] + "." + parts[1]),
		"stripped signature":  tokenQuery(parts[0] + "." + parts[1] + "."),
		"tampered payload":    tokenQuery(parts[0] + ".eyJzdWIiOiJhZG1pbiJ9." + parts[2]),
		"wrong secret":        tokenQuery(sign(t, jwt.SigningMethodHS256, []byte("other"), jwt.MapClaims{"sub": "u"})),
		"empty secret":        tokenQuery(sign(t, jwt.SigningMethodHS256, []byte(""), jwt.MapClaims{"sub": "u"})),
		"alg none":            tokenQuery(sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.MapClaims{"sub": "u"})),
		"rs256":               tokenQuery(sign(t, jwt.SigningMethodRS256, rsaKey, jwt.MapClaims{"sub": "u"})),
		"es256":               tokenQuery(sign(t, jwt.SigningMethodES256, ecKey, jwt.MapClaims{"sub": "u"})),
		"eddsa":               tokenQuery(sign(t, jwt.SigningMethodEdDSA, edKey, jwt.MapClaims{"sub": "u"})),
		"expired":             tokenQuery(sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()})),
		"not yet valid":       tokenQuery(sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{"nbf": time.Now().Add(time.Hour).Unix()})),
		"non numeric exp":     tokenQuery(sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{"exp": "tomorrow"})),
		"non string subject":  tokenQuery(sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{"sub": 42})),
		"token in other key":  url.Values{"jwt": {valid}}.Encode(),
		"token with trailing": tokenQuery(valid + "x"),
	}
	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			res := callJWT(query)
			if res.status != http.StatusUnauthorized || res.called {
				t.Errorf("status = %d, called = %v; want 401, false", res.status, res.called)
			}
		})
	}
}

func TestJWTMiddlewareIgnoresAuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, jwt.SigningMethodHS256, []byte(jwtSecret), jwt.MapClaims{}))
	rec := httptest.NewRecorder()
	JWTMiddleware(jwtSecret, http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestUserIDWithoutMiddleware(t *testing.T) {
	if id, ok := UserID(context.Background()); ok || id != "" {
		t.Errorf("UserID = %q, %v; want empty, false", id, ok)
	}
}

func BenchmarkJWTMiddleware(b *testing.B) {
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u"}).SignedString([]byte(jwtSecret))
	handler := JWTMiddleware(jwtSecret, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/ws?"+tokenQuery(token), nil)
	b.ReportAllocs()
	for b.Loop() {
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func TestWithUserID(t *testing.T) {
	for _, id := range []string{"42", ""} {
		got, ok := UserID(WithUserID(context.Background(), id))
		if !ok || got != id {
			t.Errorf("UserID(WithUserID(%q)) = %q, %v", id, got, ok)
		}
	}
}
