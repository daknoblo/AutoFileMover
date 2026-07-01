package watcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// TestWatcherRunShutsDownCleanly verifies that Run returns promptly when its
// context is cancelled and that a pending debounced scan does not keep it alive
// (the stopTimer cleanup on shutdown).
func TestWatcherRunShutsDownCleanly(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx0 := context.Background()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSource(ctx0, src); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(st, config.Config{MediaRoot: dir, StabilityWindow: time.Hour}, log)
	w := New(st, eng, log, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(ctx0)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, time.Hour) }()
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; want nil or context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not shut down within 2s of cancellation")
	}
}
