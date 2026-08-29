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
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/queue"
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
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("close store", "err", err)
		}
	}()

	// Apply persisted log level if set.
	if lvl, e := st.GetSetting(context.Background(), "log_level", ""); e == nil && lvl != "" {
		levelVar.Set(logbuf.ParseLevel(lvl))
	}

	eng := engine.New(st, cfg, log)
	q := queue.New(st, eng, cfg, log)
	w := watcher.New(st, eng, log, 3*time.Second)
	srv := web.NewServer(st, eng, q, cfg, log, w, logs, levelVar)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the watcher loop.
	go func() {
		if err := w.Run(ctx, cfg.ScanInterval); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("watcher stopped", "err", err)
		}
	}()

	// Start the background queue that executes all filesystem work, so no HTTP
	// request ever waits on the media storage.
	queueDone := make(chan struct{})
	go func() {
		defer close(queueDone)
		if err := q.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("queue worker stopped", "err", err)
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
	// Let the queue finish its current step; an unfinished job is written back
	// as pending and resumes on the next start.
	select {
	case <-queueDone:
	case <-shutdownCtx.Done():
		log.Warn("queue worker did not stop in time")
	}
}

// healthcheck performs a local request to /healthz and returns a process
// exit code. It is used as the container HEALTHCHECK (the distroless image has
// no shell or curl).
func healthcheck() int {
	port := healthcheckPort(os.Getenv("AFM_HTTP_ADDR"))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func healthcheckPort(addr string) string {
	if addr == "" {
		return "8080"
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	if strings.HasPrefix(addr, ":") {
		if port := strings.TrimPrefix(addr, ":"); port != "" {
			return port
		}
		return "8080"
	}
	if !strings.Contains(addr, ":") {
		return addr
	}
	if port := addr[strings.LastIndex(addr, ":")+1:]; port != "" {
		return port
	}
	return "8080"
}
