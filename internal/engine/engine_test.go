package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/ai"
	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/scanner"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

func TestApplyDecisions(t *testing.T) {
	files := []store.File{
		{RelPath: "Show.S01E01.mkv"},           // exact path match
		{RelPath: "subdir/Show.S01E01.nfo"},    // matched by base name only
		{RelPath: "Show.S01E01.sample.mkv"},    // delete
		{RelPath: "unmatched.txt"},             // no decision -> keep
	}
	decisions := []ai.FileDecision{
		{Path: "Show.S01E01.mkv", Action: "move", Confidence: 0.97},
		{Path: "Show.S01E01.nfo", Action: "delete", Confidence: 0.9},
		{Path: "Show.S01E01.sample.mkv", Action: "delete", Confidence: 0.95},
	}
	applyDecisions(files, decisions, "/lib/Show")

	if files[0].Action != store.FileActionMove || files[0].TargetPath != filepath.Join("/lib/Show", "Show.S01E01.mkv") {
		t.Errorf("exact move mapping failed: %+v", files[0])
	}
	if files[1].Action != store.FileActionDelete {
		t.Errorf("base-name fallback failed: %+v", files[1])
	}
	if files[2].Action != store.FileActionDelete {
		t.Errorf("sample delete failed: %+v", files[2])
	}
	if files[3].Action != store.FileActionKeep {
		t.Errorf("unmatched file should be keep: %+v", files[3])
	}
}

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

// TestSetItemTargetRejectsTraversal guards against a path-traversal regression:
// a sub-folder containing ".." must not let the move target escape the library.
func TestSetItemTargetRejectsTraversal(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	libDir := filepath.Join(dir, "Serien")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Serien", store.KindSeries, libDir)
	if err != nil {
		t.Fatal(err)
	}
	// A real directory outside the library that the "../" sub-folder resolves to.
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	item := &store.Item{
		SourcePath: filepath.Join(dir, "src", "Show"),
		Name:       "Show",
		Status:     store.StatusPendingReview,
		Files:      []store.File{{RelPath: "ep.mkv", Action: store.FileActionMove}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	if err := eng.SetItemTarget(ctx, item.ID, lib.ID, "../outside"); err == nil {
		t.Fatal("expected SetItemTarget to reject a '..' sub-folder")
	}

	got, err := st.GetItem(ctx, item.ID)
	if err != nil || got == nil {
		t.Fatalf("get item: %v", err)
	}
	if got.TargetPath == outside {
		t.Errorf("target escaped the library to %q", got.TargetPath)
	}
}

func testEngine(t *testing.T) (*Engine, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := New(st, config.Config{MediaRoot: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return eng, st, dir
}

func TestFindConflict(t *testing.T) {
	entries := []destEntry{
		{name: "Show.S01E01.1080p.WEB.H264-AAA.mkv", size: 100},
		{name: "Show.S01E02.1080p.WEB.H264-AAA.mkv", size: 100},
		{name: "poster.jpg", size: 10},
	}
	if m := findConflict("Show.S01E01.1080p.WEB.H264-AAA.mkv", entries); m == nil {
		t.Error("exact-name collision not detected")
	}
	if m := findConflict("Show.S01E01.2160p.BluRay.H265-BBB.mkv", entries); m == nil || m.name != "Show.S01E01.1080p.WEB.H264-AAA.mkv" {
		t.Errorf("same-episode collision not detected: %+v", m)
	}
	if m := findConflict("Show.S01E03.1080p.mkv", entries); m != nil {
		t.Errorf("unexpected collision for new episode: %+v", m)
	}
	if m := findConflict("Random.Movie.2020.1080p.mkv", entries); m != nil {
		t.Errorf("unexpected collision for unrelated movie: %+v", m)
	}
}

func TestDetectAndReplaceConflict(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	// Target show folder already holds an older release of S01E01.
	showDir := filepath.Join(dir, "Serien", "Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(showDir, "Show.S01E01.720p.WEB.x264-OLD.mkv")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Source item carries a newer release of the same episode (different name).
	srcDir := filepath.Join(dir, "src", "Show.S01E01")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newName := "Show.S01E01.1080p.WEB.H265-NEW.mkv"
	if err := os.WriteFile(filepath.Join(srcDir, newName), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath: srcDir,
		Name:       "Show.S01E01",
		Status:     store.StatusPendingReview,
		TargetPath: showDir,
		Files: []store.File{{
			RelPath:    newName,
			Action:     store.FileActionMove,
			TargetPath: filepath.Join(showDir, newName),
		}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	eng.detectConflicts(item.Files)
	if item.Files[0].Conflict == nil {
		t.Fatal("expected a conflict to be detected")
	}
	if item.Files[0].Conflict.ExistingName != "Show.S01E01.720p.WEB.x264-OLD.mkv" {
		t.Errorf("wrong existing file recorded: %+v", item.Files[0].Conflict)
	}
	if item.Files[0].Conflict.IncomingQuality == "" {
		t.Error("incoming quality summary should be populated")
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	// Plan-apply must be blocked while the conflict is unresolved.
	if err := eng.ApplyPlan(ctx, item.ID); err == nil {
		t.Error("ApplyPlan should refuse an unresolved conflict")
	}

	if err := eng.ResolveConflict(ctx, item.ID, newName, "replace"); err != nil {
		t.Fatalf("resolve replace: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.Files[0].Conflict != nil || !got.Files[0].Overwrite {
		t.Fatalf("replace decision not recorded: %+v", got.Files[0])
	}

	if err := eng.execFile(got, &got.Files[0], store.FileActionMove); err != nil {
		t.Fatalf("execFile move: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old existing file should have been removed on replace")
	}
	if _, err := os.Stat(filepath.Join(showDir, newName)); err != nil {
		t.Errorf("new file should have been moved in: %v", err)
	}
}

func TestResolveConflictKeep(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	showDir := filepath.Join(dir, "Serien", "Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "Show.S02E05.1080p.WEB.H264-GRP.mkv"
	existing := filepath.Join(showDir, name)
	if err := os.WriteFile(existing, []byte("keepme"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "Show.S02E05")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, name)
	if err := os.WriteFile(srcFile, []byte("dup"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath: srcDir,
		Name:       "Show.S02E05",
		Status:     store.StatusPendingReview,
		TargetPath: showDir,
		Files: []store.File{{
			RelPath:    name,
			Action:     store.FileActionMove,
			TargetPath: filepath.Join(showDir, name),
		}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	eng.detectConflicts(item.Files)
	if item.Files[0].Conflict == nil {
		t.Fatal("expected exact-name conflict")
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	if err := eng.ResolveConflict(ctx, item.ID, name, "keep"); err != nil {
		t.Fatalf("resolve keep: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.Files[0].Action != store.FileActionDelete || got.Files[0].Conflict != nil {
		t.Fatalf("keep decision not recorded: %+v", got.Files[0])
	}

	if err := eng.execFile(got, &got.Files[0], store.FileActionDelete); err != nil {
		t.Fatalf("execFile delete: %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Error("existing file should be kept untouched")
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Error("incoming duplicate should have been deleted from source")
	}
}

func TestProcessCandidateSkipsErrorItem(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// A fully configured AI endpoint that must NOT be called for an error item.
	if err := st.SaveAppSettings(ctx, store.AppSettings{
		AIBaseURL: srv.URL, AIModel: "gpt", AIAPIKey: "k", Threshold: 0.8, AutoMove: true,
	}); err != nil {
		t.Fatal(err)
	}

	srcDir := filepath.Join(dir, "src", "Broken.Item")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath:   srcDir,
		Name:         "Broken.Item",
		Status:       store.StatusError,
		ErrorMessage: "ai endpoint returned 500",
		Files:        []store.File{{RelPath: "movie.mkv", Size: 1000}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	cand := scanner.Candidate{
		Path:  srcDir,
		Name:  "Broken.Item",
		Files: []store.File{{RelPath: "movie.mkv", Size: 1000}},
	}
	if err := eng.processCandidate(ctx, cand, filepath.Dir(srcDir)); err != nil {
		t.Fatalf("processCandidate: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("AI endpoint was queried %d times for an error item; want 0", n)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got == nil || got.Status != store.StatusError {
		t.Errorf("error item should be left untouched, got %+v", got)
	}
}

func TestSetItemTargetClearsError(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	libDir := filepath.Join(dir, "Filme")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Filme", store.KindMovie, libDir)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "Movie")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath:   srcDir,
		Name:         "Movie",
		Status:       store.StatusError,
		ErrorMessage: "classify: boom",
		Files:        []store.File{{RelPath: "movie.mkv", Size: 10}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	if err := eng.SetItemTarget(ctx, item.ID, lib.ID, ""); err != nil {
		t.Fatalf("set target: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.Status != store.StatusPendingReview {
		t.Errorf("status = %q, want pending_review", got.Status)
	}
	if got.ErrorMessage != "" {
		t.Errorf("error message should be cleared, got %q", got.ErrorMessage)
	}
	if got.TargetPath != libDir {
		t.Errorf("target = %q, want %q", got.TargetPath, libDir)
	}
}

func TestSetItemTargetRoutesFilesToMove(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	libDir := filepath.Join(dir, "Filme")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Filme", store.KindMovie, libDir)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "Movie")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath: srcDir,
		Name:       "Movie",
		Status:     store.StatusError,
		Files: []store.File{
			{RelPath: "movie.mkv"},                                  // undecided -> move
			{RelPath: "sample.mkv", Action: store.FileActionDelete}, // explicit delete preserved
			{RelPath: "extras.mkv", Action: store.FileActionKeep},   // explicit keep preserved
		},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	if err := eng.SetItemTarget(ctx, item.ID, lib.ID, ""); err != nil {
		t.Fatalf("set target: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.Files[0].Action != store.FileActionMove || got.Files[0].TargetPath != filepath.Join(libDir, "movie.mkv") {
		t.Errorf("undecided file should route to move: %+v", got.Files[0])
	}
	if got.Files[1].Action != store.FileActionDelete {
		t.Errorf("explicit delete should be preserved: %+v", got.Files[1])
	}
	if got.Files[2].Action != store.FileActionKeep {
		t.Errorf("explicit keep should be preserved: %+v", got.Files[2])
	}
}

func TestPlanFileActionClearsError(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	srcDir := filepath.Join(dir, "src", "Movie")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath:   srcDir,
		Name:         "Movie",
		Status:       store.StatusError,
		ErrorMessage: "classify: boom",
		Files:        []store.File{{RelPath: "movie.mkv", Size: 10}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	if err := eng.PlanFileAction(ctx, item.ID, "movie.mkv", store.FileActionDelete); err != nil {
		t.Fatalf("plan file action: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.Status != store.StatusPendingReview {
		t.Errorf("status = %q, want pending_review", got.Status)
	}
	if got.ErrorMessage != "" {
		t.Errorf("error message should be cleared, got %q", got.ErrorMessage)
	}
	if got.Files[0].Action != store.FileActionDelete {
		t.Errorf("action = %q, want delete", got.Files[0].Action)
	}
}

func TestCreateNamedTargetFolder(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	libDir := filepath.Join(dir, "Serien")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Serien", store.KindSeries, libDir)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "New.Show.S01E01")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath:   srcDir,
		Name:         "New.Show.S01E01",
		Status:       store.StatusError,
		ErrorMessage: "classify: boom",
		Files:        []store.File{{RelPath: "ep.mkv", Action: store.FileActionMove}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	if err := eng.CreateNamedTargetFolder(ctx, item.ID, lib.ID, "New Show"); err != nil {
		t.Fatalf("create named folder: %v", err)
	}
	want := filepath.Join(libDir, "New Show")
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.TargetPath != want {
		t.Errorf("target = %q, want %q", got.TargetPath, want)
	}
	if got.Status != store.StatusPendingReview || got.ErrorMessage != "" {
		t.Errorf("error should be cleared, got status=%q msg=%q", got.Status, got.ErrorMessage)
	}
	if got.Files[0].TargetPath != filepath.Join(want, "ep.mkv") {
		t.Errorf("file target = %q, want %q", got.Files[0].TargetPath, filepath.Join(want, "ep.mkv"))
	}
}

func TestCreateNamedTargetFolderContainsTraversal(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	libDir := filepath.Join(dir, "Serien")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Serien", store.KindSeries, libDir)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "x")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath: srcDir,
		Name:       "x",
		Status:     store.StatusPendingReview,
		Files:      []store.File{{RelPath: "ep.mkv", Action: store.FileActionMove}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	// A traversal attempt sanitises to a single segment under the library.
	if err := eng.CreateNamedTargetFolder(ctx, item.ID, lib.ID, "../escape"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if got.TargetPath != filepath.Join(libDir, "escape") {
		t.Errorf("target = %q, want a direct child of the library", got.TargetPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape")); !os.IsNotExist(err) {
		t.Error("a traversal must not create a folder outside the library")
	}
}

func TestCreateNamedTargetFolderReusesExisting(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := context.Background()

	libDir := filepath.Join(dir, "Serien")
	if err := os.MkdirAll(filepath.Join(libDir, "The Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := st.AddLibrary(ctx, "Serien", store.KindSeries, libDir)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(dir, "src", "x")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{
		SourcePath: srcDir,
		Name:       "x",
		Status:     store.StatusPendingReview,
		Files:      []store.File{{RelPath: "ep.mkv", Action: store.FileActionMove}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	// Typing the folder name in a different case must reuse the existing folder,
	// not create a near-duplicate that differs only in case.
	if err := eng.CreateNamedTargetFolder(ctx, item.ID, lib.ID, "the show"); err != nil {
		t.Fatalf("create/reuse folder: %v", err)
	}
	got, _ := st.GetItem(ctx, item.ID)
	if filepath.Base(got.TargetPath) != "The Show" {
		t.Errorf("target = %q, want it to reuse the existing 'The Show' folder", got.TargetPath)
	}
	entries, _ := os.ReadDir(libDir)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected exactly one folder in the library, got %v", names)
	}
}

func TestResolveTargetUsesSubfolderFlag(t *testing.T) {
	root := t.TempDir()
	flatDir := filepath.Join(root, "Filme")
	if err := os.MkdirAll(flatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "Serien")
	if err := os.MkdirAll(filepath.Join(subDir, "The Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	libs := []store.Library{
		{ID: 1, Name: "Filme", Kind: store.KindMovie, Path: flatDir, UseSubfolders: false},
		{ID: 2, Name: "Serien", Kind: store.KindSeries, Path: subDir, UseSubfolders: true},
	}
	e := &Engine{}

	// Flat library (no sub-folders): resolves straight to the library root.
	if _, dest, ok, _ := e.resolveTarget(&ai.Result{Library: "Filme"}, libs); !ok || dest != flatDir {
		t.Errorf("flat library should resolve to its root: dest=%q ok=%v", dest, ok)
	}
	// Sub-folder library, no matching folder: needs manual review.
	if _, _, ok, _ := e.resolveTarget(&ai.Result{Library: "Serien", SeriesFolder: "Nope"}, libs); ok {
		t.Error("sub-folder library without a matching folder should need review")
	}
	// Sub-folder library, matching folder (case-insensitive): resolved.
	if _, dest, ok, _ := e.resolveTarget(&ai.Result{Library: "Serien", SeriesFolder: "the show"}, libs); !ok || dest != filepath.Join(subDir, "The Show") {
		t.Errorf("sub-folder library should resolve to the matched folder: dest=%q ok=%v", dest, ok)
	}
	// The flag overrides kind: a movie library set to use sub-folders needs one.
	libs[0].UseSubfolders = true
	if _, _, ok, _ := e.resolveTarget(&ai.Result{Library: "Filme"}, libs); ok {
		t.Error("library flipped to use sub-folders should need review when none match")
	}
}
