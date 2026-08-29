package engine

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

func lockTestEngine(t *testing.T) *Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "locks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, config.Config{MediaRoot: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestItemLocksAreIndependent is the regression guard for the original bug: a
// single global mutex meant a slow scan or file move on one item blocked every
// user action on all the others.
func TestItemLocksAreIndependent(t *testing.T) {
	eng := lockTestEngine(t)

	busy, err := eng.locks.acquire(t.Context(), "/data/dl/Slow")
	if err != nil {
		t.Fatal(err)
	}
	defer busy()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err := eng.locks.acquire(ctx, "/data/dl/Other")
	if err != nil {
		t.Fatalf("a different item must not wait for a busy one: %v", err)
	}
	release()
}

// TestItemLockHonoursContext covers the second half: an action on an item that
// is genuinely busy gives up quickly instead of hanging until the HTTP write
// timeout kills the connection.
func TestItemLockHonoursContext(t *testing.T) {
	eng := lockTestEngine(t)

	release, err := eng.locks.acquire(t.Context(), "/data/dl/Busy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := eng.locks.acquire(ctx, "/data/dl/Busy"); err == nil {
		t.Fatal("expected the contended lock to time out")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %v, expected to give up after the context deadline", elapsed)
	}
}

// TestItemLocksReleaseMapEntries makes sure the keyed lock map does not grow
// without bound as items come and go.
func TestItemLocksReleaseMapEntries(t *testing.T) {
	eng := lockTestEngine(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := filepath.Join("/data/dl", string(rune('a'+n%26)))
			release, err := eng.locks.acquire(t.Context(), key)
			if err != nil {
				return
			}
			release()
		}(i)
	}
	wg.Wait()

	eng.locks.mu.Lock()
	remaining := len(eng.locks.m)
	eng.locks.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d lock entries leaked", remaining)
	}
}

// TestExecutePlanPersistsEachFile covers the crash-safety guarantee the queue
// relies on: a plan interrupted after file N resumes at N+1 instead of redoing
// or losing the completed work.
func TestExecutePlanPersistsEachFile(t *testing.T) {
	eng := lockTestEngine(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	destDir := t.TempDir()
	for _, name := range []string{"a.mkv", "b.mkv"} {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	item := &store.Item{
		SourcePath: srcDir,
		Name:       "Release",
		Status:     store.StatusMoving,
		TargetPath: destDir,
		Files: []store.File{
			{RelPath: "a.mkv", Action: store.FileActionMove, TargetPath: filepath.Join(destDir, "a.mkv")},
			// No target and no item fallback would be needed here, but the second
			// file deliberately fails so the first one's persisted state can be
			// checked.
			{RelPath: "missing.mkv", Action: store.FileActionMove, TargetPath: filepath.Join(destDir, "missing.mkv")},
		},
	}
	if err := eng.store.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := eng.executePlan(ctx, item, false); err == nil {
		t.Fatal("expected the missing source file to fail the plan")
	}

	got, err := eng.store.GetItem(ctx, item.ID)
	if err != nil || got == nil {
		t.Fatalf("reload item: %v", err)
	}
	if !got.Files[0].Done {
		t.Fatal("the completed file must be persisted before the failure")
	}
	if got.Files[1].Done {
		t.Fatal("the failed file must not be marked done")
	}
}
