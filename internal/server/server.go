package server

import (
	"net/http"
	"net/http/pprof"
	"time"

	"real-time-ui-update-microservice/internal/auth"
	"real-time-ui-update-microservice/internal/config"
	"real-time-ui-update-microservice/internal/handlers"
	"real-time-ui-update-microservice/internal/hub"
)

func Routes(cfg *config.Config, h *hub.Hub) http.Handler {
	tokens := auth.NewTimeTokens(cfg.TimeTokenSecret, cfg.TimeWindow, cfg.AllowedSkew)
	upgrader := handlers.NewUpgrader(cfg.AllowedOrigins)

	mux := http.NewServeMux()
	mux.Handle("GET /ws", auth.JWTMiddleware(cfg.JWTSecret, handlers.Subscribe(h, hub.Private, upgrader)))
	mux.Handle("GET /ws/public", handlers.Subscribe(h, hub.Public, upgrader))
	mux.Handle("POST /update", tokens.Middleware(handlers.Publish(h, hub.Private, cfg.MaxBodyBytes)))
	mux.Handle("POST /publish", tokens.Middleware(handlers.Publish(h, hub.Public, cfg.MaxBodyBytes)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func New(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

func Pprof(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}
