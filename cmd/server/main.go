package main

import (
	"log"
	"net/http"

	"real-time-ui-update-microservice/cmd/config"
	"real-time-ui-update-microservice/cmd/internal/auth"
	"real-time-ui-update-microservice/cmd/internal/handlers"
	"real-time-ui-update-microservice/cmd/internal/hub"

	"github.com/gorilla/mux"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Create a new hub
	h := hub.NewHub()

	// Run the hub in a goroutine
	go h.Run()

	r := mux.NewRouter()

	// WebSocket endpoint with JWT auth
	r.Handle("/ws", auth.Middleware(handlers.HandleWebSocket(h)))

	// Order update endpoint with time-based token authentication
	r.Handle("/update", auth.TimeTokenMiddleware(handlers.HandleOrderUpdate(h))).Methods("POST")

	log.Printf("Server starting on :%s", cfg.Port)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, r))
}
