// Package engine orchestrates the detection, AI classification and moving of
// downloaded media items.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
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
	// mu serializes an individual item mutation (one scan candidate or one user
	// action). It is held per-candidate during a scan — never for the whole
	// scan — so quick user actions can interleave between candidates.
	mu sync.Mutex
	// scanMu serializes whole scans against each other without blocking the
	// per-item mu, so at most one ProcessSource runs at a time.
	scanMu sync.Mutex

	progMu sync.Mutex
	prog   Progress
}

// New creates a new engine.
func New(st *store.Store, cfg config.Config, log *slog.Logger) *Engine {
	return &Engine{store: st, cfg: cfg, log: log}
}

// scanContext caches the per-scan inputs that would otherwise be re-loaded from
// the database and re-read from disk for every candidate: the application
// settings, the target libraries and a single reusable AI client. Folder notes
// and each library's sub-folder listing are loaded lazily on first use, so a
// scan that classifies nothing pays no extra I/O. A scanContext is used by a
// single goroutine at a time (the scan holds e.mu), so it needs no lock. No
// code path creates a new library sub-folder during a scan, which keeps the
// memoized sub-folder listing consistent for the whole scan.
type scanContext struct {
	settings store.AppSettings
	libs     []store.Library
	client   *ai.Client

	notes       map[string]string   // folder path -> description (loaded lazily)
	notesLoaded bool                // whether notes has been loaded
	subs        map[string][]string // library path -> immediate sub-folder names
}

// newScanContext loads the settings, libraries and AI client once for a scan.
func (e *Engine) newScanContext(ctx context.Context) (*scanContext, error) {
	settings, err := e.store.LoadAppSettings(ctx)
	if err != nil {
		return nil, err
	}
	libs, err := e.store.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	return &scanContext{
		settings: settings,
		libs:     libs,
		client: ai.New(ai.Config{
			BaseURL:    settings.AIBaseURL,
			APIKey:     settings.AIAPIKey,
			Model:      settings.AIModel,
			APIVersion: settings.AIAPIVersion,
			Logger:     e.log,
		}),
		subs: map[string][]string{},
	}, nil
}

// folderNotes returns the folder-note map, loading it once on first use.
func (sc *scanContext) folderNotes(ctx context.Context, e *Engine) map[string]string {
	if !sc.notesLoaded {
		notes, err := e.store.FolderNotesByPath(ctx)
		if err != nil {
			e.log.Warn("load folder notes", "err", err)
			notes = map[string]string{}
		}
		sc.notes = notes
		sc.notesLoaded = true
	}
	return sc.notes
}

// subfolders returns (and memoizes) the immediate sub-directory names of a
// library path so each library's directory is read at most once per scan.
func (sc *scanContext) subfolders(libPath string) []string {
	if v, ok := sc.subs[libPath]; ok {
		return v
	}
	v := listSubfolders(libPath)
	sc.subs[libPath] = v
	return v
}

// ProcessAll scans every configured source folder. It returns early if ctx is
// cancelled so a shutdown is not delayed by a full scan.
func (e *Engine) ProcessAll(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
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
		if ctx.Err() != nil {
			return
		}
		e.ProcessSource(ctx, src.Path)
	}
	e.log.Info("scan finished")
}

// ProcessSource scans a single source folder and processes stable candidates.
func (e *Engine) ProcessSource(ctx context.Context, sourcePath string) {
	// Serialize scans against each other, but do NOT hold e.mu for the whole
	// scan: it is taken per-candidate below so quick user actions can run
	// between candidates instead of waiting for the entire scan to finish.
	e.scanMu.Lock()
	defer e.scanMu.Unlock()

	sc, err := e.newScanContext(ctx)
	if err != nil {
		e.log.Error("scan setup", "path", sourcePath, "err", err)
		return
	}
	e.beginScan()
	defer e.finishProgress()
	candidates, err := scanner.ScanSource(sourcePath, sc.settings.IgnorePatterns)
	if err != nil {
		e.log.Error("scan source", "path", sourcePath, "err", err)
		return
	}
	e.log.Info("scanning source", "path", sourcePath, "candidates", len(candidates))
	e.setPhase(PhaseClassifying)
	e.setTotal(len(candidates))
	for i, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		e.updateProgress(i, c.Name)
		if !c.IsStable(e.cfg.StabilityWindow) {
			e.log.Info("candidate not yet stable, skipping", "name", c.Name)
			continue
		}
		e.mu.Lock()
		perr := e.processCandidate(ctx, sc, c, sourcePath)
		e.mu.Unlock()
		if perr != nil {
			e.log.Error("process candidate", "path", c.Path, "err", perr)
		}
	}
}

func (e *Engine) processCandidate(ctx context.Context, sc *scanContext, c scanner.Candidate, sourcePath string) error {
	existing, err := e.store.FindItemBySource(ctx, c.Path)
	if err != nil {
		return err
	}

	settings := sc.settings
	aiConfigured := sc.client.Configured()

	if existing != nil {
		switch existing.Status {
		case store.StatusError:
			// A previous classification failed. Do NOT re-query the AI endpoint on
			// every scan — that would hammer a failing endpoint indefinitely. Leave
			// the item in error so it can be retried explicitly ("KI-Abgleich") or
			// resolved by setting a target by hand during review.
			return nil
		case store.StatusPendingReview:
			// Re-classify in the background only if it was never classified yet
			// (e.g. detected before the AI endpoint was configured) AND an AI
			// endpoint is available. Otherwise keep the item as-is so a manually
			// edited plan is never wiped by a re-scan.
			if existing.DetectedType != "" || !aiConfigured {
				return nil
			}
		default:
			// Already moved/confirmed/rejected. If the source folder now holds no
			// files at all (only the empty sub-folders left behind after its
			// videos were moved out), clean those leftovers up.
			if len(c.Files) == 0 && !settings.DryRun {
				if err := mover.RemoveEmptyDirs(c.Path); err != nil {
					e.log.Warn("cleanup empty leftover", "path", c.Path, "err", err)
				} else {
					e.log.Info("removed empty leftover folder", "path", c.Path)
				}
			}
			return nil
		}
	}

	libs := sc.libs

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

	res, err := sc.client.Classify(ctx, e.buildRequest(ctx, sc, c.Name, c.Files, sourcePath))
	if err != nil {
		item.Status = store.StatusError
		item.ErrorMessage = err.Error()
		if uerr := e.store.UpsertItem(ctx, item); uerr != nil {
			e.log.Error("persist classify error", "name", c.Name, "err", uerr)
		}
		return fmt.Errorf("classify: %w", err)
	}

	item.DetectedType = res.Type
	item.Probability = res.Confidence
	item.Reasoning = res.Reasoning
	item.AIRaw = fmt.Sprintf("type=%s library=%s series_folder=%s title=%s confidence=%.3f",
		res.Type, res.Library, res.SeriesFolder, res.Title, res.Confidence)

	mv, del, keep := countActions(res.Files)
	e.log.Info("classified item", "name", c.Name, "type", res.Type, "library", res.Library,
		"series_folder", res.SeriesFolder, "confidence", res.Confidence,
		"files", len(res.Files), "move", mv, "delete", del, "keep", keep)

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
	e.applyAndDetect(item.Files, res.Files, destDir)

	canAuto := settings.AutoMove && ok && res.Confidence >= settings.Threshold &&
		hasMovable(item.Files) && !anyUnresolvedConflict(item.Files)
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
	if err := e.executePlan(item, false); err != nil {
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
	if anyUnresolvedConflict(item.Files) {
		return fmt.Errorf("Konflikt mit vorhandener Datei – bitte zuerst auflösen (Ersetzen oder Vorhandene behalten)")
	}
	if uerr := e.store.UpdateItemStatus(ctx, id, store.StatusMoving, ""); uerr != nil {
		e.log.Warn("update status to moving", "id", id, "err", uerr)
	}
	e.startPhase(PhaseMoving, countPending(item.Files))
	defer e.finishProgress()
	if err := e.executePlan(item, true); err != nil {
		if uerr := e.store.UpdateItemStatus(ctx, id, store.StatusError, err.Error()); uerr != nil {
			e.log.Error("persist plan error", "id", id, "err", uerr)
		}
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

	sc, err := e.newScanContext(ctx)
	if err != nil {
		return err
	}
	if !sc.client.Configured() {
		return fmt.Errorf("KI-Endpoint nicht konfiguriert")
	}

	e.beginScan()
	e.setPhase(PhaseClassifying)
	e.setTotal(1)
	defer e.finishProgress()
	e.updateProgress(0, item.Name)

	sourcePath := filepath.Dir(item.SourcePath)
	res, err := sc.client.Classify(ctx, e.buildRequest(ctx, sc, item.Name, item.Files, sourcePath))
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}

	item.DetectedType = res.Type
	item.Probability = res.Confidence
	item.Reasoning = res.Reasoning
	item.AIRaw = fmt.Sprintf("type=%s library=%s series_folder=%s title=%s confidence=%.3f",
		res.Type, res.Library, res.SeriesFolder, res.Title, res.Confidence)

	lib, destDir, ok, _ := e.resolveTarget(res, sc.libs)
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
	e.applyAndDetect(item.Files, res.Files, destDir)
	item.Status = store.StatusPendingReview
	item.ErrorMessage = ""
	mv, del, keep := countActions(res.Files)
	e.log.Info("reclassified item", "name", item.Name, "type", res.Type, "library", res.Library,
		"series_folder", res.SeriesFolder, "confidence", res.Confidence,
		"files", len(res.Files), "move", mv, "delete", del, "keep", keep)
	return e.store.UpsertItem(ctx, item)
}

// countActions tallies the AI per-file decisions by action for logging.
func countActions(files []ai.FileDecision) (move, del, keep int) {
	for _, f := range files {
		switch f.Action {
		case ai.ActionMove:
			move++
		case ai.ActionDelete:
			del++
		default:
			keep++
		}
	}
	return
}
