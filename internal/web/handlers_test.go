package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/queue"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

type noopResyncer struct{}

func (noopResyncer) Resync(context.Context) {}

func testHTTP(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	// The server resolves symlinks in the media root; on macOS the temp dir is
	// itself a symlink (/var -> /private/var), so resolve it up front to keep
	// the reported paths comparable.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{MediaRoot: dir}
	eng := engine.New(st, cfg, log)
	q := queue.New(st, eng, cfg, log)
	var level slog.LevelVar
	srv := NewServer(st, eng, q, cfg, log, noopResyncer{}, logbuf.New(50, io.Discard), &level)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return ts, st, dir
}

func putJSON(t *testing.T, rawURL, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestGetSettingsMasksAPIKey(t *testing.T) {
	ts, st, _ := testHTTP(t)
	if err := st.SaveAppSettings(context.Background(), store.AppSettings{AIAPIKey: "supersecret", AIModel: "gpt"}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "supersecret") {
		t.Errorf("API key leaked in settings response: %s", body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["has_api_key"] != true {
		t.Errorf("has_api_key should be true, got %v", got["has_api_key"])
	}
	if _, ok := got["ai_api_key"]; ok {
		t.Error("ai_api_key must not be present in the response")
	}
}

func TestPutSettingsRejectsInvalidURL(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp := putJSON(t, ts.URL+"/api/settings", `{"ai_base_url":"ftp://nope","threshold":0.5}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid ai_base_url should be 400, got %d", resp.StatusCode)
	}
}

func TestPutSettingsRejectsThresholdRange(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp := putJSON(t, ts.URL+"/api/settings", `{"threshold":2.5}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("out-of-range threshold should be 400, got %d", resp.StatusCode)
	}
}

func TestPutSettingsValid(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp := putJSON(t, ts.URL+"/api/settings", `{"ai_base_url":"https://api.example.com","threshold":0.8}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid settings should be 200, got %d", resp.StatusCode)
	}
}

func TestBodySizeLimitRejectsHugePayload(t *testing.T) {
	ts, _, _ := testHTTP(t)
	huge := strings.Repeat("x", 2<<20) // 2 MiB, well over the 1 MiB limit
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", strings.NewReader(`{"ai_context":"`+huge+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// A connection reset from MaxBytesReader also means the body was rejected.
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("oversized body should not be accepted, got 200")
	}
}

func TestBrowseClampsTraversal(t *testing.T) {
	ts, _, dir := testHTTP(t)
	resp, err := http.Get(ts.URL + "/api/browse?path=" + url.QueryEscape("/etc"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var br struct {
		Path   string `json:"path"`
		AtRoot bool   `json:"at_root"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		t.Fatal(err)
	}
	if br.Path != filepath.Clean(dir) {
		t.Errorf("traversal not clamped to media root: path=%q want %q", br.Path, filepath.Clean(dir))
	}
	if !br.AtRoot {
		t.Error("clamped browse should report at_root=true")
	}
}

func TestBrowseClampsSymlinkEscape(t *testing.T) {
	ts, _, dir := testHTTP(t)
	outside := t.TempDir()
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/browse?path=" + url.QueryEscape(link))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var br struct {
		Path   string `json:"path"`
		AtRoot bool   `json:"at_root"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		t.Fatal(err)
	}
	if br.Path != filepath.Clean(dir) {
		t.Errorf("symlink escape not clamped to media root: path=%q want %q", br.Path, filepath.Clean(dir))
	}
	if !br.AtRoot {
		t.Error("clamped symlink browse should report at_root=true")
	}
}

func TestAddSourceRejectsSymlinkEscapingMediaRoot(t *testing.T) {
	ts, _, mediaRoot := testHTTP(t)
	outside := t.TempDir()
	escape := filepath.Join(mediaRoot, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/sources", strings.NewReader(`{"path":"`+escape+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("escaped symlink should be rejected with 400, got %d", resp.StatusCode)
	}
}

func postJSON(t *testing.T, rawURL, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestFilesystemActionsAreQueued is the core guarantee: a click never waits for
// the storage. Every filesystem action answers 202 immediately and is recorded
// as a job instead of being executed inside the request.
func TestFilesystemActionsAreQueued(t *testing.T) {
	ts, st, dir := testHTTP(t)
	ctx := context.Background()

	item := &store.Item{
		SourcePath: filepath.Join(dir, "Release"),
		Name:       "Release",
		Status:     store.StatusPendingReview,
		Files:      []store.File{{RelPath: "a.mkv", Action: store.FileActionMove}},
	}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("%s/api/items/%d", ts.URL, item.ID)

	for _, tc := range []struct{ name, url, body string }{
		{"confirm", base + "/confirm", ""},
		{"file-action", base + "/file-action", `{"rel_path":"a.mkv","action":"move"}`},
		{"reclassify", base + "/reclassify", ""},
		{"create-folder", base + "/create-folder", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, tc.url, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("%s = %d, want 202", tc.name, resp.StatusCode)
			}
			var got map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got["queued"] != true {
				t.Fatalf("%s did not report the action as queued: %v", tc.name, got)
			}
		})
	}

	counts, err := st.CountJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pending != 4 {
		t.Fatalf("pending jobs = %d, want 4", counts.Pending)
	}

	// A second identical click must not create a duplicate job.
	resp := postJSON(t, base+"/confirm", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("duplicate confirm = %d, want 202", resp.StatusCode)
	}
	counts, err = st.CountJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pending != 4 {
		t.Fatalf("duplicate click created a job: pending = %d, want 4", counts.Pending)
	}
}

// TestItemsExposeQueueState makes sure the review card can render its badge
// without a second round trip.
func TestItemsExposeQueueState(t *testing.T) {
	ts, st, dir := testHTTP(t)
	ctx := context.Background()

	item := &store.Item{SourcePath: filepath.Join(dir, "Q"), Name: "Q", Status: store.StatusPendingReview}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(ctx, item.ID, store.JobApplyPlan, store.JobPayload{}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/api/items")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var items []struct {
		ID         int64  `json:"id"`
		QueueState string `json:"queue_state"`
		QueueKind  string `json:"queue_kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].QueueState != store.JobPending || items[0].QueueKind != store.JobApplyPlan {
		t.Fatalf("queue state = %q/%q, want %q/%q",
			items[0].QueueState, items[0].QueueKind, store.JobPending, store.JobApplyPlan)
	}
}

func TestQueueEndpoint(t *testing.T) {
	ts, st, dir := testHTTP(t)
	ctx := context.Background()

	item := &store.Item{SourcePath: filepath.Join(dir, "R"), Name: "R", Status: store.StatusPendingReview}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	job, err := st.EnqueueJob(ctx, item.ID, store.JobApplyPlan, store.JobPayload{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/api/queue")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var q struct {
		Jobs []struct {
			ID       int64  `json:"id"`
			Kind     string `json:"kind"`
			ItemName string `json:"item_name"`
		} `json:"jobs"`
		Counts struct {
			Pending int `json:"pending"`
		} `json:"counts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		t.Fatal(err)
	}
	if len(q.Jobs) != 1 || q.Jobs[0].ID != job.ID {
		t.Fatalf("queue listing = %+v, want job %d", q.Jobs, job.ID)
	}
	if q.Jobs[0].ItemName != "R" {
		t.Errorf("job should carry the item name for display, got %q", q.Jobs[0].ItemName)
	}
	if q.Counts.Pending != 1 {
		t.Errorf("pending count = %d, want 1", q.Counts.Pending)
	}

	del, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/queue/%d", ts.URL, job.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	dresp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel job = %d, want 204", dresp.StatusCode)
	}
}

// TestStatusReportsQueueCounters keeps the header badge wired to real data.
func TestStatusReportsQueueCounters(t *testing.T) {
	ts, st, dir := testHTTP(t)
	ctx := context.Background()

	item := &store.Item{SourcePath: filepath.Join(dir, "S"), Name: "S", Status: store.StatusPendingReview}
	if err := st.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(ctx, item.ID, store.JobReclassify, store.JobPayload{}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["queue_pending"] != float64(1) {
		t.Fatalf("queue_pending = %v, want 1", got["queue_pending"])
	}
}
