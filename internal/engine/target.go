package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daknoblo/AutoFileMover/internal/ai"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// SetItemTarget assigns a target library (and optional series sub-folder) to an
// item and routes every movable file there: files already planned to move get a
// recomputed destination, and undecided files (e.g. a folder the AI never
// classified) are switched to "move". Explicit delete/keep choices are kept.
func (e *Engine) SetItemTarget(ctx context.Context, id, libraryID int64, subFolder string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	lib, err := e.store.GetLibrary(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("library not found")
	}
	destDir := lib.Path
	if subFolder != "" {
		destDir = filepath.Join(lib.Path, subFolder)
	}
	// Keep the destination inside the library: reject a sub-folder that escapes
	// lib.Path via ".." (filepath.Rel yields a ".." prefix in that case).
	if rel, relErr := filepath.Rel(lib.Path, destDir); relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("ungültiger Zielordner: %s", subFolder)
	}
	if info, err := os.Stat(destDir); err != nil || !info.IsDir() {
		return fmt.Errorf("Zielordner existiert nicht: %s", destDir)
	}
	item.TargetLibraryID = &lib.ID
	item.TargetPath = destDir
	routeFilesToTarget(item.Files, destDir)
	e.detectConflicts(item.Files)
	// Setting a target by hand means the user is taking over a failed or
	// unresolved classification: clear any error and route it as normal review.
	if item.Status == store.StatusError {
		item.Status = store.StatusPendingReview
	}
	item.ErrorMessage = ""
	return e.store.UpsertItem(ctx, item)
}

// resolveTarget maps a classification result to a concrete library and
// destination directory. ok is false when the item needs manual review.
func (e *Engine) resolveTarget(res *ai.Result, libs []store.Library) (lib *store.Library, destDir string, ok bool, reason string) {
	var chosen *store.Library
	for i := range libs {
		if strings.EqualFold(libs[i].Name, res.Library) {
			chosen = &libs[i]
			break
		}
	}
	if chosen == nil {
		return nil, "", false, "no matching target library for AI suggestion"
	}

	// If the model picked a sub-folder, match it (case-insensitively) against the
	// library's actual sub-folders so episodes land in the correct show folder.
	if folder := strings.TrimSpace(res.SeriesFolder); folder != "" {
		if match := matchSubfolder(chosen.Path, folder); match != "" {
			return chosen, filepath.Join(chosen.Path, match), true, ""
		}
		if chosen.UseSubfolders {
			return chosen, "", false, "suggested sub-folder does not exist; manual review required"
		}
		// Flat libraries (e.g. movies) may still go to the library root.
		return chosen, chosen.Path, true, ""
	}

	if chosen.UseSubfolders {
		return chosen, "", false, "no existing sub-folder matched; manual review required"
	}
	return chosen, chosen.Path, true, ""
}

// matchSubfolder returns the actual sub-folder name of root that equals name
// case-insensitively, or "" if none matches.
func matchSubfolder(root, name string) string {
	for _, sub := range listSubfolders(root) {
		if strings.EqualFold(sub, name) {
			return sub
		}
	}
	return ""
}

// suggestFolder records the AI's proposed destination folder on the item when a
// library matched but the show folder is missing, so the UI can offer to create
// it. A nil library or empty name clears any previous suggestion.
func (e *Engine) suggestFolder(item *store.Item, lib *store.Library, res *ai.Result) {
	item.SuggestedLibraryID = nil
	item.SuggestedFolder = ""
	if lib == nil {
		return
	}
	folder := strings.TrimSpace(res.SeriesFolder)
	if folder == "" {
		folder = sanitizeFolder(res.Title)
	}
	if folder == "" {
		return
	}
	lid := lib.ID
	item.SuggestedLibraryID = &lid
	item.SuggestedFolder = folder
}

// sanitizeFolder turns an AI title into a safe single-segment folder name.
func sanitizeFolder(title string) string {
	t := strings.TrimSpace(title)
	t = strings.ReplaceAll(t, "/", " ")
	t = strings.ReplaceAll(t, "\\", " ")
	t = strings.Trim(t, ". ")
	return strings.TrimSpace(t)
}

// CreateTargetFolder creates the AI-suggested destination folder for an item and
// sets it as the move target, recomputing each move file's destination.
func (e *Engine) CreateTargetFolder(ctx context.Context, id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	if item.SuggestedLibraryID == nil || item.SuggestedFolder == "" {
		return fmt.Errorf("kein Ordner zum Anlegen vorgeschlagen")
	}
	lib, err := e.store.GetLibrary(ctx, *item.SuggestedLibraryID)
	if err != nil {
		return fmt.Errorf("library not found")
	}
	return e.applyNewFolder(ctx, item, lib, item.SuggestedFolder)
}

// CreateNamedTargetFolder creates a new folder named folder directly under the
// given library and sets it as the item's move target. Used during manual
// review when the desired destination folder does not exist yet.
func (e *Engine) CreateNamedTargetFolder(ctx context.Context, id, libraryID int64, folder string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	lib, err := e.store.GetLibrary(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("library not found")
	}
	return e.applyNewFolder(ctx, item, lib, folder)
}

// applyNewFolder creates <lib>/<name> (a direct child of the library path), sets
// it as the item's move target, recomputes file destinations, clears any pending
// suggestion/error and persists. The caller must hold e.mu.
func (e *Engine) applyNewFolder(ctx context.Context, item *store.Item, lib store.Library, name string) error {
	folder := sanitizeFolder(name)
	if folder == "" {
		return fmt.Errorf("ungültiger Ordnername")
	}
	// If a folder with this name already exists (case-insensitively), reuse it
	// instead of creating a near-duplicate that differs only in case/spacing.
	if existing := matchSubfolder(lib.Path, folder); existing != "" {
		folder = existing
	}
	dir := filepath.Join(lib.Path, folder)
	// Safety: the new folder must be a direct child of the library path and may
	// not escape it via ".."; filepath.Rel makes the containment explicit.
	if rel, relErr := filepath.Rel(lib.Path, dir); relErr != nil || rel != folder {
		return fmt.Errorf("ungültiger Ordnername")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("Ordner anlegen: %w", err)
	}
	lid := lib.ID
	item.TargetLibraryID = &lid
	item.TargetPath = dir
	item.SuggestedLibraryID = nil
	item.SuggestedFolder = ""
	routeFilesToTarget(item.Files, dir)
	e.detectConflicts(item.Files)
	// Creating a target by hand means the user is taking over a failed or
	// unresolved classification: clear any error and route it as normal review.
	if item.Status == store.StatusError {
		item.Status = store.StatusPendingReview
	}
	item.ErrorMessage = ""
	e.log.Info("created target folder", "dir", dir, "item", item.Name)
	return e.store.UpsertItem(ctx, item)
}
