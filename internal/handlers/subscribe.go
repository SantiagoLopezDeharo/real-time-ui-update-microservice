package handlers

import (
	"net/http"
	"slices"
	"sync"

	"real-time-ui-update-microservice/internal/access"
	"real-time-ui-update-microservice/internal/auth"
	"real-time-ui-update-microservice/internal/hub"

	"github.com/gorilla/websocket"
)

type Server interface {
	Serve(conn *websocket.Conn, scope hub.Scope, channel string)
}

// Subscribe checks channel access before upgrading, so a denied client gets a
// plain 403 and never holds a connection.
func Subscribe(s Server, scope hub.Scope, upgrader *websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channel := channelFrom(r)
		userID, _ := auth.UserID(r.Context())
		if !access.CanSubscribe(channel, userID) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.Serve(conn, scope, channel)
	}
}

// NewUpgrader allows every origin when allowedOrigins is empty. Requests
// without an Origin header come from non-browser clients and are always allowed.
// Write buffers are pooled and read buffers kept small because clients only
// receive, so idle connections hold almost no buffer memory.
func NewUpgrader(allowedOrigins []string) *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  512,
		WriteBufferPool: &sync.Pool{},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			return len(allowedOrigins) == 0 || origin == "" || slices.Contains(allowedOrigins, origin)
		},
	}
}
