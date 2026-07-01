package store

import (
	"context"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, context.Background()
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}
	// Re-opening runs migrate() again; the guarded ALTER TABLE statements must be
	// harmless no-ops on a DB that already has the columns.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second open (migration not idempotent): %v", err)
	}
	_ = st2.Close()
}

func TestSettingsRoundTrip(t *testing.T) {
	st, ctx := testStore(t)
	in := AppSettings{
		AIBaseURL: "https://api.example.com", AIAPIKey: "secret", AIModel: "gpt",
		AIAPIVersion: "2024-06-01", Threshold: 0.75, AutoMove: true,
		IgnorePatterns: []string{"sample", "_UNPACK"}, AIContext: "ctx",
	}
	if err := st.SaveAppSettings(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadAppSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AIBaseURL != in.AIBaseURL || got.AIModel != in.AIModel || got.AIAPIKey != "secret" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Threshold != 0.75 || !got.AutoMove || len(got.IgnorePatterns) != 2 {
		t.Errorf("typed fields mismatch: %+v", got)
	}
}

func TestSaveAppSettingsPreservesAPIKey(t *testing.T) {
	st, ctx := testStore(t)
	if err := st.SaveAppSettings(ctx, AppSettings{AIAPIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	// A later save without a key must not wipe the stored secret.
	if err := st.SaveAppSettings(ctx, AppSettings{AIModel: "gpt"}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.LoadAppSettings(ctx)
	if got.AIAPIKey != "secret" {
		t.Errorf("API key should be preserved when empty, got %q", got.AIAPIKey)
	}
}

func TestLoadAppSettingsDefaults(t *testing.T) {
	st, ctx := testStore(t)
	got, err := st.LoadAppSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Threshold != 0.9 {
		t.Errorf("default threshold = %v, want 0.9", got.Threshold)
	}
	if !got.AutoMove {
		t.Error("auto-move should default to true")
	}
	if got.DryRun {
		t.Error("dry-run should default to false")
	}
	if len(got.IgnorePatterns) != len(DefaultIgnorePatterns) {
		t.Errorf("default ignore patterns = %v", got.IgnorePatterns)
	}
}

func TestItemUpsertGetListFilter(t *testing.T) {
	st, ctx := testStore(t)
	it := &Item{SourcePath: "/src/a", Name: "a", Status: StatusPendingReview, Files: []File{{RelPath: "a.mkv", Size: 5}}}
	if err := st.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if it.ID == 0 {
		t.Fatal("UpsertItem should populate the ID")
	}
	// Upserting the same source path updates in place (no duplicate row).
	it.Status = StatusConfirmed
	if err := st.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.ListItems(ctx, "", 10); len(all) != 1 {
		t.Fatalf("upsert by source_path should not duplicate; got %d rows", len(all))
	}
	if pending, _ := st.ListItems(ctx, StatusPendingReview, 10); len(pending) != 0 {
		t.Errorf("no pending items expected, got %d", len(pending))
	}
	if confirmed, _ := st.ListItems(ctx, StatusConfirmed, 10); len(confirmed) != 1 {
		t.Errorf("want 1 confirmed item, got %d", len(confirmed))
	}
	// files_json round-trips through the TEXT column.
	got, _ := st.GetItem(ctx, it.ID)
	if got == nil || len(got.Files) != 1 || got.Files[0].RelPath != "a.mkv" || got.Files[0].Size != 5 {
		t.Errorf("files_json round-trip failed: %+v", got)
	}
	found, _ := st.FindItemBySource(ctx, "/src/a")
	if found == nil || found.ID != it.ID {
		t.Errorf("FindItemBySource failed: %+v", found)
	}
	if err := st.DeleteItem(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := st.ListItems(ctx, "", 10); len(remaining) != 0 {
		t.Errorf("item not deleted: %+v", remaining)
	}
}

func TestSourcesCRUD(t *testing.T) {
	st, ctx := testStore(t)
	src, err := st.AddSource(ctx, "/src/a")
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListSources(ctx); len(list) != 1 || list[0].Path != "/src/a" {
		t.Fatalf("want 1 source, got %+v", list)
	}
	if err := st.DeleteSource(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListSources(ctx); len(list) != 0 {
		t.Errorf("source not deleted: %+v", list)
	}
}

func TestLibrariesSubfolderDefaults(t *testing.T) {
	st, ctx := testStore(t)
	movie, err := st.AddLibrary(ctx, "Filme", KindMovie, "/lib/Filme")
	if err != nil {
		t.Fatal(err)
	}
	if movie.UseSubfolders {
		t.Error("movie library should default to use_subfolders=false")
	}
	series, err := st.AddLibrary(ctx, "Serien", KindSeries, "/lib/Serien")
	if err != nil {
		t.Fatal(err)
	}
	if !series.UseSubfolders {
		t.Error("series library should default to use_subfolders=true")
	}
	if err := st.SetLibraryUseSubfolders(ctx, movie.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetLibrary(ctx, movie.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UseSubfolders {
		t.Error("SetLibraryUseSubfolders(true) did not persist")
	}
}

func TestFolderNotesRoundTrip(t *testing.T) {
	st, ctx := testStore(t)
	const p = "/lib/Serien/Show"
	if err := st.SetFolderNote(ctx, p, "the show"); err != nil {
		t.Fatal(err)
	}
	byPath, err := st.FolderNotesByPath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if byPath[p] != "the show" {
		t.Errorf("folder note not stored: %v", byPath)
	}
	// Updating the same path replaces in place rather than adding a row.
	if err := st.SetFolderNote(ctx, p, "updated"); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListFolderNotes(ctx); len(list) != 1 {
		t.Errorf("want 1 folder note after update, got %d", len(list))
	}
	byPath, _ = st.FolderNotesByPath(ctx)
	if byPath[p] != "updated" {
		t.Error("folder note not updated in place")
	}
}
