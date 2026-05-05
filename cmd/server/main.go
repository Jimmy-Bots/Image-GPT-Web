package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gpt-image-web/internal/api"
	"gpt-image-web/internal/config"
	"gpt-image-web/internal/storage"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "run a local healthcheck request")
	flag.Parse()
	if *healthcheck {
		runHealthcheck()
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	log.Printf(
		"config addr=%s data_dir=%s db=%s web_dir=%s images_dir=%s proxy_configured=%t base_url_configured=%t log_level=%s image_workers=%d image_queue=%d image_account_concurrency=%d",
		cfg.Addr,
		cfg.DataDir,
		cfg.DatabasePath,
		cfg.WebDir,
		cfg.ImagesDir,
		cfg.ProxyURL != "",
		cfg.BaseURL != "",
		cfg.LogLevel,
		cfg.ImageWorkerCount,
		cfg.ImageQueueSize,
		cfg.ImageAccountConcurrency,
	)

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

func runHealthcheck() {
	addr := os.Getenv("CHATGPT2API_ADDR")
	if addr == "" {
		addr = ":3000"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Fatalf("healthcheck failed: HTTP %d", resp.StatusCode)
	}
}
