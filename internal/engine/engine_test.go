package engine

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

func TestMatchSubfolder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "The Terminal List"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := matchSubfolder(root, "the terminal list"); got != "The Terminal List" {
		t.Errorf("case-insensitive match failed: got %q", got)
	}
	if got := matchSubfolder(root, "Nonexistent Show"); got != "" {
		t.Errorf("expected no match, got %q", got)
	}
}

func TestSanitizeFolder(t *testing.T) {
	cases := map[string]string{
		"  The Terminal List ": "The Terminal List",
		"a/b\\c":               "a b c",
		"..":                   "",
		"Show.":                "Show",
	}
	for in, want := range cases {
		if got := sanitizeFolder(in); got != want {
			t.Errorf("sanitizeFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateTargetFolder(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	libDir := filepath.Join(dir, "Serien")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Serien", store.KindSeries, libDir)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "Show.S01E01")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lid := lib.ID
	item := &store.Item{
		SourcePath:         srcDir,
		Name:               "Show.S01E01",
		Status:             store.StatusPendingReview,
		SuggestedLibraryID: &lid,
		SuggestedFolder:    "The Terminal List",
		Files:              []store.File{{RelPath: "ep.mkv", Action: store.FileActionMove}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	eng := New(st, config.Config{MediaRoot: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := eng.CreateTargetFolder(ctx, item.ID); err != nil {
		t.Fatalf("create target folder: %v", err)
	}

	want := filepath.Join(libDir, "The Terminal List")
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	got, err := st.GetItem(ctx, item.ID)
	if err != nil || got == nil {
		t.Fatalf("get item: %v", err)
	}
	if got.TargetPath != want {
		t.Errorf("target path = %q, want %q", got.TargetPath, want)
	}
	if got.SuggestedFolder != "" || got.SuggestedLibraryID != nil {
		t.Errorf("suggestion should be cleared after creation")
	}
	if len(got.Files) != 1 || got.Files[0].TargetPath != filepath.Join(want, "ep.mkv") {
		t.Errorf("move file target not set: %+v", got.Files)
	}
}
