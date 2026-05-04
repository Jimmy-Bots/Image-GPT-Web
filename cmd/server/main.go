package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gpt-image-web/internal/api"
	"gpt-image-web/internal/config"
	"gpt-image-web/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	store, err := storage.Open(ctx, cfg.DatabasePath, cfg.DBMaxOpenConns)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer store.Close()

	serverApp, err := api.NewServer(cfg, store)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}
	defer serverApp.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           serverApp.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("gpt-image-web listening on %s", cfg.Addr)
		errCh <- srv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
