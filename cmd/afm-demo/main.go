// Command afm-demo runs a self-contained AutoFileMover instance filled with
// sample data. It serves the real web UI and is used both as a local playground
// and as the source for the documentation screenshots.
//
// It never talks to an AI endpoint and never watches the filesystem, so the
// seeded state stays stable while the screenshots are taken.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/demo"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/store"
	"github.com/daknoblo/AutoFileMover/internal/version"
	"github.com/daknoblo/AutoFileMover/internal/web"
)

// noopResyncer stands in for the watcher, which the demo does not run.
type noopResyncer struct{}

func (noopResyncer) Resync(context.Context) {}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address")
	root := flag.String("root", "/tmp/afm-demo/dataroot", "media root for the demo tree")
	dbPath := flag.String("db", "/tmp/afm-demo/demo.db", "path of the demo database")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Build metadata is fixed so the About screenshot does not change per build.
	version.Version = "demo"
	version.Channel = "local"
	version.Commit = "demo"
	version.Date = "2026-05-04T09:00:00Z"

	if err := run(*addr, *root, *dbPath, log); err != nil {
		log.Error("demo failed", "err", err)
		os.Exit(1)
	}
}

func run(addr, root, dbPath string, log *slog.Logger) error {
	// Always start from a clean database so every run seeds identical data.
	if err := os.RemoveAll(dbPath); err != nil {
		return err
	}
	for _, glob := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.RemoveAll(glob); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			log.Error("close store", "err", cerr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := demo.Setup(ctx, st, root); err != nil {
		return err
	}

	cfg := config.Config{
		HTTPAddr:        addr,
		DBPath:          dbPath,
		MediaRoot:       root,
		StabilityWindow: 30 * time.Second,
		ScanInterval:    5 * time.Minute,
	}

	// The log buffer shown in the UI is pre-filled with fixed records and does
	// not receive the live output, which keeps the Logs screenshot stable.
	logs := logbuf.New(500, io.Discard)
	for _, line := range demo.LogLines() {
		if _, werr := logs.Write([]byte(line + "\n")); werr != nil {
			return werr
		}
	}
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)

	eng := engine.New(st, cfg, log)
	srv := web.NewServer(st, eng, cfg, log, noopResyncer{}, logs, level)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("demo ready", "url", "http://"+addr, "media_root", root)
		if lerr := httpServer.ListenAndServe(); lerr != nil && !errors.Is(lerr, http.ErrServerClosed) {
			errCh <- lerr
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
