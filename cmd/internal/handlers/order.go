package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"real-time-ui-update-microservice/cmd/internal/hub"
	"real-time-ui-update-microservice/cmd/internal/models"
)

func HandleOrderUpdate(h *hub.Hub) http.HandlerFunc {
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

		var order models.Order
		if err := json.Unmarshal(body, &order); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Validate order data
		if order.ID == "" || order.Item == "" || order.Amount <= 0 {
			http.Error(w, "Invalid order data", http.StatusBadRequest)
			return
		}

		orderJSON, err := json.Marshal(order)
		if err != nil {
			http.Error(w, "Error processing order", http.StatusInternalServerError)
			return
		}

		// Broadcast to all connected clients
		h.BroadcastMessage(orderJSON)
		w.WriteHeader(http.StatusAccepted)
	}
}
