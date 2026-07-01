package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daknoblo/AutoFileMover/internal/mediainfo"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// destEntry is a file already present in a target folder.
type destEntry struct {
	name string
	size int64
}

// videoExts are the extensions considered "the same episode" for similarity
// matching; subtitles/nfo are only matched on an exact name.
var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".mov": true, ".ts": true, ".wmv": true, ".mpg": true, ".mpeg": true,
}

// detectConflicts scans each move file's destination folder and records a
// FileConflict when an existing file would collide: either the exact same name,
// or the same episode (SxxExx) for a video file. Files the user already chose to
// overwrite, or that are not moving, have their conflict cleared.
func (e *Engine) detectConflicts(files []store.File) {
	dirCache := map[string][]destEntry{}
	for i := range files {
		f := &files[i]
		if f.Done || f.Action != store.FileActionMove || f.TargetPath == "" || f.Overwrite {
			f.Conflict = nil
			continue
		}
		destDir := filepath.Dir(f.TargetPath)
		entries, ok := dirCache[destDir]
		if !ok {
			entries = listDestEntries(destDir)
			dirCache[destDir] = entries
		}
		incoming := filepath.Base(f.TargetPath)
		match := findConflict(incoming, entries)
		if match == nil {
			f.Conflict = nil
			continue
		}
		f.Conflict = &store.FileConflict{
			ExistingName:    match.name,
			ExistingPath:    filepath.Join(destDir, match.name),
			ExistingSize:    match.size,
			ExistingQuality: mediainfo.Parse(match.name).Summary(),
			IncomingQuality: mediainfo.Parse(incoming).Summary(),
		}
	}
}

// findConflict returns the existing entry that collides with incoming: an exact
// (case-insensitive) name match wins; otherwise a video file with the same
// episode marker is considered the same content in a different release.
func findConflict(incoming string, entries []destEntry) *destEntry {
	for i := range entries {
		if strings.EqualFold(entries[i].name, incoming) {
			return &entries[i]
		}
	}
	ep := mediainfo.Episode(incoming)
	if ep == "" {
		return nil
	}
	for i := range entries {
		if !videoExts[strings.ToLower(filepath.Ext(entries[i].name))] {
			continue
		}
		if mediainfo.Episode(entries[i].name) == ep {
			return &entries[i]
		}
	}
	return nil
}

// listDestEntries returns the regular files (not sub-directories) of dir.
func listDestEntries(dir string) []destEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]destEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		out = append(out, destEntry{name: e.Name(), size: size})
	}
	return out
}

// RejectItem marks an item as rejected without moving anything.
func (e *Engine) RejectItem(ctx context.Context, id int64) error {
	return e.store.UpdateItemStatus(ctx, id, store.StatusRejected, "")
}

// ResolveConflict records the user's decision for a colliding move file:
//   - "replace": delete the existing target file on execution and move the new
//     one in (the new release wins).
//   - "keep": keep the existing target file and drop the incoming duplicate by
//     planning it for deletion from the source.
//
// Either way the conflict is cleared so the plan can be applied.
func (e *Engine) ResolveConflict(ctx context.Context, id int64, relPath, resolution string) error {
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
	if f.Conflict == nil {
		return fmt.Errorf("kein Konflikt für diese Datei")
	}
	switch resolution {
	case "replace":
		f.Action = store.FileActionMove
		f.Overwrite = true
		f.OverwritePath = f.Conflict.ExistingPath
		f.Conflict = nil
	case "keep":
		f.Action = store.FileActionDelete
		f.TargetPath = ""
		f.Overwrite = false
		f.OverwritePath = ""
		f.Conflict = nil
	default:
		return fmt.Errorf("invalid resolution")
	}
	e.log.Info("conflict resolved", "item", item.Name, "file", relPath, "resolution", resolution)
	return e.store.UpsertItem(ctx, item)
}
