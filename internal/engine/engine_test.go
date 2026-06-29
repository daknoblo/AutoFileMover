package engine

import (
	"os"
	"path/filepath"
	"testing"
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
