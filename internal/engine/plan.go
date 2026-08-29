package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daknoblo/AutoFileMover/internal/ai"
	"github.com/daknoblo/AutoFileMover/internal/mover"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// ApplyFileAction performs a single planned action (move or delete) for one file
// inside an item. It is blocked while What-If is enabled.
func (e *Engine) ApplyFileAction(ctx context.Context, id int64, relPath, action string) error {
	item, release, err := e.lockItemByID(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	if settings, err := e.store.LoadAppSettings(ctx); err == nil && settings.DryRun {
		return ErrDryRun
	}
	idx := -1
	for i := range item.Files {
		if item.Files[i].RelPath == relPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrFileNotFound
	}
	e.startPhase(PhaseMoving, 1)
	defer e.finishProgress()
	e.updateProgress(0, filepath.Base(relPath))
	if err := e.execFile(ctx, item, &item.Files[idx], action); err != nil {
		// Persist regardless so a partially applied change is never lost.
		item.Status = store.StatusError
		item.ErrorMessage = err.Error()
		if uerr := e.store.UpsertItem(ctx, item); uerr != nil {
			e.log.Error("persist file action error", "id", id, "err", uerr)
		}
		return err
	}
	e.finalize(ctx, item)
	if !pendingWork(item.Files) {
		item.Status = store.StatusConfirmed
		item.ErrorMessage = ""
	}
	return e.store.UpsertItem(ctx, item)
}

// PlanFileAction sets the planned action for a single file WITHOUT touching the
// filesystem. The review UI uses it for the per-file toggle buttons; execution
// happens later via ApplyPlan ("Apply").
func (e *Engine) PlanFileAction(ctx context.Context, id int64, relPath, action string) error {
	switch action {
	case store.FileActionMove, store.FileActionDelete, store.FileActionKeep:
	default:
		return ErrInvalidAction
	}
	item, release, err := e.lockItemByID(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	idx := -1
	for i := range item.Files {
		if item.Files[i].RelPath == relPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrFileNotFound
	}
	f := &item.Files[idx]
	if f.Done {
		return ErrFileDone
	}
	f.Action = action
	f.Overwrite = false
	f.OverwritePath = ""
	f.Conflict = nil
	if action == store.FileActionMove && relPath != "" && item.TargetPath != "" {
		f.TargetPath = filepath.Join(item.TargetPath, filepath.Base(relPath))
	} else {
		f.TargetPath = ""
	}
	e.detectConflicts(item.Files)
	// Manually planning an action means the user is taking over a failed or
	// unresolved classification: clear the error state and route it as review.
	if item.Status == store.StatusError {
		item.Status = store.StatusPendingReview
		item.ErrorMessage = ""
	}
	return e.store.UpsertItem(ctx, item)
}

// executePlan runs every undecided move/delete file then cleans up. When
// reportProgress is true it updates the shared Progress per file so the UI can
// show the running file operation; the scan path passes false (its own progress
// already covers it). Every completed file is persisted immediately so an
// interrupted multi-file plan resumes exactly where it stopped.
func (e *Engine) executePlan(ctx context.Context, item *store.Item, reportProgress bool) error {
	done := 0
	for i := range item.Files {
		f := &item.Files[i]
		if f.Done || (f.Action != store.FileActionMove && f.Action != store.FileActionDelete) {
			continue
		}
		if reportProgress {
			e.updateProgress(done, filepath.Base(f.RelPath))
		}
		if err := e.execFile(ctx, item, f, f.Action); err != nil {
			if uerr := e.store.UpsertItem(ctx, item); uerr != nil {
				e.log.Error("persist partial plan", "item", item.Name, "err", uerr)
			}
			return err
		}
		done++
		if uerr := e.store.UpsertItem(ctx, item); uerr != nil {
			e.log.Error("persist plan progress", "item", item.Name, "err", uerr)
		}
	}
	if reportProgress {
		e.updateProgress(done, "")
	}
	e.finalize(ctx, item)
	return nil
}

// countPending counts files still waiting for a move/delete.
func countPending(files []store.File) int {
	n := 0
	for _, f := range files {
		if !f.Done && (f.Action == store.FileActionMove || f.Action == store.FileActionDelete) {
			n++
		}
	}
	return n
}

// execFile moves or deletes a single file and marks it done.
func (e *Engine) execFile(ctx context.Context, item *store.Item, f *store.File, action string) error {
	src := filepath.Join(item.SourcePath, f.RelPath)
	if item.IsSingleFile() {
		src = item.SourcePath
	}
	switch action {
	case store.FileActionMove:
		dest := f.TargetPath
		if dest == "" && item.TargetPath != "" {
			dest = filepath.Join(item.TargetPath, filepath.Base(f.RelPath))
		}
		if dest == "" {
			return fmt.Errorf("%w: %s", ErrNoTarget, f.RelPath)
		}
		if f.Overwrite {
			// The user chose to replace a colliding target file: remove it first
			// so the move can proceed (Move otherwise refuses to overwrite). The
			// file to delete may differ from dest for a same-episode collision.
			rm := f.OverwritePath
			if rm == "" {
				rm = dest
			}
			if err := mover.Delete(ctx, rm); err != nil {
				return fmt.Errorf("vorhandene Datei ersetzen: %w", err)
			}
			e.log.Info("replacing existing target", "removed", rm, "dest", dest)
		}
		if _, err := mover.Move(ctx, src, filepath.Dir(dest)); err != nil {
			return err
		}
		f.TargetPath = dest
		f.Overwrite = false
		f.OverwritePath = ""
		f.Conflict = nil
		e.log.Info("moved file", "file", f.RelPath, "dest", dest)
	case store.FileActionDelete:
		if err := mover.Delete(ctx, src); err != nil {
			return err
		}
		e.log.Info("deleted file", "file", src)
	default:
		return fmt.Errorf("%w: %s", ErrInvalidAction, f.RelPath)
	}
	f.Action = action
	f.Done = true
	return nil
}

// finalize prunes the source folder once nothing is left to process: any
// directories that became empty (e.g. the per-episode sub-folders of a season
// pack whose videos were moved out) are removed, and the source folder itself
// if it ends up empty.
func (e *Engine) finalize(ctx context.Context, item *store.Item) {
	if pendingWork(item.Files) {
		return // work remaining
	}
	if !item.IsSingleFile() {
		if err := mover.RemoveEmptyDirs(ctx, item.SourcePath); err != nil {
			e.log.Warn("cleanup source folder", "path", item.SourcePath, "err", err)
		}
	}
}

// pendingWork reports whether any move/delete file is still waiting to run.
func pendingWork(files []store.File) bool {
	for i := range files {
		f := files[i]
		if !f.Done && (f.Action == store.FileActionMove || f.Action == store.FileActionDelete) {
			return true
		}
	}
	return false
}

// applyDecisions maps the AI per-file decisions onto the item files and resolves
// a destination path for every file that should move into destDir. Matching is
// tolerant: it first tries the exact relative path, then falls back to the base
// file name, so a model that returns a slightly different path still maps.
func applyDecisions(files []store.File, decisions []ai.FileDecision, destDir string) {
	byPath := make(map[string]ai.FileDecision, len(decisions))
	byBase := make(map[string]ai.FileDecision, len(decisions))
	for _, d := range decisions {
		p := strings.TrimSpace(d.Path)
		byPath[p] = d
		base := filepath.Base(filepath.FromSlash(p))
		if _, exists := byBase[base]; !exists {
			byBase[base] = d
		}
	}
	for i := range files {
		// A fresh classification re-evaluates collisions from scratch.
		files[i].Overwrite = false
		files[i].OverwritePath = ""
		files[i].Conflict = nil
		d, ok := byPath[files[i].RelPath]
		if !ok {
			d, ok = byBase[filepath.Base(files[i].RelPath)]
		}
		if !ok {
			files[i].Action = store.FileActionKeep
			continue
		}
		files[i].Action = d.Action
		files[i].Probability = d.Confidence
		files[i].Reason = d.Reason
		if d.Action == store.FileActionMove && destDir != "" {
			files[i].TargetPath = filepath.Join(destDir, filepath.Base(files[i].RelPath))
		}
	}
}

// applyAndDetect maps the AI per-file decisions onto the item files and then
// re-checks each planned move for a collision with an existing target file.
// The two steps always run together after a (re)classification.
func (e *Engine) applyAndDetect(files []store.File, decisions []ai.FileDecision, destDir string) {
	applyDecisions(files, decisions, destDir)
	e.detectConflicts(files)
}

// hasMovable reports whether at least one file is planned to move with a target.
func hasMovable(files []store.File) bool {
	for _, f := range files {
		if f.Action == store.FileActionMove && f.TargetPath != "" {
			return true
		}
	}
	return false
}

// anyUnresolvedMove reports whether a file should move but has no target yet.
func anyUnresolvedMove(files []store.File) bool {
	for _, f := range files {
		if !f.Done && f.Action == store.FileActionMove && f.TargetPath == "" {
			return true
		}
	}
	return false
}

// anyUnresolvedConflict reports whether a file planned to move still collides
// with an existing target file that the user has not resolved yet.
func anyUnresolvedConflict(files []store.File) bool {
	for i := range files {
		if !files[i].Done && files[i].Action == store.FileActionMove && files[i].Conflict != nil {
			return true
		}
	}
	return false
}

// routeFilesToTarget points every movable file at destDir. Files that are still
// undecided (no action yet, e.g. a folder the AI never classified) are switched
// to "move" so that picking a target by hand also plans the move. Explicit
// delete/keep choices and already-done files are left untouched.
func routeFilesToTarget(files []store.File, destDir string) {
	for i := range files {
		f := &files[i]
		if f.Done || f.RelPath == "" {
			continue
		}
		if f.Action == store.FileActionDelete || f.Action == store.FileActionKeep {
			continue
		}
		f.Action = store.FileActionMove
		f.TargetPath = filepath.Join(destDir, filepath.Base(f.RelPath))
		f.Overwrite = false
		f.OverwritePath = ""
	}
}
