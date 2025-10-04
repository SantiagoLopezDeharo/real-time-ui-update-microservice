package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"real-time-ui-update-microservice/cmd/internal/hub"
)

func HandleOrderUpdate(h *hub.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Read the raw request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Error reading body", http.StatusBadRequest)
			return
		}

		// Basic validation - ensure it's valid JSON
		if !isValidJSON(body) {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Broadcast the raw JSON to authenticated clients (used by backend)
		h.BroadcastToAuthenticated(body)
		w.WriteHeader(http.StatusAccepted)
	}
}

	// HandleOrderPublish is a public endpoint that broadcasts updates to public (unauthenticated) clients
	func HandleOrderPublish(h *hub.Hub) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "Error reading body", http.StatusBadRequest)
				return
			}

			if !isValidJSON(body) {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}

			// Broadcast to public clients
			h.BroadcastToPublic(body)
			w.WriteHeader(http.StatusAccepted)
		}
	}

// isValidJSON checks if the byte slice contains valid JSON
func isValidJSON(data []byte) bool {
	// For a generic service, we just check if it can be unmarshaled into interface{}
	var js interface{}
	return json.Unmarshal(data, &js) == nil
}
