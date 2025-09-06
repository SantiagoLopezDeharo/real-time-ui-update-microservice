package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"real-time-ui-update-microservice/cmd/config"
)

// TimeTokenMiddleware validates time-based tokens for API endpoints
func TimeTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Load()

		// Get token from header
		token := r.Header.Get("X-API-Token")
		if token == "" {
			http.Error(w, "Token required", http.StatusUnauthorized)
			return
		}

		// Validate the token
		if !validateTimeToken(token, cfg.TimeTokenSecret, cfg.TimeWindow, cfg.AllowedSkew) {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// validateTimeToken checks if a time-based token is valid
func validateTimeToken(token, secret string, timeWindow, allowedSkew int) bool {
	// Convert URL-safe base64 to standard base64
	token = convertUrlSafeToStandardBase64(token)

	// Token format: base64(epoch_window:hmac)
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		fmt.Println("Base64 decode error:", err)
		return false
	}

	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		fmt.Println("Invalid parts count:", len(parts))
		return false
	}

	// Parse the time window
	window, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		fmt.Println("Window parse error:", err)
		return false
	}

	// Check if the token is within the valid time window
	currentWindow := time.Now().Unix() / int64(timeWindow)
	if window < currentWindow-int64(allowedSkew) ||
		window > currentWindow+int64(allowedSkew) {
		fmt.Printf("Window out of range: %d, current: %d, skew: %d\n", window, currentWindow, allowedSkew)
		return false
	}

	// Validate the HMAC - convert received MAC to URL-safe for comparison
	expectedMAC := generateTimeTokenHMAC(parts[0], secret)
	receivedMAC := convertStandardToUrlSafeBase64(parts[1])

	return hmac.Equal([]byte(expectedMAC), []byte(receivedMAC))
}

// GenerateTimeToken creates a new time-based token (for Node.js backend)
func GenerateTimeToken(secret string, timeWindow int) string {
	currentWindow := time.Now().Unix() / int64(timeWindow)
	windowStr := strconv.FormatInt(currentWindow, 10)
	mac := generateTimeTokenHMAC(windowStr, secret)

	token := fmt.Sprintf("%s:%s", windowStr, mac)
	return base64.StdEncoding.EncodeToString([]byte(token))
}

// generateTimeTokenHMAC creates the HMAC for a time window
func generateTimeTokenHMAC(window, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(window))

	// Use URL-safe base64 encoding to match Node.js
	mac := base64.URLEncoding.EncodeToString(h.Sum(nil))

	// Remove padding for consistency with Node.js implementation
	return strings.TrimRight(mac, "=")
}

// convertUrlSafeToStandardBase64 converts URL-safe base64 to standard base64
func convertUrlSafeToStandardBase64(s string) string {
	// Replace URL-safe characters with standard base64 characters
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")

	// Add padding if needed
	if rem := len(s) % 4; rem != 0 {
		s += strings.Repeat("=", 4-rem)
	}

	return s
}

// convertStandardToUrlSafeBase64 converts standard base64 to URL-safe base64
func convertStandardToUrlSafeBase64(s string) string {
	// Replace standard base64 characters with URL-safe characters
	s = strings.ReplaceAll(s, "+", "-")
	s = strings.ReplaceAll(s, "/", "_")

	// Remove padding
	s = strings.TrimRight(s, "=")

	return s
}
