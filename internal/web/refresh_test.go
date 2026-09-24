package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

func TestListRefreshesFilesystemBeforeResponding(t *testing.T) {
	for _, endpoint := range []string{"/items", "/queue"} {
		t.Run(endpoint, func(t *testing.T) {
			ts, st, dir := testHTTP(t)
			source := filepath.Join(dir, "downloads")
			if err := os.Mkdir(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddSource(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(source, "movie.mkv")
			if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
				t.Fatal(err)
			}
			item := &store.Item{SourcePath: path, Name: "movie.mkv", Status: store.StatusPendingReview,
				Files: []store.File{{RelPath: "movie.mkv", Size: 5}}}
			if err := st.UpsertItem(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			if _, err := st.EnqueueJob(t.Context(), item.ID, store.JobApplyPlan, store.JobPayload{}); err != nil {
				t.Fatal(err)
			}
			readList := func() []json.RawMessage {
				t.Helper()
				resp, err := http.Get(ts.URL + "/api" + endpoint)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d", resp.StatusCode)
				}
				var entries []json.RawMessage
				if endpoint == "/queue" {
					var body struct {
						Jobs []json.RawMessage `json:"jobs"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					entries = body.Jobs
				} else if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
					t.Fatal(err)
				}
				return entries
			}
			if entries := readList(); len(entries) != 1 {
				t.Fatalf("before deletion = %d, want 1", len(entries))
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if entries := readList(); len(entries) != 0 {
				t.Fatalf("after deletion = %d, want 0", len(entries))
			}
			counts, err := st.CountJobs(t.Context())
			if err != nil || counts != (store.JobCounts{}) {
				t.Fatalf("stale queue count = %+v, %v", counts, err)
			}
		})
	}
}

func TestListReportsUnavailableSource(t *testing.T) {
	ts, st, dir := testHTTP(t)
	source := filepath.Join(dir, "unavailable")
	if _, err := st.AddSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{SourcePath: filepath.Join(source, "movie.mkv"), Status: store.StatusError}
	if err := st.UpsertItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/items", "/queue"} {
		resp, err := http.Get(ts.URL + "/api" + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusInternalServerError || body["error"] == "" {
			t.Fatalf("response = %d, %+v, %v", resp.StatusCode, body, err)
		}
	}
	if got, err := st.GetItem(t.Context(), item.ID); err != nil || got == nil {
		t.Fatalf("unavailable source lost its item: %v", err)
	}
}

func TestClearHistoryOnlyRemovesRecords(t *testing.T) {
	ts, st, dir := testHTTP(t)
	path := filepath.Join(dir, "keep.mkv")
	if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{store.StatusConfirmed, store.StatusPendingReview} {
		item := &store.Item{SourcePath: path + status, Status: status,
			Files: []store.File{{RelPath: "keep.mkv", TargetPath: path, Done: true}}}
		if err := st.UpsertItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/history", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Removed int `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || result.Removed != 1 {
		t.Fatalf("clear = %d, %+v", resp.StatusCode, result)
	}
	items, err := st.ListItems(t.Context(), "", 0)
	if err != nil || len(items) != 1 || items[0].Status != store.StatusPendingReview {
		t.Fatalf("open work affected: %+v, %v", items, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("cleanup touched files: %q, %v", data, err)
	}
}
