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
	"time"

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

	progMu sync.Mutex
	prog   Progress
}

// Phase values for Progress.
const (
	PhaseIdle        = "idle"
	PhaseScanning    = "scanning"
	PhaseClassifying = "classifying"
)

// Progress describes the state of an in-flight scan for the UI status display.
type Progress struct {
	// Active is true while a scan is running.
	Active bool `json:"active"`
	// Phase is the current activity: idle, scanning or classifying.
	Phase string `json:"phase"`
	// Current is the name of the folder/file being processed right now.
	Current string `json:"current"`
	// Done is the number of candidates already processed.
	Done int `json:"done"`
	// Total is the number of candidates in this scan.
	Total int `json:"total"`
	// Percent is Done/Total as a whole number (0..100).
	Percent int `json:"percent"`
	// ETASeconds is the estimated remaining time in seconds.
	ETASeconds int `json:"eta_seconds"`

	startedAt time.Time
}

// New creates a new engine.
func New(st *store.Store, cfg config.Config, log *slog.Logger) *Engine {
	return &Engine{store: st, cfg: cfg, log: log}
}

// GetProgress returns a snapshot of the current scan progress.
func (e *Engine) GetProgress() Progress {
	e.progMu.Lock()
	defer e.progMu.Unlock()
	if e.prog.Phase == "" {
		e.prog.Phase = PhaseIdle
	}
	return e.prog
}

// beginScan marks the start of a scan in the filesystem-reading phase.
func (e *Engine) beginScan() {
	e.progMu.Lock()
	e.prog = Progress{Active: true, Phase: PhaseScanning, startedAt: time.Now()}
	e.progMu.Unlock()
}

func (e *Engine) setTotal(total int) {
	e.progMu.Lock()
	e.prog.Total = total
	e.progMu.Unlock()
}

func (e *Engine) updateProgress(done int, current string) {
	e.progMu.Lock()
	e.prog.Phase = PhaseClassifying
	e.prog.Done = done
	e.prog.Current = current
	if e.prog.Total > 0 {
		e.prog.Percent = done * 100 / e.prog.Total
	}
	if elapsed := time.Since(e.prog.startedAt).Seconds(); done > 0 && done < e.prog.Total {
		e.prog.ETASeconds = int((elapsed / float64(done)) * float64(e.prog.Total-done))
	} else {
		e.prog.ETASeconds = 0
	}
	e.progMu.Unlock()
}

func (e *Engine) finishProgress() {
	e.progMu.Lock()
	e.prog.Active = false
	e.prog.Phase = PhaseIdle
	e.prog.Current = ""
	e.prog.ETASeconds = 0
	if e.prog.Total > 0 {
		e.prog.Done = e.prog.Total
		e.prog.Percent = 100
	}
	e.progMu.Unlock()
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
	e.beginScan()
	defer e.finishProgress()
	candidates, err := scanner.ScanSource(sourcePath, ignore)
	if err != nil {
		e.log.Error("scan source", "path", sourcePath, "err", err)
		return
	}
	e.log.Info("scanning source", "path", sourcePath, "candidates", len(candidates))
	e.setTotal(len(candidates))
	for i, c := range candidates {
		e.updateProgress(i, c.Name)
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
	aiConfigured := client.Configured()

	if existing != nil {
		switch existing.Status {
		case store.StatusError:
			// retry below
		case store.StatusPendingReview:
			// Re-classify in the background only if it was never classified yet
			// (e.g. detected before the AI endpoint was configured) AND an AI
			// endpoint is available. Otherwise keep the item as-is so a manually
			// edited plan is never wiped by a re-scan.
			if existing.DetectedType != "" || !aiConfigured {
				return nil
			}
		default:
			return nil // already moved/confirmed/rejected
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

	// Empty folders carry no files to classify. Surface them as a single
	// deletable entry instead of spending an AI call on them.
	if len(c.Files) == 0 {
		item.DetectedType = "empty"
		item.Probability = 0
		item.Files = []store.File{{
			RelPath: "",
			Action:  store.FileActionDelete,
			Reason:  "empty folder",
		}}
		item.Reasoning = "empty folder"
		return e.store.UpsertItem(ctx, item)
	}

	if !aiConfigured {
		item.Reasoning = "AI endpoint not configured; queued for manual review"
		return e.store.UpsertItem(ctx, item)
	}

	res, err := client.Classify(ctx, e.buildRequest(ctx, c.Name, c.Files, libs, sourcePath, settings.AIContext))
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
		item.SuggestedLibraryID = nil
		item.SuggestedFolder = ""
	} else {
		if reason != "" {
			item.Reasoning = strings.TrimSpace(item.Reasoning + " | " + reason)
		}
		e.suggestFolder(item, lib, res)
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
	if settings, err := e.store.LoadAppSettings(ctx); err == nil && settings.DryRun {
		return fmt.Errorf("What-If-Modus aktiv: es werden keine Dateien verschoben oder gelöscht")
	}
	if !pendingWork(item.Files) {
		return fmt.Errorf("nichts auszuführen")
	}
	if anyUnresolvedMove(item.Files) {
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

// ReclassifyItem re-runs the AI classification for an existing review item and
// updates the suggested per-file actions and target WITHOUT executing anything.
// Progress is reported on the same status channel as a scan.
func (e *Engine) ReclassifyItem(ctx context.Context, id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	hasReal := false
	for _, f := range item.Files {
		if f.RelPath != "" {
			hasReal = true
			break
		}
	}
	if !hasReal {
		return fmt.Errorf("nichts zu analysieren")
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
		return fmt.Errorf("KI-Endpoint nicht konfiguriert")
	}
	libs, err := e.store.ListLibraries(ctx)
	if err != nil {
		return err
	}

	e.beginScan()
	e.setTotal(1)
	defer e.finishProgress()
	e.updateProgress(0, item.Name)

	sourcePath := filepath.Dir(item.SourcePath)
	res, err := client.Classify(ctx, e.buildRequest(ctx, item.Name, item.Files, libs, sourcePath, settings.AIContext))
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}

	item.DetectedType = res.Type
	item.Probability = res.Confidence
	item.Reasoning = res.Reasoning
	item.AIRaw = fmt.Sprintf("type=%s library=%s series_folder=%s title=%s confidence=%.3f",
		res.Type, res.Library, res.SeriesFolder, res.Title, res.Confidence)

	lib, destDir, ok, _ := e.resolveTarget(res, libs)
	if ok && lib != nil {
		lid := lib.ID
		item.TargetLibraryID = &lid
		item.TargetPath = destDir
		item.SuggestedLibraryID = nil
		item.SuggestedFolder = ""
	} else {
		item.TargetLibraryID = nil
		item.TargetPath = ""
		e.suggestFolder(item, lib, res)
	}
	applyDecisions(item.Files, res.Files, destDir)
	item.Status = store.StatusPendingReview
	item.ErrorMessage = ""
	e.log.Info("reclassified item", "name", item.Name, "confidence", res.Confidence)
	return e.store.UpsertItem(ctx, item)
}

// ApplyFileAction performs a single planned action (move or delete) for one file
// inside an item. It is blocked while What-If is enabled.
func (e *Engine) ApplyFileAction(ctx context.Context, id int64, relPath, action string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	if settings, err := e.store.LoadAppSettings(ctx); err == nil && settings.DryRun {
		return fmt.Errorf("What-If-Modus aktiv: es werden keine Dateien verschoben oder gelöscht")
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
		return fmt.Errorf("invalid action")
	}
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
	f := &item.Files[idx]
	if f.Done {
		return fmt.Errorf("file already processed")
	}
	f.Action = action
	if action == store.FileActionMove && relPath != "" && item.TargetPath != "" {
		f.TargetPath = filepath.Join(item.TargetPath, filepath.Base(relPath))
	} else {
		f.TargetPath = ""
	}
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
	if pendingWork(item.Files) {
		return // work remaining
	}
	if !item.IsSingleFile() {
		_ = mover.RemoveIfEmpty(item.SourcePath)
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

// anyUnresolvedMove reports whether a file should move but has no target yet.
func anyUnresolvedMove(files []store.File) bool {
	for _, f := range files {
		if !f.Done && f.Action == store.FileActionMove && f.TargetPath == "" {
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

	// If the model picked a sub-folder, match it (case-insensitively) against the
	// library's actual sub-folders so episodes land in the correct show folder.
	if folder := strings.TrimSpace(res.SeriesFolder); folder != "" {
		if match := matchSubfolder(chosen.Path, folder); match != "" {
			return chosen, filepath.Join(chosen.Path, match), true, ""
		}
		if chosen.Kind == store.KindSeries {
			return chosen, "", false, "suggested series folder does not exist; manual review required"
		}
		// Movies/documentaries may still go to the library root.
		return chosen, chosen.Path, true, ""
	}

	if chosen.Kind == store.KindSeries {
		return chosen, "", false, "no existing series folder matched; manual review required"
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
	dir := filepath.Join(lib.Path, item.SuggestedFolder)
	// Safety: the new folder must be a direct child of the library path.
	if filepath.Dir(dir) != filepath.Clean(lib.Path) {
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
	for i := range item.Files {
		if item.Files[i].Action == store.FileActionMove && !item.Files[i].Done {
			item.Files[i].TargetPath = filepath.Join(dir, filepath.Base(item.Files[i].RelPath))
		}
	}
	e.log.Info("created target folder", "dir", dir, "item", item.Name)
	return e.store.UpsertItem(ctx, item)
}

// buildRequest constructs the AI classification request, including existing
// series folders and any user-provided folder descriptions so the model can
// map to an existing show with extra context.
func (e *Engine) buildRequest(ctx context.Context, name string, srcFiles []store.File, libs []store.Library, sourcePath, globalContext string) ai.Request {
	notes, _ := e.store.FolderNotesByPath(ctx)

	files := make([]ai.FileInput, 0, len(srcFiles))
	for _, f := range srcFiles {
		if f.RelPath == "" {
			continue // synthetic empty-folder marker, nothing to classify
		}
		files = append(files, ai.FileInput{Path: f.RelPath, SizeBytes: f.Size})
	}
	infos := make([]ai.LibraryInfo, 0, len(libs))
	for _, l := range libs {
		info := ai.LibraryInfo{Name: l.Name, Kind: l.Kind, Description: notes[l.Path]}
		// Include the existing sub-folder structure of every library so the model
		// can map an item into the matching show/collection folder, not just the
		// library root.
		for _, name := range listSubfolders(l.Path) {
			info.ExistingFolders = append(info.ExistingFolders, ai.ExistingFolder{
				Name:        name,
				Description: notes[filepath.Join(l.Path, name)],
			})
		}
		infos = append(infos, info)
	}
	return ai.Request{
		Name:          name,
		Files:         files,
		GlobalContext: globalContext,
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
