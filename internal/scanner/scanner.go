// Package scanner inspects source (download) folders and turns each top-level
// entry into a candidate item with its contained files.
package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

// Candidate is a detected top-level entry inside a source folder.
type Candidate struct {
	// Name is the base name of the entry (folder or file name).
	Name string
	// Path is the absolute path of the entry.
	Path string
	// IsDir reports whether the entry is a directory.
	IsDir bool
	// Files lists the contained files (for a single file: the file itself).
	Files []store.File
	// LastModified is the most recent modification time within the entry.
	LastModified time.Time
}

// ScanSource returns the candidate items directly inside sourcePath. Entries
// whose name matches an ignore pattern are skipped, and files matching a
// pattern are excluded from each candidate's file list.
func ScanSource(sourcePath string, ignore []string) ([]Candidate, error) {
	entries, err := os.ReadDir(sourcePath)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip hidden/partial files
		}
		if matchesAny(name, ignore) {
			continue // skip ignored folders/files (e.g. _UNPACK, sample)
		}
		full := filepath.Join(sourcePath, name)
		c, err := inspect(full, e.IsDir(), ignore)
		if err != nil {
			continue // unreadable entry; skip
		}
		out = append(out, c)
	}
	return out, nil
}

// matchesAny reports whether name matches any ignore pattern. A pattern with
// glob characters is matched with filepath.Match against the base name;
// otherwise a case-insensitive substring match is used.
func matchesAny(name string, ignore []string) bool {
	lower := strings.ToLower(name)
	for _, p := range ignore {
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := filepath.Match(strings.ToLower(p), lower); ok {
				return true
			}
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func inspect(path string, isDir bool, ignore []string) (Candidate, error) {
	c := Candidate{Name: filepath.Base(path), Path: path, IsDir: isDir}
	if !isDir {
		info, err := os.Stat(path)
		if err != nil {
			return c, err
		}
		c.Files = []store.File{{
			RelPath: info.Name(),
			Size:    info.Size(),
			Ext:     strings.ToLower(filepath.Ext(info.Name())),
		}}
		c.LastModified = info.ModTime()
		return c, nil
	}

	var latest time.Time
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // ignore traversal errors on individual entries
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if matchesAny(info.Name(), ignore) {
			return nil // skip ignored files (e.g. sample)
		}
		rel, _ := filepath.Rel(path, p)
		c.Files = append(c.Files, store.File{
			RelPath: rel,
			Size:    info.Size(),
			Ext:     strings.ToLower(filepath.Ext(info.Name())),
		})
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	c.LastModified = latest
	return c, err
}

// IsStable reports whether the candidate has not been modified within the given
// window, i.e. the download is likely complete.
func (c Candidate) IsStable(window time.Duration) bool {
	if c.LastModified.IsZero() {
		return false
	}
	return time.Since(c.LastModified) >= window
}
