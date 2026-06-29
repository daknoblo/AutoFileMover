// Package engine orchestrates the detection, AI classification and moving of
// downloaded media items.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/daknoblo/AutoFileMover/internal/ai"
	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/mover"
	"github.com/daknoblo/AutoFileMover/internal/scanner"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// Engine ties the store, AI client and filesystem together.
type Engine struct {
	store *store.Store
	cfg   config.Config
	log   *slog.Logger
	mu    sync.Mutex
}

// New creates a new engine.
func New(st *store.Store, cfg config.Config, log *slog.Logger) *Engine {
	return &Engine{store: st, cfg: cfg, log: log}
}

// ProcessAll scans every configured source folder.
func (e *Engine) ProcessAll(ctx context.Context) {
	sources, err := e.store.ListSources(ctx)
	if err != nil {
		e.log.Error("list sources", "err", err)
		return
	}
	if len(sources) == 0 {
		e.log.Warn("scan: no source folder configured")
		return
	}
	e.log.Info("scan started", "sources", len(sources))
	for _, src := range sources {
		e.ProcessSource(ctx, src.Path)
	}
	e.log.Info("scan finished")
}

// ProcessSource scans a single source folder and processes stable candidates.
func (e *Engine) ProcessSource(ctx context.Context, sourcePath string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var ignore []string
	if settings, err := e.store.LoadAppSettings(ctx); err == nil {
		ignore = settings.IgnorePatterns
	}
	candidates, err := scanner.ScanSource(sourcePath, ignore)
	if err != nil {
		e.log.Error("scan source", "path", sourcePath, "err", err)
		return
	}
	e.log.Info("scanning source", "path", sourcePath, "candidates", len(candidates))
	for _, c := range candidates {
		if !c.IsStable(e.cfg.StabilityWindow) {
			e.log.Info("candidate not yet stable, skipping", "name", c.Name)
			continue
		}
		if err := e.processCandidate(ctx, c, sourcePath); err != nil {
			e.log.Error("process candidate", "path", c.Path, "err", err)
		}
	}
}

func (e *Engine) processCandidate(ctx context.Context, c scanner.Candidate, sourcePath string) error {
	existing, err := e.store.FindItemBySource(ctx, c.Path)
	if err != nil {
		return err
	}
	if existing != nil {
		switch existing.Status {
		case store.StatusError:
			// retry below
		default:
			return nil // already handled or waiting for review
		}
	}

	libs, err := e.store.ListLibraries(ctx)
	if err != nil {
		return err
	}

	item := &store.Item{
		SourcePath: c.Path,
		Name:       c.Name,
		Files:      c.Files,
		Status:     store.StatusPendingReview,
	}
	if existing != nil {
		item.ID = existing.ID
	}

	settings, err := e.store.LoadAppSettings(ctx)
	if err != nil {
		return err
	}
	client := ai.New(ai.Config{
		BaseURL:    settings.AIBaseURL,
		APIKey:     settings.AIAPIKey,
		Model:      settings.AIModel,
		APIVersion: settings.AIAPIVersion,
	})
	if !client.Configured() {
		item.Reasoning = "AI endpoint not configured; queued for manual review"
		return e.store.UpsertItem(ctx, item)
	}

	res, err := client.Classify(ctx, e.buildRequest(ctx, c, libs, sourcePath))
	if err != nil {
		item.Status = store.StatusError
		item.ErrorMessage = err.Error()
		_ = e.store.UpsertItem(ctx, item)
		return fmt.Errorf("classify: %w", err)
	}

	item.DetectedType = res.Type
	item.Probability = res.Confidence
	item.Reasoning = res.Reasoning
	item.AIRaw = fmt.Sprintf("type=%s library=%s series_folder=%s title=%s confidence=%.3f",
		res.Type, res.Library, res.SeriesFolder, res.Title, res.Confidence)

	lib, destDir, ok, reason := e.resolveTarget(res, libs)
	if ok && lib != nil {
		id := lib.ID
		item.TargetLibraryID = &id
		item.TargetPath = destDir
	} else if reason != "" {
		item.Reasoning = strings.TrimSpace(item.Reasoning + " | " + reason)
	}

	// Map the per-file AI decisions onto the scanned files and resolve a target
	// path for every file that should move.
	applyDecisions(item.Files, res.Files, destDir)

	canAuto := settings.AutoMove && ok && res.Confidence >= settings.Threshold && hasMovable(item.Files)
	if !canAuto {
		item.Status = store.StatusPendingReview
		return e.store.UpsertItem(ctx, item)
	}

	// What-if mode: confident enough, but do not touch the filesystem.
	if settings.DryRun {
		item.Status = store.StatusPendingReview
		item.Reasoning = strings.TrimSpace("[What-If] würde automatisch ausführen → " + destDir + " | " + item.Reasoning)
		e.log.Info("what-if: would auto-process", "name", c.Name, "dest", destDir, "confidence", res.Confidence)
		return e.store.UpsertItem(ctx, item)
	}

	// Confident enough: execute the per-file plan now.
	item.Status = store.StatusMoving
	if err := e.store.UpsertItem(ctx, item); err != nil {
		return err
	}
	if err := e.executePlan(item); err != nil {
		item.Status = store.StatusError
		item.ErrorMessage = err.Error()
		return e.store.UpsertItem(ctx, item)
	}
	item.Status = store.StatusAutoMoved
	item.ErrorMessage = ""
	e.log.Info("auto-processed item", "name", c.Name, "dest", destDir, "confidence", res.Confidence)
	return e.store.UpsertItem(ctx, item)
}

// ApplyPlan carries out every planned move/delete for an item, then removes the
// emptied source folder. Used by the "Alles ausführen" button after review.
func (e *Engine) ApplyPlan(ctx context.Context, id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	if !hasMovable(item.Files) {
		return fmt.Errorf("kein Ziel aufgelöst – bitte zuerst eine Bibliothek/Datei wählen")
	}
	_ = e.store.UpdateItemStatus(ctx, id, store.StatusMoving, "")
	if err := e.executePlan(item); err != nil {
		_ = e.store.UpdateItemStatus(ctx, id, store.StatusError, err.Error())
		return err
	}
	item.Status = store.StatusConfirmed
	item.ErrorMessage = ""
	e.log.Info("plan applied", "name", item.Name)
	return e.store.UpsertItem(ctx, item)
}

// ApplyFileAction performs a single planned action (move or delete) for one file
// inside an item, even while What-If is enabled (an explicit manual override).
func (e *Engine) ApplyFileAction(ctx context.Context, id int64, relPath, action string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	idx := -1
	for i := range item.Files {
		if item.Files[i].RelPath == relPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("file not found in item")
	}
	if err := e.execFile(item, &item.Files[idx], action); err != nil {
		return err
	}
	e.finalize(item)
	return e.store.UpsertItem(ctx, item)
}

// executePlan runs every undecided move/delete file then cleans up.
func (e *Engine) executePlan(item *store.Item) error {
	for i := range item.Files {
		f := &item.Files[i]
		if f.Done || (f.Action != store.FileActionMove && f.Action != store.FileActionDelete) {
			continue
		}
		if err := e.execFile(item, f, f.Action); err != nil {
			return err
		}
	}
	e.finalize(item)
	return nil
}

// execFile moves or deletes a single file and marks it done.
func (e *Engine) execFile(item *store.Item, f *store.File, action string) error {
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
			return fmt.Errorf("kein Zielordner für %s", f.RelPath)
		}
		if _, err := mover.Move(src, filepath.Dir(dest)); err != nil {
			return err
		}
		f.TargetPath = dest
		e.log.Info("moved file", "file", f.RelPath, "dest", dest)
	case store.FileActionDelete:
		if err := mover.Delete(src); err != nil {
			return err
		}
		e.log.Info("deleted file", "file", src)
	default:
		return fmt.Errorf("nichts zu tun für %s", f.RelPath)
	}
	f.Action = action
	f.Done = true
	return nil
}

// finalize removes the emptied source folder once nothing is left to process.
func (e *Engine) finalize(item *store.Item) {
	for i := range item.Files {
		if !item.Files[i].Done && (item.Files[i].Action == store.FileActionMove || item.Files[i].Action == store.FileActionDelete) {
			return // work remaining
		}
	}
	if !item.IsSingleFile() {
		_ = mover.RemoveIfEmpty(item.SourcePath)
	}
}

// applyDecisions maps the AI per-file decisions onto the item files and resolves
// a destination path for every file that should move into destDir.
func applyDecisions(files []store.File, decisions []ai.FileDecision, destDir string) {
	byPath := make(map[string]ai.FileDecision, len(decisions))
	for _, d := range decisions {
		byPath[d.Path] = d
	}
	for i := range files {
		d, ok := byPath[files[i].RelPath]
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

// hasMovable reports whether at least one file is planned to move with a target.
func hasMovable(files []store.File) bool {
	for _, f := range files {
		if f.Action == store.FileActionMove && f.TargetPath != "" {
			return true
		}
	}
	return false
}

// RejectItem marks an item as rejected without moving anything.
func (e *Engine) RejectItem(ctx context.Context, id int64) error {
	return e.store.UpdateItemStatus(ctx, id, store.StatusRejected, "")
}

// SetItemTarget assigns a target library (and optional series sub-folder) to an
// item and recomputes the destination for every file planned to move. Used when
// the AI could not resolve a target and the user picks one during review.
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
	if info, err := os.Stat(destDir); err != nil || !info.IsDir() {
		return fmt.Errorf("Zielordner existiert nicht: %s", destDir)
	}
	item.TargetLibraryID = &lib.ID
	item.TargetPath = destDir
	for i := range item.Files {
		if item.Files[i].Action == store.FileActionMove && !item.Files[i].Done {
			item.Files[i].TargetPath = filepath.Join(destDir, filepath.Base(item.Files[i].RelPath))
		}
	}
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
	if chosen.Kind == store.KindSeries {
		if res.SeriesFolder == "" {
			return chosen, "", false, "no existing series folder matched; manual review required"
		}
		dir := filepath.Join(chosen.Path, res.SeriesFolder)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return chosen, "", false, "suggested series folder does not exist; manual review required"
		}
		return chosen, dir, true, ""
	}
	return chosen, chosen.Path, true, ""
}

// buildRequest constructs the AI classification request, including existing
// series folders and any user-provided folder descriptions so the model can
// map to an existing show with extra context.
func (e *Engine) buildRequest(ctx context.Context, c scanner.Candidate, libs []store.Library, sourcePath string) ai.Request {
	notes, _ := e.store.FolderNotesByPath(ctx)

	files := make([]ai.FileInput, 0, len(c.Files))
	for _, f := range c.Files {
		files = append(files, ai.FileInput{Path: f.RelPath, SizeBytes: f.Size})
	}
	infos := make([]ai.LibraryInfo, 0, len(libs))
	for _, l := range libs {
		info := ai.LibraryInfo{Name: l.Name, Kind: l.Kind, Description: notes[l.Path]}
		if l.Kind == store.KindSeries {
			for _, name := range listSubfolders(l.Path) {
				info.ExistingFolders = append(info.ExistingFolders, ai.ExistingFolder{
					Name:        name,
					Description: notes[filepath.Join(l.Path, name)],
				})
			}
		}
		infos = append(infos, info)
	}
	return ai.Request{
		Name:          c.Name,
		Files:         files,
		SourceContext: notes[sourcePath],
		Libraries:     infos,
	}
}

// listSubfolders returns the immediate sub-directory names of root.
func listSubfolders(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
