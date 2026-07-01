package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daknoblo/AutoFileMover/internal/ai"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// buildRequest constructs the AI classification request, including existing
// series folders and any user-provided folder descriptions so the model can
// map to an existing show with extra context. Folder notes and per-library
// sub-folder listings are taken from the per-scan cache in sc.
func (e *Engine) buildRequest(ctx context.Context, sc *scanContext, name string, srcFiles []store.File, sourcePath string) ai.Request {
	notes := sc.folderNotes(ctx, e)

	files := make([]ai.FileInput, 0, len(srcFiles))
	for _, f := range srcFiles {
		if f.RelPath == "" {
			continue // synthetic empty-folder marker, nothing to classify
		}
		files = append(files, ai.FileInput{Path: f.RelPath, SizeBytes: f.Size})
	}
	infos := make([]ai.LibraryInfo, 0, len(sc.libs))
	for _, l := range sc.libs {
		info := ai.LibraryInfo{Name: l.Name, Kind: l.Kind, Description: notes[l.Path]}
		// Include the existing sub-folder structure of every library so the model
		// can map an item into the matching show/collection folder, not just the
		// library root.
		for _, name := range sc.subfolders(l.Path) {
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
		GlobalContext: sc.settings.AIContext,
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
