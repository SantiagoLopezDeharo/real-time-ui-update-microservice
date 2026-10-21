package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"real-time-ui-update-microservice/internal/config"
	"real-time-ui-update-microservice/internal/hub"
	"real-time-ui-update-microservice/internal/server"

	"github.com/joho/godotenv"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h := hub.New(hub.DefaultOptions())
	srv := server.New(":"+cfg.Port, server.Routes(cfg, h))
	errs := make(chan error, 2)

	go func() { errs <- serve(srv) }()
	slog.Info("server listening", "port", cfg.Port)

	if cfg.PprofAddr != "" {
		debug := server.Pprof(cfg.PprofAddr)
		go func() { errs <- serve(debug) }()
		defer debug.Close()
		slog.Info("pprof listening", "addr", cfg.PprofAddr)
	}

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(srv.Shutdown(shutdownCtx), h.Shutdown(shutdownCtx))
}

func serve(srv *http.Server) error {
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
