package mover

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveFileAndCleanup(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	file := filepath.Join(src, "movie.mkv")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	final, err := Move(file, dst)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("source should be gone")
	}
}

func TestMoveRefusesOverwrite(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	a := filepath.Join(src, "x.mkv")
	_ = os.WriteFile(a, []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(dst, "x.mkv"), []byte("2"), 0o644)
	if _, err := Move(a, dst); err == nil {
		t.Fatal("expected collision error")
	}
}

func TestDeleteAndRemoveIfEmpty(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "junk.nfo")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if err := Delete(f); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := Delete(f); err != nil {
		t.Fatalf("delete missing should be nil: %v", err)
	}
	if err := RemoveIfEmpty(dir); err != nil {
		t.Fatalf("remove empty: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("empty dir should be removed")
	}
}

func TestCheckWritable(t *testing.T) {
	if err := CheckWritable(t.TempDir()); err != nil {
		t.Fatalf("writable dir reported error: %v", err)
	}
	if err := CheckWritable(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("missing dir should report not writable")
	}
}

func TestRemoveEmptyDirs(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join(root, "release")
	for _, sub := range []string{"E01", "E02", "E03"} {
		if err := os.MkdirAll(filepath.Join(rel, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// E03 keeps a file; E01 and E02 are empty.
	if err := os.WriteFile(filepath.Join(rel, "E03", "keep.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveEmptyDirs(rel); err != nil {
		t.Fatalf("remove empty dirs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rel, "E01")); !os.IsNotExist(err) {
		t.Error("empty E01 should have been removed")
	}
	if _, err := os.Stat(filepath.Join(rel, "E02")); !os.IsNotExist(err) {
		t.Error("empty E02 should have been removed")
	}
	if _, err := os.Stat(filepath.Join(rel, "E03", "keep.mkv")); err != nil {
		t.Error("E03 with a file must be kept")
	}
	if _, err := os.Stat(rel); err != nil {
		t.Error("release folder must remain while it still holds files")
	}

	// Remove the remaining file: the whole tree should now be pruned.
	_ = os.Remove(filepath.Join(rel, "E03", "keep.mkv"))
	if err := RemoveEmptyDirs(rel); err != nil {
		t.Fatalf("remove empty dirs (2): %v", err)
	}
	if _, err := os.Stat(rel); !os.IsNotExist(err) {
		t.Error("fully emptied release folder should be removed")
	}
}
