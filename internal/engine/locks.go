package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

// itemLocks serializes mutations per item instead of globally, so a slow scan or
// a long-running file operation on one item never blocks user actions on the
// others. Locks are keyed by source path because a freshly scanned candidate has
// no database id yet.
type itemLocks struct {
	mu sync.Mutex
	m  map[string]*itemLock
}

// itemLock is a context-aware mutex; refs counts the holder plus all waiters so
// the map entry can be dropped once nobody references it.
type itemLock struct {
	ch   chan struct{}
	refs int
}

func newItemLocks() *itemLocks {
	return &itemLocks{m: map[string]*itemLock{}}
}

// acquire blocks until the lock for key is free or ctx is done. The returned
// release func must be called exactly once when it returns a nil error.
func (l *itemLocks) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	entry, ok := l.m[key]
	if !ok {
		entry = &itemLock{ch: make(chan struct{}, 1)}
		l.m[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case entry.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-entry.ch
				l.drop(key, entry)
			})
		}, nil
	case <-ctx.Done():
		l.drop(key, entry)
		return nil, ctx.Err()
	}
}

func (l *itemLocks) drop(key string, entry *itemLock) {
	l.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.m, key)
	}
	l.mu.Unlock()
}

// lockItemByID looks the item up, locks it and re-reads it under the lock so a
// caller never acts on a snapshot that changed while it was waiting.
func (e *Engine) lockItemByID(ctx context.Context, id int64) (*store.Item, func(), error) {
	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return nil, nil, ErrItemNotFound
	}
	release, err := e.locks.acquire(ctx, item.SourcePath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrItemBusy, item.Name)
	}
	item, err = e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		release()
		return nil, nil, ErrItemNotFound
	}
	return item, release, nil
}
