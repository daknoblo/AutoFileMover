package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

func clearQueueForTest(t *testing.T, baseURL string) int64 {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, baseURL+"/api/queue", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("queue cleanup did not respond promptly: %v", err)
	}
	defer resp.Body.Close()
	var result struct {
		Removed int64 `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cleanup status = %d", resp.StatusCode)
	}
	return result.Removed
}

func TestClearQueueOnlyRemovesRecords(t *testing.T) {
	ts, st, dir, srv := testHTTPServer(t)
	path := filepath.Join(dir, "keep.mkv")
	if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &store.Item{SourcePath: path, Status: store.StatusConfirmed}
	if err := st.UpsertItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{store.JobDone, store.JobFailed, store.JobRunning, store.JobPending} {
		job, err := st.EnqueueJob(t.Context(), item.ID, store.JobFileAction, store.JobPayload{RelPath: status})
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case store.JobDone:
			err = st.CompleteJob(t.Context(), job.ID)
		case store.JobFailed:
			err = st.FailJob(t.Context(), job.ID, "failed")
		case store.JobRunning:
			var claimed *store.Job
			claimed, err = st.ClaimNextJob(t.Context())
			if err == nil && (claimed == nil || claimed.ID != job.ID) {
				t.Fatalf("unexpected claimed job: %+v", claimed)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := clearQueueForTest(t, ts.URL); n != 2 {
		t.Fatalf("removed = %d, want 2", n)
	}
	waitForSourceRefresh(t, srv)
	if counts, err := st.CountJobs(t.Context()); err != nil || counts != (store.JobCounts{Pending: 1, Running: 1}) {
		t.Fatalf("active work affected: %+v, %v", counts, err)
	}
	if got, err := st.GetItem(t.Context(), item.ID); err != nil || got == nil || got.Status != store.StatusConfirmed {
		t.Fatalf("history affected: %+v, %v", got, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("cleanup touched files: %q, %v", data, err)
	}
	if n := clearQueueForTest(t, ts.URL); n != 0 {
		t.Fatalf("second cleanup = %d, want 0", n)
	}
}

func TestClearQueueForcesFreshSourceCheck(t *testing.T) {
	for _, available := range []bool{true, false} {
		name := "offline"
		if available {
			name = "available"
		}
		t.Run(name, func(t *testing.T) {
			ts, st, dir, srv := testHTTPServer(t)
			source := filepath.Join(dir, "downloads")
			if available {
				if err := os.Mkdir(source, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.AddSource(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			srv.scheduleSourceRefresh(srv.engine.RefreshSources, false)
			waitForSourceRefresh(t, srv)
			before := srv.sourceRefreshStatus()
			item := &store.Item{SourcePath: filepath.Join(source, "missing.mkv"), Status: store.StatusPendingReview}
			if err := st.UpsertItem(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			if _, err := st.EnqueueJob(t.Context(), item.ID, store.JobApplyPlan, store.JobPayload{}); err != nil {
				t.Fatal(err)
			}
			if n := clearQueueForTest(t, ts.URL); n != 0 {
				t.Fatalf("pending work removed without source check: %d", n)
			}
			waitForSourceRefresh(t, srv)
			after := srv.sourceRefreshStatus()
			if !after.StartedAt.After(before.StartedAt) {
				t.Fatal("explicit cleanup did not bypass the refresh cooldown")
			}
			jobs, err := st.ListJobs(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.GetItem(t.Context(), item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if available {
				if len(jobs) != 0 || got != nil || after.Error != "" {
					t.Fatalf("missing source not cleaned: jobs=%+v, item=%+v, refresh=%+v", jobs, got, after)
				}
			} else if len(jobs) != 1 || got == nil || after.Error == "" {
				t.Fatalf("offline source not protected/reported: jobs=%+v, item=%+v, refresh=%+v", jobs, got, after)
			}
		})
	}
}
