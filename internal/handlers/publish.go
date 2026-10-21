package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"real-time-ui-update-microservice/internal/hub"
)

type Broadcaster interface {
	Broadcast(scope hub.Scope, channel string, payload []byte) int
}

func Publish(b Broadcaster, scope hub.Scope, maxBodyBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
				http.Error(w, "Body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Error reading body", http.StatusBadRequest)
			return
		}

		if !json.Valid(body) {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		b.Broadcast(scope, channelFrom(r), body)
		w.WriteHeader(http.StatusAccepted)
	}
}

func channelFrom(r *http.Request) string {
	if ch := r.URL.Query().Get("channel"); ch != "" {
		return ch
	}
	return "default"
}
