package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSourceListsCandidates(t *testing.T) {
	root := t.TempDir()

	movie := filepath.Join(root, "Movie.2020")
	mustMkdir(t, filepath.Join(movie, "sub"))
	writeFile(t, filepath.Join(movie, "movie.mkv"), "video")
	writeFile(t, filepath.Join(movie, "sub", "movie.srt"), "sub")

	writeFile(t, filepath.Join(root, "loose.mkv"), "x")

	// Hidden entry and an ignored top-level folder must be skipped.
	writeFile(t, filepath.Join(root, ".partial"), "x")
	mustMkdir(t, filepath.Join(root, "_UNPACK_x"))

	cands, err := ScanSource(root, []string{"_UNPACK"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Candidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}
	if _, ok := byName[".partial"]; ok {
		t.Error("hidden entry should be skipped")
	}
	if _, ok := byName["_UNPACK_x"]; ok {
		t.Error("ignored folder should be skipped")
	}
	m, ok := byName["Movie.2020"]
	if !ok {
		t.Fatal("Movie.2020 candidate missing")
	}
	if !m.IsDir || len(m.Files) != 2 {
		t.Errorf("expected dir with 2 files, got IsDir=%v files=%d", m.IsDir, len(m.Files))
	}
	loose, ok := byName["loose.mkv"]
	if !ok || loose.IsDir || len(loose.Files) != 1 {
		t.Errorf("loose file candidate wrong: %+v", loose)
	}
}

func TestScanSourceEmptyFolderUsesDirMtime(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "Empty"))

	cands, err := ScanSource(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Name != "Empty" {
		t.Fatalf("expected 1 empty candidate, got %+v", cands)
	}
	if cands[0].LastModified.IsZero() {
		t.Error("empty folder should fall back to a non-zero dir mtime")
	}
	if len(cands[0].Files) != 0 {
		t.Errorf("empty folder should carry no files, got %d", len(cands[0].Files))
	}
}

func TestMatchesAny(t *testing.T) {
	if !matchesAny("Movie.sample.mkv", []string{"sample"}) {
		t.Error("substring match failed")
	}
	if !matchesAny("show_UNPACK", []string{"*UNPACK*"}) {
		t.Error("glob match failed")
	}
	if matchesAny("Clean.Movie", []string{"sample", "_UNPACK"}) {
		t.Error("unexpected match on clean name")
	}
	if matchesAny("anything", []string{""}) {
		t.Error("empty pattern must not match")
	}
}

func TestIsStable(t *testing.T) {
	old := Candidate{LastModified: time.Now().Add(-time.Minute)}
	if !old.IsStable(30 * time.Second) {
		t.Error("candidate older than the window should be stable")
	}
	fresh := Candidate{LastModified: time.Now().Add(time.Minute)}
	if fresh.IsStable(30 * time.Second) {
		t.Error("candidate modified within the window should not be stable")
	}
	var zero Candidate
	if zero.IsStable(time.Second) {
		t.Error("zero modification time should not be stable")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
