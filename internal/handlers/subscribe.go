package handlers

import (
	"net/http"
	"slices"

	"real-time-ui-update-microservice/internal/hub"

	"github.com/gorilla/websocket"
)

type Server interface {
	Serve(conn *websocket.Conn, scope hub.Scope, channel string)
}

func Subscribe(s Server, scope hub.Scope, upgrader *websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.Serve(conn, scope, channelFrom(r))
	}
}

// NewUpgrader allows every origin when allowedOrigins is empty. Requests
// without an Origin header come from non-browser clients and are always allowed.
func NewUpgrader(allowedOrigins []string) *websocket.Upgrader {
	return &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			return len(allowedOrigins) == 0 || origin == "" || slices.Contains(allowedOrigins, origin)
		},
	}
}
