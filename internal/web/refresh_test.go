package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

func waitForSourceRefresh(t *testing.T, srv *Server) {
	t.Helper()
	srv.refreshMu.Lock()
	call := srv.refresh
	srv.refreshMu.Unlock()
	if call == nil {
		t.Fatal("no refresh scheduled")
	}
	select {
	case <-call.done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish")
	}
}

func expireSourceRefresh(srv *Server) {
	srv.refreshMu.Lock()
	defer srv.refreshMu.Unlock()
	srv.refresh.status.FinishedAt = time.Now().Add(-sourceRefreshInterval)
}

func TestListRefreshesFilesystemInBackground(t *testing.T) {
	for _, endpoint := range []string{"/items", "/queue"} {
		t.Run(endpoint, func(t *testing.T) {
			ts, st, dir, srv := testHTTPServer(t)
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
			waitForSourceRefresh(t, srv)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			expireSourceRefresh(srv)
			readList()
			waitForSourceRefresh(t, srv)
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
	ts, st, dir, srv := testHTTPServer(t)
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
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list unavailable: %d", resp.StatusCode)
		}
		waitForSourceRefresh(t, srv)
		resp, err = http.Get(ts.URL + "/api/status")
		if err != nil {
			t.Fatal(err)
		}
		var status statusDTO
		err = json.NewDecoder(resp.Body).Decode(&status)
		_ = resp.Body.Close()
		if err != nil || status.SourceRefresh.Error == "" || status.SourceRefresh.Running {
			t.Fatalf("missing refresh error: %+v, %v", status.SourceRefresh, err)
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

func TestListsRemainAvailableWhileRefreshIsBlocked(t *testing.T) {
	ts, st, dir, srv := testHTTPServer(t)
	item := &store.Item{SourcePath: filepath.Join(dir, "cached"), Status: store.StatusPendingReview}
	if err := st.UpsertItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(t.Context(), item.ID, store.JobApplyPlan, store.JobPayload{}); err != nil {
		t.Fatal(err)
	}
	started := make(chan bool, 1)
	release := make(chan struct{})
	defer close(release)
	srv.scheduleSourceRefresh(func(ctx context.Context) error {
		_, deadline := ctx.Deadline()
		started <- deadline
		<-release
		return nil
	}, false)
	if <-started {
		t.Fatal("background refresh must not inherit an HTTP deadline")
	}
	before := srv.sourceRefreshStatus()
	if n := clearQueueForTest(t, ts.URL); n != 0 {
		t.Fatalf("cleanup removed active work: %d", n)
	}
	if after := srv.sourceRefreshStatus(); after != before {
		t.Fatalf("cleanup replaced a running refresh: %+v", after)
	}
	client := &http.Client{Timeout: time.Second}
	for range 3 {
		for _, endpoint := range []string{"/items", "/queue", "/status"} {
			resp, err := client.Get(ts.URL + "/api" + endpoint)
			if err != nil {
				t.Fatalf("%s blocked on filesystem I/O: %v", endpoint, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s status = %d", endpoint, resp.StatusCode)
			}
			switch endpoint {
			case "/items":
				var items []itemDTO
				if err := json.NewDecoder(resp.Body).Decode(&items); err != nil || len(items) != 1 {
					t.Errorf("cached items not served: %+v, %v", items, err)
				}
			case "/queue":
				var queue queueResponse
				if err := json.NewDecoder(resp.Body).Decode(&queue); err != nil || len(queue.Jobs) != 1 || queue.Counts.Pending != 1 {
					t.Errorf("cached queue not served: %+v, %v", queue, err)
				}
			case "/status":
				var status statusDTO
				if err := json.NewDecoder(resp.Body).Decode(&status); err != nil || !status.SourceRefresh.Running {
					t.Errorf("refresh status not reported: %+v, %v", status.SourceRefresh, err)
				}
			}
			_ = resp.Body.Close()
		}
	}
}

func TestRefreshFailurePersistsUntilSuccessfulRetry(t *testing.T) {
	_, _, _, srv := testHTTPServer(t)
	srv.scheduleSourceRefresh(func(context.Context) error { return errors.New("share offline") }, false)
	waitForSourceRefresh(t, srv)
	failed := srv.sourceRefreshStatus()
	if failed.Error != "share offline" || failed.FinishedAt.IsZero() || !failed.LastSuccessAt.IsZero() {
		t.Fatalf("failure status = %+v", failed)
	}
	srv.scheduleSourceRefresh(func(context.Context) error { return nil }, false)
	if got := srv.sourceRefreshStatus(); got != failed {
		t.Fatalf("poll restarted refresh without cooldown: %+v", got)
	}
	expireSourceRefresh(srv)
	started := make(chan struct{})
	release := make(chan struct{})
	srv.scheduleSourceRefresh(func(context.Context) error {
		close(started)
		<-release
		return nil
	}, false)
	<-started
	retrying := srv.sourceRefreshStatus()
	close(release)
	if !retrying.Running || retrying.Error != "share offline" {
		t.Fatalf("retry hid previous failure: %+v", retrying)
	}
	waitForSourceRefresh(t, srv)
	success := srv.sourceRefreshStatus()
	if success.Running || success.Error != "" || success.LastSuccessAt.IsZero() {
		t.Fatalf("successful refresh status = %+v", success)
	}
}
