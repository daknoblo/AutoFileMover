// Package watcher watches the configured source folders for filesystem changes
// and triggers processing in a debounced fashion.
package watcher

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// Watcher observes source folders and asks the engine to process changes.
type Watcher struct {
	store    *store.Store
	engine   *engine.Engine
	log      *slog.Logger
	debounce time.Duration

	mu      sync.Mutex
	fsw     *fsnotify.Watcher
	watched map[string]struct{}
	timer   *time.Timer
}

// New creates a new watcher.
func New(st *store.Store, eng *engine.Engine, log *slog.Logger, debounce time.Duration) *Watcher {
	return &Watcher{
		store:    st,
		engine:   eng,
		log:      log,
		debounce: debounce,
		watched:  map[string]struct{}{},
	}
}

// Run starts the watcher until ctx is cancelled. It also performs an initial
// scan and a periodic fallback scan every scanInterval.
func (w *Watcher) Run(ctx context.Context, scanInterval time.Duration) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fsw = fsw
	defer fsw.Close()

	w.Resync(ctx)
	w.engine.ProcessAll(ctx)

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.Resync(ctx)
			w.engine.ProcessAll(ctx)
		case event, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			w.log.Debug("fs event", "op", event.Op.String(), "name", event.Name)
			w.schedule(ctx)
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Error("watcher error", "err", err)
		}
	}
}

// Resync reconciles the set of watched paths with the configured sources.
func (w *Watcher) Resync(ctx context.Context) {
	sources, err := w.store.ListSources(ctx)
	if err != nil {
		w.log.Error("watcher resync: list sources", "err", err)
		return
	}
	want := map[string]struct{}{}
	for _, s := range sources {
		want[s.Path] = struct{}{}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Add new paths.
	for p := range want {
		if _, ok := w.watched[p]; ok {
			continue
		}
		if err := w.fsw.Add(p); err != nil {
			w.log.Warn("watch add failed", "path", p, "err", err)
			continue
		}
		w.watched[p] = struct{}{}
		w.log.Info("watching source", "path", p)
	}
	// Remove stale paths.
	for p := range w.watched {
		if _, ok := want[p]; ok {
			continue
		}
		_ = w.fsw.Remove(p)
		delete(w.watched, p)
		w.log.Info("stopped watching source", "path", p)
	}
}

// schedule debounces processing so a burst of events results in a single scan.
func (w *Watcher) schedule(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() {
		w.engine.ProcessAll(ctx)
	})
}
