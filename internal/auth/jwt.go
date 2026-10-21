package auth

import (
	"context"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
)

type userIDKey struct{}

var hmacMethods = []string{
	jwt.SigningMethodHS256.Alg(),
	jwt.SigningMethodHS384.Alg(),
	jwt.SigningMethodHS512.Alg(),
}

func JWTMiddleware(secret string, next http.Handler) http.Handler {
	key := []byte(secret)
	parser := jwt.NewParser(jwt.WithValidMethods(hmacMethods))
	keyFunc := func(*jwt.Token) (any, error) { return key, nil }

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		token, err := parser.Parse(tokenStr, keyFunc)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		subject, _ := token.Claims.GetSubject()
		ctx := context.WithValue(r.Context(), userIDKey{}, subject)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey{}).(string)
	return id, ok
}
