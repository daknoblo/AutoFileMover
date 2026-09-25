package engine

import (
	"path/filepath"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/ai"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

const gb = 1 << 30

// releaseFiles mirrors a typical scene release: one feature plus its artwork
// and metadata sidecars.
func releaseFiles() []store.File {
	return []store.File{
		{RelPath: "movie.jpg", Size: 1_100_000},
		{RelPath: "movie.mkv", Size: 5 * gb},
		{RelPath: "movie.nfo", Size: 10_400},
	}
}

func decide(path, action string) ai.FileDecision {
	return ai.FileDecision{Path: path, Action: action, Confidence: 1}
}

func actionOf(t *testing.T, files []store.File, relPath string) store.File {
	t.Helper()
	for _, f := range files {
		if f.RelPath == relPath {
			return f
		}
	}
	t.Fatalf("file %q missing from %+v", relPath, files)
	return store.File{}
}

func TestArtworkAndMetadataAreDeletedNotKept(t *testing.T) {
	files := releaseFiles()
	applyDecisions(files, []ai.FileDecision{
		decide("movie.jpg", ai.ActionDelete),
		decide("movie.mkv", ai.ActionMove),
		decide("movie.nfo", ai.ActionDelete),
	}, "/dataroot/filme")

	if got := actionOf(t, files, "movie.mkv"); got.Action != store.FileActionMove ||
		got.TargetPath != filepath.Join("/dataroot/filme", "movie.mkv") {
		t.Fatalf("feature not moved: %+v", got)
	}
	for _, junk := range []string{"movie.jpg", "movie.nfo"} {
		if got := actionOf(t, files, junk); got.Action != store.FileActionDelete {
			t.Errorf("%s action = %q, want delete", junk, got.Action)
		}
		if got := actionOf(t, files, junk); got.TargetPath != "" {
			t.Errorf("%s must not carry a destination: %q", junk, got.TargetPath)
		}
	}
}

// A finished file can only have been moved or deleted, because execFile is the
// sole writer of Done and rejects every other action. The UI therefore must not
// present a completed delete and an untouched file the same way.
func TestOnlyMoveAndDeleteCanCompleteAFile(t *testing.T) {
	eng, _, dir := testEngine(t)
	item := &store.Item{SourcePath: filepath.Join(dir, "release"), Files: releaseFiles()}
	for i := range item.Files {
		if err := eng.execFile(t.Context(), item, &item.Files[i], store.FileActionKeep); err == nil {
			t.Fatalf("keep must not complete a file: %+v", item.Files[i])
		}
		if item.Files[i].Done {
			t.Fatalf("a rejected action must not mark the file done: %+v", item.Files[i])
		}
	}
}

func TestLargestVideoIsNeverAutoDeleted(t *testing.T) {
	// A model that mixes up the feature and the sample would otherwise destroy
	// the download, which no later step can undo.
	files := []store.File{
		{RelPath: "movie.mkv", Size: 5 * gb},
		{RelPath: "movie-sample.mkv", Size: 40 << 20},
	}
	applyDecisions(files, []ai.FileDecision{
		decide("movie.mkv", ai.ActionDelete),
		decide("movie-sample.mkv", ai.ActionMove),
	}, "/dataroot/filme")

	feature := actionOf(t, files, "movie.mkv")
	if feature.Action != store.FileActionKeep {
		t.Fatalf("the feature was not protected: %+v", feature)
	}
	if feature.TargetPath != "" {
		t.Errorf("a protected file must not keep a destination: %q", feature.TargetPath)
	}
	if feature.Reason == "" {
		t.Error("the downgrade must explain itself in the UI")
	}
}

func TestSmallerVideoIsStillDeletedAsASample(t *testing.T) {
	// The common case: two videos, only the smaller one is junk. The guard must
	// not turn this into manual review.
	files := []store.File{
		{RelPath: "movie.mkv", Size: 5 * gb},
		{RelPath: "second.mkv", Size: 45 << 20},
	}
	applyDecisions(files, []ai.FileDecision{
		decide("movie.mkv", ai.ActionMove),
		decide("second.mkv", ai.ActionDelete),
	}, "/dataroot/filme")

	if got := actionOf(t, files, "movie.mkv"); got.Action != store.FileActionMove {
		t.Fatalf("feature action = %q", got.Action)
	}
	if got := actionOf(t, files, "second.mkv"); got.Action != store.FileActionDelete {
		t.Fatalf("sample action = %q, want delete", got.Action)
	}
}

func TestASampleOnlyFolderStaysCleanable(t *testing.T) {
	// Here the largest video is the sample itself, so deleting it is correct and
	// the guard must step aside.
	for _, name := range []string{"Some.Release-sample.mkv", "movie.TRAILER.mkv"} {
		files := []store.File{{RelPath: name, Size: 40 << 20}}
		applyDecisions(files, []ai.FileDecision{decide(name, ai.ActionDelete)}, "/dataroot/filme")
		if files[0].Action != store.FileActionDelete {
			t.Errorf("%s action = %q, want delete", name, files[0].Action)
		}
	}
}

func TestGuardIgnoresNonVideoAndCompletedFiles(t *testing.T) {
	// A huge archive is not a video, so deleting it stays a normal decision.
	files := []store.File{
		{RelPath: "release.rar", Size: 6 * gb},
		{RelPath: "movie.mkv", Size: 1 * gb},
	}
	applyDecisions(files, []ai.FileDecision{
		decide("release.rar", ai.ActionDelete),
		decide("movie.mkv", ai.ActionMove),
	}, "/dataroot/filme")
	if got := actionOf(t, files, "release.rar"); got.Action != store.FileActionDelete {
		t.Fatalf("archive action = %q, want delete", got.Action)
	}

	// An already moved feature must not make a later junk video untouchable.
	done := []store.File{
		{RelPath: "movie.mkv", Size: 5 * gb, Action: store.FileActionMove, Done: true},
		{RelPath: "extra.mkv", Size: 50 << 20},
	}
	applyDecisions(done, []ai.FileDecision{decide("extra.mkv", ai.ActionDelete)}, "/dataroot/filme")
	if got := actionOf(t, done, "extra.mkv"); got.Action != store.FileActionDelete {
		t.Fatalf("leftover action = %q, want delete", got.Action)
	}
	if got := actionOf(t, done, "movie.mkv"); got.Action != store.FileActionMove || !got.Done {
		t.Fatalf("completed file was rewritten: %+v", got)
	}
}
