package auth

import (
	"context"
	"net/http"

	"real-time-ui-update-microservice/cmd/config"

	"github.com/golang-jwt/jwt/v5"
)

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		cfg := config.Load()

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			return []byte(cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), "userID", claims["sub"])
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
