package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/daknoblo/AutoFileMover/internal/scanner"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// RefreshSources reconciles persisted items without classifying or moving
// anything. Even ignored and unstable entries are checked for external changes.
func (e *Engine) RefreshSources(ctx context.Context) error {
	n, err := e.store.PruneOrphanJobs(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		e.log.Info("removed orphan queue jobs", "jobs", n)
	}
	sources, err := e.store.ListSources(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.reconcileSource(ctx, source.Path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) reconcileSource(ctx context.Context, sourcePath string) error {
	// Failure to read a source is not evidence that its children were deleted.
	if _, err := os.ReadDir(sourcePath); err != nil {
		return fmt.Errorf("refresh source %s: %w", sourcePath, err)
	}
	items, err := e.store.ListItems(ctx, "", 0)
	if err != nil {
		return err
	}
	jobs, err := e.store.OpenJobsByItem(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if filepath.Dir(item.SourcePath) != filepath.Clean(sourcePath) {
			continue
		}
		if item.IsHistory() {
			if _, hasWork := jobs[item.ID]; !hasWork {
				continue
			}
		}
		if err := e.refreshItem(ctx, item.ID); err != nil {
			errs = append(errs, fmt.Errorf("refresh %s: %w", item.SourcePath, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) refreshItem(ctx context.Context, id int64) error {
	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return err
	}
	release, ok := e.locks.tryAcquire(item.SourcePath)
	if !ok {
		return nil // An active operation owns the journal until the next refresh.
	}
	defer release()
	item, err = e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return err
	}
	return e.refreshItemFiles(ctx, item)
}

// refreshItemFiles requires the item lock. Read under the lock, not before it:
// an in-flight move may have changed the source while the scan was waiting.
func (e *Engine) refreshItemFiles(ctx context.Context, item *store.Item) error {
	c, err := scanner.Inspect(item.SourcePath)
	if os.IsNotExist(err) {
		entries, readErr := os.ReadDir(filepath.Dir(item.SourcePath))
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if entry.Name() == filepath.Base(item.SourcePath) {
				return err // e.g. a broken symlink, not an absent entry
			}
		}
		if err := e.store.RemoveMissingItem(ctx, item.ID); err != nil {
			return err
		}
		e.log.Info("reconciled missing source entry", "path", item.SourcePath)
		return nil
	}
	if err != nil {
		return err
	}
	if item.IsHistory() {
		return nil
	}
	if c.SkippedFiles > 0 {
		return fmt.Errorf("incomplete file listing: %d unreadable entries", c.SkippedFiles)
	}
	unchanged := make([]store.File, 0, len(c.Files))
	old := make(map[string]store.File, len(item.Files))
	for _, f := range item.Files {
		old[f.RelPath] = f
	}
	for _, f := range c.Files {
		if prev, ok := old[f.RelPath]; ok && !prev.Done && prev.Size == f.Size {
			unchanged = append(unchanged, f)
		}
	}
	if len(c.Files) == 0 {
		unchanged = append(unchanged, store.File{RelPath: ""})
	}
	if err := e.store.RemoveObsoleteFileJobs(ctx, item.ID, unchanged); err != nil {
		return err
	}
	files := reconcileFiles(item.Files, c.Files)
	if !reflect.DeepEqual(files, item.Files) {
		item.Files = files
		if err := e.store.UpsertItem(ctx, item); err != nil {
			return err
		}
		e.log.Info("refreshed source files", "path", item.SourcePath, "files", len(files))
	}
	return nil
}

func reconcileFiles(old, current []store.File) []store.File {
	byPath := make(map[string]store.File, len(old))
	for _, f := range old {
		byPath[f.RelPath] = f
	}
	files := make([]store.File, 0, len(current))
	for _, f := range current {
		if prev, ok := byPath[f.RelPath]; ok && !prev.Done && prev.Size == f.Size {
			f = prev
		} else {
			f.Action = store.FileActionKeep
			f.Reason = "new or changed file; review required"
		}
		files = append(files, f)
		delete(byPath, f.RelPath)
	}
	// Completed actions are a transfer journal, not stale source files.
	for _, f := range old {
		if _, absent := byPath[f.RelPath]; absent && f.Done && f.RelPath != "" {
			files = append(files, f)
		}
	}
	if len(current) == 0 {
		files = append(files, store.File{RelPath: "", Action: store.FileActionDelete, Reason: "empty folder"})
	}
	return files
}
