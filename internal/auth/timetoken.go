package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TimeTokens issues and validates tokens of the form
// base64url(window ":" base64url(HMAC-SHA256(secret, window))), where window is
// the unix time divided by the window size. Padding and the standard base64
// alphabet are accepted on both layers for interoperability.
type TimeTokens struct {
	secret []byte
	window int64
	skew   int64
	now    func() time.Time
}

func NewTimeTokens(secret string, windowSeconds, allowedSkew int) *TimeTokens {
	return &TimeTokens{
		secret: []byte(secret),
		window: int64(windowSeconds),
		skew:   int64(allowedSkew),
		now:    time.Now,
	}
}

func (t *TimeTokens) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-API-Token")
		if token == "" {
			http.Error(w, "Token required", http.StatusUnauthorized)
			return
		}
		if !t.Valid(token) {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (t *TimeTokens) Valid(token string) bool {
	decoded, ok := decodeBase64(token)
	if !ok {
		return false
	}

	windowStr, macStr, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false
	}

	window, err := strconv.ParseInt(windowStr, 10, 64)
	if err != nil {
		return false
	}
	current := t.currentWindow()
	if window < current-t.skew || window > current+t.skew {
		return false
	}

	receivedMAC, ok := decodeBase64(macStr)
	if !ok {
		return false
	}
	return hmac.Equal(t.mac(windowStr), receivedMAC)
}

func (t *TimeTokens) Generate() string {
	windowStr := strconv.FormatInt(t.currentWindow(), 10)
	mac := base64.RawURLEncoding.EncodeToString(t.mac(windowStr))
	return base64.RawURLEncoding.EncodeToString([]byte(windowStr + ":" + mac))
}

func (t *TimeTokens) currentWindow() int64 {
	return t.now().Unix() / t.window
}

func (t *TimeTokens) mac(window string) []byte {
	h := hmac.New(sha256.New, t.secret)
	h.Write([]byte(window))
	return h.Sum(nil)
}

var toURLAlphabet = strings.NewReplacer("+", "-", "/", "_")

func decodeBase64(s string) ([]byte, bool) {
	s = toURLAlphabet.Replace(strings.TrimRight(s, "="))
	decoded, err := base64.RawURLEncoding.DecodeString(s)
	return decoded, err == nil
}
