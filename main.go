// Command timemore-dot-prometheus-exporter receives brews from the Caparaó Android app,
// stores them in SQLite (the source of truth) and copies them to Prometheus with the
// remote write 1.0 protocol, keeping each brew's original timestamps.
//
// Usage:
//
//	timemore-dot-prometheus-exporter          serve the API and run the sync loop (default)
//	timemore-dot-prometheus-exporter serve    same
//	timemore-dot-prometheus-exporter sync     run one sync and exit (exit 1 if it failed)
//	timemore-dot-prometheus-exporter resend   mark every brew unsent, then run one sync
//
// This file reads the configuration from the environment and wires the packages in
// internal/ together.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/api"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/remotewrite"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/store"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/syncer"
)

const recommendedTokenLen = 32

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if len(os.Args) > 2 || (cmd != "serve" && cmd != "sync" && cmd != "resend") {
		fmt.Fprintln(os.Stderr, "usage: timemore-dot-prometheus-exporter [serve|sync|resend]")
		os.Exit(2)
	}

	writeURL := os.Getenv("REMOTE_WRITE_URL")
	if writeURL == "" {
		logger.Fatal("REMOTE_WRITE_URL is required (example: http://prometheus:9090/api/v1/write)")
	}
	parsedURL, err := url.Parse(writeURL)
	if err != nil {
		logger.Fatalf("REMOTE_WRITE_URL: %v", err)
	}
	dbPath := envOr("DB_PATH", "/data/caparao.db")
	interval, err := time.ParseDuration(envOr("SYNC_INTERVAL", "15m"))
	if err != nil || interval <= 0 {
		logger.Fatalf("SYNC_INTERVAL must be a positive duration such as 15m: %q", os.Getenv("SYNC_INTERVAL"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer st.Close()

	sy := &syncer.Syncer{
		Store:  st,
		Writer: remotewrite.NewClient(writeURL, &http.Client{Timeout: 60 * time.Second}),
		Logger: logger,
	}

	switch cmd {
	case "resend":
		n, err := st.ResetSent(ctx)
		if err != nil {
			logger.Fatalf("resend: %v", err)
		}
		logger.Printf("resend: %d brews marked unsent", n)
		fallthrough
	case "sync":
		res, err := sy.Run(ctx)
		logger.Printf("sync: sent %d, rejected %d", res.Sent, res.Rejected)
		if err != nil {
			logger.Printf("ERROR sync: %v (the rest stays pending)", err)
			os.Exit(1)
		}
		return
	}

	token := os.Getenv("AUTH_TOKEN")
	if token == "" {
		logger.Fatal("AUTH_TOKEN is required (generate one with: openssl rand -hex 32)")
	}
	if len(token) < recommendedTokenLen {
		logger.Printf("WARN AUTH_TOKEN is shorter than %d characters; use a long random token outside development", recommendedTokenLen)
	}
	addr := envOr("LISTEN_ADDR", ":8080")

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer(token, st, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		sy.Loop(ctx, interval)
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Printf("listening on %s, database %s, writing to %s every %s", addr, dbPath, parsedURL.Redacted(), interval)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
	<-syncDone
	logger.Print("stopped")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
