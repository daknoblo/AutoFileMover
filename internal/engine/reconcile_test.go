package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

func TestEmptySourceRemovesMissingItemsAndJobs(t *testing.T) {
	for _, scan := range []bool{false, true} {
		t.Run(fmt.Sprintf("scan=%t", scan), func(t *testing.T) {
			eng, st, dir := testEngine(t)
			ctx := t.Context()
			source := filepath.Join(dir, "downloads")
			if err := os.Mkdir(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddSource(ctx, source); err != nil {
				t.Fatal(err)
			}
			// More than the API limit, to catch cleanup limited to visible rows.
			for i := range 505 {
				status := store.StatusPendingReview
				if i%2 == 0 {
					status = store.StatusError
				}
				item := &store.Item{SourcePath: filepath.Join(source, fmt.Sprint(i)), Status: status}
				if err := st.UpsertItem(ctx, item); err != nil {
					t.Fatal(err)
				}
				job, err := st.EnqueueJob(ctx, item.ID, store.JobApplyPlan, store.JobPayload{})
				if err != nil {
					t.Fatal(err)
				}
				if i%2 == 0 {
					if err := st.FailJob(ctx, job.ID, "source missing"); err != nil {
						t.Fatal(err)
					}
				}
			}
			history := &store.Item{SourcePath: filepath.Join(source, "completed"), Status: store.StatusConfirmed}
			if err := st.UpsertItem(ctx, history); err != nil {
				t.Fatal(err)
			}
			done, err := st.EnqueueJob(ctx, history.ID, store.JobApplyPlan, store.JobPayload{})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.CompleteJob(ctx, done.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.EnqueueJob(ctx, history.ID, store.JobReclassify, store.JobPayload{}); err != nil {
				t.Fatal(err)
			}
			if scan {
				eng.ProcessSource(ctx, source)
			} else if err := eng.RefreshSources(ctx); err != nil {
				t.Fatal(err)
			}
			items, err := st.ListItems(ctx, "", 0)
			if err != nil || len(items) != 1 || items[0].ID != history.ID {
				t.Fatalf("items = %d, err = %v; want only history", len(items), err)
			}
			jobs, err := st.ListJobs(ctx, 500)
			if err != nil || len(jobs) != 1 || jobs[0].ID != done.ID {
				t.Fatalf("jobs = %+v, err = %v; want only completed job", jobs, err)
			}
			counts, err := st.CountJobs(ctx)
			if err != nil || counts != (store.JobCounts{}) {
				t.Fatalf("counts = %+v, err = %v", counts, err)
			}
		})
	}
}

func TestRefreshSourceFilesPreservesDecisions(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := t.Context()
	source := filepath.Join(dir, "downloads")
	path := filepath.Join(source, "_UNPACK_existing")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"same.mkv", "changed.mkv", "new.mkv"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	same := store.File{RelPath: "same.mkv", Size: 4, Action: store.FileActionMove,
		TargetPath: "/library/same.mkv", Overwrite: true, OverwritePath: "/library/old.mkv"}
	done := store.File{RelPath: "done.mkv", Action: store.FileActionMove, Done: true}
	item := &store.Item{SourcePath: path, Status: store.StatusError, DetectedType: "movie",
		ErrorMessage: "AI failed", Files: []store.File{
			same, done,
			{RelPath: "missing.mkv", Action: store.FileActionDelete},
			{RelPath: "changed.mkv", Size: 1, Action: store.FileActionDelete},
		}}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(ctx, item.ID, store.JobFileAction,
		store.JobPayload{RelPath: "missing.mkv", Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(ctx, item.ID, store.JobFileAction,
		store.JobPayload{RelPath: "changed.mkv", Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	eng.cfg.StabilityWindow = time.Hour // Refresh must not wait for stability.
	if err := eng.RefreshSources(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != item.Status || got.ErrorMessage != item.ErrorMessage || len(got.Files) != 4 {
		t.Fatalf("unexpected refreshed item: %+v", got)
	}
	byPath := map[string]store.File{}
	for _, f := range got.Files {
		byPath[f.RelPath] = f
	}
	if !reflect.DeepEqual(byPath["same.mkv"], same) || !reflect.DeepEqual(byPath["done.mkv"], done) {
		t.Fatalf("lost decisions or journal: %+v", got.Files)
	}
	for _, name := range []string{"changed.mkv", "new.mkv"} {
		if f := byPath[name]; f.Action != store.FileActionKeep || f.Size != 4 || f.Done || f.Overwrite {
			t.Fatalf("new/changed file not reset for review: %+v", f)
		}
	}
	jobs, err := st.ListJobs(ctx, 0)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("obsolete file job retained: %+v, %v", jobs, err)
	}
}

func TestUnavailableSourceRetainsItems(t *testing.T) {
	eng, st, dir := testEngine(t)
	source := filepath.Join(dir, "offline")
	if _, err := st.AddSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{SourcePath: filepath.Join(source, "Movie"), Status: store.StatusPendingReview}
	if err := st.UpsertItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	if err := eng.RefreshSources(t.Context()); err == nil {
		t.Fatal("expected explicit refresh error")
	}
	got, err := st.GetItem(t.Context(), item.ID)
	if err != nil || got == nil {
		t.Fatalf("unavailable source lost its item: %v", err)
	}
}

func TestRefreshLeavesBusyItemAlone(t *testing.T) {
	eng, st, dir := testEngine(t)
	if _, err := st.AddSource(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{SourcePath: filepath.Join(dir, "moving"), Status: store.StatusMoving}
	if err := st.UpsertItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	release, err := eng.locks.acquire(t.Context(), item.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = eng.RefreshSources(ctx)
	release()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetItem(t.Context(), item.ID); err != nil || got == nil {
		t.Fatalf("busy item removed: %v", err)
	}
	if err := eng.RefreshSources(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetItem(t.Context(), item.ID); err != nil || got != nil {
		t.Fatalf("missing item not removed after lock release: %+v, %v", got, err)
	}
}

func TestRefreshExistingEmptyFolderAndOtherSources(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := t.Context()
	source := filepath.Join(dir, "downloads")
	empty := filepath.Join(source, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{empty, filepath.Join(dir, "downloads-other", "missing")} {
		item := &store.Item{SourcePath: path, Status: store.StatusPendingReview,
			Files: []store.File{{RelPath: "gone.mkv", Action: store.FileActionMove}}}
		if err := st.UpsertItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.RefreshSources(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := st.ListItems(ctx, "", 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %+v, %v", items, err)
	}
	for _, item := range items {
		if len(item.Files) != 1 {
			t.Fatalf("files = %+v", item.Files)
		}
		if item.SourcePath == empty {
			if f := item.Files[0]; f.RelPath != "" || f.Action != store.FileActionDelete {
				t.Fatalf("expected empty folder marker, got %+v", f)
			}
		} else if item.Files[0].RelPath != "gone.mkv" {
			t.Fatal("unconfigured source was modified")
		}
	}
}

func TestIncompleteListingDoesNotDropFiles(t *testing.T) {
	eng, st, dir := testEngine(t)
	ctx := t.Context()
	source := filepath.Join(dir, "downloads")
	itemPath := filepath.Join(source, "release")
	unreadable := filepath.Join(itemPath, "private")
	if err := os.MkdirAll(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	if _, err := os.ReadDir(unreadable); err == nil {
		t.Skip("filesystem permits reading despite removed permissions")
	}
	if _, err := st.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{SourcePath: itemPath, Status: store.StatusPendingReview,
		Files: []store.File{{RelPath: "private/movie.mkv", Size: 10, Action: store.FileActionMove}}}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := eng.RefreshSources(ctx); err == nil {
		t.Fatal("incomplete listing must report a refresh error")
	}
	got, err := st.GetItem(ctx, item.ID)
	if err != nil || got == nil || !reflect.DeepEqual(got.Files, item.Files) {
		t.Fatalf("incomplete listing changed files: %+v, %v", got, err)
	}
}

func TestReclassifyReadsCurrentFiles(t *testing.T) {
	eng, st, dir := testEngine(t)
	requests := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- string(body)
		http.Error(w, "test endpoint", http.StatusBadRequest)
	}))
	defer srv.Close()
	ctx := t.Context()
	if err := st.SaveAppSettings(ctx, store.AppSettings{AIBaseURL: srv.URL, AIModel: "test", AIAPIKey: "test"}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "Release")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "current-only.mkv"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{SourcePath: source, Name: "Release", Status: store.StatusPendingReview,
		Files: []store.File{{RelPath: "stale-only.mkv"}, {RelPath: "completed-only.mkv", Done: true}}}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := eng.ReclassifyItem(ctx, item.ID); err == nil {
		t.Fatal("expected mock endpoint error")
	}
	select {
	case body := <-requests:
		if !strings.Contains(body, "current-only.mkv") || strings.Contains(body, "stale-only.mkv") ||
			strings.Contains(body, "completed-only.mkv") {
			t.Fatalf("classification request did not use current files: %s", body)
		}
	default:
		t.Fatal("classification endpoint not called")
	}
}
