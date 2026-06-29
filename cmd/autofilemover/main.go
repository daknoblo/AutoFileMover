// Command autofilemover runs the download-sorting service: it watches source
// folders, classifies new media via an AI endpoint and moves it into the
// matching media library, exposing a web UI for configuration and review.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/store"
	"github.com/daknoblo/AutoFileMover/internal/version"
	"github.com/daknoblo/AutoFileMover/internal/watcher"
	"github.com/daknoblo/AutoFileMover/internal/web"
)

func main() {
	// The distroless runtime image has no shell, so the container HEALTHCHECK
	// calls the binary itself with -healthcheck.
	if len(os.Args) > 1 && (os.Args[1] == "-healthcheck" || os.Args[1] == "healthcheck") {
		os.Exit(healthcheck())
	}

	cfg := config.Load()
	levelVar := new(slog.LevelVar)
	levelVar.Set(logbuf.ParseLevel(os.Getenv("AFM_LOG_LEVEL")))
	logs := logbuf.New(1000, os.Stdout)
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: levelVar}))
	log.Info("starting autofilemover", "version", version.Get().String())

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		log.Error("create data dir", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Apply persisted log level if set.
	if lvl, e := st.GetSetting(context.Background(), "log_level", ""); e == nil && lvl != "" {
		levelVar.Set(logbuf.ParseLevel(lvl))
	}

	eng := engine.New(st, cfg, log)
	w := watcher.New(st, eng, log, 3*time.Second)
	srv := web.NewServer(st, eng, cfg, log, w, logs, levelVar)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the watcher loop.
	go func() {
		if err := w.Run(ctx, cfg.ScanInterval); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("watcher stopped", "err", err)
		}
	}()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Info("starting http server", "addr", cfg.HTTPAddr, "media_root", cfg.MediaRoot)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
}

// healthcheck performs a local request to /api/health and returns a process
// exit code. It is used as the container HEALTHCHECK (the distroless image has
// no shell or curl).
func healthcheck() int {
	addr := os.Getenv("AFM_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
