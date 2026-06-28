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
	for _, src := range sources {
		e.ProcessSource(ctx, src.Path)
	}
}

// ProcessSource scans a single source folder and processes stable candidates.
func (e *Engine) ProcessSource(ctx context.Context, sourcePath string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	candidates, err := scanner.ScanSource(sourcePath)
	if err != nil {
		e.log.Error("scan source", "path", sourcePath, "err", err)
		return
	}
	for _, c := range candidates {
		if !c.IsStable(e.cfg.StabilityWindow) {
			e.log.Debug("candidate not yet stable, skipping", "path", c.Path)
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
		item.TargetPath = filepath.Join(destDir, c.Name)
	} else if reason != "" {
		item.Reasoning = strings.TrimSpace(item.Reasoning + " | " + reason)
	}

	autoMove := settings.AutoMove && ok && res.Confidence >= settings.Threshold
	if !autoMove {
		item.Status = store.StatusPendingReview
		return e.store.UpsertItem(ctx, item)
	}

	// What-if mode: confident enough, but do not touch the filesystem.
	if settings.DryRun {
		item.Status = store.StatusPendingReview
		item.Reasoning = strings.TrimSpace("[What-If] würde automatisch verschoben nach " + item.TargetPath + " | " + item.Reasoning)
		e.log.Info("what-if: would auto-move", "name", c.Name, "dest", item.TargetPath, "confidence", res.Confidence)
		return e.store.UpsertItem(ctx, item)
	}

	// Confident enough: move now.
	item.Status = store.StatusMoving
	if err := e.store.UpsertItem(ctx, item); err != nil {
		return err
	}
	finalPath, err := mover.Move(c.Path, destDir)
	if err != nil {
		item.Status = store.StatusError
		item.ErrorMessage = err.Error()
		return e.store.UpsertItem(ctx, item)
	}
	item.TargetPath = finalPath
	item.Status = store.StatusAutoMoved
	item.ErrorMessage = ""
	e.log.Info("auto-moved item", "name", c.Name, "dest", finalPath, "confidence", res.Confidence)
	return e.store.UpsertItem(ctx, item)
}

// ConfirmItem moves an item to the chosen library (and optional sub-folder for
// series) after manual review.
func (e *Engine) ConfirmItem(ctx context.Context, id, libraryID int64, subFolder string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, err := e.store.GetItem(ctx, id)
	if err != nil || item == nil {
		return fmt.Errorf("item not found")
	}
	if settings, err := e.store.LoadAppSettings(ctx); err == nil && settings.DryRun {
		return fmt.Errorf("What-If-Modus aktiv: es werden keine Dateien verschoben")
	}
	lib, err := e.store.GetLibrary(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("library not found")
	}
	destDir := lib.Path
	if subFolder != "" {
		destDir = filepath.Join(lib.Path, subFolder)
	}
	if _, err := os.Stat(item.SourcePath); err != nil {
		_ = e.store.UpdateItemStatus(ctx, id, store.StatusError, "source no longer exists")
		return fmt.Errorf("source no longer exists: %s", item.SourcePath)
	}

	_ = e.store.UpdateItemStatus(ctx, id, store.StatusMoving, "")
	finalPath, err := mover.Move(item.SourcePath, destDir)
	if err != nil {
		_ = e.store.UpdateItemStatus(ctx, id, store.StatusError, err.Error())
		return err
	}
	libID := lib.ID
	_ = e.store.UpdateItemTarget(ctx, id, &libID, finalPath)
	e.log.Info("confirmed move", "name", item.Name, "dest", finalPath)
	return e.store.UpdateItemStatus(ctx, id, store.StatusConfirmed, "")
}

// RejectItem marks an item as rejected without moving anything.
func (e *Engine) RejectItem(ctx context.Context, id int64) error {
	return e.store.UpdateItemStatus(ctx, id, store.StatusRejected, "")
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

	files := make([]string, 0, len(c.Files))
	for _, f := range c.Files {
		files = append(files, f.RelPath)
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
